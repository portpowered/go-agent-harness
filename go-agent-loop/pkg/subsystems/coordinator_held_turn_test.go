package subsystems

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

func controlTick(cpType messages.ControlPlaneMessageType) state.Buffers {
	return state.Buffers{UserControlPlaneMessage: []messages.Message{{
		Role:         messages.RoleUser,
		ContentParts: []messages.ContentPart{messages.ControlPlanePart{ControlPlaneMessageType: cpType}},
	}}}
}

// A duplex session that closes or stops before the response a held turn
// waited for ends must still record that turn: the provider already received
// it. It joins history and the kernel stream as the loop ends.
func TestCoordinator_SessionEndRecordsHeldUserTurn(t *testing.T) {
	for _, cpType := range []messages.ControlPlaneMessageType{messages.ControlPlaneMessageTypeSessionClose, messages.ControlPlaneMessageTypeStop} {
		t.Run(string(cpType), func(t *testing.T) {
			c := NewCoordinator(nil)
			ls := newCoordinatorTestState()
			ls.Mode = state.DuplexSession
			ls.ToolExecutionAvailable = true
			openToolCallResponse(t, c, ls)
			exchangeTick(t, c, ls, secondUserTurn())
			nextRequest(t, ls)

			exchangeTick(t, c, ls, controlTick(cpType))

			if !ls.Inputs.TerminateLoop {
				t.Fatal("session end did not terminate the loop")
			}
			assertMessages(t, "history", ls.History.ConversationBuffer, "[user:first user:second]")
			if kernel, ok := ls.Outputs.KernelDeltaInbox.Read(); !ok || kernel.Source != messages.User {
				t.Fatalf("kernel record: got %+v ok=%t, want the held user turn", kernel, ok)
			}
		})
	}
}

// A user's input transcription (TRANSCRIPT deltas with RoleUser, as the
// OpenAI and Grok realtime providers emit them) is not an assistant
// response, so a typed turn after it is not held behind its own answer.
func TestCoordinator_UserInputTranscriptDoesNotHoldTypedTurn(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Mode = state.DuplexSession
	exchangeTick(t, c, ls, state.Buffers{ModelInputDelta: []messages.StreamMessage{{
		Type: messages.StreamTypeTranscriptDelta, Role: messages.RoleUser, Value: messages.NewTranscriptDeltaValue("spoken words"),
	}}})
	exchangeTick(t, c, ls, secondUserTurn())
	exchangeTick(t, c, ls, state.Buffers{ModelInputDelta: []messages.StreamMessage{{
		Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("reply"),
	}}})
	exchangeTick(t, c, ls, finalAnswer())

	assertMessages(t, "history", ls.History.ConversationBuffer, "[user:second assistant:answer]")
}

// Several turn-based user turns held behind one open response are answered
// by exactly one inference, and the loop does not end on that answer.
func TestCoordinator_HeldTurnsAreAnsweredOnce(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.ToolExecutionAvailable = true
	openToolCallResponse(t, c, ls)
	exchangeTick(t, c, ls, secondUserTurn())
	exchangeTick(t, c, ls, state.Buffers{UserOutputMessage: []messages.Message{messages.NewTextMessage(messages.RoleUser, "third")}})
	if n := ls.Outputs.ModelInbox.Len(); n != 0 {
		t.Fatalf("dispatched %d inferences while the turns were held", n)
	}

	exchangeTick(t, c, ls, finalAnswer())

	assertMessages(t, "request", nextRequest(t, ls).Messages, "[user:first assistant:answer user:second user:third]")
	if ls.Inputs.TerminateLoop || ls.Outputs.ModelInbox.Len() != 0 {
		t.Fatalf("terminate=%t queued=%d: want one inference and a live loop", ls.Inputs.TerminateLoop, ls.Outputs.ModelInbox.Len())
	}
}

// A subsystem after the coordinator may end the loop; CoordinatorDelta still
// places held turns ahead of LOOP.END.
func TestCoordinatorDelta_PlacesHeldUserTurnsBeforeLoopEnd(t *testing.T) {
	ls := newCoordinatorTestState()
	ls.History.HeldUserMessages = []messages.Message{messages.NewTextMessage(messages.RoleUser, "second")}
	ls.Inputs.TerminateLoop = true

	if err := NewCoordinatorDelta(ls.Outputs.KernelDeltaInbox, nil).Execute(context.Background(), ls); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	assertMessages(t, "history", ls.History.ConversationBuffer, "[user:second]")
	first, _ := ls.Outputs.KernelDeltaInbox.Read()
	last, _ := ls.Outputs.KernelDeltaInbox.Read()
	if first.Source != messages.User || last.Delta.Type != messages.StreamTypeLoopEnd {
		t.Fatalf("kernel stream: got %s then %s, want the user turn then LOOP.END", first.Source, last.Delta.Type)
	}
}

// An interrupt in the gap between a tool call and its result, with no text
// and no held turns, cancels the batch: the call gets a cancelled result so
// the resumed request is valid and ends on that result.
func TestInterruptHandler_CancelledToolCallGetsAResult(t *testing.T) {
	ls := newCoordinatorTestState()
	ls.History.ConversationBuffer = []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "first"),
		{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{{ID: "call-x", Name: "lookup"}, {ID: "call-y", Name: "lookup"}}},
		{Role: messages.RoleTool, ToolCallID: "call-y", ContentParts: []messages.ContentPart{messages.NewTextPart("ok")}},
	}
	ls.Inputs = controlTick(messages.ControlPlaneMessageTypeInterrupt)

	if err := NewInterruptHandler(nil, nil, nil).Execute(context.Background(), ls); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	history := ls.History.ConversationBuffer
	assertMessages(t, "history", history, "[user:first assistant:tool_call tool:result tool:result]")
	if got := history[3]; got.ToolCallID != "call-x" || got.TextContent() != interruptedToolResultText {
		t.Fatalf("cancelled result: got %+v, want call-x cancelled", got)
	}
	nextRequest(t, ls)
}
