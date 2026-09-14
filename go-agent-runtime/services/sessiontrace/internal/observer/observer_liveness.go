package observer

import (
	"context"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
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
	SessionSilentProviderEmptyResponseClassification = sessiontrace.SilentProviderEmptyResponseClassification
	// SessionSilentProviderTimeoutClassification identifies a response that was
	// opened or explicitly requested but produced no further provider event
	// before the participant-owned watchdog expired.
	SessionSilentProviderTimeoutClassification = sessiontrace.SilentProviderTimeoutClassification
)

// SessionLivenessTimer and SessionLivenessClock are aliases of the existing
// duration timer seam. Keeping one small timer contract lets callers use a
// deterministic clock without coupling liveness to wall time.
type SessionLivenessTimer = sessiontrace.LivenessTimer
type SessionLivenessClock = sessiontrace.LivenessClock

func (o *observerState) livenessFailure() error {
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
func (o *observerState) latchLivenessFailure(err error, facts *failureFacts) bool {
	return o.latchLivenessFailureAtGeneration(err, facts, 0, false)
}

func (o *observerState) latchLivenessFailureAtGeneration(err error, facts *failureFacts, generation uint64, requireGeneration bool) bool {
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
func (o *observerState) livenessEvents() <-chan struct{} {
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
func sessionLivenessErrorChannel(ctx context.Context, observer *observerState) <-chan error {
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

func forwardSessionErrors(ctx context.Context, merged chan<- error, source <-chan error, stop context.CancelFunc) {
	for err := range source {
		if err == nil {
			continue
		}
		select {
		case merged <- err:
			stop()
		case <-ctx.Done():
		}
		return
	}
}

func mergeSessionErrorChannels(ctx context.Context, first, second <-chan error) <-chan error {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	merged := make(chan error, 1)
	mergeContext, stop := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		forwardSessionErrors(mergeContext, merged, first, stop)
	}()
	go func() {
		defer workers.Done()
		forwardSessionErrors(mergeContext, merged, second, stop)
	}()
	go func() {
		workers.Wait()
		close(merged)
		stop()
	}()
	return merged
}

func (o *observerState) ensureLivenessStateLocked() {
	if o.livenessWakeCh == nil {
		o.livenessWakeCh = make(chan struct{}, 1)
	}
	if o.livenessControlCh == nil {
		o.livenessControlCh = make(chan struct{}, 1)
	}
}

func (o *observerState) signalLivenessLocked() {
	o.ensureLivenessStateLocked()
	select {
	case o.livenessWakeCh <- struct{}{}:
	default:
	}
}

func (o *observerState) signalLivenessControlLocked() {
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
func (o *observerState) armProviderProgress() {
	o.setProviderProgress(false)
}

func (o *observerState) resetProviderProgress() {
	o.setProviderProgress(true)
}

func (o *observerState) setProviderProgress(onlyIfArmed bool) {
	if o == nil {
		return
	}
	clock, ok := o.providerProgressClock(onlyIfArmed)
	if !ok {
		return
	}
	timer := clock.NewTimer(sessionProviderLivenessTimeout)
	if timer == nil {
		return
	}
	oldTimer, startWatcher, watcherStop, ok := o.installProviderProgress(timer, onlyIfArmed)
	if !ok {
		timer.Stop()
		return
	}
	if oldTimer != nil {
		oldTimer.Stop()
	}
	if startWatcher {
		go o.watchProviderProgress(watcherStop)
	}
}

func (o *observerState) providerProgressClock(onlyIfArmed bool) (SessionLivenessClock, bool) {
	o.livenessMu.Lock()
	if o.livenessStopped || o.livenessErr != nil || o.failure != nil || o.localToolDepth > 0 || (onlyIfArmed && !o.livenessArmed) {
		o.livenessMu.Unlock()
		return nil, false
	}
	clock := o.livenessClock
	if clock == nil {
		clock = platformSessionLivenessClock{source: platformclock.Real{}}
	}
	o.ensureLivenessStateLocked()
	o.livenessMu.Unlock()
	return clock, true
}

func (o *observerState) installProviderProgress(timer SessionLivenessTimer, onlyIfArmed bool) (SessionLivenessTimer, bool, <-chan struct{}, bool) {
	o.livenessMu.Lock()
	if o.livenessStopped || o.livenessErr != nil || o.failure != nil || o.localToolDepth > 0 || (onlyIfArmed && !o.livenessArmed) {
		o.livenessMu.Unlock()
		return nil, false, nil, false
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
	return oldTimer, startWatcher, watcherStop, true
}

func (o *observerState) watchProviderProgress(stop <-chan struct{}) {
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

func (o *observerState) expireProviderProgress(generation uint64) {
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

func (o *observerState) disarmProviderProgress() {
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
func (o *observerState) beginLocalToolExecution() {
	if o == nil {
		return
	}
	o.livenessMu.Lock()
	o.localToolDepth++
	o.livenessMu.Unlock()
	o.disarmProviderProgress()
}

func (o *observerState) endLocalToolExecution() {
	if o == nil {
		return
	}
	o.livenessMu.Lock()
	if o.localToolDepth > 0 {
		o.localToolDepth--
	}
	o.livenessMu.Unlock()
}

func (o *observerState) stopLiveness() {
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
