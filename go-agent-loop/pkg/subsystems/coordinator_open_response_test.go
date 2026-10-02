package subsystems

import (
	"context"
	"fmt"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// exchangeTick runs one coordinator tick whose inputs are exactly inputs.
// Like the engine, it first appends the tick's complete messages to history
// (UpdateWorldHistory) and replaces the per-tick inputs.
func exchangeTick(t *testing.T, c *Coordinator, ls *state.LoopState, inputs state.Buffers) {
	t.Helper()
	ls.Inputs = inputs
	ls.History.ConversationBuffer = append(ls.History.ConversationBuffer, inputs.ToolOutputMessage...)
	ls.History.ConversationBuffer = append(ls.History.ConversationBuffer, inputs.UserOutputMessage...)
	ls.History.ConversationBuffer = append(ls.History.ConversationBuffer, inputs.ModelOutputMessage...)
	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func toolCallDeltas() []messages.StreamMessage {
	return []messages.StreamMessage{
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue("call-x", "lookup")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue("call-x", "lookup", "{}")},
	}
}

// openToolCallResponse starts from history [user first] and leaves a response
// open whose two tool-call deltas are recorded in the window at indices 1-2.
func openToolCallResponse(t *testing.T, c *Coordinator, ls *state.LoopState) {
	t.Helper()
	ls.History.ConversationBuffer = []messages.Message{messages.NewTextMessage(messages.RoleUser, "first")}
	deltas := toolCallDeltas()
	ls.History.ConversationDeltaBuffer = append([]messages.StreamMessage{{Role: messages.RoleUser}}, deltas...)
	ls.History.ModelDeltaStartIndex = 1
	ls.History.CurrentModelDeltaCount = len(deltas)
	exchangeTick(t, c, ls, state.Buffers{ModelInputDelta: deltas})
}

func secondUserTurn() state.Buffers {
	return state.Buffers{UserOutputMessage: []messages.Message{messages.NewTextMessage(messages.RoleUser, "second")}}
}

func messageEnd() []messages.StreamMessage {
	return []messages.StreamMessage{{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})}}
}

func finalAnswer() state.Buffers {
	return state.Buffers{ModelInputDelta: messageEnd(), ModelOutputMessage: []messages.Message{messages.NewTextMessage(messages.RoleAssistant, "answer")}}
}

func toolCallAnswer() state.Buffers {
	return state.Buffers{ModelInputDelta: messageEnd(), ModelOutputMessage: []messages.Message{{
		Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{{ID: "call-x", Name: "lookup"}},
	}}}
}

func toolResult() state.Buffers {
	return state.Buffers{
		ToolInputDelta:    []messages.StreamMessage{{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}},
		ToolOutputMessage: []messages.Message{{Role: messages.RoleTool, ToolCallID: "call-x", ContentParts: []messages.ContentPart{messages.NewTextPart("ok")}}},
	}
}

// describeMessages renders messages as role plus identifying content.
func describeMessages(history []messages.Message) string {
	out := make([]string, 0, len(history))
	for _, msg := range history {
		switch {
		case len(msg.ToolCalls) > 0:
			out = append(out, fmt.Sprintf("%s:tool_call", msg.Role))
		case msg.Role == messages.RoleTool:
			out = append(out, fmt.Sprintf("%s:result", msg.Role))
		default:
			out = append(out, fmt.Sprintf("%s:%s", msg.Role, msg.TextContent()))
		}
	}
	return fmt.Sprint(out)
}

func assertMessages(t *testing.T, what string, got []messages.Message, want string) {
	t.Helper()
	if described := describeMessages(got); described != want {
		t.Fatalf("%s:\n got %s\nwant %s", what, described, want)
	}
}

// nextRequest reads the next inference request and fails if it ends on an
// assistant message, which providers treat as a prefill and may reject.
func nextRequest(t *testing.T, ls *state.LoopState) messages.InferenceRequest {
	t.Helper()
	req, ok := ls.Outputs.ModelInbox.Read()
	if !ok {
		t.Fatal("no inference request dispatched")
	}
	if n := len(req.Messages); n == 0 || req.Messages[n-1].Role == messages.RoleAssistant {
		t.Fatalf("inference request ends on an assistant message: %s", describeMessages(req.Messages))
	}
	return req
}

func assertResponseWindow(t *testing.T, ls *state.LoopState, start, count int) {
	t.Helper()
	if ls.History.ModelDeltaStartIndex != start || ls.History.CurrentModelDeltaCount != count {
		t.Fatalf("model delta window: got start=%d count=%d, want start=%d count=%d (the open response lost its recorded deltas)",
			ls.History.ModelDeltaStartIndex, ls.History.CurrentModelDeltaCount, start, count)
	}
}

// A duplex user turn during an open response keeps the response's window and
// is forwarded at once, but joins history only after the response's tool
// call and result, so the result still directly follows its call.
func TestCoordinator_DuplexUserTurnDuringOpenResponseIsForwardedAndPlacedAfterIt(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Mode = state.DuplexSession
	ls.ToolExecutionAvailable = true
	openToolCallResponse(t, c, ls)

	exchangeTick(t, c, ls, secondUserTurn())
	assertResponseWindow(t, ls, 1, 2)
	assertMessages(t, "forwarded request", nextRequest(t, ls).Messages, "[user:first user:second]")
	assertMessages(t, "history while held", ls.History.ConversationBuffer, "[user:first]")

	exchangeTick(t, c, ls, toolCallAnswer())
	exchangeTick(t, c, ls, toolResult())
	assertMessages(t, "history", ls.History.ConversationBuffer, "[user:first assistant:tool_call tool:result user:second]")
	assertMessages(t, "continuation", nextRequest(t, ls).Messages, "[user:first assistant:tool_call tool:result]")
}

// A turn-based user turn during an open response that ends in a final answer
// is answered after that answer, from [first, answer, second], instead of
// the loop ending with it unanswered.
func TestCoordinator_TurnBasedUserTurnDuringFinalResponseIsAnsweredAfterIt(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	openToolCallResponse(t, c, ls)

	exchangeTick(t, c, ls, secondUserTurn())
	assertResponseWindow(t, ls, 1, 2)
	if ls.History.CurrentPassID != 0 || ls.Outputs.ModelInbox.Len() != 0 || ls.Outputs.KernelDeltaInbox.Len() != 0 {
		t.Fatalf("pass=%d queued=%d kernel=%d: the held user turn retired or reordered the open response",
			ls.History.CurrentPassID, ls.Outputs.ModelInbox.Len(), ls.Outputs.KernelDeltaInbox.Len())
	}

	exchangeTick(t, c, ls, finalAnswer())
	if ls.Inputs.TerminateLoop {
		t.Fatal("loop terminated with the held user turn unanswered")
	}
	assertMessages(t, "deferred request", nextRequest(t, ls).Messages, "[user:first assistant:answer user:second]")
	assertMessages(t, "history", ls.History.ConversationBuffer, "[user:first assistant:answer user:second]")
}

// A turn-based user turn that lands after a tool-call response ended but
// before its tool result joins history after the result, and the tool
// continuation answers it: [first, tool_call, result, second].
func TestCoordinator_TurnBasedUserTurnBeforeToolResultFollowsTheResult(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.ToolExecutionAvailable = true
	openToolCallResponse(t, c, ls)
	exchangeTick(t, c, ls, toolCallAnswer())
	if _, ok := ls.Outputs.ToolInbox.Read(); !ok {
		t.Fatal("tool batch not dispatched")
	}

	exchangeTick(t, c, ls, secondUserTurn())
	if ls.Outputs.ModelInbox.Len() != 0 {
		t.Fatal("user turn dispatched while its tool result was outstanding")
	}
	exchangeTick(t, c, ls, toolResult())

	want := "[user:first assistant:tool_call tool:result user:second]"
	assertMessages(t, "continuation", nextRequest(t, ls).Messages, want)
	assertMessages(t, "history", ls.History.ConversationBuffer, want)
	if ls.Outputs.ModelInbox.Len() != 0 {
		t.Fatal("held user turn dispatched a second inference")
	}
}

// An interrupt cancels the open exchange; InterruptHandler places the held
// turn and its resumed inference answers it, so the coordinator must not
// answer it again.
func TestCoordinator_InterruptSettlesHeldUserTurn(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	openToolCallResponse(t, c, ls)
	exchangeTick(t, c, ls, secondUserTurn())

	interrupt := state.Buffers{UserControlPlaneMessage: []messages.Message{{
		Role:         messages.RoleUser,
		ContentParts: []messages.ContentPart{messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeInterrupt}},
	}}}
	ls.Inputs = interrupt
	if err := NewInterruptHandler(nil, nil, nil).Execute(context.Background(), ls); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	assertMessages(t, "history after interrupt", ls.History.ConversationBuffer, "[user:first assistant:tool_call user:second]")
	nextRequest(t, ls)
	exchangeTick(t, c, ls, interrupt)
	exchangeTick(t, c, ls, finalAnswer())

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
	exchangeTick(t, c, ls, state.Buffers{ModelInputDelta: []messages.StreamMessage{{Type: messages.StreamTypeError, Value: messages.NewErrorValue("boom")}}})

	exchangeTick(t, c, ls, secondUserTurn())

	assertResponseWindow(t, ls, len(ls.History.ConversationDeltaBuffer), 0)
	if req := nextRequest(t, ls); req.LoopPassID != 1 {
		t.Fatalf("inference request pass: got %d, want a fresh pass 1", req.LoopPassID)
	}
}

// SESSION.OPEN is session lifecycle, not response content: a user turn after
// it joins history at once instead of waiting for a MESSAGE.END that never
// comes.
func TestCoordinator_SessionOpenDoesNotHoldUserTurns(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Mode = state.DuplexSession
	exchangeTick(t, c, ls, state.Buffers{ModelInputDelta: []messages.StreamMessage{{
		Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("s", "session"),
	}}})

	exchangeTick(t, c, ls, secondUserTurn())

	assertMessages(t, "history", ls.History.ConversationBuffer, "[user:second]")
	assertMessages(t, "request", nextRequest(t, ls).Messages, "[user:second]")
}
