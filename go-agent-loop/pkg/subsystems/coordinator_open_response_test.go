package subsystems

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// openResponseTick runs one coordinator tick whose inputs are exactly inputs,
// the way the engine clears per-tick inputs between ticks.
func openResponseTick(t *testing.T, c *Coordinator, ls *state.LoopState, inputs state.Buffers) {
	t.Helper()
	ls.Inputs = inputs
	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// openToolCallResponse leaves a response open whose two tool-call deltas are
// already recorded in the history window at indices 1 and 2.
func openToolCallResponse(t *testing.T, c *Coordinator, ls *state.LoopState) {
	t.Helper()
	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue("call-x", "lookup")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue("call-x", "lookup", "{}")},
	}
	ls.History.ConversationDeltaBuffer = append([]messages.StreamMessage{{Role: messages.RoleUser}}, deltas...)
	ls.History.ModelDeltaStartIndex = 1
	ls.History.CurrentModelDeltaCount = len(deltas)
	openResponseTick(t, c, ls, state.Buffers{ModelInputDelta: deltas})
}

// secondUserTurn is a user turn that arrives while a response is open.
func secondUserTurn() state.Buffers {
	return state.Buffers{UserOutputMessage: []messages.Message{messages.NewTextMessage(messages.RoleUser, "second")}}
}

func finalAnswer() state.Buffers {
	return state.Buffers{
		ModelInputDelta:    []messages.StreamMessage{{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})}},
		ModelOutputMessage: []messages.Message{messages.NewTextMessage(messages.RoleAssistant, "answer")},
	}
}

func assertResponseWindow(t *testing.T, ls *state.LoopState, start, count int) {
	t.Helper()
	if ls.History.ModelDeltaStartIndex != start || ls.History.CurrentModelDeltaCount != count {
		t.Fatalf("model delta window: got start=%d count=%d, want start=%d count=%d (the open response lost its recorded deltas)",
			ls.History.ModelDeltaStartIndex, ls.History.CurrentModelDeltaCount, start, count)
	}
}

// A duplex user turn during an open response keeps the response's window
// and still forwards the turn to the provider at once.
func TestCoordinator_DuplexUserTurnDuringOpenResponseKeepsResponseWindow(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Mode = state.DuplexSession
	openToolCallResponse(t, c, ls)

	openResponseTick(t, c, ls, secondUserTurn())

	assertResponseWindow(t, ls, 1, 2)
	if req, ok := ls.Outputs.ModelInbox.Read(); !ok || req.LoopPassID != 1 {
		t.Fatalf("inference request: got %+v ok=%t, want pass 1 forwarding the user turn", req, ok)
	}
}

// A turn-based user turn during an open response is recorded but its
// inference waits: a pass bump would retire the open response. Once that
// response ends as a final answer, the deferred turn is answered instead of
// ending the loop.
func TestCoordinator_TurnBasedUserTurnDuringOpenResponseIsAnsweredAfterIt(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	openToolCallResponse(t, c, ls)

	openResponseTick(t, c, ls, secondUserTurn())
	assertResponseWindow(t, ls, 1, 2)
	if ls.History.CurrentPassID != 0 || ls.Outputs.ModelInbox.Len() != 0 {
		t.Fatalf("pass=%d queued=%d: the user turn retired the open response", ls.History.CurrentPassID, ls.Outputs.ModelInbox.Len())
	}
	if kernel, ok := ls.Outputs.KernelDeltaInbox.Read(); !ok || kernel.Source != messages.User {
		t.Fatalf("kernel record: got %+v ok=%t, want the user turn recorded on arrival", kernel, ok)
	}

	openResponseTick(t, c, ls, finalAnswer())
	if ls.Inputs.TerminateLoop {
		t.Fatal("loop terminated with the deferred user turn unanswered")
	}
	if req, ok := ls.Outputs.ModelInbox.Read(); !ok || req.LoopPassID != 1 {
		t.Fatalf("deferred inference: got %+v ok=%t, want pass 1", req, ok)
	}
}

// An interrupt cancels the open response and its resumed inference carries
// the whole conversation, so the deferred turn must not be answered twice.
func TestCoordinator_InterruptSettlesDeferredUserTurn(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	openToolCallResponse(t, c, ls)
	openResponseTick(t, c, ls, secondUserTurn())

	openResponseTick(t, c, ls, state.Buffers{UserControlPlaneMessage: []messages.Message{{
		Role:         messages.RoleUser,
		ContentParts: []messages.ContentPart{messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeInterrupt}},
	}}})
	openResponseTick(t, c, ls, finalAnswer())

	if !ls.Inputs.TerminateLoop || ls.Outputs.ModelInbox.Len() != 0 {
		t.Fatalf("terminate=%t queued=%d: after an interrupt the final answer must end the loop", ls.Inputs.TerminateLoop, ls.Outputs.ModelInbox.Len())
	}
}

// A terminal ERROR ends the open response, so a later user turn starts a
// fresh pass exactly as before.
func TestCoordinator_TerminalErrorClosesOpenResponse(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	openToolCallResponse(t, c, ls)
	openResponseTick(t, c, ls, state.Buffers{ModelInputDelta: []messages.StreamMessage{{Type: messages.StreamTypeError, Value: messages.NewErrorValue("boom")}}})

	openResponseTick(t, c, ls, secondUserTurn())

	assertResponseWindow(t, ls, len(ls.History.ConversationDeltaBuffer), 0)
	if req, ok := ls.Outputs.ModelInbox.Read(); !ok || req.LoopPassID != 1 {
		t.Fatalf("inference request: got %+v ok=%t, want a fresh pass 1", req, ok)
	}
}
