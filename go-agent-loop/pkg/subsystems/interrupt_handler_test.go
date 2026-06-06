package subsystems

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-loop/pkg/state"
)

// noopCanceller is a test double for ExecutionCanceller that records whether it was called.
type noopCanceller struct {
	cancelled bool
}

func (n *noopCanceller) CancelCurrentExecution() { n.cancelled = true }

// buildLoopStateWithPartialModelDeltas returns a LoopState whose ConversationDeltaBuffer
// contains the given partial model deltas starting at index 0 (ModelDeltaStartIndex=0).
func buildLoopStateWithPartialModelDeltas(text string) *state.LoopState {
	partialDeltas := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue(), LoopPassID: 1},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue(), LoopPassID: 1},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(text), LoopPassID: 1},
	}

	s := &state.LoopState{}
	s.History.ConversationDeltaBuffer = append(s.History.ConversationDeltaBuffer, partialDeltas...)
	s.History.ModelDeltaStartIndex = 0
	s.History.CurrentModelDeltaCount = len(partialDeltas)
	s.History.CurrentPassID = 1
	s.Outputs = state.OutputBuffers{
		ModelInbox: *messages.NewTypedBuffer[messages.InferenceRequest](8),
	}
	return s
}

// makeInterruptMessage builds a user message that carries a ControlPlanePart of type interrupt.
func makeInterruptMessage(text string) messages.Message {
	parts := []messages.ContentPart{
		messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeInterrupt},
	}
	if text != "" {
		parts = append(parts, messages.NewTextPart(text))
	}
	return messages.Message{Role: messages.RoleUser, ContentParts: parts}
}

func TestInterruptHandler_NoInterrupt(t *testing.T) {
	model := &noopCanceller{}
	tool := &noopCanceller{}
	h := NewInterruptHandler(model, tool, nil)

	ls := &state.LoopState{}
	ls.Outputs.ModelInbox = *messages.NewTypedBuffer[messages.InferenceRequest](8)

	if err := h.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if model.cancelled {
		t.Error("model should not be cancelled when no interrupt is present")
	}
	if tool.cancelled {
		t.Error("tool should not be cancelled when no interrupt is present")
	}
	if _, ok := ls.Outputs.ModelInbox.Read(); ok {
		t.Error("no InferenceRequest should be dispatched when no interrupt")
	}
}

func TestInterruptHandler_InterruptWithPartialModelResponse(t *testing.T) {
	model := &noopCanceller{}
	tool := &noopCanceller{}
	h := NewInterruptHandler(model, tool, nil)

	ls := buildLoopStateWithPartialModelDeltas("hello wor")
	ls.Inputs.UserControlPlaneMessage = []messages.Message{makeInterruptMessage("")}

	if err := h.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Runners should be cancelled.
	if !model.cancelled {
		t.Error("model runner should be cancelled on interrupt")
	}
	if !tool.cancelled {
		t.Error("tool runner should be cancelled on interrupt")
	}

	// Partial model response should be saved to ConversationBuffer.
	if len(ls.History.ConversationBuffer) == 0 {
		t.Fatal("expected partial message to be added to ConversationBuffer")
	}
	partial := ls.History.ConversationBuffer[0]
	if partial.Role != messages.RoleAssistant {
		t.Errorf("partial role: got %s, want assistant", partial.Role)
	}
	if partial.TextContent() != "hello wor" {
		t.Errorf("partial text: got %q, want %q", partial.TextContent(), "hello wor")
	}

	// CurrentPassID should be incremented.
	if ls.History.CurrentPassID != 2 {
		t.Errorf("CurrentPassID: got %d, want 2", ls.History.CurrentPassID)
	}

	// A new InferenceRequest should be dispatched.
	req, ok := ls.Outputs.ModelInbox.Read()
	if !ok {
		t.Fatal("expected a new InferenceRequest to be dispatched after interrupt")
	}
	if req.LoopPassID != 2 {
		t.Errorf("InferenceRequest.LoopPassID: got %d, want 2", req.LoopPassID)
	}

	// Model delta tracking should be reset.
	if ls.History.CurrentModelDeltaCount != 0 {
		t.Errorf("CurrentModelDeltaCount should be reset to 0, got %d", ls.History.CurrentModelDeltaCount)
	}
}

func TestInterruptHandler_InterruptWithFollowUpText(t *testing.T) {
	model := &noopCanceller{}
	tool := &noopCanceller{}
	h := NewInterruptHandler(model, tool, nil)

	ls := buildLoopStateWithPartialModelDeltas("partial")
	ls.Inputs.UserControlPlaneMessage = []messages.Message{makeInterruptMessage("new direction")}

	if err := h.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ConversationBuffer should have: partial assistant message + follow-up user message.
	if len(ls.History.ConversationBuffer) < 2 {
		t.Fatalf("expected at least 2 messages in ConversationBuffer, got %d", len(ls.History.ConversationBuffer))
	}
	if ls.History.ConversationBuffer[0].Role != messages.RoleAssistant {
		t.Errorf("first message: got role %s, want assistant", ls.History.ConversationBuffer[0].Role)
	}
	followUp := ls.History.ConversationBuffer[1]
	if followUp.Role != messages.RoleUser {
		t.Errorf("follow-up role: got %s, want user", followUp.Role)
	}
	if followUp.TextContent() != "new direction" {
		t.Errorf("follow-up text: got %q, want %q", followUp.TextContent(), "new direction")
	}

	// InferenceRequest must include both the partial and the follow-up.
	req, ok := ls.Outputs.ModelInbox.Read()
	if !ok {
		t.Fatal("expected a new InferenceRequest to be dispatched")
	}
	if len(req.Messages) < 2 {
		t.Errorf("InferenceRequest.Messages: got %d, want >=2", len(req.Messages))
	}
}

func TestInterruptHandler_InterruptWithNoPartial(t *testing.T) {
	model := &noopCanceller{}
	tool := &noopCanceller{}
	h := NewInterruptHandler(model, tool, nil)

	// No model deltas in flight.
	ls := &state.LoopState{}
	ls.Outputs.ModelInbox = *messages.NewTypedBuffer[messages.InferenceRequest](8)
	ls.History.CurrentPassID = 1
	ls.Inputs.UserControlPlaneMessage = []messages.Message{makeInterruptMessage("")}

	if err := h.Execute(context.Background(), ls); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No partial message: ConversationBuffer should be empty.
	if len(ls.History.ConversationBuffer) != 0 {
		t.Errorf("expected empty ConversationBuffer when no partial, got %d messages", len(ls.History.ConversationBuffer))
	}

	// Pass ID should still be incremented.
	if ls.History.CurrentPassID != 2 {
		t.Errorf("CurrentPassID: got %d, want 2", ls.History.CurrentPassID)
	}

	// New inference should still be dispatched.
	if _, ok := ls.Outputs.ModelInbox.Read(); !ok {
		t.Error("expected InferenceRequest even when no partial model response")
	}
}

func TestInterruptHandler_TickGroup(t *testing.T) {
	h := NewInterruptHandler(nil, nil, nil)
	if h.TickGroup() != TickGroupInterruptHandler {
		t.Errorf("TickGroup: got %d, want %d", h.TickGroup(), TickGroupInterruptHandler)
	}
	if TickGroupInterruptHandler >= TickGroupCoordinator {
		t.Errorf("InterruptHandler must run before Coordinator: %d >= %d",
			TickGroupInterruptHandler, TickGroupCoordinator)
	}
}
