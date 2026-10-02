package subsystems

import (
	"context"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/state"
)

// firstResponseText is the complete text of the first overlapping response.
const firstResponseText = "first"

func overlapDelta(responseID string, deltaType messages.StreamMessageType, value messages.StreamMessageValue) messages.StreamMessage {
	return messages.StreamMessage{Type: deltaType, ResponseID: responseID, Role: messages.RoleAssistant, Value: value}
}

func overlapStart(responseID string) messages.StreamMessage {
	return overlapDelta(responseID, messages.StreamTypeMessageStart, messages.NewMessageStartValue())
}

func overlapText(responseID, text string) messages.StreamMessage {
	return overlapDelta(responseID, messages.StreamTypeTextDelta, messages.NewTextDeltaValue(text))
}

func overlapEnd(responseID string) messages.StreamMessage {
	return overlapDelta(responseID, messages.StreamTypeMessageEnd, messages.NewMessageEndValue(messages.TokenUsage{}))
}

func engineOutput(text string, globalIndex int) messages.Message {
	message := messages.NewTextMessage(messages.RoleAssistant, text)
	message.GlobalIndex = globalIndex
	message.ActorID = "model"
	message.ActorStreamID = "stream-1"
	return message
}

func readUserTexts(ls *state.LoopState) []string {
	var texts []string
	for {
		request, ok := ls.Outputs.UserInbox.Read()
		if !ok {
			return texts
		}
		texts = append(texts, request.Message.TextContent())
	}
}

// TestCoordinator_OverlappingResponsesDispatchEachCompletedResponse covers a
// duplex session where a second provider response opens before the first
// ends. The engine's single-window reconstruction interleaves both, so the
// coordinator must replace it with each response's own assembly, at the
// response boundary, keeping the engine's trace metadata and history slot.
func TestCoordinator_OverlappingResponsesDispatchEachCompletedResponse(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Mode = state.DuplexSession
	user := messages.NewTextMessage(messages.RoleUser, "hi")
	mixed := engineOutput("first second", 7)
	ls.History.ConversationBuffer = []messages.Message{user, mixed}
	ls.Inputs.ModelInputDelta = []messages.StreamMessage{
		overlapStart("r1"), overlapText("r1", firstResponseText),
		overlapStart("r2"), overlapText("r2", "second"),
		overlapEnd("r1"),
	}
	ls.Inputs.ModelOutputMessage = []messages.Message{mixed}

	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if got := readUserTexts(ls); len(got) != 1 || got[0] != firstResponseText {
		t.Fatalf("first tick delivered %q, want only the completed first response", got)
	}
	if got := ls.Inputs.ModelOutputMessage; len(got) != 1 || got[0].TextContent() != firstResponseText || got[0].GlobalIndex != 7 || got[0].ActorID != "model" || got[0].ActorStreamID != "stream-1" {
		t.Fatalf("model outputs = %+v, want the first response with the engine's metadata", got)
	}
	if history := ls.History.ConversationBuffer; len(history) != 2 || history[1].TextContent() != firstResponseText {
		t.Fatalf("history = %d messages ending %q, want the interleaved engine output replaced", len(history), history[len(history)-1].TextContent())
	}
	if ls.Inputs.TerminateLoop {
		t.Fatal("duplex session terminated on a model response")
	}

	// The engine output for the second tick is not at the history tail (the
	// kernel already recorded something after it), so history keeps it and
	// gains the completed second response.
	ls.Inputs = state.Buffers{
		ModelInputDelta:    []messages.StreamMessage{overlapText("r2", " more"), overlapEnd("r2")},
		ModelOutputMessage: []messages.Message{engineOutput("second more", 9)},
	}
	if err := c.Execute(context.Background(), ls); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got := readUserTexts(ls); len(got) != 1 || got[0] != "second more" {
		t.Fatalf("second tick delivered %q, want the second response's own text", got)
	}
	if got := ls.Inputs.ModelOutputMessage; len(got) != 1 || got[0].GlobalIndex != 9 {
		t.Fatalf("second model outputs = %+v, want global index 9", got)
	}
	if history := ls.History.ConversationBuffer; len(history) != 3 || !strings.Contains(history[2].TextContent(), "second more") {
		t.Fatalf("history after the second response has %d messages, want 3", len(history))
	}
}

// TestCoordinator_UnknownResponseDeltaIsRejected keeps a delta for a response
// that never started from being folded into a live sibling's message.
func TestCoordinator_UnknownResponseDeltaIsRejected(t *testing.T) {
	c := NewCoordinator(nil)
	ls := newCoordinatorTestState()
	ls.Inputs.ModelInputDelta = []messages.StreamMessage{overlapStart("r1"), overlapText("ghost", "x")}
	if err := c.Execute(context.Background(), ls); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("Execute error = %v, want an unknown-response rejection", err)
	}
}
