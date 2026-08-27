package subsystems

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/logging"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// ToolResultForwarder delivers completed tool results to the duplex session
// transport so realtime providers observe what each tool returned.
//
// In DuplexSession mode, assembled tool results (Inputs.ToolOutputMessage)
// are consumed by the Coordinator and routed into the conversation history,
// but nothing carried them onto the provider-wire outbound path. This
// subsystem closes that gap: on the tick that processes a batch of assembled
// tool results it emits exactly one
//
//	StreamMessage{Type: StreamTypeToolCallEnd, Value: NewToolCallEndValue(callID, name, output)}
//
// per completed call, in call order, into the session model runner's
// UserEventInbox — the same outbound path used for user audio and control
// plane turns. Providers translate TOOLCALL.END into their wire shape (e.g.
// conversation.item.create with a function_call_output item on OpenAI
// Realtime).
//
// Delivery is exactly-once per ToolCallID for the lifetime of the loop:
// forwarded IDs are remembered, so re-running ticks over the same loop state
// cannot produce a second delivery. Results without a usable ToolCallID are
// skipped because they cannot be paired with an originating call on the wire.
//
// The subsystem is a no-op outside DuplexSession; turn-based modes never send
// to a session sink.
type ToolResultForwarder struct {
	sessionEvents chan<- messages.StreamMessage
	logger        logging.Logger

	forwarded map[string]struct{}
}

func NewToolResultForwarder(sessionEvents chan<- messages.StreamMessage, logger logging.Logger) *ToolResultForwarder {
	return &ToolResultForwarder{
		sessionEvents: sessionEvents,
		logger:        logger,
		forwarded:     make(map[string]struct{}),
	}
}

var _ Subsystem = (*ToolResultForwarder)(nil)

// TickGroupToolResultForwarder runs after the Coordinator so forwarded tool
// results are enqueued after the tick's coordination side effects but before
// later bookkeeping groups.
const TickGroupToolResultForwarder TickGroup = 2

// TickGroup implements [Subsystem].
func (f *ToolResultForwarder) TickGroup() TickGroup {
	return TickGroupToolResultForwarder
}

// Execute implements [Subsystem]. It forwards every not-yet-delivered tool
// result from the current tick's inputs to the session sink.
func (f *ToolResultForwarder) Execute(ctx context.Context, curr *state.LoopState) error {
	if curr.Mode != state.DuplexSession || f.sessionEvents == nil {
		return nil
	}
	if len(curr.Inputs.ToolOutputMessage) == 0 {
		return nil
	}
	// The coordinator dispatches the current tool outputs as one inference
	// request. If any member is rich, the model runner must own the complete
	// batch so text-only siblings cannot be sent here and then sent again via
	// the complete-message path.
	richBatch := toolResultsContainImage(curr.Inputs.ToolOutputMessage)
	for _, msg := range curr.Inputs.ToolOutputMessage {
		callID := msg.ToolCallID
		if callID == "" {
			f.logInfo("tool result forwarder: skipping result without ToolCallID")
			continue
		}
		if richBatch {
			// Rich results, including text-only siblings in this batch, are
			// delivered by the session model runner's complete-message path.
			// Sending only the text sibling through TOOLCALL.END would duplicate
			// it when the runner sends the complete batch.
			f.logInfo("tool result forwarder: deferring rich result to complete-message path",
				logging.Field{Key: "tool_call_id", Value: callID})
			continue
		}
		if _, done := f.forwarded[callID]; done {
			continue
		}
		streamMsg := messages.StreamMessage{
			Type:  messages.StreamTypeToolCallEnd,
			Value: messages.NewToolCallEndValue(callID, resolveToolName(curr.History.ConversationBuffer, msg), serializedToolOutput(msg)),
		}
		select {
		case f.sessionEvents <- streamMsg:
		case <-ctx.Done():
			return ctx.Err()
		}
		f.forwarded[callID] = struct{}{}
		f.logInfo("tool result forwarder: delivered tool result", logging.Field{Key: "tool_call_id", Value: callID})
	}
	return nil
}

func toolResultsContainImage(results []messages.Message) bool {
	for _, result := range results {
		for _, part := range result.ContentParts {
			if _, ok := part.(messages.ImagePart); ok {
				return true
			}
		}
	}
	return false
}

// serializedToolOutput projects a tool result message onto the flat string
// carried on the provider wire. Precedence matches the documented
// ToolCallResponse contract: ordered concatenation of text parts when any are
// present, otherwise the flat content. Assembled tool messages carry content
// exclusively as parts (the delta protocol has no flat-content representation),
// so the flat fallback is empty by construction.
func serializedToolOutput(msg messages.Message) string {
	var out string
	hasText := false
	for _, part := range msg.ContentParts {
		if t, ok := part.(messages.TextPart); ok {
			hasText = true
			out += t.Text
		}
	}
	if !hasText {
		return ""
	}
	return out
}

// resolveToolName recovers the requested tool name for a result. The delta
// protocol does not persist the name onto tool-result deltas, so the name is
// looked up from the assistant message that issued the call; the message's own
// Name field (when populated) wins only if the history lookup finds nothing.
func resolveToolName(history []messages.Message, msg messages.Message) string {
	if msg.Name != "" {
		return msg.Name
	}
	for i := len(history) - 1; i >= 0; i-- {
		for _, call := range history[i].ToolCalls {
			if call.ID == msg.ToolCallID && call.Name != "" {
				return call.Name
			}
		}
	}
	return ""
}

func (f *ToolResultForwarder) logInfo(msg string, fields ...logging.Field) {
	if f.logger != nil {
		f.logger.Info(msg, fields...)
	}
}
