package messages

// InteractionEventType identifies the loop-facing projection of a normalized
// gateway interaction event.
type InteractionEventType string

const (
	InteractionEventStart              InteractionEventType = "interaction.start"
	InteractionEventTextDelta          InteractionEventType = "text.delta"
	InteractionEventFinalMessage       InteractionEventType = "message.final"
	InteractionEventToolCallRequest    InteractionEventType = "tool.call.request"
	InteractionEventToolResultAccepted InteractionEventType = "tool.result.accepted"
	InteractionEventUsage              InteractionEventType = "usage"
	InteractionEventError              InteractionEventType = "error"
	InteractionEventCancellation       InteractionEventType = "cancellation"
	InteractionEventEnd                InteractionEventType = "interaction.end"
)

// InteractionEvent is the loop-owned projection of a normalized gateway event.
// Adapters outside go-agent-loop translate provider-neutral gateway events into
// this shape so the loop can react without importing gateway packages.
type InteractionEvent struct {
	InteractionID string
	Sequence      int64
	Type          InteractionEventType
	Provider      string
	Model         string
	TextDelta     string
	FinalMessage  *Message
	ToolCall      *ToolCall
	Usage         *TokenUsage
	Error         *InteractionError
	Cancellation  *InteractionCancellation
}

// InteractionError captures normalized terminal failure details.
type InteractionError struct {
	Code           string
	Message        string
	Classification string
	Retryable      bool
}

// InteractionCancellation captures normalized cancellation details.
type InteractionCancellation struct {
	Reason  string
	Message string
}

// InteractionState tracks the loop's view of the current normalized interaction.
type InteractionState struct {
	ActiveInteractionID string
	Provider            string
	Model               string
	LatestSequence      int64
	PendingToolCalls    []ToolCall
	FinalMessage        *Message
	Usage               *TokenUsage
	TerminalError       *InteractionError
	Cancellation        *InteractionCancellation
	Completed           bool
}

// CloneInteractionState copies the state so callers can inspect it without
// sharing mutable slices or pointers back into the loop.
func CloneInteractionState(in InteractionState) InteractionState {
	out := in
	out.PendingToolCalls = cloneToolCalls(in.PendingToolCalls)
	if in.FinalMessage != nil {
		msg := cloneMessage(*in.FinalMessage)
		out.FinalMessage = &msg
	}
	if in.Usage != nil {
		usage := *in.Usage
		out.Usage = &usage
	}
	if in.TerminalError != nil {
		err := *in.TerminalError
		out.TerminalError = &err
	}
	if in.Cancellation != nil {
		cancel := *in.Cancellation
		out.Cancellation = &cancel
	}
	return out
}

func cloneToolCalls(in []ToolCall) []ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]ToolCall, len(in))
	copy(out, in)
	return out
}

func cloneMessage(in Message) Message {
	out := in
	if len(in.ContentParts) > 0 {
		out.ContentParts = append([]ContentPart(nil), in.ContentParts...)
	}
	out.ToolCalls = cloneToolCalls(in.ToolCalls)
	if in.Index != nil {
		index := *in.Index
		out.Index = &index
	}
	return out
}
