package agentruntime

import (
	"errors"
	"strconv"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

type failureFacts struct {
	classification string
	terminalReason string
	provenance     string
	outputState    string
	errorType      string
	code           string
	failingEvent   string
}

func (o *sessionProgressObserver) failureSnapshot() *failureFacts {
	if o == nil {
		return nil
	}
	o.livenessMu.Lock()
	f := o.failure
	if f == nil {
		o.livenessMu.Unlock()
		return nil
	}
	copy := *f
	o.livenessMu.Unlock()
	return &copy
}

func (o *sessionProgressObserver) clearFailure() {
	if o == nil {
		return
	}
	o.livenessMu.Lock()
	o.failure = nil
	o.livenessMu.Unlock()
}

// audioTurnCounters tracks per-turn byte attribution between MESSAGE.END
// boundaries.
func (o *sessionProgressObserver) captureFailureFromError(v *messages.ErrorValue) {
	if o == nil || v == nil || v.IsNonTerminal() {
		return
	}
	// Cancellation is an intentional terminal state, including the provider's
	// acknowledgement of the room's forced bound cancellation. It must not be
	// converted into a session_failure diagnostic; the room observer projects
	// the cancellation fields after the loop finishes.
	if v.TerminalReason == messages.TerminalReasonCancellation ||
		(v.Classification == providers.ErrorClassCancellation && v.TerminalReason == "") ||
		v.Classification == RoomBoundCancelledClassification {
		return
	}
	facts := factsFromErrorValue(v)
	err := v.Err
	if err == nil && v.Message != "" {
		err = errors.New(v.Message)
	}
	if !o.acceptFailureObservation(facts, err) {
		return
	}
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

// factsFromSessionRunError recovers the typed provider ERROR that the engine
// consumed as its terminal run error. Terminal ERROR deltas stop the engine
// before they can cross the consumer-facing delta stream, so the session
// observer must inspect the preserved StreamDeltaError wrapper instead.
func factsFromSessionRunError(err error) *failureFacts {
	if err == nil {
		return nil
	}
	var deltaErr *engine.StreamDeltaError
	if !errors.As(err, &deltaErr) || deltaErr == nil || deltaErr.Value == nil {
		return nil
	}
	return factsFromErrorValue(deltaErr.Value)
}

func (o *sessionProgressObserver) acceptFailureObservation(facts *failureFacts, err error) bool {
	if o == nil || facts == nil {
		return false
	}
	observation := sessionTerminalObservationFromFailure(facts, err)
	accepted := o.notifyTerminalObservation(observation)
	if !accepted {
		return false
	}
	// Set the local facts before invoking room failure ownership. The callback
	// may cancel this observer's loop synchronously; finish must still retain
	// the typed provider fields while that teardown is in flight.
	o.failure = facts
	if o.failureObserver != nil {
		o.failureObserver(observation)
	}
	return true
}

// captureFailureFromClose captures only failure-worthy session closes; clean,
// caller-authored completions are never failures. A provider_close terminal
// reason is a failure only when the model runner synthesized it because the
// provider transport died without an explicit close (marker reason
// "provider_closed"); an explicit wire session.closed is normal teardown.
func (o *sessionProgressObserver) captureFailureFromClose(v *messages.SessionCloseValue) {
	if o == nil || v == nil {
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
	if !o.acceptFailureObservation(f, nil) {
		return
	}
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
