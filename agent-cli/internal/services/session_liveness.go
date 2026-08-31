package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	platformclock "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
)

const (
	// sessionProviderLivenessTimeout is deliberately independent from the
	// session's overall max-duration bound. A participant can stay in a room
	// for much longer than one provider response, while a response that stops
	// making progress must still release its participant promptly.
	sessionProviderLivenessTimeout = 10 * time.Second

	// SessionSilentProviderEmptyResponseClassification identifies a provider
	// response that reached an explicit partial-output terminal boundary
	// without producing any admissible assistant output.
	SessionSilentProviderEmptyResponseClassification = "silent_provider_empty_response"
	// SessionSilentProviderTimeoutClassification identifies a response that was
	// opened or explicitly requested but produced no further provider event
	// before the participant-owned watchdog expired.
	SessionSilentProviderTimeoutClassification = "silent_provider_timeout"
)

// SessionLivenessTimer and SessionLivenessClock are aliases of the existing
// duration timer seam. Keeping one small timer contract lets callers use a
// deterministic clock without coupling liveness to wall time.
type SessionLivenessTimer = SessionDurationTimer
type SessionLivenessClock = SessionDurationClock

type platformSessionLivenessClock struct {
	source platformclock.TimerSource
}

func (c platformSessionLivenessClock) NewTimer(duration time.Duration) SessionLivenessTimer {
	return c.source.NewTimer(duration)
}

func sessionLivenessClockFromSource(source platformclock.Source) SessionLivenessClock {
	source = platformclock.Ensure(source)
	timerSource, ok := source.(platformclock.TimerSource)
	if !ok {
		return nil
	}
	return platformSessionLivenessClock{source: timerSource}
}

var (
	// ErrSilentProviderEmptyResponse is the stable sentinel for an explicit
	// empty provider response. The error intentionally contains no provider
	// status details or response payload.
	ErrSilentProviderEmptyResponse = errors.New("silent provider returned an empty response")
	// ErrSilentProviderTimeout is the stable sentinel for a provider that stops
	// emitting events while a response remains outstanding.
	ErrSilentProviderTimeout = errors.New("silent provider response timed out")
)

// SessionLivenessError carries the credential-free terminal facts associated
// with a provider liveness failure. Its Error string is deliberately stable so
// participant results can expose the condition without leaking provider data.
type SessionLivenessError struct {
	Classification     string
	ResponseID         string
	TerminalReason     messages.TerminalReason
	TerminalProvenance messages.TerminalProvenance
	OutputState        messages.TerminalOutputState
	Usage              messages.TokenUsage
}

func (e *SessionLivenessError) Error() string {
	if e == nil {
		return "session liveness failure"
	}
	classification := strings.TrimSpace(e.Classification)
	if classification == "" {
		classification = SessionSilentProviderEmptyResponseClassification
	}
	return fmt.Sprintf("%s: provider response produced no observable output", classification)
}

func (e *SessionLivenessError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.Classification {
	case SessionSilentProviderTimeoutClassification:
		return ErrSilentProviderTimeout
	default:
		return ErrSilentProviderEmptyResponse
	}
}

func newSilentProviderEmptyResponseError(msg messages.StreamMessage, value *messages.MessageEndValue) *SessionLivenessError {
	err := &SessionLivenessError{
		Classification:     SessionSilentProviderEmptyResponseClassification,
		ResponseID:         strings.TrimSpace(msg.ResponseID),
		TerminalReason:     messages.TerminalReasonTerminalFailure,
		TerminalProvenance: messages.TerminalProvenanceSession,
		OutputState:        messages.TerminalOutputNone,
	}
	if value != nil {
		err.Usage = value.Usage
	}
	return err
}

func sessionLivenessMetadata(err error) (classification string, terminalReason messages.TerminalReason, provenance messages.TerminalProvenance, outputState messages.TerminalOutputState) {
	var livenessErr *SessionLivenessError
	if !errors.As(err, &livenessErr) || livenessErr == nil {
		return "", "", "", ""
	}
	return livenessErr.Classification, livenessErr.TerminalReason, livenessErr.TerminalProvenance, livenessErr.OutputState
}

// responseHasToolLifecycleObligation snapshots the current response's tool
// state before MESSAGE.END processing clears toolCallInTurn. A pending result
// or continuation owns the terminal boundary and must not be diagnosed as an
// empty provider response.
func (o *sessionProgressObserver) responseHasToolLifecycleObligation() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	if o.toolCallInTurn || len(o.unresolvedToolCalls) > 0 {
		return true
	}
	for _, state := range o.toolContinuations {
		if state != nil && state.resultAccepted && !state.continuationComplete {
			return true
		}
	}
	return false
}

func responseCancellationBoundary(value *messages.MessageEndValue) bool {
	if value == nil {
		return false
	}
	if isLocalResponseCancellation(value) || value.TerminalReason == messages.TerminalReasonCancellation {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(value.Status), "cancelled")
}

func (o *sessionProgressObserver) observeSilentProviderEmptyResponse(msg messages.StreamMessage, value *messages.MessageEndValue, outputPresent, toolObligation bool) {
	if o == nil || value == nil || outputPresent || toolObligation || responseCancellationBoundary(value) {
		return
	}
	if value.TerminalReason != messages.TerminalReasonPartialOutput ||
		value.OutputState != messages.TerminalOutputNone ||
		value.Usage.CompletionTokens != 0 {
		return
	}
	err := newSilentProviderEmptyResponseError(msg, value)
	o.latchLivenessFailure(err, &failureFacts{
		classification: SessionSilentProviderEmptyResponseClassification,
		terminalReason: string(messages.TerminalReasonTerminalFailure),
		provenance:     string(messages.TerminalProvenanceSession),
		outputState:    string(messages.TerminalOutputNone),
		failingEvent:   string(messages.StreamTypeMessageEnd),
	})
}

func (o *sessionProgressObserver) livenessFailure() error {
	if o == nil {
		return nil
	}
	o.livenessMu.Lock()
	defer o.livenessMu.Unlock()
	return o.livenessErr
}

// latchLivenessFailure is the first-cause boundary shared by the empty
// response classifier and the watchdog. The callback runs outside the mutex so
// room lifecycle notification can synchronously wake its coordinator without
// creating a lock cycle.
func (o *sessionProgressObserver) latchLivenessFailure(err error, facts *failureFacts) bool {
	return o.latchLivenessFailureAtGeneration(err, facts, 0, false)
}

func (o *sessionProgressObserver) latchLivenessFailureAtGeneration(err error, facts *failureFacts, generation uint64, requireGeneration bool) bool {
	if o == nil || err == nil {
		return false
	}
	o.livenessMu.Lock()
	if o.livenessErr != nil || o.failure != nil || o.livenessStopped || (requireGeneration && (!o.livenessArmed || o.livenessGeneration != generation)) {
		o.livenessMu.Unlock()
		return false
	}
	o.livenessErr = err
	o.failure = facts
	o.livenessArmed = false
	timer := o.livenessTimer
	o.livenessTimer = nil
	o.livenessGeneration++
	notify := o.livenessObserver
	o.signalLivenessControlLocked()
	o.livenessMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if notify != nil {
		notify(err)
	}
	// Publish the session-loop wake only after the owner callback has recorded
	// any room-level explanation and terminal metadata. Otherwise the loop can
	// begin teardown concurrently with that callback and expose a generic
	// disconnect before the typed liveness cause is visible.
	o.livenessMu.Lock()
	o.signalLivenessLocked()
	o.livenessMu.Unlock()
	return true
}

// livenessEvents is a stable wake channel. The timer itself is intentionally
// swapped behind the observer mutex, so the session loop never has to select
// directly on a channel that can be replaced concurrently.
func (o *sessionProgressObserver) livenessEvents() <-chan struct{} {
	if o == nil {
		return nil
	}
	o.livenessMu.Lock()
	defer o.livenessMu.Unlock()
	o.ensureLivenessStateLocked()
	return o.livenessWakeCh
}

// sessionLivenessErrorChannel translates the observer's stable wake channel
// into the error channel already consumed by the session loops. A wake is only
// published for a latched liveness failure while a loop is running; teardown
// cancellation releases this small bridge when no failure occurred.
func sessionLivenessErrorChannel(ctx context.Context, observer *sessionProgressObserver) <-chan error {
	if observer == nil {
		return nil
	}
	events := observer.livenessEvents()
	if events == nil {
		return nil
	}
	errorsCh := make(chan error, 1)
	go func() {
		defer close(errorsCh)
		select {
		case <-events:
			if err := observer.livenessFailure(); err != nil {
				errorsCh <- err
			}
		case <-ctx.Done():
		}
	}()
	return errorsCh
}

func mergeSessionErrorChannels(ctx context.Context, first, second <-chan error) <-chan error {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	merged := make(chan error, 1)
	go func() {
		defer close(merged)
		for first != nil || second != nil {
			select {
			case err, ok := <-first:
				if !ok {
					first = nil
					continue
				}
				if err == nil {
					continue
				}
				select {
				case merged <- err:
				case <-ctx.Done():
				}
				return
			case err, ok := <-second:
				if !ok {
					second = nil
					continue
				}
				if err == nil {
					continue
				}
				select {
				case merged <- err:
				case <-ctx.Done():
				}
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return merged
}

func (o *sessionProgressObserver) setLivenessClock(clock SessionLivenessClock) {
	if o == nil || clock == nil {
		return
	}
	o.livenessMu.Lock()
	if !o.livenessStopped {
		o.livenessClock = clock
	}
	o.livenessMu.Unlock()
}

func (o *sessionProgressObserver) ensureLivenessStateLocked() {
	if o.livenessWakeCh == nil {
		o.livenessWakeCh = make(chan struct{}, 1)
	}
	if o.livenessControlCh == nil {
		o.livenessControlCh = make(chan struct{}, 1)
	}
}

func (o *sessionProgressObserver) signalLivenessLocked() {
	o.ensureLivenessStateLocked()
	select {
	case o.livenessWakeCh <- struct{}{}:
	default:
	}
}

func (o *sessionProgressObserver) signalLivenessControlLocked() {
	o.ensureLivenessStateLocked()
	select {
	case o.livenessControlCh <- struct{}{}:
	default:
	}
}

// armProviderProgress starts or replaces the participant-owned watchdog for
// one outstanding response-producing dispatch. Replacing the timer on each
// accepted provider event implements the reset-on-progress contract without
// relying on timer.Reset, whose semantics are awkward across clock seams.
func (o *sessionProgressObserver) armProviderProgress() {
	o.setProviderProgress(false)
}

func (o *sessionProgressObserver) resetProviderProgress() {
	o.setProviderProgress(true)
}

func (o *sessionProgressObserver) setProviderProgress(onlyIfArmed bool) {
	if o == nil {
		return
	}
	o.livenessMu.Lock()
	if o.livenessStopped || o.livenessErr != nil || o.failure != nil || o.localToolDepth > 0 || (onlyIfArmed && !o.livenessArmed) {
		o.livenessMu.Unlock()
		return
	}
	clock := o.livenessClock
	if clock == nil {
		clock = realSessionDurationClock{}
	}
	o.ensureLivenessStateLocked()
	o.livenessMu.Unlock()

	timer := clock.NewTimer(sessionProviderLivenessTimeout)
	if timer == nil {
		return
	}

	o.livenessMu.Lock()
	if o.livenessStopped || o.livenessErr != nil || o.failure != nil || o.localToolDepth > 0 || (onlyIfArmed && !o.livenessArmed) {
		o.livenessMu.Unlock()
		timer.Stop()
		return
	}
	oldTimer := o.livenessTimer
	o.livenessGeneration++
	o.livenessTimer = timer
	o.livenessArmed = true
	o.ensureLivenessStateLocked()
	startWatcher := !o.livenessWatcherStarted
	if startWatcher {
		o.livenessWatcherStarted = true
		o.livenessWatcherStop = make(chan struct{})
	}
	o.signalLivenessControlLocked()
	watcherStop := o.livenessWatcherStop
	o.livenessMu.Unlock()
	if oldTimer != nil {
		oldTimer.Stop()
	}
	if startWatcher {
		go o.watchProviderProgress(watcherStop)
	}
}

func (o *sessionProgressObserver) watchProviderProgress(stop <-chan struct{}) {
	for {
		o.livenessMu.Lock()
		if o.livenessStopped {
			o.livenessMu.Unlock()
			return
		}
		timerCh := (<-chan time.Time)(nil)
		generation := o.livenessGeneration
		if o.livenessArmed && o.livenessTimer != nil {
			timerCh = o.livenessTimer.C()
		}
		control := o.livenessControlCh
		o.livenessMu.Unlock()

		select {
		case <-timerCh:
			o.expireProviderProgress(generation)
		case <-control:
		case <-stop:
			return
		}
	}
}

func (o *sessionProgressObserver) expireProviderProgress(generation uint64) {
	if o == nil {
		return
	}
	err := &SessionLivenessError{
		Classification:     SessionSilentProviderTimeoutClassification,
		TerminalReason:     messages.TerminalReasonTerminalFailure,
		TerminalProvenance: messages.TerminalProvenanceSession,
		OutputState:        messages.TerminalOutputNone,
	}
	o.livenessMu.Lock()
	if o.livenessStopped || o.livenessErr != nil || o.failure != nil || !o.livenessArmed || o.livenessGeneration != generation {
		o.livenessMu.Unlock()
		return
	}
	o.livenessMu.Unlock()
	o.latchLivenessFailureAtGeneration(err, &failureFacts{
		classification: SessionSilentProviderTimeoutClassification,
		terminalReason: string(messages.TerminalReasonTerminalFailure),
		provenance:     string(messages.TerminalProvenanceSession),
		outputState:    string(messages.TerminalOutputNone),
		failingEvent:   failingEventRun,
	}, generation, true)
}

func (o *sessionProgressObserver) disarmProviderProgress() {
	if o == nil {
		return
	}
	o.livenessMu.Lock()
	if !o.livenessArmed && o.livenessTimer == nil {
		o.livenessMu.Unlock()
		return
	}
	o.livenessArmed = false
	o.livenessGeneration++
	timer := o.livenessTimer
	o.livenessTimer = nil
	o.signalLivenessControlLocked()
	o.livenessMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

// beginLocalToolExecution suppresses provider liveness while a participant's
// own tool is running. Tool duration is a local operation and can legitimately
// exceed the provider response budget; the subsequent ordinary response.create
// is what reopens the provider watchdog.
func (o *sessionProgressObserver) beginLocalToolExecution() {
	if o == nil {
		return
	}
	o.livenessMu.Lock()
	o.localToolDepth++
	o.livenessMu.Unlock()
	o.disarmProviderProgress()
}

func (o *sessionProgressObserver) endLocalToolExecution() {
	if o == nil {
		return
	}
	o.livenessMu.Lock()
	if o.localToolDepth > 0 {
		o.localToolDepth--
	}
	o.livenessMu.Unlock()
}

func (o *sessionProgressObserver) stopLiveness() {
	if o == nil {
		return
	}
	o.livenessMu.Lock()
	if o.livenessStopped {
		o.livenessMu.Unlock()
		return
	}
	o.livenessStopped = true
	o.livenessArmed = false
	o.livenessGeneration++
	timer := o.livenessTimer
	o.livenessTimer = nil
	o.signalLivenessControlLocked()
	stop := o.livenessWatcherStop
	o.livenessWatcherStop = nil
	o.signalLivenessLocked()
	o.livenessMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if stop != nil {
		close(stop)
	}
}

// observeProviderEvent resets an already outstanding response on every
// normalized provider event. MESSAGE.START and the compatible AUDIO.START
// boundary also arm a watchdog for server-VAD responses that had no outbound
// RESPONSE.CREATE dispatch visible to this adapter.
func (o *sessionProgressObserver) observeProviderEvent(msg messages.StreamMessage) {
	if o == nil || msg.Role == messages.RoleTool {
		return
	}
	switch msg.Type {
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		o.armProviderProgress()
	default:
		o.resetProviderProgress()
	}
}

func (o *sessionProgressObserver) observeProviderDispatch(msg messages.StreamMessage) {
	if o == nil || msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
		return
	}
	switch msg.Type {
	case messages.StreamTypeMessageEnd, messages.StreamTypeResponseCreate:
		o.armProviderProgress()
	}
}

func applyRoomParticipantTerminalMetadata(result *RoomParticipantResult, lifecycle *roomParticipantLifecycle, err error) {
	if result == nil {
		return
	}
	classification, terminalReason, provenance, outputState := "", messages.TerminalReason(""), messages.TerminalProvenance(""), messages.TerminalOutputState("")
	if lifecycle != nil {
		classification, terminalReason, provenance, outputState = lifecycle.terminalMetadata()
	}
	if classification == "" {
		classification, terminalReason, provenance, outputState = sessionLivenessMetadata(err)
	}
	if classification == "" {
		return
	}
	result.Classification = classification
	result.TerminalReason = terminalReason
	result.TerminalProvenance = provenance
	result.OutputState = outputState
}
