package agentruntime

import devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

const (
	// DefaultRoomOutputDir is retained for configured-room compatibility when
	// --out is omitted. Bare room launches allocate a fresh sibling under the
	// effective config directory before runtime side effects begin.
	DefaultRoomOutputDir = "room-run"
	// DefaultRoomBoundShutdownGrace is the one fixed drain budget applied after
	// a duration or turn bound closes room admission. Existing responses may
	// finish during this window; responses still active at its end are
	// deliberately cancelled.
	DefaultRoomBoundShutdownGrace = 250 * time.Millisecond
	// roomBoundResponseCancelTimeout bounds the provider-facing cancel write.
	// The room does not wait for a provider acknowledgement; the cancellation
	// signal and session close remain the authoritative force path.
	roomBoundResponseCancelTimeout = 50 * time.Millisecond

	// Room lifecycle cleanup is deliberately finite. Provider/session contracts
	// are expected to make Close and cancellation progress, but a diagnostic is
	// preferable to making the room caller wait forever when an injected or
	// third-party owner violates that contract.
	roomCleanupTimeout   = time.Second
	roomAdmissionTimeout = 5 * time.Second
)

type roomParticipantPlan struct {
	manifest              room.Participant
	options               SessionRunOptions
	inferencer            messages.SessionInferencer
	startupErr            error
	replay                bool
	replayLoop            sessionLoopOptions
	secret                string
	tracker               *roomConnectTrackingInferencer
	participant           *roomParticipantRuntime
	inputAudioSampleRate  int
	capabilityCoordinator SessionCapabilityCoordinator
}

type roomParticipantRuntime struct {
	plan            *roomParticipantPlan
	ctx             context.Context
	cancel          context.CancelFunc
	admissionCtx    context.Context
	admissionCancel context.CancelFunc
	loopReady       chan *agentloop.AgentLoop
	participantDone chan struct{}
	mixerDone       chan struct{}
	observerDone    chan struct{}
	observerOnce    sync.Once
	// replayFrameAcks is populated only for provider participants in a
	// room-owned deterministic replay. The room scheduler advances one mixer
	// frame and waits for this acknowledgement after the mixed frame has been
	// accepted by the participant session.
	replayFrameAcks chan struct{}
	mixer           *room.PCM16Mixer
	ingress         *roomAudioIngressLedger
	input           *devicegw.DeviceSource
	output          *devicegw.DeviceSink
	lifecycle       *roomParticipantLifecycle
	// diagnosticSink is the combined per-participant SessionDiagnosticSink
	// (evidence, stream, and RoomRunOptions.OnDiagnostic). It is set once, at
	// the top of runRoomParticipant, before that goroutine's result can reach
	// the coordinator, so reading it from finishParticipant is race-free. A
	// human participant has no SessionRunOptions/RTCDeviceBinding to carry a
	// playback observer, so the coordinator uses this field directly to name
	// which participant's speaker queue overflowed; see
	// emitRoomParticipantPlaybackOverflowDiagnostic.
	diagnosticSink SessionDiagnosticSink
	// outboundLoudness applies this participant's fixed, voice-specific gain
	// (see VoiceLoudnessGainDB) to its own synthesized audio before it
	// reaches evidence, any peer mixer, or a local monitor, so --voice
	// selection does not leave one room participant audibly quieter than
	// another.
	outboundLoudness *audio.LoudnessNormalizer
}

func (r *roomParticipantRuntime) markObserverDone() {
	if r == nil || r.observerDone == nil {
		return
	}
	r.observerOnce.Do(func() { close(r.observerDone) })
}

type roomCleanupWaiter struct {
	timer *time.Timer
}

func (w *roomCleanupWaiter) start() {
	if w == nil || w.timer != nil {
		return
	}
	w.timer = time.NewTimer(roomCleanupTimeout)
}

func (w *roomCleanupWaiter) done() <-chan time.Time {
	if w == nil || w.timer == nil {
		return nil
	}
	return w.timer.C
}

func (w *roomCleanupWaiter) stop() {
	if w == nil || w.timer == nil {
		return
	}
	if !w.timer.Stop() {
		select {
		case <-w.timer.C:
		default:
		}
	}
	w.timer = nil
}

type roomLifecycleWorkError struct {
	outstanding []string
}

func (e *roomLifecycleWorkError) Error() string {
	if e == nil || len(e.outstanding) == 0 {
		return "room lifecycle work did not complete"
	}
	return "room lifecycle work did not complete: " + strings.Join(e.outstanding, "; ")
}

func newRoomLifecycleWorkError(outstanding ...string) error {
	seen := make(map[string]struct{}, len(outstanding))
	ordered := make([]string, 0, len(outstanding))
	for _, item := range outstanding {
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		ordered = append(ordered, item)
	}
	if len(ordered) == 0 {
		return nil
	}
	sort.Strings(ordered)
	return &roomLifecycleWorkError{outstanding: ordered}
}

func roomLifecycleWorkLabel(participantID, phase string) string {
	if participantID == "" {
		return phase
	}
	return fmt.Sprintf("participant %q phase %s", participantID, phase)
}

func roomParticipantOutstandingWork(runtime *roomParticipantRuntime) []string {
	if runtime == nil || runtime.plan == nil {
		return []string{"participant runtime"}
	}
	id := runtime.plan.manifest.ID
	outstanding := make([]string, 0, 6)
	if runtime.plan.startupErr != nil {
		// Setup failure owns the participant's terminal result; the missing
		// device or mixer is not outstanding work after that result is queued.
	} else if roomParticipantIsHuman(runtime.plan) {
		if runtime.lifecycle == nil || !runtime.lifecycle.deviceHasReady() {
			outstanding = append(outstanding, roomLifecycleWorkLabel(id, "devices"))
		}
	} else if runtime.plan.tracker == nil {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "connect"))
	} else if _, ready := runtime.plan.tracker.outcome(); !ready {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "connect"))
	}
	if runtime.lifecycle == nil {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "lifecycle"))
		return outstanding
	}
	created, closed, transportDone, closeErr := runtime.lifecycle.ownedSessionSnapshot()
	if created && !closed {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.close"))
	}
	if closeErr != nil {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.close.error"))
	}
	if created && !roomChannelClosed(transportDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "session.transport"))
	}
	if runtime.participantDone != nil && !roomChannelClosed(runtime.participantDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "participant.loop"))
	}
	if runtime.mixerDone != nil && !roomChannelClosed(runtime.mixerDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "mixer"))
	}
	if runtime.observerDone != nil && !roomChannelClosed(runtime.observerDone) {
		outstanding = append(outstanding, roomLifecycleWorkLabel(id, "observer"))
	}
	return outstanding
}

type roomParticipantLifecycle struct {
	mu                         sync.Mutex
	connected                  bool
	connectErr                 error
	deviceReady                bool
	sessionCreated             bool
	ownedSessionClosed         bool
	sessionCloseErr            error
	ownedSession               *roomTrackedSession
	sessionOpened              bool
	sessionClosed              bool
	transportEnded             bool
	transportDone              <-chan struct{}
	stateChanged               chan<- struct{}
	admissionClosed            <-chan struct{}
	roomStopping               bool
	boundShutdown              bool
	boundCancellation          bool
	stopReason                 RoomTerminationReason
	boundTrigger               string
	boundResponsePending       bool
	runDone                    bool
	closeReason                string
	terminalReason             messages.TerminalReason
	terminalKind               ParticipantTerminationReason
	terminalErr                error
	terminalClassification     string
	terminalProvenance         messages.TerminalProvenance
	terminalOutputState        messages.TerminalOutputState
	transportTerminalErr       func() error
	turns                      int
	responseInFlight           bool
	responseID                 string
	responseCancelSent         bool
	responseTerminalPending    bool
	pendingToolCalls           map[string]struct{}
	acceptedToolResults        map[string]struct{}
	boundContinuationRequested bool
	terminalObservation        roomParticipantTerminalObservation
	terminalObserved           bool
	failureObserved            bool
}

func (l *roomParticipantLifecycle) markConnected(err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.connected = err == nil
	l.connectErr = err
	l.signalLocked()
	l.mu.Unlock()
}

// markDeviceReady records the readiness boundary for a human participant.
// Human participants have no provider ConnectSession outcome, so their
// capture and playback handles become the equivalent startup admission
// signal once both have been acquired.
func (l *roomParticipantLifecycle) markDeviceReady() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.connected = true
	l.deviceReady = true
	l.connectErr = nil
	l.signalLocked()
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) deviceHasReady() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.deviceReady
}

func (l *roomParticipantLifecycle) signalLocked() {
	if l == nil || l.stateChanged == nil {
		return
	}
	select {
	case l.stateChanged <- struct{}{}:
	default:
	}
}

func (l *roomParticipantLifecycle) markSessionCreated() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.sessionCreated = true
	l.signalLocked()
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) setOwnedSession(session *roomTrackedSession) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.ownedSession = session
	l.signalLocked()
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) closeOwnedSession() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	session := l.ownedSession
	l.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}

func (l *roomParticipantLifecycle) markOwnedSessionClosed(err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.ownedSessionClosed = true
	l.sessionCloseErr = err
	l.signalLocked()
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) markTerminalLocked(reason ParticipantTerminationReason, err error) {
	if l.terminalKind != "" || reason == "" {
		return
	}
	l.terminalKind = reason
	l.terminalErr = err
}

// markParticipantFailure records a fault owned by this participant without
// changing the room's terminal state. The first observed participant terminal
// cause remains authoritative; a later cancellation or cleanup observation
// must not replace it.
func (l *roomParticipantLifecycle) markParticipantFailure(err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if !l.roomStopping {
		l.markTerminalLocked(ParticipantTerminationError, err)
	}
	l.signalLocked()
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) markLivenessFailure(err error) {
	if l == nil {
		return
	}
	classification, terminalReason, provenance, outputState := sessionLivenessMetadata(err)
	l.mu.Lock()
	if l.terminalKind == "" {
		l.terminalKind = ParticipantTerminationError
		l.terminalErr = err
		l.terminalReason = terminalReason
		l.terminalClassification = classification
		l.terminalProvenance = provenance
		l.terminalOutputState = outputState
		l.signalLocked()
	}
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) setTerminalObservationLocked(observation roomParticipantTerminalObservation) {
	if l == nil {
		return
	}
	if observation.terminalReason == "" {
		observation.terminalReason = string(messages.TerminalReasonSessionClose)
	}
	if observation.outputState == "" {
		observation.outputState = string(messages.TerminalOutputNone)
	}
	if observation.terminalProvenance == "" && (observation.terminalReason != "" || observation.terminationDisposition != "" || observation.terminationTrigger != "" || observation.failure) {
		observation.terminalProvenance = defaultRoomTerminalProvenance(observation.terminationDisposition, observation.terminalReason)
	}
	l.terminalObservation = observation
	l.terminalObserved = true
}

func (l *roomParticipantLifecycle) recordFailureLocked(err error) {
	if l == nil || err == nil || roomCancellationOnly(err) || l.failureObserved || l.boundCancellation {
		return
	}
	classification := providers.ErrorClassification(err)
	if classification == "" {
		classification = providers.ErrorClassUnknown
	}
	l.failureObserved = true
	l.setTerminalObservationLocked(roomParticipantTerminalObservation{
		terminationTrigger:     ParticipantTerminationTriggerSessionFailure,
		terminationDisposition: ParticipantTerminationDispositionFailed,
		classification:         classification,
		terminalReason:         string(messages.TerminalReasonTerminalFailure),
		terminalProvenance:     string(messages.TerminalProvenanceSession),
		outputState:            deriveOutputState(l.sessionOpened, l.turns),
		err:                    err,
		failure:                true,
	})
}

func (l *roomParticipantLifecycle) recordDisconnectedLocked() {
	if l == nil || l.failureObserved || l.terminalObserved {
		return
	}
	l.setTerminalObservationLocked(roomParticipantTerminalObservation{
		terminationTrigger:     ParticipantTerminationTriggerProviderClose,
		terminationDisposition: ParticipantTerminationDispositionDisconnected,
		terminalReason:         string(messages.TerminalReasonProviderClose),
		terminalProvenance:     string(messages.TerminalProvenanceSession),
		outputState:            deriveOutputState(l.sessionOpened, l.turns),
	})
}

func (l *roomParticipantLifecycle) transportTerminalErrorLocked() error {
	if l == nil || l.transportTerminalErr == nil {
		return nil
	}
	return l.transportTerminalErr()
}

func (l *roomParticipantLifecycle) transportTerminalErrorSnapshot() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	err := l.transportTerminalErrorLocked()
	l.mu.Unlock()
	return err
}

func (l *roomParticipantLifecycle) clearToolContinuationLocked() {
	if l == nil {
		return
	}
	l.pendingToolCalls = nil
	l.acceptedToolResults = nil
	l.boundContinuationRequested = false
}

func (l *roomParticipantLifecycle) toolCallID(msg messages.StreamMessage) string {
	if l == nil {
		return ""
	}
	if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
		return strings.TrimSpace(firstNonBlankToolCallID(value.ToolCallID, msg.ToolCallId))
	}
	if value, ok := msg.Value.(*messages.ToolCallStartValue); ok && value != nil {
		return strings.TrimSpace(firstNonBlankToolCallID(value.ToolCallID, msg.ToolCallId))
	}
	return strings.TrimSpace(msg.ToolCallId)
}

// recordToolResultSend records a provider-facing tool result only after the
// underlying session accepted it. The accepted result remains an obligation
// until its follow-up assistant response reaches a terminal observation.
func (l *roomParticipantLifecycle) recordToolResultSend(callID string, accepted, requestsContinuation bool) {
	if l == nil || !accepted || strings.TrimSpace(callID) == "" {
		return
	}
	l.mu.Lock()
	if l.acceptedToolResults == nil {
		l.acceptedToolResults = make(map[string]struct{})
	}
	delete(l.pendingToolCalls, callID)
	l.acceptedToolResults[callID] = struct{}{}
	if l.boundShutdown && !l.boundCancellation && requestsContinuation {
		l.boundContinuationRequested = true
	}
	l.signalLocked()
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) recordToolContinuationRequest(accepted bool) {
	if l == nil || !accepted {
		return
	}
	l.mu.Lock()
	if l.boundShutdown && !l.boundCancellation && len(l.acceptedToolResults) > 0 {
		l.boundContinuationRequested = true
	}
	l.signalLocked()
	l.mu.Unlock()
}

// recordResponseCancellation remembers a provider-facing RESPONSE.CANCEL that
// another session-loop owner already sent. The latch is response-scoped and
// is reset only when a new provider response starts.
func (l *roomParticipantLifecycle) recordResponseCancellation() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.responseInFlight {
		l.responseCancelSent = true
	}
	l.signalLocked()
	l.mu.Unlock()
}

// cancelActiveResponse sends the one room-owned cancellation for a response
// that remained active through the grace window. It latches before sending so
// a racing loop shutdown or repeated force call cannot emit a duplicate.
// Failure to enqueue the cancellation is intentionally not promoted to a
// participant failure: force shutdown still closes the session and the room
// bound remains the authoritative terminal cause.
func (l *roomParticipantLifecycle) cancelActiveResponse() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if !l.boundCancellation || !l.boundResponsePending || !l.responseInFlight || l.responseCancelSent || l.ownedSession == nil {
		l.mu.Unlock()
		return
	}
	l.responseCancelSent = true
	session := l.ownedSession
	l.signalLocked()
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), roomBoundResponseCancelTimeout)
	defer cancel()
	_ = session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCancel,
		Value: messages.NewResponseCancelValue(),
	})
}

// admitResponseTerminal is checked after the shared observer has seen a
// provider MESSAGE.END but before it awards a room turn. It makes a terminal
// event belonging to the response that was already in flight the only
// response that can cross the grace boundary.
func (l *roomParticipantLifecycle) admitResponseTerminal() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.roomStopping {
		return true
	}
	return l.boundShutdown && !l.boundCancellation && l.boundResponsePending
}

func (l *roomParticipantLifecycle) admitSessionMessageAfterBound(msg messages.StreamMessage) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.boundShutdown || l.boundCancellation {
		return false
	}
	switch msg.Type {
	case messages.StreamTypeToolCallEnd:
		callID := l.toolCallID(msg)
		if callID == "" {
			return false
		}
		_, pending := l.pendingToolCalls[callID]
		return pending
	case messages.StreamTypeResponseCreate:
		return len(l.acceptedToolResults) > 0 && !l.boundContinuationRequested
	default:
		return false
	}
}

func (l *roomParticipantLifecycle) admitCompleteToolResultAfterBound(msg messages.Message) bool {
	if l == nil || strings.TrimSpace(msg.ToolCallID) == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.boundShutdown || l.boundCancellation {
		return false
	}
	_, pending := l.pendingToolCalls[msg.ToolCallID]
	return pending
}

// observeTerminal records the first genuine failure and the most recent
// successful response terminal. A bound response is promoted to a distinct
// completed_during_grace disposition only when its terminal observation
// arrives after the coordinator has closed admission.
func (l *roomParticipantLifecycle) observeTerminal(observation sessionTerminalObservation) bool {
	if l == nil || observation.TerminalReason == "" && observation.Classification == "" && observation.OutputState == "" && observation.Err == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if observation.Failure {
		// The model runner synthesizes a provider-close failure boundary when a
		// session transport ends without an explicit provider error. That is a
		// participant disconnect, not a room failure. Keep a real terminal
		// transport error authoritative when one exists.
		if observation.TerminalReason == string(messages.TerminalReasonProviderClose) &&
			observation.FailingEvent == string(messages.StreamTypeSessionClose) &&
			l.transportTerminalErrorLocked() == nil {
			if l.terminalKind == "" {
				l.recordDisconnectedLocked()
				l.markTerminalLocked(ParticipantTerminationDisconnected, nil)
			}
			return false
		}
		if l.boundCancellation {
			return false
		}
		if l.failureObserved {
			// A synthetic loop/CLI failure can race the engine's preserved
			// provider ERROR. Keep the first genuine provider/session evidence
			// when it is more specific than that fallback, while still refusing
			// any less-authoritative late failure.
			if !moreSpecificFailureObservation(observation, l.terminalObservation) {
				return false
			}
			failureErr := observation.Err
			if failureErr != nil && strings.TrimSpace(failureErr.Error()) == "session stream error" && l.terminalObservation.err != nil {
				// The transport watcher can retain the provider's real error before
				// the observer consumes a synthetic provider-close delta. Preserve
				// that causal error instead of replacing it with the fallback.
				failureErr = l.terminalObservation.err
			}
			l.setTerminalObservationLocked(roomParticipantTerminalObservation{
				terminationTrigger:     ParticipantTerminationTriggerSessionFailure,
				terminationDisposition: ParticipantTerminationDispositionFailed,
				classification:         observation.Classification,
				terminalReason:         observation.TerminalReason,
				terminalProvenance:     observation.TerminalProvenance,
				outputState:            observation.OutputState,
				err:                    failureErr,
				failure:                true,
			})
			if l.terminalKind == ParticipantTerminationError {
				l.terminalErr = failureErr
			}
			l.signalLocked()
			return true
		}
		l.failureObserved = true
		l.boundResponsePending = false
		l.responseTerminalPending = false
		l.clearToolContinuationLocked()
		l.setTerminalObservationLocked(roomParticipantTerminalObservation{
			terminationTrigger:     ParticipantTerminationTriggerSessionFailure,
			terminationDisposition: ParticipantTerminationDispositionFailed,
			classification:         observation.Classification,
			terminalReason:         observation.TerminalReason,
			terminalProvenance:     observation.TerminalProvenance,
			outputState:            observation.OutputState,
			err:                    observation.Err,
			failure:                true,
		})
		l.signalLocked()
		return true
	}
	if l.failureObserved {
		return false
	}
	if l.boundShutdown && !l.boundResponsePending && l.terminalObserved {
		// The coordinator already latched this participant as completed before
		// the forced cancellation. A final room-bound cancellation callback is
		// for the response that was pending at the bound, not for a participant
		// whose response had already ended (or never started).
		return false
	}
	// Once grace has expired, a late provider completion is no longer an
	// authoritative terminal observation for this participant. The observer's
	// room-bound cancellation snapshot is the source of truth for output_state;
	// accepting the late completion here would report a successful reason with
	// a cancelled disposition.
	if l.boundShutdown && l.boundCancellation && !observation.RoomBound && observation.TerminalReason != string(messages.TerminalReasonCancellation) {
		return false
	}
	terminal := roomParticipantTerminalObservation{
		classification:     observation.Classification,
		terminalReason:     observation.TerminalReason,
		terminalProvenance: observation.TerminalProvenance,
		outputState:        observation.OutputState,
	}
	l.responseTerminalPending = false
	if l.boundShutdown && l.boundResponsePending {
		terminal.terminationTrigger = l.boundTrigger
		if l.boundCancellation || observation.RoomBound || observation.TerminalReason == string(messages.TerminalReasonCancellation) {
			terminal.terminationDisposition = ParticipantTerminationDispositionCancelledAfterGrace
			terminal.classification = RoomBoundCancelledClassification
			terminal.terminalReason = string(messages.TerminalReasonCancellation)
		} else {
			terminal.terminationDisposition = ParticipantTerminationDispositionCompletedDuringGrace
		}
		l.boundResponsePending = false
	} else if l.boundShutdown {
		terminal.terminationTrigger = l.boundTrigger
		terminal.terminationDisposition = l.boundDispositionLocked()
	} else if l.stopReason != "" {
		terminal.terminationTrigger = string(l.stopReason)
		terminal.terminationDisposition = ParticipantTerminationDispositionStopped
	}
	l.clearToolContinuationLocked()
	l.setTerminalObservationLocked(terminal)
	l.signalLocked()
	return true
}

func moreSpecificFailureObservation(incoming sessionTerminalObservation, existing roomParticipantTerminalObservation) bool {
	if incoming.Classification != "" && incoming.Classification != providers.ErrorClassUnknown && incoming.Classification != providers.ErrorClassCancellation {
		if existing.classification == "" || existing.classification == providers.ErrorClassUnknown || existing.classification == providers.ErrorClassCancellation {
			return true
		}
	}
	return existing.terminalProvenance == string(messages.TerminalProvenanceCLI) && incoming.TerminalProvenance != "" && incoming.TerminalProvenance != string(messages.TerminalProvenanceCLI)
}

func (l *roomParticipantLifecycle) boundDispositionLocked() string {
	if l == nil {
		return ""
	}
	if l.boundResponsePending && l.boundCancellation {
		return ParticipantTerminationDispositionCancelledAfterGrace
	}
	return ParticipantTerminationDispositionCompleted
}

func (l *roomParticipantLifecycle) observe(msg messages.StreamMessage) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	switch msg.Type {
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		if msg.Role != messages.RoleTool {
			// MESSAGE.START opens a new response. AUDIO.START can be the first
			// boundary for an audio-only response, but it can also be a modality
			// start inside an already-active MESSAGE.START response; do not reset
			// the response-scoped cancellation latch in the latter case.
			newResponse := msg.Type == messages.StreamTypeMessageStart || !l.responseInFlight
			if responseID := strings.TrimSpace(msg.ResponseID); responseID != "" && responseID != l.responseID {
				newResponse = true
			}
			l.responseInFlight = true
			l.responseID = msg.ResponseID
			if newResponse {
				l.responseCancelSent = false
			}
			l.responseTerminalPending = false
			l.signalLocked()
		}
	case messages.StreamTypeToolCallStart, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd:
		if msg.Role != messages.RoleTool && (!l.boundShutdown || (!l.boundCancellation && l.boundResponsePending)) {
			callID := l.toolCallID(msg)
			if callID != "" {
				if l.pendingToolCalls == nil {
					l.pendingToolCalls = make(map[string]struct{})
				}
				l.pendingToolCalls[callID] = struct{}{}
			}
		}
	case messages.StreamTypeMessageEnd:
		if msg.Role != messages.RoleTool {
			l.responseInFlight = false
			l.responseID = ""
			if !l.boundShutdown {
				l.responseTerminalPending = true
			}
			l.signalLocked()
		}
	case messages.StreamTypeSessionOpen:
		l.sessionOpened = true
		l.signalLocked()
	case messages.StreamTypeSessionClose:
		l.sessionClosed = true
		l.signalLocked()
		if value, ok := msg.Value.(*messages.SessionCloseValue); ok && value != nil {
			l.closeReason = value.Reason
			l.terminalReason = value.TerminalReason
			if l.terminalReason == "" && value.Reason == "provider_closed" {
				l.terminalReason = messages.TerminalReasonProviderClose
			}
			if !l.failureObserved && !l.terminalObserved && !(l.boundShutdown && l.boundCancellation) {
				closeObservation := roomParticipantTerminalObservation{
					classification:     value.Classification,
					terminalReason:     string(value.TerminalReason),
					terminalProvenance: string(value.TerminalProvenance),
					outputState:        string(value.OutputState),
				}
				if value.Reason == "provider_closed" {
					closeObservation.terminationTrigger = ParticipantTerminationTriggerProviderClose
					closeObservation.terminationDisposition = ParticipantTerminationDispositionDisconnected
					if closeObservation.terminalReason == "" {
						closeObservation.terminalReason = string(messages.TerminalReasonProviderClose)
					}
					if closeObservation.outputState == "" {
						closeObservation.outputState = string(messages.TerminalOutputNotApplicable)
					}
				} else {
					closeObservation.terminationTrigger = ParticipantTerminationTriggerParticipantCompletion
					closeObservation.terminationDisposition = ParticipantTerminationDispositionCompleted
					if closeObservation.terminalReason == "" {
						closeObservation.terminalReason = string(messages.TerminalReasonSessionClose)
					}
					if closeObservation.outputState == "" {
						closeObservation.outputState = string(messages.TerminalOutputNotApplicable)
					}
				}
				l.setTerminalObservationLocked(closeObservation)
			}
		}
		if !l.roomStopping || l.boundShutdown && !l.boundCancellation {
			terminalErr := error(nil)
			if l.transportTerminalErr != nil {
				terminalErr = l.transportTerminalErr()
			}
			if terminalErr != nil && !roomCancellationOnly(terminalErr) {
				l.recordFailureLocked(terminalErr)
				l.markTerminalLocked(ParticipantTerminationError, terminalErr)
			} else {
				l.markTerminalLocked(classifyRoomSessionClose(l.closeReason, l.terminalReason), nil)
			}
		}
	}
	return l.turns
}

// observeAdmittedTurn advances room progress only after the shared session
// observer has accepted a provider response as a completed turn. Raw
// MESSAGE.END events are intentionally not sufficient: a provider can emit an
// empty response boundary before producing any assistant output.
func (l *roomParticipantLifecycle) observeAdmittedTurn() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	l.turns++
	l.signalLocked()
	turns := l.turns
	l.mu.Unlock()
	return turns
}

func (l *roomParticipantLifecycle) markTransportEndedWithError(terminalErr error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.transportEnded = true
	l.signalLocked()
	if !l.roomStopping || l.boundShutdown && !l.boundCancellation {
		if terminalErr != nil && !roomCancellationOnly(terminalErr) {
			l.recordFailureLocked(terminalErr)
			l.markTerminalLocked(ParticipantTerminationError, terminalErr)
		} else {
			l.recordDisconnectedLocked()
			l.markTerminalLocked(ParticipantTerminationDisconnected, nil)
		}
	}
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) setTransportDone(done <-chan struct{}, terminalError func() error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.transportDone = done
	l.transportTerminalErr = terminalError
	l.signalLocked()
	l.mu.Unlock()
	if roomChannelClosed(done) {
		var terminalErr error
		if terminalError != nil {
			terminalErr = terminalError()
		}
		l.markTransportEndedWithError(terminalErr)
	}
}

func (l *roomParticipantLifecycle) transportHasEnded() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	ended := l.transportEnded
	done := l.transportDone
	terminalError := l.transportTerminalErr
	l.mu.Unlock()
	if ended || done == nil {
		return ended
	}
	if roomChannelClosed(done) {
		var terminalErr error
		if terminalError != nil {
			terminalErr = terminalError()
		}
		l.markTransportEndedWithError(terminalErr)
		return true
	}
	return false
}

// markCoordinatorStopping records intentional room teardown before the
// coordinator cancels participant contexts. A transport that is already done
// at this boundary is causal; one that closes afterwards belongs to the
// coordinator's teardown and must not be reported as a provider disconnect.
func (l *roomParticipantLifecycle) markCoordinatorStopping(bound bool, reason ...RoomTerminationReason) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if roomChannelClosed(l.transportDone) {
		l.transportEnded = true
		var terminalErr error
		if l.transportTerminalErr != nil {
			terminalErr = l.transportTerminalErr()
		}
		if terminalErr != nil && !roomCancellationOnly(terminalErr) {
			l.recordFailureLocked(terminalErr)
			l.markTerminalLocked(ParticipantTerminationError, terminalErr)
		} else {
			l.recordDisconnectedLocked()
			l.markTerminalLocked(ParticipantTerminationDisconnected, nil)
		}
	}
	l.roomStopping = true
	l.boundShutdown = bound
	if len(reason) > 0 {
		l.stopReason = reason[0]
	}
	if bound {
		boundReason := RoomTerminationReason("")
		if len(reason) > 0 {
			boundReason = reason[0]
		}
		midResponse := l.responseInFlight || l.responseTerminalPending || len(l.pendingToolCalls) > 0 || len(l.acceptedToolResults) > 0
		l.boundTrigger = roomBoundTerminationTrigger(boundReason, midResponse)
		l.boundResponsePending = midResponse
		if !midResponse && !l.failureObserved && l.terminalKind == "" {
			if l.terminalObserved {
				l.terminalObservation.terminationTrigger = l.boundTrigger
				l.terminalObservation.terminationDisposition = ParticipantTerminationDispositionCompleted
			} else {
				l.setTerminalObservationLocked(roomParticipantTerminalObservation{
					terminationTrigger:     l.boundTrigger,
					terminationDisposition: ParticipantTerminationDispositionCompleted,
					terminalReason:         string(l.terminalReason),
					terminalProvenance:     string(messages.TerminalProvenanceLoop),
					outputState:            string(messages.TerminalOutputNone),
				})
			}
		}
	}
	l.signalLocked()
	l.mu.Unlock()
}

// markBoundCancellation records the second phase of a bound shutdown. A
// provider close observed after this point is teardown-owned unless it carries
// an independent terminal error.
func (l *roomParticipantLifecycle) markBoundCancellation() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.boundCancellation = true
	if l.boundResponsePending && !l.failureObserved {
		// Keep the pending marker until the session observer reports its actual
		// output state. terminalObservationSnapshot supplies the same clean
		// cancellation fallback if an uncooperative provider never acknowledges.
		l.terminalObservation.terminationTrigger = l.boundTrigger
		l.terminalObservation.terminationDisposition = ParticipantTerminationDispositionCancelledAfterGrace
		l.terminalObservation.classification = RoomBoundCancelledClassification
		l.terminalObservation.terminalReason = string(messages.TerminalReasonCancellation)
		l.terminalObservation.terminalProvenance = string(messages.TerminalProvenanceRoom)
		l.terminalObservation.outputState = string(messages.TerminalOutputNone)
		l.terminalObserved = true
	}
	l.signalLocked()
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) markRunDone(runErr error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.runDone = true
	l.signalLocked()
	if runErr != nil && !roomCancellationOnly(runErr) {
		l.recordFailureLocked(runErr)
		l.markTerminalLocked(ParticipantTerminationError, runErr)
	} else if l.failureObserved {
		l.markTerminalLocked(ParticipantTerminationError, l.terminalObservation.err)
	}
	l.mu.Unlock()
}

func (l *roomParticipantLifecycle) runHasFinished() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runDone
}

func (l *roomParticipantLifecycle) snapshot() (connected bool, sessionOpened bool, sessionClosed bool, closeReason string, terminalReason messages.TerminalReason, turns int, connectErr error) {
	if l == nil {
		return false, false, false, "", "", 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.connected, l.sessionOpened, l.sessionClosed, l.closeReason, l.terminalReason, l.turns, l.connectErr
}

func (l *roomParticipantLifecycle) terminal() (ParticipantTerminationReason, error, bool) {
	if l == nil {
		return "", nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failureObserved && l.terminalKind == "" {
		return ParticipantTerminationError, l.terminalObservation.err, true
	}
	return l.terminalKind, l.terminalErr, l.terminalKind != ""
}

func (l *roomParticipantLifecycle) terminalMetadata() (string, messages.TerminalReason, messages.TerminalProvenance, messages.TerminalOutputState) {
	if l == nil {
		return "", "", "", ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.terminalClassification, l.terminalReason, l.terminalProvenance, l.terminalOutputState
}

func (l *roomParticipantLifecycle) terminalObservationSnapshot() roomParticipantTerminalObservation {
	if l == nil {
		return roomParticipantTerminalObservation{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	observation := l.terminalObservation
	if l.failureObserved {
		observation.failure = true
		if observation.terminationTrigger == "" {
			observation.terminationTrigger = ParticipantTerminationTriggerSessionFailure
		}
		if observation.terminationDisposition == "" {
			observation.terminationDisposition = ParticipantTerminationDispositionFailed
		}
		return observation
	}
	if l.boundShutdown {
		if observation.terminationTrigger == "" {
			observation.terminationTrigger = l.boundTrigger
		}
		if l.boundResponsePending && l.boundCancellation {
			observation.terminationDisposition = ParticipantTerminationDispositionCancelledAfterGrace
			observation.classification = RoomBoundCancelledClassification
			observation.terminalReason = string(messages.TerminalReasonCancellation)
			observation.terminalProvenance = string(messages.TerminalProvenanceRoom)
			if observation.outputState == "" {
				observation.outputState = string(messages.TerminalOutputNone)
			}
		}
		if observation.terminationDisposition == "" {
			observation.terminationDisposition = l.boundDispositionLocked()
		}
	}
	if observation.terminationTrigger == "" && l.stopReason != "" {
		observation.terminationTrigger = string(l.stopReason)
		observation.terminationDisposition = ParticipantTerminationDispositionStopped
	}
	if observation.terminalReason == "" && l.terminalReason != "" {
		observation.terminalReason = string(l.terminalReason)
	}
	if observation.outputState == "" {
		observation.outputState = string(messages.TerminalOutputNone)
	}
	if observation.terminalProvenance == "" {
		observation.terminalProvenance = defaultRoomTerminalProvenance(observation.terminationDisposition, observation.terminalReason)
	}
	return observation
}

func (l *roomParticipantLifecycle) ownedSessionSnapshot() (created, closed bool, transportDone <-chan struct{}, closeErr error) {
	if l == nil {
		return false, false, nil, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sessionCreated, l.ownedSessionClosed, l.transportDone, l.sessionCloseErr
}

// roomConnectTrackingInferencer preserves the existing SessionInferencer
// contract while exposing the first ConnectSession outcome to the room's
// initial-start barrier.
type roomConnectTrackingInferencer struct {
	inner      messages.SessionInferencer
	result     chan error
	outcomes   chan<- roomConnectionOutcome
	once       sync.Once
	mu         sync.Mutex
	ready      bool
	connectErr error
	lifecycle  *roomParticipantLifecycle
}

type roomConnectionOutcome struct {
	tracker *roomConnectTrackingInferencer
	err     error
}

func newRoomConnectTrackingInferencer(inner messages.SessionInferencer) *roomConnectTrackingInferencer {
	return &roomConnectTrackingInferencer{inner: inner, result: make(chan error, 1)}
}

func (i *roomConnectTrackingInferencer) setOutcomeSink(outcomes chan<- roomConnectionOutcome) {
	if i == nil {
		return
	}
	i.outcomes = outcomes
}

func (i *roomConnectTrackingInferencer) publish(err error) {
	if i == nil {
		return
	}
	i.once.Do(func() {
		i.mu.Lock()
		i.ready = true
		i.connectErr = err
		i.mu.Unlock()
		i.result <- err
		if i.outcomes != nil {
			i.outcomes <- roomConnectionOutcome{tracker: i, err: err}
		}
	})
}

func (i *roomConnectTrackingInferencer) outcome() (error, bool) {
	if i == nil {
		return nil, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.connectErr, i.ready
}

func (i *roomConnectTrackingInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.inner == nil {
		err := errors.New("room participant session inferencer is nil")
		if i != nil {
			i.publish(err)
		}
		return nil, err
	}
	session, err := i.inner.ConnectSession(ctx)
	if err == nil && session == nil {
		err = errors.New("room participant session is nil")
	}
	if err != nil {
		if session != nil {
			if i.lifecycle != nil {
				i.lifecycle.markSessionCreated()
			}
			var admissionClosed <-chan struct{}
			if i.lifecycle != nil {
				admissionClosed = i.lifecycle.admissionClosed
			}
			tracked := &roomTrackedSession{Session: session, lifecycle: i.lifecycle, admissionClosed: admissionClosed}
			if i.lifecycle != nil {
				i.lifecycle.setOwnedSession(tracked)
				terminalError := func() error { return terminalSessionError(tracked) }
				i.lifecycle.setTransportDone(tracked.Done(), terminalError)
			}
			if closeErr := tracked.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close failed session: %w", closeErr))
			}
		}
		i.publish(err)
		return nil, err
	}
	if err == nil && session != nil && i.lifecycle != nil {
		i.lifecycle.markSessionCreated()
		tracked := &roomTrackedSession{Session: session, lifecycle: i.lifecycle, admissionClosed: i.lifecycle.admissionClosed}
		i.lifecycle.setOwnedSession(tracked)
		terminalError := func() error { return terminalSessionError(tracked) }
		i.lifecycle.setTransportDone(tracked.Done(), terminalError)
		recordSessionEnd := func() {
			i.lifecycle.markTransportEndedWithError(terminalError())
		}
		go func() {
			select {
			case <-tracked.Done():
				recordSessionEnd()
			case <-ctx.Done():
				if roomChannelClosed(tracked.Done()) {
					recordSessionEnd()
				} else {
					// Cancellation can win before the model runner reaches its
					// deferred session Close (for example while the room is still
					// admitting a sibling). The tracker owns this idempotent
					// fallback so a connected provider cannot outlive the room.
					_ = tracked.Close()
				}
			}
		}()
		session = tracked
	}
	i.publish(err)
	return session, err
}

var _ messages.SessionInferencer = (*roomConnectTrackingInferencer)(nil)
