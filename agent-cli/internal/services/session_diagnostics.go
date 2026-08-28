// This file contains the session diagnostic contract: the canonical structured
// failure record, per-turn accounting records, unexecutable tool-call records,
// and the observer that derives them from the session loop's delta stream.
//
// Field names and values documented here are a stable operator contract; see
// docs/architecture/s2s-session-diagnostic-contract.md.
package services

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

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
	// SessionDiagnosticEventTurn is emitted once per completed assistant turn
	// (MESSAGE.END) with per-turn input/output byte accounting.
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
// loop's existing audio-input seam (AgentLoop.SendAudioInput). The injection
// fires as soon as AfterCompletedTurns assistant turns have completed, and its
// bytes are attributed to the then in-flight turn (turn index
// AfterCompletedTurns+1).
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
	// turnAdmission is an optional owner-controlled admission boundary for
	// MESSAGE.END. Returning false keeps the raw stream event observable but
	// prevents it from advancing completed-turn state or evidence.
	turnAdmission  func(messages.StreamMessage) bool
	runtime        *sessionRuntimeObservationRecorder
	provider       string
	model          string
	sawSessionOpen bool
	sessionID      string
	// sessionUpdated is scoped to the current SESSION.OPEN round trip. A
	// subsequent SESSION.OPEN resets it so an acknowledgement from an older
	// connection cannot release a new connection's scheduled input.
	sessionUpdated        bool
	requireSessionUpdated bool
	turnsCompleted        int
	scheduledInputs       int
	dispatchedInputs      int
	completedScheduled    int
	scheduledTurnBase     int
	scheduledTurnBaseSet  bool
	counters              audioTurnCounters
	totals                audioTurnCounters
	pendingInputs         []ScheduledAudioInput

	toolStateMu           sync.Mutex
	unresolvedToolCalls   map[string]struct{}
	acceptedToolCalls     map[string]struct{}
	toolResultRejections  map[string]messages.SessionSendStatus
	toolLifecycleCh       chan struct{}
	toolContinuations     map[string]*toolContinuationState
	toolCallInTurn        bool
	messageEndSeen        bool
	providerToolCallSeen  bool
	assistantResponseDone bool
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
	toolName                 string
	providerCallObserved     bool
	resultAccepted           bool
	toolResponseComplete     bool
	continuationRequested    bool
	continuationTerminalSeen bool
	continuationComplete     bool
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

// observeProviderToolCallStart records a provider tool call as soon as its
// correlated ID is available. A provider can terminate between TOOLCALL.START
// and TOOLCALL.END; retaining that partial request prevents cancellation or
// transport close from looking like a clean session.
func (o *sessionProgressObserver) observeProviderToolCallStart(callID, name string) {
	if o == nil || strings.TrimSpace(callID) == "" {
		return
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	o.ensureToolStateLocked()
	if !o.toolResultsEnabled {
		return
	}
	_, accepted := o.acceptedToolCalls[callID]
	state := o.toolContinuations[callID]
	if state == nil {
		state = &toolContinuationState{}
		o.toolContinuations[callID] = state
	}
	if strings.TrimSpace(name) != "" {
		state.toolName = name
	}
	state.providerCallObserved = true
	state.resultAccepted = accepted
	o.providerToolCallSeen = true
	if accepted {
		return
	}
	o.unresolvedToolCalls[callID] = struct{}{}
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
	if o == nil || strings.TrimSpace(callID) == "" {
		return
	}
	o.observeProviderToolCallStart(callID, name)
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
	if state.continuationRequested && state.continuationTerminalSeen && state.toolResponseComplete {
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
		if state.resultAccepted && state.continuationTerminalSeen && state.toolResponseComplete {
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
		if state.continuationTerminalSeen && state.toolResponseComplete {
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
				o.observeProviderToolCallStart(firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId), v.Name)
			}
		case *messages.ToolCallDeltaValue:
			if v != nil {
				o.observeProviderToolCallStart(strings.TrimSpace(msg.ToolCallId), "")
			}
		case *messages.ToolCallEndValue:
			if v != nil {
				o.observeProviderToolCallWithID(firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId), v.Name)
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

// account is the single observation seam: every counted byte crosses here
// exactly once, forwarding to the metrics recorder and advancing both the
// per-turn counters and the lifetime totals in one step. Recording failures
// are diagnostics-only and never alter session behavior.
func (o *sessionProgressObserver) account(direction metrics.Direction, modality metrics.Modality, n int) {
	if o == nil || n <= 0 {
		return
	}
	if o.productionSink != nil {
		_ = o.productionSink.Record(direction, modality, int64(n))
	}
	if o.recorder != nil {
		_ = o.recorder.Record(direction, modality, int64(n))
	}
	o.counters.account(direction, modality, uint64(n))
	o.totals.account(direction, modality, uint64(n))
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
	turnAdmitted := true
	if msg.Type == messages.StreamTypeMessageEnd && o.turnAdmission != nil {
		turnAdmitted = o.turnAdmission(msg)
	}
	if o.streamObserver != nil {
		o.streamObserver(msg)
	}
	if msg.Type == messages.StreamTypeMessageEnd && !turnAdmitted {
		return
	}
	switch msg.Type {
	case messages.StreamTypeSessionOpen:
		o.sawSessionOpen = true
		o.sessionID = ""
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
		o.toolStateMu.Lock()
		o.assistantResponseDone = false
		o.toolCallInTurn = false
		o.messageEndSeen = false
		o.toolStateMu.Unlock()
	case *messages.TextStartValue, *messages.AudioStartValue, *messages.ReasoningStartValue,
		*messages.ImageStartValue, *messages.VideoStartValue, *messages.FileStartValue,
		*messages.EmbeddingStartValue, *messages.TranscriptStartValue:
		// Compatible providers may omit MESSAGE.START between persistent
		// responses. Any content-start boundary is enough to distinguish a new
		// response from a duplicate MESSAGE.END for the previous one.
		o.toolStateMu.Lock()
		o.assistantResponseDone = false
		o.messageEndSeen = false
		o.toolStateMu.Unlock()
	case *messages.AudioDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityAudio, len(v.Content))
		o.toolStateMu.Lock()
		o.messageEndSeen = false
		o.toolStateMu.Unlock()
	case *messages.TextDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityText, len(v.Content))
		o.toolStateMu.Lock()
		o.messageEndSeen = false
		o.toolStateMu.Unlock()
	case *messages.TranscriptDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityText, len(v.Text))
		o.toolStateMu.Lock()
		o.messageEndSeen = false
		o.toolStateMu.Unlock()
	case *messages.ToolCallStartValue:
		o.observeProviderToolCallStart(firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId), v.Name)
		o.toolDeltaSeen = false
		o.toolStateMu.Lock()
		o.assistantResponseDone = false
		o.toolCallInTurn = o.toolResultsEnabled
		o.toolStateMu.Unlock()
	case *messages.ToolCallDeltaValue:
		o.observeProviderToolCallStart(strings.TrimSpace(msg.ToolCallId), "")
		o.account(metrics.DirectionOutput, metrics.ModalityTool, len(v.PartialJSON))
		o.toolDeltaSeen = true
		o.toolStateMu.Lock()
		o.assistantResponseDone = false
		o.toolCallInTurn = o.toolResultsEnabled
		o.toolStateMu.Unlock()
	case *messages.ToolCallEndValue:
		o.observeProviderToolCallWithID(firstNonBlankToolCallID(v.ToolCallID, msg.ToolCallId), v.Name)
		if !o.toolResultsEnabledForObservation() {
			o.emitToolCallRecord(v)
		}
		o.toolStateMu.Lock()
		o.assistantResponseDone = false
		o.toolCallInTurn = o.toolResultsEnabled
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
		if o.observeProviderMessageEnd(msg.Role) {
			o.completeTurn()
		}
	case *messages.ErrorValue:
		o.captureFailureFromError(v)
	case *messages.SessionCloseValue:
		o.captureFailureFromClose(v)
	}
}

func (o *sessionProgressObserver) toolResultsEnabledForObservation() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	return o.toolResultsEnabled
}

// observeProviderMessageEnd advances the provider response state. The first
// MESSAGE.END after a tool call closes the provider's function-call response;
// only a later non-tool MESSAGE.END can complete an accepted continuation.
// The bool return reports whether this boundary is one new, terminal assistant
// response and should therefore count as a completed turn.
func (o *sessionProgressObserver) observeProviderMessageEnd(role messages.Role) bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	toolTurn := o.toolCallInTurn
	duplicateEnd := o.messageEndSeen
	o.messageEndSeen = true
	continuationChanged := false
	if toolTurn {
		for _, state := range o.toolContinuations {
			if state != nil && state.providerCallObserved && !state.toolResponseComplete {
				state.toolResponseComplete = true
			}
		}
	} else if role != messages.RoleTool {
		for _, state := range o.toolContinuations {
			if state == nil || !state.toolResponseComplete || state.continuationComplete {
				continue
			}
			state.continuationTerminalSeen = true
			if state.resultAccepted && state.continuationRequested {
				state.continuationComplete = true
				continuationChanged = true
			}
		}
	}
	pending := false
	for _, state := range o.toolContinuations {
		if state != nil && state.resultAccepted && !state.continuationComplete {
			pending = true
			break
		}
	}
	terminalAssistantResponse := role != messages.RoleTool && !toolTurn && len(o.unresolvedToolCalls) == 0 && !pending
	if terminalAssistantResponse {
		o.assistantResponseDone = true
	}
	o.toolCallInTurn = false
	lifecycleCh := o.toolLifecycleCh
	o.toolStateMu.Unlock()
	if continuationChanged {
		select {
		case lifecycleCh <- struct{}{}:
		default:
		}
	}
	return terminalAssistantResponse && !duplicateEnd
}

// assistantResponseCompleted reports whether a non-tool assistant response
// reached MESSAGE.END without another tool call still in the turn.
func (o *sessionProgressObserver) assistantResponseCompleted() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	return o.assistantResponseDone
}

func (o *sessionProgressObserver) providerToolCallObserved() bool {
	if o == nil {
		return false
	}
	o.toolStateMu.Lock()
	defer o.toolStateMu.Unlock()
	return o.providerToolCallSeen
}

// noteUserTextInput accounts for prompt text injected into the session as
// user input.
func (o *sessionProgressObserver) noteUserTextInput(text string) {
	if o == nil || text == "" {
		return
	}
	o.account(metrics.DirectionInput, metrics.ModalityText, len(text))
}

// dispatchScheduledInputs delivers due scheduled audio through the loop's
// existing SendAudioInput seam and attributes the bytes to the in-flight turn.
func (o *sessionProgressObserver) dispatchScheduledInputs(ctx context.Context, loop scheduledSessionInputSender) error {
	if o == nil || loop == nil {
		return nil
	}
	// A response boundary is not enough to release the next spoken turn. The
	// current provider call must have its accepted result and terminal
	// continuation first; this check keeps scheduling independent of the
	// particular input source that created the call.
	if o.hasToolLifecycleObligation() || !o.scheduledAudioReady() {
		return nil
	}
	for len(o.pendingInputs) > 0 && o.pendingInputs[0].AfterCompletedTurns <= o.turnsCompleted && !o.hasToolLifecycleObligation() {
		input := o.pendingInputs[0]
		inputIndex := o.scheduledInputs - len(o.pendingInputs) + 1
		if err := loop.SendAudioInput(ctx, input.PCM); err != nil {
			return fmt.Errorf("send scheduled audio input %d: %w", inputIndex, err)
		}
		if input.EndOfTurn {
			if err := loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); err != nil {
				return fmt.Errorf("send scheduled audio input %d end-of-turn: %w", inputIndex, err)
			}
		}
		if !o.scheduledTurnBaseSet {
			o.scheduledTurnBase = o.turnsCompleted
			o.scheduledTurnBaseSet = true
		}
		o.dispatchedInputs++
		o.pendingInputs = o.pendingInputs[1:]
		o.account(metrics.DirectionInput, metrics.ModalityAudio, len(input.PCM))
	}
	return nil
}

// scheduledAudioReady reports whether the scheduler may release its next
// input. The acknowledgement requirement is opt-in so replay and existing
// non-OpenAI session paths preserve their previous behavior.
func (o *sessionProgressObserver) scheduledAudioReady() bool {
	return o == nil || !o.requireSessionUpdated || (o.sawSessionOpen && o.sessionUpdated)
}

func (o *sessionProgressObserver) scheduledAudioAwaitingConfiguration() bool {
	return o != nil && o.requireSessionUpdated && len(o.pendingInputs) > 0 && !o.scheduledAudioReady()
}

// scheduledAudioComplete reports whether every scheduled input has been
// accepted and its corresponding assistant response has crossed MESSAGE.END.
// It is intentionally separate from replay capture inspection: live planning
// owns the decision to close after the schedule, while replay follows its
// captured lifecycle.
func (o *sessionProgressObserver) scheduledAudioComplete() bool {
	return o != nil && o.scheduledInputs > 0 && len(o.pendingInputs) == 0 && o.completedScheduled >= o.scheduledInputs && !o.hasToolLifecycleObligation()
}

func (o *sessionProgressObserver) scheduledAudioIncomplete() bool {
	return o != nil && o.scheduledInputs > 0 && !o.scheduledAudioComplete()
}

// scheduledAudioCounts returns the terminal schedule counters in a stable
// order. Completed is the number of scheduled inputs whose assistant response
// reached MESSAGE.END; it is distinct from the total session turn count when
// a prompt or seed turn precedes scheduled audio.
func (o *sessionProgressObserver) scheduledAudioCounts() (completed, dispatched, scheduled int) {
	if o == nil {
		return 0, 0, 0
	}
	return o.completedScheduled, o.dispatchedInputs, o.scheduledInputs
}

// noteProviderUsage accumulates the provider-reported token usage delivered on
// MESSAGE.END. Each value is an incremental contribution for the completed
// turn; the terminal runtime observation publishes the resulting
// session-cumulative totals.
func (o *sessionProgressObserver) noteProviderUsage(usage messages.TokenUsage) {
	if o == nil {
		return
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 || usage.ReasoningTokens < 0 {
		return
	}
	o.usagePrompt += uint64(usage.PromptTokens)
	o.usageCompletion += uint64(usage.CompletionTokens)
	o.usageTotal += uint64(usage.TotalTokens)
	o.usageReasoning += uint64(usage.ReasoningTokens)
	if usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 || usage.ReasoningTokens != 0 {
		o.usageSeen = true
	}
}

// completeTurn closes the current turn boundary and emits the per-turn record.
func (o *sessionProgressObserver) completeTurn() {
	o.turnsCompleted++
	if o.scheduledTurnBaseSet && o.turnsCompleted > o.scheduledTurnBase {
		completed := o.turnsCompleted - o.scheduledTurnBase
		if completed > o.dispatchedInputs {
			completed = o.dispatchedInputs
		}
		if completed > o.scheduledInputs {
			completed = o.scheduledInputs
		}
		o.completedScheduled = completed
	}
	if o.runtime != nil {
		o.runtime.turnCompleted(o.turnsCompleted)
	}
	if o.sink != nil {
		o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
			Event: SessionDiagnosticEventTurn,
			Fields: map[string]string{
				fieldTurnIndex:        strconv.Itoa(o.turnsCompleted),
				fieldInputAudioBytes:  strconv.FormatUint(o.counters.inputAudio, 10),
				fieldOutputToolBytes:  strconv.FormatUint(o.counters.outTool, 10),
				fieldInputTextBytes:   strconv.FormatUint(o.counters.inputText, 10),
				fieldOutputAudioBytes: strconv.FormatUint(o.counters.outAudio, 10),
				fieldOutputTextBytes:  strconv.FormatUint(o.counters.outText, 10),
			},
		})
	}
	o.counters.reset()
}

func (o *sessionProgressObserver) captureFailureFromError(v *messages.ErrorValue) {
	if o.failure != nil || v == nil {
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
