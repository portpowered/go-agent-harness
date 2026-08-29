package messages

// Terminal taxonomy and terminal metadata-bearing stream values, including completion, error, refusal, and loop-end values, live in this file.

// TerminalReason identifies the public reason a stream, session, replay, or
// loop surface reached a terminal state. It is an additive machine-readable
// field for terminal payloads; existing human-readable messages and provider
// reason strings remain unchanged.
type TerminalReason string

const (
	// TerminalReasonProviderAuthoredCompletion means the provider emitted an
	// explicit successful completion signal, such as MESSAGE.END.
	TerminalReasonProviderAuthoredCompletion TerminalReason = "provider_authored_completion"
	// TerminalReasonLoopSynthesizedCompletion means the loop created a successful
	// terminal boundary after reconstructing provider output.
	TerminalReasonLoopSynthesizedCompletion TerminalReason = "loop_synthesized_completion"
	// TerminalReasonCancellation means caller cancellation or deadline ended the
	// operation. Returned Go errors should preserve context.Canceled or
	// context.DeadlineExceeded where available.
	TerminalReasonCancellation TerminalReason = "cancellation"
	// TerminalReasonReplayDivergence means replay input diverged from the recorded
	// fixture or capture.
	TerminalReasonReplayDivergence TerminalReason = "replay_divergence"
	// TerminalReasonReplayIncomplete means replay ended before all required
	// recorded events were consumed.
	TerminalReasonReplayIncomplete TerminalReason = "replay_incomplete"
	// TerminalReasonSessionClose means a bidirectional session closed without a
	// more specific provider, cancellation, replay, or failure reason.
	TerminalReasonSessionClose TerminalReason = "session_close"
	// TerminalReasonPartialOutput means usable output was emitted before a
	// cancellation or failure terminal state.
	TerminalReasonPartialOutput TerminalReason = "partial_output"
	// TerminalReasonProviderClose means the provider closed the stream/session
	// transport without an explicit provider-authored completion event.
	TerminalReasonProviderClose TerminalReason = "provider_close"
	// TerminalReasonTerminalFailure means the operation ended with a non-
	// cancellation failure.
	TerminalReasonTerminalFailure TerminalReason = "terminal_failure"
)

// TerminalProvenance identifies which layer authored the terminal state.
type TerminalProvenance string

const (
	TerminalProvenanceProvider TerminalProvenance = "provider"
	TerminalProvenanceLoop     TerminalProvenance = "loop"
	TerminalProvenanceGateway  TerminalProvenance = "gateway"
	TerminalProvenanceSession  TerminalProvenance = "session"
	TerminalProvenanceReplay   TerminalProvenance = "replay"
	TerminalProvenanceCLI      TerminalProvenance = "cli"
)

// TerminalOutputState identifies whether terminal output is complete, partial,
// absent, or not applicable.
type TerminalOutputState string

const (
	TerminalOutputComplete      TerminalOutputState = "complete"
	TerminalOutputPartial       TerminalOutputState = "partial"
	TerminalOutputNone          TerminalOutputState = "none"
	TerminalOutputNotApplicable TerminalOutputState = "not_applicable"
)

// TerminalSource identifies who authored a terminal MESSAGE.END boundary.
type TerminalSource string

const (
	// TerminalSourceProvider means the upstream provider or session produced the
	// terminal boundary. Empty legacy MESSAGE.END values are treated as provider
	// authored for compatibility.
	TerminalSourceProvider TerminalSource = "provider"
	// TerminalSourceLoopSynthesized means the loop synthesized the terminal
	// boundary from a non-streaming result or an upstream stream that closed
	// without MESSAGE.END.
	TerminalSourceLoopSynthesized TerminalSource = "loop_synthesized"
)

// MessageEndTerminalSource returns the public terminal source for a MESSAGE.END
// value. Nil and legacy empty-source values are provider-authored by default.
func MessageEndTerminalSource(v *MessageEndValue) TerminalSource {
	if v == nil || v.TerminalSource == "" {
		return TerminalSourceProvider
	}
	return v.TerminalSource
}

// MessageEndValue is the value for MESSAGE.END (inner type "message_end").
type MessageEndValue struct {
	Type  string     `json:"type"` // "message_end"
	Usage TokenUsage `json:"usage,omitempty"`
	// Status and StatusDetails preserve the provider's terminal response
	// outcome without making consumers parse provider-specific wire events.
	// Status is commonly "completed", "failed", "cancelled", or
	// "incomplete". StatusDetails contains only compact, provider-sanitized
	// detail text; it is never a raw provider JSON object.
	Status             string              `json:"status,omitempty"`
	StatusDetails      string              `json:"status_details,omitempty"`
	TerminalReason     TerminalReason      `json:"terminal_reason,omitempty"`
	TerminalProvenance TerminalProvenance  `json:"terminal_provenance,omitempty"`
	OutputState        TerminalOutputState `json:"output_state,omitempty"`
	TerminalSource     TerminalSource      `json:"terminal_source,omitempty"`
}

func (*MessageEndValue) streamMessageValue() {}

// NewMessageEndValue returns a value for MESSAGE.END.
func NewMessageEndValue(usage TokenUsage) *MessageEndValue {
	return &MessageEndValue{Type: "message_end", Usage: usage}
}

// NewMessageEndValueWithTerminal returns a MESSAGE.END value with additive
// terminal metadata. Existing callers that do not need terminal metadata should
// keep using NewMessageEndValue.
func NewMessageEndValueWithTerminal(usage TokenUsage, reason TerminalReason, provenance TerminalProvenance, outputState TerminalOutputState) *MessageEndValue {
	return &MessageEndValue{
		Type:               "message_end",
		Usage:              usage,
		TerminalReason:     reason,
		TerminalProvenance: provenance,
		OutputState:        outputState,
	}
}

// NewSynthesizedMessageEndValue returns a loop-authored MESSAGE.END boundary.
func NewSynthesizedMessageEndValue(usage TokenUsage) *MessageEndValue {
	value := NewMessageEndValueWithTerminal(
		usage,
		TerminalReasonLoopSynthesizedCompletion,
		TerminalProvenanceLoop,
		TerminalOutputComplete,
	)
	value.TerminalSource = TerminalSourceLoopSynthesized
	return value
}

// ErrorValue is the value for ERROR (inner type "error").
type ErrorValue struct {
	Type           string `json:"type"`                     // "error"
	Message        string `json:"message"`                  // error description
	Classification string `json:"classification,omitempty"` // public gateway taxonomy classification
	// NonTerminal marks an informational provider diagnostic that does not end
	// the stream or session. Error values remain terminal by default for
	// backwards compatibility; producers must opt into this behavior explicitly.
	NonTerminal        bool                `json:"non_terminal,omitempty"`
	TerminalReason     TerminalReason      `json:"terminal_reason,omitempty"`
	TerminalProvenance TerminalProvenance  `json:"terminal_provenance,omitempty"`
	OutputState        TerminalOutputState `json:"output_state,omitempty"`
	ErrorType          string              `json:"error_type,omitempty"` // provider error category
	Code               string              `json:"code,omitempty"`       // provider error code
	Param              string              `json:"param,omitempty"`      // provider parameter associated with the error
	EventID            string              `json:"event_id,omitempty"`   // related client event ID when provided
	Err                error               `json:"-"`                    // typed in-process error for errors.Is/errors.As
}

func (*ErrorValue) streamMessageValue() {}

// IsNonTerminal reports whether the error is an informational diagnostic that
// must not terminate the stream or session.
func (v *ErrorValue) IsNonTerminal() bool {
	return v != nil && v.NonTerminal
}

// IsTerminal reports whether the error ends the stream or session. A nil error
// value is treated as terminal to preserve the legacy behavior of typed ERROR
// values whose payload is unexpectedly nil.
func (v *ErrorValue) IsTerminal() bool {
	return v == nil || !v.NonTerminal
}

// NewErrorValue returns a value for ERROR.
func NewErrorValue(message string) *ErrorValue {
	return &ErrorValue{Type: "error", Message: message}
}

// NewErrorValueWithClassification returns an ERROR value with a public gateway
// taxonomy classification for stream/event consumers.
func NewErrorValueWithClassification(message, classification string) *ErrorValue {
	return &ErrorValue{Type: "error", Message: message, Classification: classification}
}

// NewNonTerminalErrorValue returns an informational ERROR diagnostic. It is
// intentionally opt-in; ordinary ERROR values continue to terminate streams.
func NewNonTerminalErrorValue(message, classification string) *ErrorValue {
	return &ErrorValue{
		Type:           "error",
		Message:        message,
		Classification: classification,
		NonTerminal:    true,
	}
}

// NewErrorValueWithTerminal returns an ERROR value with public error
// classification and terminal metadata. Message remains the compatibility
// field for operator-readable text; callers should branch on Classification,
// TerminalReason, TerminalProvenance, and OutputState instead of parsing it.
func NewErrorValueWithTerminal(message, classification string, reason TerminalReason, provenance TerminalProvenance, outputState TerminalOutputState) *ErrorValue {
	return &ErrorValue{
		Type:               "error",
		Message:            message,
		Classification:     classification,
		TerminalReason:     reason,
		TerminalProvenance: provenance,
		OutputState:        outputState,
	}
}

// NewErrorValueWithError returns an ERROR value that preserves the original
// in-process error for callers that branch with errors.Is or errors.As.
func NewErrorValueWithError(err error) *ErrorValue {
	if err == nil {
		return NewErrorValue("")
	}
	return &ErrorValue{Type: "error", Message: err.Error(), Err: err}
}

// NewErrorValueWithDetails returns an ERROR value with provider-supplied context.
func NewErrorValueWithDetails(message, errorType, code, param, eventID string) *ErrorValue {
	return &ErrorValue{
		Type:      "error",
		Message:   message,
		ErrorType: errorType,
		Code:      code,
		Param:     param,
		EventID:   eventID,
	}
}

// NewNonTerminalErrorValueWithDetails returns an informational ERROR
// diagnostic with bounded provider error details.
func NewNonTerminalErrorValueWithDetails(message, errorType, code, param, eventID string) *ErrorValue {
	return &ErrorValue{
		Type:        "error",
		Message:     message,
		ErrorType:   errorType,
		Code:        code,
		Param:       param,
		EventID:     eventID,
		NonTerminal: true,
	}
}

// RefusalValue is the value for REFUSAL (inner type "refusal").
// Carries the complete accumulated refusal text from a model response.
type RefusalValue struct {
	Type    string `json:"type"`    // "refusal"
	Message string `json:"message"` // complete refusal text
}

func (*RefusalValue) streamMessageValue() {}

// NewRefusalValue returns a value for REFUSAL.
func NewRefusalValue(message string) *RefusalValue {
	return &RefusalValue{Type: "refusal", Message: message}
}

// LoopEndValue is the value for LOOP.END (inner type "loop_end").
type LoopEndValue struct {
	Type string `json:"type"` // "loop_end"
}

func (*LoopEndValue) streamMessageValue() {}

// NewLoopEndValue returns a value for LOOP.END.
func NewLoopEndValue() *LoopEndValue { return &LoopEndValue{Type: "loop_end"} }
