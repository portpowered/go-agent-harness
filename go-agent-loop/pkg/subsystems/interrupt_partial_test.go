package subsystems

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// TestInterruptHandler_SavesOnlyPartialResponsesWithContent decides which
// interrupted model responses become history: anything the model actually
// produced (text, reasoning or a tool call) is kept so the resumed inference
// sees it, while a response that only opened its text part is dropped.
func TestInterruptHandler_SavesOnlyPartialResponsesWithContent(t *testing.T) {
	start := messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}
	tests := []struct {
		name   string
		deltas []messages.StreamMessage
		saved  bool
	}{
		{
			name:   "opened text only",
			deltas: []messages.StreamMessage{start, {Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()}},
		},
		{
			name:   "reasoning only",
			deltas: []messages.StreamMessage{start, {Type: messages.StreamTypeReasoningDelta, Value: messages.NewReasoningDeltaValue("weighing options")}},
			saved:  true,
		},
		{
			name: "completed tool call",
			deltas: []messages.StreamMessage{
				start,
				{Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue("call-1", "lookup")},
				{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("call-1", "lookup", `{"q":"x"}`)},
			},
			saved: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ls := &state.LoopState{}
			ls.History.ConversationDeltaBuffer = tt.deltas
			ls.History.CurrentModelDeltaCount = len(tt.deltas)
			ls.Outputs.ModelInbox = messages.NewTypedBuffer[messages.InferenceRequest](8)
			ls.Inputs.UserControlPlaneMessage = []messages.Message{makeInterruptMessage("")}

			if err := NewInterruptHandler(nil, nil, nil).Execute(context.Background(), ls); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got := len(ls.History.ConversationBuffer) == 1; got != tt.saved {
				t.Fatalf("partial saved = %t (history %d), want %t", got, len(ls.History.ConversationBuffer), tt.saved)
			}
			request, ok := ls.Outputs.ModelInbox.Read()
			if !ok || len(request.Messages) != len(ls.History.ConversationBuffer) {
				t.Fatalf("resumed inference = %+v (ok %t), want the updated history", request, ok)
			}
		})
	}
}

// TestInterruptHandler_IgnoresDeltaWindowBeyondTheBuffer keeps a stale delta
// window (one already trimmed from the buffer) from panicking or saving a
// fabricated partial.
func TestInterruptHandler_IgnoresDeltaWindowBeyondTheBuffer(t *testing.T) {
	ls := buildLoopStateWithPartialModelDeltas("hello")
	ls.History.CurrentModelDeltaCount = len(ls.History.ConversationDeltaBuffer) + 1
	ls.Inputs.UserControlPlaneMessage = []messages.Message{makeInterruptMessage("")}
	if err := NewInterruptHandler(nil, nil, nil).Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ls.History.ConversationBuffer) != 0 {
		t.Fatalf("history = %d messages, want none from an out-of-range window", len(ls.History.ConversationBuffer))
	}
	if ls.History.CurrentPassID != 2 {
		t.Fatalf("pass = %d, want 2 so late deltas of the cancelled pass are dropped", ls.History.CurrentPassID)
	}
}

// TestInteractionEvents_AcceptedToolResultsClearOnlyTheirPendingCall tracks
// parallel tool calls: each accepted result removes its own call, and the
// interaction has no pending calls once every result is accepted.
func TestInteractionEvents_AcceptedToolResultsClearOnlyTheirPendingCall(t *testing.T) {
	subsystem := NewInteractionEvents(nil)
	ls := newInteractionEventsTestState()
	ls.Outputs.KernelDeltaInbox = messages.NewTypedBuffer[messages.KernelDeltaRequest](32)
	first := &messages.ToolCall{ID: "tool-1", Name: "lookup", Arguments: `{}`}
	second := &messages.ToolCall{ID: "tool-2", Name: "lookup", Arguments: `{}`}
	ls.Inputs.InteractionEvents = []messages.InteractionEvent{
		{InteractionID: "int-1", Sequence: 1, Type: messages.InteractionEventStart},
		{InteractionID: "int-1", Sequence: 2, Type: messages.InteractionEventToolCallRequest, ToolCall: first},
		{InteractionID: "int-1", Sequence: 3, Type: messages.InteractionEventToolCallRequest, ToolCall: second},
		{InteractionID: "int-1", Sequence: 4, Type: messages.InteractionEventToolResultAccepted},
		{InteractionID: "int-1", Sequence: 5, Type: messages.InteractionEventToolResultAccepted, ToolCall: &messages.ToolCall{ID: "tool-1"}},
	}
	if err := subsystem.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if pending := ls.Interaction.PendingToolCalls; len(pending) != 1 || pending[0].ID != "tool-2" {
		t.Fatalf("pending calls = %+v, want only tool-2", pending)
	}

	ls.Inputs.InteractionEvents = []messages.InteractionEvent{
		{InteractionID: "int-1", Sequence: 6, Type: messages.InteractionEventToolResultAccepted, ToolCall: &messages.ToolCall{}},
		{InteractionID: "int-1", Sequence: 7, Type: messages.InteractionEventToolResultAccepted, ToolCall: &messages.ToolCall{ID: "tool-2"}},
	}
	if err := subsystem.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if ls.Interaction.PendingToolCalls != nil {
		t.Fatalf("pending calls = %+v, want none", ls.Interaction.PendingToolCalls)
	}
}
