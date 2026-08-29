// This file contains the session diagnostic contract: the canonical structured
// failure record, per-turn accounting records, unexecutable tool-call records,
// and the observer that derives them from the session loop's delta stream.
//
// Field names and values documented here are a stable operator contract; see
// docs/architecture/s2s-session-diagnostic-contract.md.
package services

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

// Canonical diagnostic record event names.
const (
	// SessionDiagnosticEventFailure is emitted exactly once per terminal
	// session failure with the canonical failure field map.
	SessionDiagnosticEventFailure = "session_failure"
	// SessionDiagnosticEventTurn is emitted once per admitted assistant turn
	// (a non-empty output response at MESSAGE.END) with per-turn input/output
	// byte accounting.
	SessionDiagnosticEventTurn = "session_turn_completed"
	// SessionDiagnosticEventToolCall is emitted per provider tool-call event
	// that cannot be executed by the session runtime.
	SessionDiagnosticEventToolCall = "session_tool_call_unexecutable"
	// SessionDiagnosticEventMetrics is emitted exactly once per session run
	// after the final delta crosses, carrying the terminal per-direction and
	// per-modality byte matrix plus provider-reported token usage.
	SessionDiagnosticEventMetrics = "session_metrics"
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
	SessionDiagnosticFieldUnresolvedToolResultCount = "unresolved_tool_result_count"
	SessionDiagnosticFieldUnresolvedToolCallIDs     = "unresolved_tool_call_ids"
	// These fields identify a provider-accepted read_image result whose model
	// continuation did not reach a terminal response.
	SessionDiagnosticFieldPendingImageContinuationCount = "pending_image_continuation_count"
	SessionDiagnosticFieldPendingImageContinuationIDs   = "pending_image_continuation_call_ids"
	// These fields identify provider-accepted non-image tool results whose model
	// continuation did not reach a terminal response.
	SessionDiagnosticFieldPendingToolContinuationCount = "pending_tool_continuation_count"
	SessionDiagnosticFieldPendingToolContinuationIDs   = "pending_tool_continuation_call_ids"
	// These fields identify a scheduled-audio session that terminated before
	// every configured input completed its assistant turn.
	SessionDiagnosticFieldScheduledInputCount  = "scheduled_input_count"
	SessionDiagnosticFieldDispatchedInputCount = "dispatched_input_count"
	SessionDiagnosticFieldCompletedTurnCount   = "completed_turn_count"
	// These fields retain bounded provider terminal context for pending
	// continuations. Values are encoded as comma-separated call_id=value pairs.
	SessionDiagnosticFieldPendingContinuationStatuses = "pending_continuation_statuses"
	SessionDiagnosticFieldPendingContinuationDetails  = "pending_continuation_details"
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

// SessionDiagnosticRecord is one canonical structured diagnostic record. Fields
// carries exact structured values keyed by stable names; no human prose is
// included so automated responders can assert on it directly.
type SessionDiagnosticRecord struct {
	Event  string
	Fields map[string]string
}

// SessionDiagnosticSink receives structured session diagnostic records. Sinks
// are optional injection seams following the established
// SessionInferencer/WebSocketDialer precedent on SessionRunOptions; with no
// sink injected no diagnostic records are produced. Executable session tools
// still retain the lifecycle safety contract even when no sink is attached.
type SessionDiagnosticSink interface {
	RecordSessionDiagnostic(SessionDiagnosticRecord)
}

// SessionStreamObserver receives every stream delta consumed by a session
// runner, including tool-result deltas emitted after the session tool adapter
// has normalized their call identity. It is an optional observation seam and
// does not alter session behavior when nil.
type SessionStreamObserver func(messages.StreamMessage)

// ScheduledAudioInput schedules one raw PCM user-audio injection through the
// loop's existing audio-input seam (AgentLoop.SendAudioInput). The default
// completion-gated policy fires after AfterCompletedTurns assistant turns have
// completed; the active-response policy may fire at the immediately preceding
// response's non-terminal boundary. Its bytes are attributed to the then
// in-flight turn (turn index AfterCompletedTurns+1).
type ScheduledAudioInput struct {
	AfterCompletedTurns int
	PCM                 []byte
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
type failureFacts struct {
	classification string
	terminalReason string
	provenance     string
	outputState    string
	errorType      string
	code           string
	failingEvent   string
}

// audioTurnCounters tracks per-turn byte attribution between MESSAGE.END
// boundaries.
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
	turnAdmission  func(messages.StreamMessage) bool
	runtime        *sessionRuntimeObservationRecorder
	provider       string
	model          string
	sawSessionOpen bool
	sessionID      string
	// sessionUpdated is scoped to the current SESSION.OPEN round trip. A
	// subsequent SESSION.OPEN resets it so an acknowledgement from an older
	// connection cannot release a new connection's scheduled input.
	sessionUpdated         bool
	requireSessionUpdated  bool
	scheduledAudioDispatch ScheduledAudioDispatchPolicy
	activeResponse         bool
	activeResponseID       string
	completedResponseIDs   map[string]struct{}
	retiredResponseIDs     map[string]struct{}
	turnsCompleted         int
	scheduledInputs        int
	dispatchedInputs       int
	completedScheduled     int
	scheduledTurnBase      int
	scheduledTurnBaseSet   bool
	counters               audioTurnCounters
	totals                 audioTurnCounters
	pendingInputs          []ScheduledAudioInput

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

	failure *failureFacts

	emitOnce    sync.Once
	metricsOnce sync.Once
}

type toolContinuationState struct {
	toolName                    string
	responseID                  string
	providerCallObserved        bool
	resultAccepted              bool
	toolResponseComplete        bool
	continuationRequested       bool
	continuationResponseID      string
	continuationTerminalSeen    bool
	continuationStatus          string
	continuationStatusDetails   string
	continuationTerminalReason  messages.TerminalReason
	continuationOutputObserved  bool
	continuationFailureObserved bool
	continuationComplete        bool
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
		sink:                 sink,
		recorder:             recorder,
		productionSink:       productionSink,
		provider:             provider,
		model:                model,
		unresolvedToolCalls:  make(map[string]struct{}),
		acceptedToolCalls:    make(map[string]struct{}),
		toolResultRejections: make(map[string]messages.SessionSendStatus),
		toolLifecycleCh:      make(chan struct{}, 1),
		toolContinuations:    make(map[string]*toolContinuationState),
		completedResponseIDs: make(map[string]struct{}),
		retiredResponseIDs:   make(map[string]struct{}),
	}
}

func (o *sessionProgressObserver) setToolResultsEnabled(enabled bool) {
	if o == nil {
		return
	}
	o.toolStateMu.Lock()
	o.toolResultsEnabled = enabled
	o.toolStateMu.Unlock()
}

func (o *sessionProgressObserver) ensureToolStateLocked() {
	if o.unresolvedToolCalls == nil {
		o.unresolvedToolCalls = make(map[string]struct{})
	}
	if o.acceptedToolCalls == nil {
		o.acceptedToolCalls = make(map[string]struct{})
	}
	if o.toolResultRejections == nil {
		o.toolResultRejections = make(map[string]messages.SessionSendStatus)
	}
	if o.toolLifecycleCh == nil {
		o.toolLifecycleCh = make(chan struct{}, 1)
	}
	if o.toolContinuations == nil {
		o.toolContinuations = make(map[string]*toolContinuationState)
	}
}

func normalizeContinuationStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func sanitizeContinuationDetail(detail string) string {
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, detail)
	detail = strings.Join(strings.Fields(detail), " ")
	const maxDetailBytes = 256
	if len(detail) > maxDetailBytes {
		return detail[:maxDetailBytes]
	}
	return detail
}

// continuationCanCompleteLocked is deliberately stricter than a provider
// terminal boundary. A continuation is successful only when the provider
// reports completed (or a legacy provider omits status) and the assistant
// emitted customer-visible text, transcript, or audio.
func continuationCanCompleteLocked(state *toolContinuationState) bool {
	if state == nil || !state.resultAccepted || !state.continuationRequested || !state.toolResponseComplete || !state.continuationTerminalSeen {
		return false
	}
	status := normalizeContinuationStatus(state.continuationStatus)
	if state.continuationFailureObserved || (status != "" && status != "completed") {
		return false
	}
	if state.continuationTerminalReason != "" && state.continuationTerminalReason != messages.TerminalReasonProviderAuthoredCompletion && state.continuationTerminalReason != messages.TerminalReasonLoopSynthesizedCompletion {
		return false
	}
	return state.continuationOutputObserved
}

func continuationTerminalFailureLocked(state *toolContinuationState) bool {
	if state == nil || !state.resultAccepted || !state.continuationRequested || !state.toolResponseComplete || !state.continuationTerminalSeen || state.continuationComplete {
		return false
	}
	status := normalizeContinuationStatus(state.continuationStatus)
	if state.continuationFailureObserved || (status != "" && status != "completed") {
		return true
	}
	if state.continuationTerminalReason != "" && state.continuationTerminalReason != messages.TerminalReasonProviderAuthoredCompletion && state.continuationTerminalReason != messages.TerminalReasonLoopSynthesizedCompletion {
		return true
	}
	return !state.continuationOutputObserved
}

// observeProviderToolCall records the completed provider tool-call obligation.
// Empty IDs are deliberately ignored because they cannot be correlated with a
// later result.
func (o *sessionProgressObserver) observeProviderToolCall(v *messages.ToolCallEndValue) {
	if o == nil || v == nil {
		return
	}
	o.observeProviderToolCallWithID(v.ToolCallID, v.Name)
}

func (o *sessionProgressObserver) observeProviderToolCallWithID(callID, name string) {
	o.observeProviderToolCallWithIDForResponse(callID, name, "")
}

// noteToolResultAccepted resolves exactly one provider call after the
// provider-facing session send boundary reports success. Execution completion,
// queueing, and rejected sends do not reach this method.
func (o *sessionProgressObserver) noteToolResultAccepted(callID string) {
	if o == nil || strings.TrimSpace(callID) == "" {
		return
	}
	o.toolStateMu.Lock()
	o.ensureToolStateLocked()
	delete(o.unresolvedToolCalls, callID)
	o.acceptedToolCalls[callID] = struct{}{}
	state := o.toolContinuations[callID]
	if state == nil {
		state = &toolContinuationState{}
		o.toolContinuations[callID] = state
	}
	state.resultAccepted = true
	if continuationCanCompleteLocked(state) {
		state.continuationComplete = true
	}
	delete(o.toolResultRejections, callID)
	lifecycleCh := o.toolLifecycleCh
	o.toolStateMu.Unlock()

	// One wake-up is enough even when several results are accepted before the
	// session loop selects this branch: the close predicate observes the whole
	// current set, not a count of wake-ups.
	select {
	case lifecycleCh <- struct{}{}:
	default:
	}
}

// noteToolContinuationRequested advances every accepted result in the
// current provider batch at the explicit response.create send boundary. The
// control event carries no call ID because one provider response may continue
// several parallel function calls; accepted results are therefore the
// correlation set. The operation is idempotent for duplicate control events.
func (o *sessionProgressObserver) noteToolContinuationRequested() {
	if o == nil {
		return
	}
	o.toolStateMu.Lock()
	o.ensureToolStateLocked()
	changed := false
	for callID := range o.acceptedToolCalls {
		state := o.toolContinuations[callID]
		if state == nil {
			// The provider delta consumer can observe TOOLCALL.END after the
			// model runner has already accepted the result and response.create.
			// Preserve that early continuation request by creating a call-ID
			// placeholder for the later provider event to enrich.
			state = &toolContinuationState{resultAccepted: true}
			o.toolContinuations[callID] = state
		}
		if !state.resultAccepted || state.continuationComplete || state.continuationRequested {
			continue
		}
		state.continuationRequested = true
		if continuationCanCompleteLocked(state) {
			state.continuationComplete = true
		}
		changed = true
	}
	lifecycleCh := o.toolLifecycleCh
	o.toolStateMu.Unlock()
	if changed {
		select {
		case lifecycleCh <- struct{}{}:
		default:
		}
	}
}

// noteToolContinuationRequestedFor is used by complete-message providers.
// SendMessage may represent a whole rich batch, so the exact call is marked
// first and any already accepted sibling is advanced by the batch-level
// method as well.
func (o *sessionProgressObserver) noteToolContinuationRequestedFor(callID string) {
	if o == nil || strings.TrimSpace(callID) == "" {
		return
	}
	o.toolStateMu.Lock()
	o.ensureToolStateLocked()
	state := o.toolContinuations[callID]
	if state == nil {
		if _, accepted := o.acceptedToolCalls[callID]; accepted {
			state = &toolContinuationState{resultAccepted: true}
			o.toolContinuations[callID] = state
		}
	}
	if state != nil && state.resultAccepted {
		state.continuationRequested = true
		if continuationCanCompleteLocked(state) {
			state.continuationComplete = true
		}
	}
	o.toolStateMu.Unlock()
	o.noteToolContinuationRequested()
}

// toolLifecycleEvents wakes the session close controller whenever a result or
// its continuation changes state. The channel is intentionally coalescing: the
// close predicate always reads the complete state snapshot rather than
// interpreting one wake-up as one completed call.
func (o *sessionProgressObserver) toolLifecycleEvents() <-chan struct{} {
	if o == nil {
		return nil
	}
	o.toolStateMu.Lock()
	o.ensureToolStateLocked()
	ch := o.toolLifecycleCh
	o.toolStateMu.Unlock()
	return ch
}

// observeBufferedProviderToolLifecycle recovers provider tool identity that
// the engine has already committed to conversation history but that could not
// reach the consumer-facing delta buffer before a terminal shutdown. It only
// observes model/assistant tool events; tool-runner result deltas are not
// provider requests and must not create a second obligation.
func (o *sessionProgressObserver) observeBufferedProviderToolLifecycle(deltas []messages.StreamMessage) {
	if o == nil {
		return
	}
	for _, msg := range deltas {
		if msg.Role != messages.RoleAssistant && msg.ActorID != messages.Model {
			continue
		}
		switch v := msg.Value.(type) {
		case *messages.ToolCallStartValue:
			if v != nil {
				o.observeProviderToolCallStartForResponse(firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId), v.Name, msg.ResponseID)
			}
		case *messages.ToolCallDeltaValue:
			if v != nil {
				o.observeProviderToolCallStartForResponse(strings.TrimSpace(msg.ToolCallId), "", msg.ResponseID)
			}
		case *messages.ToolCallEndValue:
			if v != nil {
				o.observeProviderToolCallWithIDForResponse(firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId), v.Name, msg.ResponseID)
			}
		}
	}
}

func firstNonBlankToolCallID(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

// noteToolResultRejected remembers a failed provider-facing result send
// without resolving its outstanding call ID. A result can be rejected before
// the outer session consumer observes the provider's completed call delta, so
// rejection also registers the call as unresolved. It is intentionally
// idempotent; only the first rejection is retained so repeated attempts cannot
// rewrite the terminal status for a call.
func (o *sessionProgressObserver) noteToolResultRejected(callID string, outcome messages.SessionSendOutcome) {
	if o == nil || strings.TrimSpace(callID) == "" || outcome.OK() {
		return
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	o.ensureToolStateLocked()
	if _, accepted := o.acceptedToolCalls[callID]; accepted {
		return
	}
	o.unresolvedToolCalls[callID] = struct{}{}
	if _, recorded := o.toolResultRejections[callID]; !recorded {
		o.toolResultRejections[callID] = outcome.Status
	}
}

func (o *sessionProgressObserver) hasUnresolvedToolCalls() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	return len(o.unresolvedToolCalls) > 0
}

// hasPendingToolContinuations reports accepted results that still own the
// current turn. It intentionally includes the interval before the explicit
// continuation request so provider acceptance alone cannot release dispatch
// or close eligibility.
func (o *sessionProgressObserver) hasPendingToolContinuations() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	for _, state := range o.toolContinuations {
		if state != nil && state.resultAccepted && !state.continuationComplete {
			return true
		}
	}
	return false
}

// hasToolLifecycleObligation is the shared close/dispatch predicate for all
// tool kinds. An unresolved result and an accepted-but-not-terminal
// continuation are both incomplete provider work.
func (o *sessionProgressObserver) hasToolLifecycleObligation() bool {
	return o != nil && (o.hasUnresolvedToolCalls() || o.hasPendingToolContinuations())
}

// hasPendingImageContinuations is distinct from unresolved tool results. A
// read_image result can be accepted by the provider while its response.create
// continuation is still in flight.
func (o *sessionProgressObserver) hasPendingImageContinuations() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	for _, state := range o.toolContinuations {
		if state != nil && state.toolName == tools.ReadImageToolID && state.resultAccepted && !state.continuationComplete {
			return true
		}
	}
	return false
}

// pendingImageContinuationCallIDs returns a deterministic snapshot of calls
// whose accepted result still lacks a terminal model continuation.
func (o *sessionProgressObserver) pendingImageContinuationCallIDs() []string {
	if o == nil {
		return nil
	}
	o.toolStateMu.Lock()
	ids := make([]string, 0, len(o.toolContinuations))
	for id, state := range o.toolContinuations {
		if state != nil && state.toolName == tools.ReadImageToolID && state.resultAccepted && !state.continuationComplete {
			ids = append(ids, id)
		}
	}
	o.toolStateMu.Unlock()
	sort.Strings(ids)
	return ids
}

// pendingToolContinuationCallIDs returns accepted call IDs that have not yet
// reached a terminal continuation. IDs are sorted for deterministic errors and
// diagnostics.
func (o *sessionProgressObserver) pendingToolContinuationCallIDs() []string {
	if o == nil {
		return nil
	}
	o.toolStateMu.Lock()
	ids := make([]string, 0, len(o.toolContinuations))
	for id, state := range o.toolContinuations {
		if state != nil && state.resultAccepted && !state.continuationComplete {
			ids = append(ids, id)
		}
	}
	o.toolStateMu.Unlock()
	sort.Strings(ids)
	return ids
}

func (o *sessionProgressObserver) pendingNonImageToolContinuationCallIDs() []string {
	if o == nil {
		return nil
	}
	o.toolStateMu.Lock()
	ids := make([]string, 0, len(o.toolContinuations))
	for id, state := range o.toolContinuations {
		if state != nil && state.toolName != tools.ReadImageToolID && state.resultAccepted && !state.continuationComplete {
			ids = append(ids, id)
		}
	}
	o.toolStateMu.Unlock()
	sort.Strings(ids)
	return ids
}

func (o *sessionProgressObserver) pendingImageContinuationSnapshot() ([]string, map[string]string, map[string]string) {
	if o == nil {
		return nil, nil, nil
	}
	o.toolStateMu.Lock()
	ids := make([]string, 0, len(o.toolContinuations))
	statuses := make(map[string]string)
	details := make(map[string]string)
	for id, state := range o.toolContinuations {
		if state == nil || state.toolName != tools.ReadImageToolID || !state.resultAccepted || state.continuationComplete {
			continue
		}
		ids = append(ids, id)
		if state.continuationStatus != "" {
			statuses[id] = state.continuationStatus
		}
		if state.continuationStatusDetails != "" {
			details[id] = state.continuationStatusDetails
		}
	}
	o.toolStateMu.Unlock()
	sort.Strings(ids)
	return ids, statuses, details
}

func (o *sessionProgressObserver) pendingNonImageToolContinuationSnapshot() ([]string, map[string]string, map[string]string) {
	if o == nil {
		return nil, nil, nil
	}
	o.toolStateMu.Lock()
	ids := make([]string, 0, len(o.toolContinuations))
	statuses := make(map[string]string)
	details := make(map[string]string)
	for id, state := range o.toolContinuations {
		if state == nil || state.toolName == tools.ReadImageToolID || !state.resultAccepted || state.continuationComplete {
			continue
		}
		ids = append(ids, id)
		if state.continuationStatus != "" {
			statuses[id] = state.continuationStatus
		}
		if state.continuationStatusDetails != "" {
			details[id] = state.continuationStatusDetails
		}
	}
	o.toolStateMu.Unlock()
	sort.Strings(ids)
	return ids, statuses, details
}

// pendingContinuationMetadata returns deterministic provider context for all
// accepted continuations still pending at terminal time. The diagnostic uses
// the same call-ID correlation as the typed errors.
func (o *sessionProgressObserver) pendingContinuationMetadata() (map[string]string, map[string]string) {
	if o == nil {
		return nil, nil
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	statuses := make(map[string]string)
	details := make(map[string]string)
	for id, state := range o.toolContinuations {
		if state == nil || !state.resultAccepted || state.continuationComplete {
			continue
		}
		if state.continuationStatus != "" {
			statuses[id] = state.continuationStatus
		}
		if state.continuationStatusDetails != "" {
			details[id] = state.continuationStatusDetails
		}
	}
	return statuses, details
}

// unresolvedToolCallIDs returns a deterministic snapshot for lifecycle
// consumers and future terminal diagnostics.
func (o *sessionProgressObserver) unresolvedToolCallIDs() []string {
	if o == nil {
		return nil
	}
	o.toolStateMu.Lock()
	ids := make([]string, 0, len(o.unresolvedToolCalls))
	for id := range o.unresolvedToolCalls {
		ids = append(ids, id)
	}
	o.toolStateMu.Unlock()
	sort.Strings(ids)
	return ids
}

func (o *sessionProgressObserver) unresolvedToolResultSendStatuses() map[string]messages.SessionSendStatus {
	if o == nil {
		return nil
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	statuses := make(map[string]messages.SessionSendStatus, len(o.toolResultRejections))
	for id, status := range o.toolResultRejections {
		if _, outstanding := o.unresolvedToolCalls[id]; outstanding {
			statuses[id] = status
		}
	}
	return statuses
}

// scheduleAudioInputs registers caller-scheduled user audio injections.
func (o *sessionProgressObserver) scheduleAudioInputs(inputs []ScheduledAudioInput) {
	if o == nil {
		return
	}
	o.pendingInputs = append(o.pendingInputs, inputs...)
	o.scheduledInputs += len(inputs)
}

// observe consumes one delta crossing. It must run before any error-bearing
// message reaches output rendering so the failure facts survive drain errors.
func (o *sessionProgressObserver) observe(msg messages.StreamMessage) {
	if o == nil {
		return
	}
	if o.streamObserver != nil {
		o.streamObserver(msg)
	}
	msgResponseID := strings.TrimSpace(msg.ResponseID)
	responseLifecycleID := msgResponseID
	newResponseBoundary := false
	switch msg.Type {
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		// The normalized provider boundary is active from response creation
		// (MESSAGE.START) or the compatible audio-only start through its
		// terminal MESSAGE.END. Use the envelope type as the source of truth so
		// a provider with an empty value still participates in scheduling.
		newResponseBoundary = o.beginObservedResponse(msgResponseID)
		if !o.responseEventBelongsToActive(msgResponseID) {
			return
		}
	case messages.StreamTypeMessageEnd:
		// A terminal event is allowed to advance state only for the active
		// response owner. A late terminal for a previous response remains
		// observable through the stream observer but cannot complete a turn.
		if !o.ownsObservedResponseEnd(msgResponseID) {
			return
		}
		if !o.activeResponse && msgResponseID != "" {
			newResponseBoundary = o.beginObservedResponse(msgResponseID)
		}
		if responseLifecycleID == "" {
			responseLifecycleID = o.activeResponseID
		}
	case messages.StreamTypeSessionClose:
		o.activeResponse = false
		o.activeResponseID = ""
	default:
		if responseScopedStreamType(msg.Type) && !o.responseEventBelongsToActive(msgResponseID) {
			return
		}
	}
	if responseLifecycleID == "" {
		responseLifecycleID = o.activeResponseID
	}
	switch msg.Type {
	case messages.StreamTypeSessionOpen:
		o.sawSessionOpen = true
		o.sessionID = ""
		o.activeResponse = false
		o.activeResponseID = ""
		o.completedResponseIDs = make(map[string]struct{})
		o.retiredResponseIDs = make(map[string]struct{})
		o.resetObservedResponseState()
		if v, ok := msg.Value.(*messages.SessionOpenValue); ok && v != nil {
			o.sessionID = v.SessionID
		}
		o.sessionUpdated = false
	case messages.StreamTypeSessionUpdated:
		if !o.sawSessionOpen {
			break
		}
		updatedID := ""
		if v, ok := msg.Value.(*messages.SessionUpdatedValue); ok && v != nil {
			updatedID = v.SessionID
		}
		// Some compatible transports omit the session ID. When both sides
		// provide one, require an exact match to the current connection.
		if o.sessionID != "" && updatedID != "" && o.sessionID != updatedID {
			break
		}
		o.sessionUpdated = true
	}

	switch v := msg.Value.(type) {
	case *messages.SessionOpenValue:
		o.sawSessionOpen = true
	case *messages.MessageStartValue:
		if newResponseBoundary {
			o.resetObservedResponseState()
		}
	case *messages.TextStartValue, *messages.AudioStartValue, *messages.ReasoningStartValue,
		*messages.ImageStartValue, *messages.VideoStartValue, *messages.FileStartValue,
		*messages.EmbeddingStartValue, *messages.TranscriptStartValue:
		// Compatible providers may omit MESSAGE.START between persistent
		// responses. Any content-start boundary is enough to distinguish a new
		// response from a duplicate MESSAGE.END for the previous one.
		if newResponseBoundary {
			o.resetObservedResponseState()
		}
		o.toolStateMu.Lock()
		if o.messageEndSeen || o.assistantResponseDone {
			o.assistantOutputObserved = false
		}
		o.beginResponseContentLocked()
		o.assistantResponseDone = false
		o.toolStateMu.Unlock()
	case *messages.AudioDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityAudio, len(v.Content))
		o.toolStateMu.Lock()
		if o.messageEndSeen {
			o.assistantOutputObserved = false
		}
		o.beginResponseContentLocked()
		if assistantResponseDelta(msg) && len(v.Content) > 0 {
			o.responseOutputAudioBytes += uint64(len(v.Content))
		}
		if len(v.Content) > 0 && msg.Role != messages.RoleTool && msg.Role != messages.RoleUser {
			o.assistantOutputObserved = true
		}
		o.toolStateMu.Unlock()
	case *messages.TextDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityText, len(v.Content))
		o.toolStateMu.Lock()
		if o.messageEndSeen {
			o.assistantOutputObserved = false
		}
		o.beginResponseContentLocked()
		if assistantResponseDelta(msg) && len(v.Content) > 0 {
			o.responseOutputTextBytes += uint64(len(v.Content))
		}
		if strings.TrimSpace(v.Content) != "" && msg.Role != messages.RoleTool && msg.Role != messages.RoleUser {
			o.assistantOutputObserved = true
		}
		o.toolStateMu.Unlock()
	case *messages.TranscriptDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityText, len(v.Text))
		o.toolStateMu.Lock()
		if o.messageEndSeen {
			o.assistantOutputObserved = false
		}
		o.beginResponseContentLocked()
		if assistantResponseDelta(msg) && len(v.Text) > 0 {
			o.responseOutputTextBytes += uint64(len(v.Text))
		}
		if strings.TrimSpace(v.Text) != "" && msg.Role != messages.RoleTool && msg.Role != messages.RoleUser {
			o.assistantOutputObserved = true
		}
		o.toolStateMu.Unlock()
	case *messages.TranscriptEndValue:
		o.toolStateMu.Lock()
		if o.messageEndSeen {
			o.assistantOutputObserved = false
		}
		o.beginResponseContentLocked()
		if assistantResponseDelta(msg) && len(v.FullText) > 0 {
			o.responseOutputTextBytes += uint64(len(v.FullText))
		}
		if strings.TrimSpace(v.FullText) != "" && msg.Role != messages.RoleTool && msg.Role != messages.RoleUser {
			o.assistantOutputObserved = true
		}
		o.toolStateMu.Unlock()
	case *messages.ToolCallStartValue:
		o.observeProviderToolCallStartForResponse(firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId), v.Name, responseLifecycleID)
		o.toolDeltaSeen = false
		o.toolStateMu.Lock()
		o.beginResponseContentLocked()
		o.assistantResponseDone = false
		o.toolCallInTurn = o.toolResultsEnabled
		o.toolStateMu.Unlock()
	case *messages.ToolCallDeltaValue:
		o.observeProviderToolCallStartForResponse(strings.TrimSpace(msg.ToolCallId), "", responseLifecycleID)
		o.account(metrics.DirectionOutput, metrics.ModalityTool, len(v.PartialJSON))
		o.toolDeltaSeen = true
		o.toolStateMu.Lock()
		o.beginResponseContentLocked()
		o.assistantResponseDone = false
		o.toolCallInTurn = o.toolResultsEnabled
		o.toolStateMu.Unlock()
	case *messages.ToolCallEndValue:
		callID := firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId)
		o.observeProviderToolCallWithIDForResponse(callID, v.Name, responseLifecycleID)
		if !o.toolResultsEnabledForObservation() {
			o.emitToolCallRecord(v)
		}
		o.toolStateMu.Lock()
		o.beginResponseContentLocked()
		o.assistantResponseDone = false
		o.toolCallInTurn = o.toolResultsEnabled
		// A complete, correlated tool call is provider output even when the
		// caller has no executor. Tool-enabled sessions still keep the existing
		// intermediate-call/continuation lifecycle below; this flag only
		// prevents a valid tool-only response from being mistaken for empty.
		if strings.TrimSpace(callID) != "" && strings.TrimSpace(v.Name) != "" {
			o.responseActionableTool = true
		}
		if o.toolResultsEnabled {
			o.providerToolCallSeen = true
		}
		o.toolStateMu.Unlock()
		if !o.toolDeltaSeen {
			o.account(metrics.DirectionOutput, metrics.ModalityTool, len(v.Arguments))
		}
		o.toolDeltaSeen = false
	case *messages.MessageEndValue:
		o.noteProviderUsage(v.Usage)
		o.setAssistantResponseDone(false)
		outputPresent := o.responseHasAdmissibleOutput()
		candidate := o.observeProviderMessageEndForResponse(msg.Role, v, responseLifecycleID, outputPresent)
		admitted := candidate && outputPresent
		if admitted && o.turnAdmission != nil {
			admitted = o.turnAdmission(msg)
		}
		o.setAssistantResponseDone(admitted)
		o.toolStateMu.Lock()
		o.messageEndAdmitted = admitted
		o.toolStateMu.Unlock()
		if admitted {
			o.completeTurn()
			if o.admittedTurnObserver != nil {
				o.admittedTurnObserver(msg)
			}
		}
		o.finishObservedResponse(responseLifecycleID)
	case *messages.ErrorValue:
		o.captureFailureFromError(v)
	case *messages.SessionCloseValue:
		o.captureFailureFromClose(v)
		o.activeResponse = false
		o.activeResponseID = ""
	}
}

func (o *sessionProgressObserver) captureFailureFromError(v *messages.ErrorValue) {
	if o.failure != nil || v == nil || v.IsNonTerminal() {
		return
	}
	o.failure = factsFromErrorValue(v)
}

// factsFromErrorValue maps one typed ERROR stream value onto the canonical
// failure facts, applying the public taxonomy defaults for absent fields.
func factsFromErrorValue(v *messages.ErrorValue) *failureFacts {
	f := &failureFacts{
		classification: v.Classification,
		terminalReason: string(v.TerminalReason),
		provenance:     string(v.TerminalProvenance),
		outputState:    string(v.OutputState),
		errorType:      v.ErrorType,
		code:           v.Code,
		failingEvent:   string(messages.StreamTypeError),
	}
	if f.classification == "" {
		f.classification = providers.ErrorClassUnknown
	}
	if f.terminalReason == "" {
		f.terminalReason = string(messages.TerminalReasonTerminalFailure)
	}
	if f.provenance == "" {
		f.provenance = string(messages.TerminalProvenanceProvider)
	}
	if f.outputState == "" {
		f.outputState = string(messages.TerminalOutputNone)
	}
	return f
}

// captureFailureFromClose captures only failure-worthy session closes; clean,
// caller-authored completions are never failures. A provider_close terminal
// reason is a failure only when the model runner synthesized it because the
// provider transport died without an explicit close (marker reason
// "provider_closed"); an explicit wire session.closed is normal teardown.
func (o *sessionProgressObserver) captureFailureFromClose(v *messages.SessionCloseValue) {
	if o.failure != nil || v == nil {
		return
	}
	switch v.TerminalReason {
	case messages.TerminalReasonProviderClose:
		if v.Reason != "provider_closed" {
			return
		}
	case messages.TerminalReasonTerminalFailure,
		messages.TerminalReasonReplayDivergence,
		messages.TerminalReasonReplayIncomplete:
	default:
		return
	}
	f := &failureFacts{
		classification: v.Classification,
		terminalReason: string(v.TerminalReason),
		provenance:     string(v.TerminalProvenance),
		outputState:    string(v.OutputState),
		failingEvent:   string(messages.StreamTypeSessionClose),
	}
	if f.classification == "" {
		f.classification = providers.ErrorClassUnknown
	}
	if f.provenance == "" {
		f.provenance = string(messages.TerminalProvenanceSession)
	}
	if f.outputState == "" || v.TerminalReason == messages.TerminalReasonProviderClose {
		// The model runner synthesizes transport-death closes without output
		// knowledge; derive the state from what the stream actually delivered.
		f.outputState = deriveOutputState(o.sawSessionOpen, o.turnsCompleted)
	}
	o.failure = f
}

func (o *sessionProgressObserver) unresolvedToolResultFailureFacts(failingEvent string) *failureFacts {
	return &failureFacts{
		classification: SessionUnresolvedToolResultClassification,
		terminalReason: string(messages.TerminalReasonTerminalFailure),
		provenance:     string(messages.TerminalProvenanceSession),
		outputState:    deriveOutputState(o.sawSessionOpen, o.turnsCompleted),
		failingEvent:   failingEvent,
	}
}

func (o *sessionProgressObserver) imageContinuationFailureFacts(failingEvent string) *failureFacts {
	return &failureFacts{
		classification: SessionImageContinuationClassification,
		terminalReason: string(messages.TerminalReasonTerminalFailure),
		provenance:     string(messages.TerminalProvenanceSession),
		outputState:    deriveOutputState(o.sawSessionOpen, o.turnsCompleted),
		failingEvent:   failingEvent,
	}
}

func (o *sessionProgressObserver) toolContinuationFailureFacts(failingEvent string) *failureFacts {
	return &failureFacts{
		classification: SessionToolContinuationClassification,
		terminalReason: string(messages.TerminalReasonTerminalFailure),
		provenance:     string(messages.TerminalProvenanceSession),
		outputState:    deriveOutputState(o.sawSessionOpen, o.turnsCompleted),
		failingEvent:   failingEvent,
	}
}

func (o *sessionProgressObserver) scheduledAudioFailureFacts(failingEvent string) *failureFacts {
	return &failureFacts{
		classification: SessionScheduledAudioClassification,
		terminalReason: string(messages.TerminalReasonTerminalFailure),
		provenance:     string(messages.TerminalProvenanceSession),
		outputState:    deriveOutputState(o.sawSessionOpen, o.turnsCompleted),
		failingEvent:   failingEvent,
	}
}

// emitToolCallRecord reports a provider tool-call event that cannot be
// executed because this session has no tool executor. Tool-enabled sessions
// resolve the call through their participant-local executor instead.
func (o *sessionProgressObserver) emitToolCallRecord(v *messages.ToolCallEndValue) {
	if o.sink == nil || v == nil {
		return
	}
	o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
		Event: SessionDiagnosticEventToolCall,
		Fields: map[string]string{
			fieldToolName:              v.Name,
			fieldToolCallID:            v.ToolCallID,
			fieldFailureClassification: providers.ErrorClassUnsupportedRequest,
			fieldFailureReason:         "no_tool_executor_in_session_runtime",
			fieldTurnIndex:             strconv.Itoa(o.turnsCompleted + 1),
		},
	})
}

// finish enriches err with any unresolved lifecycle failure, emits terminal
// records, and returns the enriched error so termination paths read as plain
// returns.
func (o *sessionProgressObserver) finish(err error) error {
	if o == nil {
		return err
	}
	err = withUnresolvedToolResults(err, o)
	err = withPendingToolContinuations(err, o)
	err = withPendingImageContinuations(err, o)
	o.emitTerminal(err)
	o.emitMetricsMatrix()
	if o.runtime != nil {
		o.runtime.terminalWithAccounting(o.turnsCompleted, err, o.finalAccounting())
	}
	return err
}

// finalAccounting captures the runtime-owned production counters only after
// the session consumer has drained every accounted delta. The optional
// MetricsRecorder and SessionStreamObserver are deliberately not consulted.
func (o *sessionProgressObserver) finalAccounting() *SessionFinalAccounting {
	if o == nil {
		return nil
	}
	accounting := &SessionFinalAccounting{
		PromptTokens:     o.usagePrompt,
		CompletionTokens: o.usageCompletion,
		TotalTokens:      o.usageTotal,
		ReasoningTokens:  o.usageReasoning,
		UsageSemantics:   SessionTokenUsageIncremental,
	}
	if o.productionSink != nil {
		accounting.Metrics = o.productionSink.Snapshot()
	}
	return accounting
}

// emitMetricsMatrix emits the terminal per-direction/per-modality byte matrix
// exactly once per run, after every delta it summarizes has crossed. The
// provider-reported message-end token usage rides alongside so operators can
// compare both accounting sources; byte counts and token counts measure
// different units and are not expected to be numerically equal.
func (o *sessionProgressObserver) emitMetricsMatrix() {
	if o == nil || o.sink == nil {
		return
	}
	o.metricsOnce.Do(func() {
		fields := map[string]string{
			fieldProvider:         o.provider,
			fieldModel:            o.model,
			fieldTurnsCompleted:   strconv.Itoa(o.turnsCompleted),
			fieldInputAudioBytes:  strconv.FormatUint(o.totals.inputAudio, 10),
			fieldInputTextBytes:   strconv.FormatUint(o.totals.inputText, 10),
			fieldOutputAudioBytes: strconv.FormatUint(o.totals.outAudio, 10),
			fieldOutputTextBytes:  strconv.FormatUint(o.totals.outText, 10),
			fieldOutputToolBytes:  strconv.FormatUint(o.totals.outTool, 10),
		}
		if o.usageSeen {
			fields[fieldProviderPromptTokens] = strconv.FormatUint(o.usagePrompt, 10)
			fields[fieldProviderCompletionTokens] = strconv.FormatUint(o.usageCompletion, 10)
			fields[fieldProviderTotalTokens] = strconv.FormatUint(o.usageTotal, 10)
			fields[fieldProviderReasoningTokens] = strconv.FormatUint(o.usageReasoning, 10)
		}
		o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
			Event:  SessionDiagnosticEventMetrics,
			Fields: fields,
		})
	})
}

func deriveOutputState(sawSessionOpen bool, turnsCompleted int) string {
	switch {
	case !sawSessionOpen:
		return string(messages.TerminalOutputNone)
	case turnsCompleted > 0:
		return string(messages.TerminalOutputPartial)
	default:
		return string(messages.TerminalOutputNone)
	}
}
