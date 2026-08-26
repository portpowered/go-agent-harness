// This file contains the session diagnostic contract: the canonical structured
// failure record, per-turn accounting records, unexecutable tool-call records,
// and the observer that derives them from the session loop's delta stream.
//
// Field names and values documented here are a stable operator contract; see
// docs/architecture/s2s-session-diagnostic-contract.md.
package services

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
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

	fieldToolName              = "tool_name"
	fieldToolCallID            = "tool_call_id"
	fieldFailureClassification = "failure_classification"
	fieldFailureReason         = "failure_reason"
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
// sink injected no diagnostic records are produced and runtime behavior is
// unchanged.
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
// from the delta stream consumed by the session runner. It is owned by the
// runner's single consumer goroutine; no internal locking is required except
// for the exactly-once terminal emission guard.
type sessionProgressObserver struct {
	sink           SessionDiagnosticSink
	recorder       metrics.Recorder
	streamObserver SessionStreamObserver
	provider       string
	model          string
	sawSessionOpen bool
	turnsCompleted int
	counters       audioTurnCounters
	totals         audioTurnCounters
	pendingInputs  []ScheduledAudioInput

	// toolDeltaSeen tracks whether the in-flight provider tool call streamed
	// TOOLCALL.DELTA bytes, so a terminal TOOLCALL.END carrying full arguments
	// is counted only when no deltas preceded it.
	toolDeltaSeen bool

	usagePrompt     uint64
	usageCompletion uint64
	usageTotal      uint64
	usageSeen       bool

	failure *failureFacts

	emitOnce    sync.Once
	metricsOnce sync.Once
}

func newSessionProgressObserver(sink SessionDiagnosticSink, recorder metrics.Recorder, provider, model string) *sessionProgressObserver {
	return &sessionProgressObserver{
		sink:     sink,
		recorder: recorder,
		provider: provider,
		model:    model,
	}
}

// account is the single observation seam: every counted byte crosses here
// exactly once, forwarding to the metrics recorder and advancing both the
// per-turn counters and the lifetime totals in one step. Recording failures
// are diagnostics-only and never alter session behavior.
func (o *sessionProgressObserver) account(direction metrics.Direction, modality metrics.Modality, n int) {
	if o == nil || n <= 0 {
		return
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
	switch v := msg.Value.(type) {
	case *messages.SessionOpenValue:
		o.sawSessionOpen = true
	case *messages.AudioDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityAudio, len(v.Content))
	case *messages.TextDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityText, len(v.Content))
	case *messages.TranscriptDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityText, len(v.Text))
	case *messages.ToolCallStartValue:
		o.toolDeltaSeen = false
	case *messages.ToolCallDeltaValue:
		o.account(metrics.DirectionOutput, metrics.ModalityTool, len(v.PartialJSON))
		o.toolDeltaSeen = true
	case *messages.ToolCallEndValue:
		o.emitToolCallRecord(v)
		if !o.toolDeltaSeen {
			o.account(metrics.DirectionOutput, metrics.ModalityTool, len(v.Arguments))
		}
		o.toolDeltaSeen = false
	case *messages.MessageEndValue:
		o.noteProviderUsage(v.Usage)
		o.completeTurn()
	case *messages.ErrorValue:
		o.captureFailureFromError(v)
	case *messages.SessionCloseValue:
		o.captureFailureFromClose(v)
	}
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
func (o *sessionProgressObserver) dispatchScheduledInputs(ctx context.Context, loop *agentloop.AgentLoop) {
	if o == nil || loop == nil {
		return
	}
	for len(o.pendingInputs) > 0 && o.pendingInputs[0].AfterCompletedTurns <= o.turnsCompleted {
		input := o.pendingInputs[0]
		o.pendingInputs = o.pendingInputs[1:]
		if err := loop.SendAudioInput(ctx, input.PCM); err != nil {
			continue
		}
		o.account(metrics.DirectionInput, metrics.ModalityAudio, len(input.PCM))
	}
}

// noteProviderUsage accumulates the provider-reported token usage delivered on
// MESSAGE.END so the terminal metrics matrix can surface both accounting
// sources side by side.
func (o *sessionProgressObserver) noteProviderUsage(usage messages.TokenUsage) {
	if o == nil {
		return
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 {
		return
	}
	o.usagePrompt += uint64(usage.PromptTokens)
	o.usageCompletion += uint64(usage.CompletionTokens)
	o.usageTotal += uint64(usage.TotalTokens)
	if usage.PromptTokens != 0 || usage.CompletionTokens != 0 || usage.TotalTokens != 0 {
		o.usageSeen = true
	}
}

// completeTurn closes the current turn boundary and emits the per-turn record.
func (o *sessionProgressObserver) completeTurn() {
	o.turnsCompleted++
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

// emitToolCallRecord reports a provider tool-call event that cannot be
// executed. The session runtime has no tool execution path, so every provider
// tool call is unexecutable by construction and must not pass silently.
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

// emitTerminal emits at most one canonical failure record per session run. A
// captured stream failure always wins; otherwise a non-cancellation run error
// becomes a synthesized connect/run-phase failure. Clean runs emit nothing.
func (o *sessionProgressObserver) emitTerminal(runErr error) {
	if o == nil || o.sink == nil {
		return
	}
	o.emitOnce.Do(func() {
		f := o.failure
		if f == nil {
			if runErr == nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
				return
			}
			var deltaErr *engine.StreamDeltaError
			if errors.As(runErr, &deltaErr) && deltaErr.Value != nil {
				// The engine terminates the hot loop on ERROR deltas and the
				// typed value may never cross the consumer deltas; recover the
				// canonical facts from the run error itself.
				f = factsFromErrorValue(deltaErr.Value)
			}
		}
		if f == nil {
			classification := providers.ErrorClassification(runErr)
			if classification == "" {
				classification = providers.ErrorClassUnknown
			}
			failingEvent := failingEventRun
			if !o.sawSessionOpen {
				failingEvent = failingEventConnect
			}
			f = &failureFacts{
				classification: classification,
				terminalReason: string(messages.TerminalReasonTerminalFailure),
				provenance:     string(messages.TerminalProvenanceCLI),
				outputState:    deriveOutputState(o.sawSessionOpen, o.turnsCompleted),
				failingEvent:   failingEvent,
			}
		}
		fields := map[string]string{
			fieldClassification:     f.classification,
			fieldTerminalReason:     f.terminalReason,
			fieldTerminalProvenance: f.provenance,
			fieldOutputState:        f.outputState,
			fieldProvider:           o.provider,
			fieldModel:              o.model,
			fieldTurnsCompleted:     strconv.Itoa(o.turnsCompleted),
			fieldFailingEvent:       f.failingEvent,
		}
		if f.errorType != "" {
			fields[fieldProviderErrorType] = f.errorType
		}
		if f.code != "" {
			fields[fieldProviderErrorCode] = f.code
		}
		o.sink.RecordSessionDiagnostic(SessionDiagnosticRecord{
			Event:  SessionDiagnosticEventFailure,
			Fields: fields,
		})
	})
}

// finish emits the terminal diagnostic record and returns err unchanged so
// termination paths read as plain returns.
func (o *sessionProgressObserver) finish(err error) error {
	o.emitTerminal(err)
	o.emitMetricsMatrix()
	return err
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
