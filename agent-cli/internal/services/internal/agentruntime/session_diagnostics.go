// This file contains the session diagnostic contract: the canonical structured
// failure record, per-turn accounting records, unexecutable tool-call records,
// and the observer that derives them from the session loop's delta stream.
//
// Field names and values documented here are a stable operator contract; see
// docs/architecture/s2s-session-diagnostic-contract.md.
package agentruntime

import sessioncontract "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"

import (
	"context"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"sync"
)

const (
	// SessionDiagnosticEventFailure is emitted exactly once per terminal
	// session failure with the canonical failure field map.
	SessionDiagnosticEventFailure = sessioncontract.SessionDiagnosticEventFailure
	// SessionDiagnosticEventTerminal is emitted exactly once for a clean
	// terminal outcome that is not a failure, including CLI user cancellation.
	SessionDiagnosticEventTerminal = sessioncontract.SessionDiagnosticEventTerminal
	// SessionDiagnosticEventTurn is emitted once per admitted assistant turn
	// (a non-empty output response at MESSAGE.END) with per-turn input/output
	// byte accounting.
	SessionDiagnosticEventTurn = sessioncontract.SessionDiagnosticEventTurn
	// SessionDiagnosticEventToolCall is emitted per provider tool-call event
	// that cannot be executed by the session runtime.
	SessionDiagnosticEventToolCall = sessioncontract.SessionDiagnosticEventToolCall
	// SessionDiagnosticEventMetrics is emitted exactly once per session run
	// after the final delta crosses, carrying the terminal per-direction and
	// per-modality byte matrix plus provider-reported token usage.
	SessionDiagnosticEventMetrics = sessioncontract.SessionDiagnosticEventMetrics
	// SessionDiagnosticEventRoomBound is the room-owned terminal projection
	// emitted once for a participant affected by a duration/turn bound. It is
	// deliberately separate from session_failure: a bound cancellation is an
	// expected room outcome, not a provider failure.
	SessionDiagnosticEventRoomBound = sessioncontract.SessionDiagnosticEventRoomBound
)

// Stable field keys for canonical diagnostic records.
const (
	fieldClassification     = "classification"
	fieldTerminalReason     = "terminal_reason"
	fieldTerminalProvenance = "terminal_provenance"
	fieldOutputState        = "output_state"
	fieldProvider           = "provider"
	fieldModel              = "model"
	fieldTurnsCompleted     = "turns_completed"
	fieldFailingEvent       = "failing_event"
	fieldProviderErrorType  = "provider_error_type"
	fieldProviderErrorCode  = "provider_error_code"

	fieldTurnIndex        = "turn_index"
	fieldInputAudioBytes  = "input_audio_bytes"
	fieldInputTextBytes   = "input_text_bytes"
	fieldOutputAudioBytes = "output_audio_bytes"
	fieldOutputTextBytes  = "output_text_bytes"
	fieldOutputToolBytes  = "output_tool_bytes"

	fieldProviderPromptTokens     = "provider_prompt_tokens"
	fieldProviderCompletionTokens = "provider_completion_tokens"
	fieldProviderTotalTokens      = "provider_total_tokens"
	fieldProviderReasoningTokens  = "provider_reasoning_tokens"

	fieldToolName              = "tool_name"
	fieldToolCallID            = "tool_call_id"
	fieldFailureClassification = "failure_classification"
	fieldFailureReason         = "failure_reason"

	// These fields extend the canonical session_failure record when a terminal
	// path leaves provider-requested tool results unresolved.
	SessionDiagnosticFieldUnresolvedToolResultCount = sessioncontract.SessionDiagnosticFieldUnresolvedToolResultCount
	SessionDiagnosticFieldUnresolvedToolCallIDs     = sessioncontract.SessionDiagnosticFieldUnresolvedToolCallIDs
	// These fields identify a provider-accepted read_image result whose model
	// continuation did not reach a terminal response.
	SessionDiagnosticFieldPendingImageContinuationCount = sessioncontract.SessionDiagnosticFieldPendingImageContinuationCount
	SessionDiagnosticFieldPendingImageContinuationIDs   = sessioncontract.SessionDiagnosticFieldPendingImageContinuationIDs
	// These fields identify provider-accepted non-image tool results whose model
	// continuation did not reach a terminal response.
	SessionDiagnosticFieldPendingToolContinuationCount = sessioncontract.SessionDiagnosticFieldPendingToolContinuationCount
	SessionDiagnosticFieldPendingToolContinuationIDs   = sessioncontract.SessionDiagnosticFieldPendingToolContinuationIDs
	// These fields identify a scheduled-audio session that terminated before
	// every configured input completed its assistant turn.
	SessionDiagnosticFieldScheduledInputCount  = sessioncontract.SessionDiagnosticFieldScheduledInputCount
	SessionDiagnosticFieldDispatchedInputCount = sessioncontract.SessionDiagnosticFieldDispatchedInputCount
	SessionDiagnosticFieldCompletedTurnCount   = sessioncontract.SessionDiagnosticFieldCompletedTurnCount
	// These fields retain bounded provider terminal context for pending
	// continuations. Values are encoded as comma-separated call_id=value pairs.
	SessionDiagnosticFieldPendingContinuationStatuses = sessioncontract.SessionDiagnosticFieldPendingContinuationStatuses
	SessionDiagnosticFieldPendingContinuationCodes    = sessioncontract.SessionDiagnosticFieldPendingContinuationCodes
	SessionDiagnosticFieldPendingContinuationDetails  = sessioncontract.SessionDiagnosticFieldPendingContinuationDetails
	// These fields describe obligations resolved as cancelled by an operator
	// SIGINT. They are emitted on session_terminal, never as failure fields.
	SessionDiagnosticFieldCancelledBy                      = sessioncontract.SessionDiagnosticFieldCancelledBy
	SessionDiagnosticFieldCancelledScheduledInputCount     = sessioncontract.SessionDiagnosticFieldCancelledScheduledInputCount
	SessionDiagnosticFieldCancelledToolResultCount         = sessioncontract.SessionDiagnosticFieldCancelledToolResultCount
	SessionDiagnosticFieldCancelledToolResultCallIDs       = sessioncontract.SessionDiagnosticFieldCancelledToolResultCallIDs
	SessionDiagnosticFieldCancelledToolContinuationCount   = sessioncontract.SessionDiagnosticFieldCancelledToolContinuationCount
	SessionDiagnosticFieldCancelledToolContinuationCallIDs = sessioncontract.SessionDiagnosticFieldCancelledToolContinuationCallIDs
)

const (
	fieldUnresolvedToolResultCount = SessionDiagnosticFieldUnresolvedToolResultCount
	fieldUnresolvedToolCallIDs     = SessionDiagnosticFieldUnresolvedToolCallIDs
)

// Failing-event identities used when no stream event authored the failure.
const (
	failingEventConnect = "SESSION.CONNECT"
	failingEventRun     = "SESSION.RUN"
)

// ScheduledAudioInput schedules one raw PCM user-audio injection through the
// loop's existing audio-input seam (AgentLoop.SendAudioInput). The default
// completion-gated policy fires after AfterCompletedTurns assistant turns have
// completed; the active-response policy may fire at the immediately preceding
// response's non-terminal boundary. Its bytes are attributed to the then
// in-flight turn (turn index AfterCompletedTurns+1).
type ScheduledAudioInput struct {
	AfterCompletedTurns int
	PCM                 []byte
	// SourceSampleRate is the native rate of PCM. Zero explicitly means the
	// caller/replay bytes already use the resolved provider rate.
	SourceSampleRate int
	// EndOfTurn sends MESSAGE.END after this input so realtime providers
	// commit the audio and create one response before the next scheduled turn.
	// The zero value preserves the diagnostics-only injection behavior.
	EndOfTurn bool
}

// scheduledSessionInputSender is the narrow loop seam used by the scheduler.
// Keeping it separate from AgentLoop makes the ordering contract directly
// observable in service tests while production still uses AgentLoop's session
// input APIs.
type scheduledSessionInputSender interface {
	SendAudioInput(context.Context, []byte) error
	SendSessionEvent(context.Context, messages.StreamMessage) error
}

// failureFacts holds the typed terminal facts captured from the first
// failure-bearing stream value observed for a session run.
type audioTurnCounters struct {
	inputAudio uint64
	inputText  uint64
	outAudio   uint64
	outText    uint64
	outTool    uint64
}

func (c *audioTurnCounters) reset() {
	c.inputAudio, c.inputText, c.outAudio, c.outText, c.outTool = 0, 0, 0, 0, 0
}

// account advances exactly one direction-and-modality series; every counted
// byte reaches both counter instances through the observer seam.
func (c *audioTurnCounters) account(direction metrics.Direction, modality metrics.Modality, n uint64) {
	switch {
	case direction == metrics.DirectionInput && modality == metrics.ModalityAudio:
		c.inputAudio += n
	case direction == metrics.DirectionInput && modality == metrics.ModalityText:
		c.inputText += n
	case direction == metrics.DirectionOutput && modality == metrics.ModalityAudio:
		c.outAudio += n
	case direction == metrics.DirectionOutput && modality == metrics.ModalityText:
		c.outText += n
	case direction == metrics.DirectionOutput && modality == metrics.ModalityTool:
		c.outTool += n
	}
}

// sessionProgressObserver derives metrics observations and diagnostic records
// from the delta stream consumed by the session runner. Most state is owned by
// the runner's single consumer goroutine. Outstanding tool state is also
// touched by the provider-send wrapper, so that small state machine has its
// own synchronization boundary.
type sessionProgressObserver struct {
	sink           SessionDiagnosticSink
	recorder       metrics.Recorder
	productionSink *metrics.InMemorySink
	streamObserver SessionStreamObserver
	// admittedTurnObserver runs after this observer has admitted one provider
	// response as a completed turn. Room accounting uses this boundary instead
	// of counting raw MESSAGE.END events.
	admittedTurnObserver SessionStreamObserver
	// turnAdmission is an optional owner-controlled admission boundary for
	// an otherwise valid completed response. Returning false keeps the raw
	// stream event observable but prevents it from advancing completed-turn
	// state or evidence.
	turnAdmission      func(messages.StreamMessage) bool
	runtime            *sessionRuntimeObservationRecorder
	cancellationIntent *SessionCancellationIntent
	provider           string
	model              string
	sawSessionOpen     bool
	sessionID          string
	// sessionUpdated is scoped to the current SESSION.OPEN round trip. A
	// subsequent SESSION.OPEN resets it so an acknowledgement from an older
	// connection cannot release a new connection's scheduled input.
	sessionUpdated                bool
	requireSessionUpdated         bool
	scheduledAudioDispatch        ScheduledAudioDispatchPolicy
	activeResponse                bool
	activeResponseID              string
	completedResponseIDs          map[string]struct{}
	retiredResponseIDs            map[string]struct{}
	turnsCompleted                int
	scheduledInputs               int
	dispatchedInputs              int
	completedScheduled            int
	scheduledTurnBase             int
	scheduledTurnBaseSet          bool
	scheduledResponses            []scheduledAudioResponseLifecycle
	scheduledResponseByID         map[string]int
	nextScheduledResponse         int
	activeScheduledResponseIndex  int
	activeScheduledResponseID     string
	activeScheduledResponseSet    bool
	logicalScheduledResponseIndex int
	logicalScheduledResponseID    string
	logicalScheduledResponseSet   bool
	// retryCandidate retains the scheduled owner of the most recent eligible
	// terminal until the session runner decides whether to wait and retry. It
	// is needed for legacy transports whose MESSAGE.END omits response_id and
	// whose normal response cleanup clears the active/logical owner.
	retryCandidateIndex int
	retryCandidateSet   bool
	retryCandidateID    string
	counters            audioTurnCounters
	totals              audioTurnCounters
	pendingInputs       []ScheduledAudioInput
	// Room mixer input is admitted by a background pump rather than the
	// session delta consumer. Keep its per-turn and lifetime byte totals behind
	// their own lock so concurrent provider observation remains race-free.
	roomInputMu         sync.Mutex
	roomInputTurnBytes  uint64
	roomInputTotalBytes uint64

	toolStateMu             sync.Mutex
	unresolvedToolCalls     map[string]struct{}
	acceptedToolCalls       map[string]struct{}
	toolResultRejections    map[string]messages.SessionSendStatus
	toolLifecycleCh         chan struct{}
	toolContinuations       map[string]*toolContinuationState
	toolCallInTurn          bool
	messageEndSeen          bool
	messageEndAdmitted      bool
	providerToolCallSeen    bool
	assistantResponseDone   bool
	assistantOutputObserved bool
	// These fields describe only the current provider response. The logical
	// turn counters intentionally span a provider tool-call response and its
	// later continuation, so they cannot be used to decide whether the current
	// response itself emitted output.
	responseOutputTextBytes  uint64
	responseOutputAudioBytes uint64
	responseActionableTool   bool
	// toolResultsEnabled is false for explicit no-tools session plans, where a
	// provider tool event is reported as unexecutable rather than creating an
	// obligation that no executor can satisfy.
	toolResultsEnabled bool

	// toolDeltaSeen tracks whether the in-flight provider tool call streamed
	// TOOLCALL.DELTA bytes, so a terminal TOOLCALL.END carrying full arguments
	// is counted only when no deltas preceded it.
	toolDeltaSeen bool

	usagePrompt     uint64
	usageCompletion uint64
	usageTotal      uint64
	usageReasoning  uint64
	usageSeen       bool

	livenessMu             sync.Mutex
	livenessErr            error
	livenessObserver       func(error)
	livenessClock          SessionLivenessClock
	livenessTimer          SessionLivenessTimer
	livenessWakeCh         chan struct{}
	livenessControlCh      chan struct{}
	livenessWatcherStop    chan struct{}
	livenessWatcherStarted bool
	livenessGeneration     uint64
	livenessArmed          bool
	livenessStopped        bool
	localToolDepth         int
	failure                *failureFacts
	// userCancelled is set once by finish after the explicit SIGINT marker has
	// proved that all observed causes were cancellation-only.
	userCancelled bool
	// roomBoundCancellation is set for the deliberate second phase of a room
	// bound shutdown. It suppresses only cancellation-derived incomplete
	// lifecycle errors; an independently observed provider failure remains
	// authoritative.
	roomBoundCancellation bool
	// terminalObserver receives authoritative terminal observations even when
	// no diagnostic sink is configured. Room orchestration uses it to preserve
	// the same terminal fields in participant results and run manifests.
	terminalObserver func(sessionTerminalObservation) bool
	// failureObserver lets room orchestration close the room at the same
	// observation boundary as a typed provider failure. It is intentionally
	// separate from the diagnostic sink so a failure still produces exactly
	// one canonical session_failure record.
	failureObserver func(sessionTerminalObservation)

	emitOnce    sync.Once
	metricsOnce sync.Once
}

func (o *sessionProgressObserver) markRoomBoundCancellation() {
	if o != nil {
		o.roomBoundCancellation = true
	}
}

func (o *sessionProgressObserver) notifyTerminalObservation(observation sessionTerminalObservation) bool {
	if o == nil {
		return false
	}
	if o.terminalObserver == nil {
		return true
	}
	return o.terminalObserver(observation)
}

func (o *sessionProgressObserver) notifyFailureObservation(observation sessionTerminalObservation) bool {
	if !observation.Failure || !o.notifyTerminalObservation(observation) {
		return false
	}
	if o.failureObserver != nil {
		o.failureObserver(observation)
	}
	return true
}

func newSessionProgressObserver(sink SessionDiagnosticSink, recorder metrics.Recorder, provider, model string) *sessionProgressObserver {
	productionSink, err := metrics.NewInMemorySink()
	if err != nil {
		// The default histogram configuration is package-owned and validated by
		// NewInMemorySink. Reaching this branch means the production accounting
		// invariant cannot be constructed, so fail immediately rather than
		// publishing a partial terminal snapshot.
		panic(err)
	}
	return &sessionProgressObserver{
		sink:                  sink,
		recorder:              recorder,
		productionSink:        productionSink,
		provider:              provider,
		model:                 model,
		unresolvedToolCalls:   make(map[string]struct{}),
		acceptedToolCalls:     make(map[string]struct{}),
		toolResultRejections:  make(map[string]messages.SessionSendStatus),
		toolLifecycleCh:       make(chan struct{}, 1),
		toolContinuations:     make(map[string]*toolContinuationState),
		completedResponseIDs:  make(map[string]struct{}),
		retiredResponseIDs:    make(map[string]struct{}),
		scheduledResponseByID: make(map[string]int),
		livenessClock:         realSessionDurationClock{},
		livenessWakeCh:        make(chan struct{}, 1),
	}
}

func (o *sessionProgressObserver) scheduleAudioInputs(inputs []ScheduledAudioInput) {
	if o == nil {
		return
	}
	o.pendingInputs = append(o.pendingInputs, inputs...)
	o.scheduledInputs += len(inputs)
}

// observe consumes one delta crossing. It must run before any error-bearing
// message reaches output rendering so the failure facts survive drain errors.
