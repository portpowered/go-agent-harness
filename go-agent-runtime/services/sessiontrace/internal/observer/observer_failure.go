package observer

import (
	"errors"
	"strconv"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	m "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

func (o *observerState) failureSnapshot() *failureFacts {
	if o == nil {
		return nil
	}
	o.livenessMu.Lock()
	defer o.livenessMu.Unlock()
	if o.failure == nil {
		return nil
	}
	copy := *o.failure
	return &copy
}

func (o *observerState) clearFailure() {
	if o == nil {
		return
	}
	o.livenessMu.Lock()
	o.failure = nil
	o.livenessMu.Unlock()
}

func (o *observerState) captureFailureFromError(value *m.ErrorValue) {
	facts, err := normalizeErrorFacts(value)
	if facts != nil {
		o.acceptFailureObservation(facts, err)
	}
}

func factsFromSessionRunError(err error) *failureFacts {
	if err == nil {
		return nil
	}
	var streamErr *engine.StreamDeltaError
	if !errors.As(err, &streamErr) || streamErr == nil {
		return nil
	}
	returnFacts, _ := normalizeErrorFacts(streamErr.Value)
	return returnFacts
}

func (o *observerState) acceptFailureObservation(facts *failureFacts, err error) bool {
	if o == nil || facts == nil || facts.failingEvent == "" {
		return false
	}
	o.livenessMu.Lock()
	if o.failure != nil {
		o.livenessMu.Unlock()
		return false
	}
	copy := *facts
	o.failure = &copy
	o.livenessMu.Unlock()
	if o.notifyFailureObservation(sessionTerminalObservationFromFailure(&copy, effectiveFailureError(&copy, err))) {
		return true
	}
	o.clearFailure()
	return false
}

func (o *observerState) captureFailureFromClose(value *m.SessionCloseValue) {
	if o == nil || value == nil {
		return
	}
	facts := normalizeCloseFacts(value, o.sawSessionOpen, o.turnsCompleted)
	if facts != nil {
		o.acceptFailureObservation(facts, nil)
	}
}

func (o *observerState) emitToolCallRecord(value *m.ToolCallEndValue) {
	if o == nil || o.sink == nil || value == nil {
		return
	}
	o.sink.RecordSessionDiagnostic(sessiontrace.DiagnosticRecord{
		Event: sessiontrace.SessionDiagnosticEventToolCall,
		Fields: map[string]string{
			"tool_name":              value.Name,
			"tool_call_id":           value.ToolCallID,
			"failure_classification": "unsupported_request",
			"failure_reason":         "no_tool_executor_in_session_runtime",
			"turn_index":             strconv.Itoa(o.turnsCompleted + 1),
		},
	})
}

func deriveOutputState(open bool, turns int) string {
	if !open || turns <= 0 {
		return string(m.TerminalOutputNone)
	}
	return string(m.TerminalOutputPartial)
}

func normalizeErrorFacts(value *m.ErrorValue) (*failureFacts, error) {
	if value == nil || value.IsNonTerminal() || ignoredCancellation(value) {
		return nil, nil
	}
	facts := &failureFacts{
		classification: value.Classification,
		terminalReason: string(value.TerminalReason),
		provenance:     string(value.TerminalProvenance),
		outputState:    string(value.OutputState),
		errorType:      value.ErrorType,
		code:           value.Code,
		failingEvent:   string(m.StreamTypeError),
	}
	if facts.classification == "" {
		facts.classification = "unknown"
	}
	if facts.terminalReason == "" {
		facts.terminalReason = string(m.TerminalReasonTerminalFailure)
	}
	if facts.provenance == "" {
		facts.provenance = string(m.TerminalProvenanceProvider)
	}
	if facts.outputState == "" {
		facts.outputState = string(m.TerminalOutputNone)
	}
	var err error
	if value.Err != nil {
		err = value.Err
	} else if value.Message != "" {
		err = errors.New(value.Message)
	}
	return facts, err
}

func ignoredCancellation(value *m.ErrorValue) bool {
	return value.TerminalReason == m.TerminalReasonCancellation ||
		(value.Classification == "cancellation" && value.TerminalReason == "") ||
		value.Classification == "room_bound_cancelled"
}

func normalizeCloseFacts(value *m.SessionCloseValue, open bool, turns int) *failureFacts {
	if value == nil {
		return nil
	}
	if value.TerminalReason == m.TerminalReasonProviderClose && value.Reason != "provider_closed" {
		return nil
	}
	switch value.TerminalReason {
	case m.TerminalReasonProviderClose, m.TerminalReasonTerminalFailure, m.TerminalReasonReplayDivergence, m.TerminalReasonReplayIncomplete:
	default:
		return nil
	}
	facts := &failureFacts{
		classification: value.Classification,
		terminalReason: string(value.TerminalReason),
		provenance:     string(value.TerminalProvenance),
		outputState:    string(value.OutputState),
		failingEvent:   string(m.StreamTypeSessionClose),
	}
	if facts.classification == "" {
		facts.classification = "unknown"
	}
	if facts.provenance == "" {
		facts.provenance = string(m.TerminalProvenanceSession)
	}
	if facts.outputState == "" || value.TerminalReason == m.TerminalReasonProviderClose {
		facts.outputState = deriveOutputState(open, turns)
	}
	return facts
}

func effectiveFailureError(facts *failureFacts, err error) error {
	if err != nil {
		return err
	}
	if facts != nil && facts.errorType != "" {
		return errors.New(facts.errorType)
	}
	return errors.New("session stream error")
}
