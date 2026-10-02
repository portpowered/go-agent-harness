package sessions

import (
	"fmt"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// TestSessionUserTurnDuringToolCallResponseStillExecutesToolCall pins the
// dropped tool call behind "tool invocation tally: got 0". The ordering is the
// realistic duplex one: the provider answers the first user turn only after
// receiving it, streams a tool call the client sees complete, and the user
// types again before the provider's MESSAGE.END. Nothing cancelled the
// response, so its tool call must execute. The second user turn reaches the
// provider at once, but joins history after the tool call and its result, so
// the result still directly follows its call.
func TestSessionUserTurnDuringToolCallResponseStillExecutesToolCall(t *testing.T) {
	t.Parallel()
	const wait = 3 * time.Second
	inf := NewMockSessionInferencer()
	tool := NewMockToolExecutor().AddResult("lookup", `{"ok":true}`)
	scenario := NewSessionScenario(t, inf, tool)
	scenario.Start()
	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, wait) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}

	scenario.SendText("first: please look this up")
	if !inf.WaitForSentCount(t.Context(), messages.StreamTypeTextDelta, 1, wait) {
		t.Fatal("provider never received the first user turn")
	}
	inf.AddServerEventSequence(t.Context(), []messages.StreamMessage{
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue("call-x", "lookup")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue("call-x", "lookup", `{}`)},
	})
	if !scenario.WaitForEvent(messages.StreamTypeToolCallEnd, wait) {
		t.Fatal("client never saw TOOLCALL.END")
	}

	scenario.SendText("second: also, one more thing")
	if !inf.WaitForSentCount(t.Context(), messages.StreamTypeTextDelta, 2, wait) {
		t.Fatal("provider never received the second user turn")
	}
	inf.AddServerEvent(t.Context(), messages.StreamMessage{
		Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})

	// The tool result reaches the provider in the tick that records it in
	// history, so once it arrived both the execution and the history are final.
	if !inf.WaitForSentCount(t.Context(), messages.StreamTypeToolCallEnd, 1, wait) {
		t.Fatalf("provider never received the tool result: the tool call the client saw complete was dropped (executor calls: %d)", len(tool.Calls()))
	}
	history := scenario.Loop.GetConversationHistory()
	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if calls := tool.Calls(); len(calls) != 1 || calls[0].ID != "call-x" {
		t.Fatalf("tool executions: got %+v, want exactly call-x", calls)
	}
	if cancels := inf.SentCount(messages.StreamTypeResponseCancel); cancels != 0 {
		t.Fatalf("RESPONSE.CANCEL sent %d times: a typed user turn must not cancel the response", cancels)
	}
	want := []string{
		"user:first: please look this up",
		"assistant:tool_call=call-x",
		"tool:result=call-x",
		"user:second: also, one more thing",
	}
	if got := describeHistory(history); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("history:\n got %q\nwant %q", got, want)
	}
}

// describeHistory renders each history message as role plus its identifying
// content: text, tool call IDs, or the tool result's call ID.
func describeHistory(history []messages.Message) []string {
	out := make([]string, 0, len(history))
	for _, msg := range history {
		switch {
		case len(msg.ToolCalls) > 0:
			for _, call := range msg.ToolCalls {
				out = append(out, fmt.Sprintf("%s:tool_call=%s", msg.Role, call.ID))
			}
		case msg.Role == messages.RoleTool:
			out = append(out, fmt.Sprintf("%s:result=%s", msg.Role, msg.ToolCallID))
		default:
			out = append(out, fmt.Sprintf("%s:%s", msg.Role, msg.TextContent()))
		}
	}
	return out
}

// TestSessionCloseRecordsUserTurnHeldDuringResponse closes the session while
// a response is still open and a typed user turn is held behind it. The
// provider already received that turn, so history must still record it.
func TestSessionCloseRecordsUserTurnHeldDuringResponse(t *testing.T) {
	t.Parallel()
	const wait = 3 * time.Second
	inf := NewMockSessionInferencer()
	scenario := NewSessionScenario(t, inf, NewMockToolExecutor())
	scenario.Start()
	if !scenario.WaitForEvent(messages.StreamTypeSessionOpen, wait) {
		t.Fatal("timed out waiting for SESSION.OPEN")
	}
	scenario.SendText("first")
	if !inf.WaitForSentCount(t.Context(), messages.StreamTypeTextDelta, 1, wait) {
		t.Fatal("provider never received the first user turn")
	}
	inf.AddServerEvent(t.Context(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("partial")})
	if !scenario.WaitForEvent(messages.StreamTypeTextDelta, wait) {
		t.Fatal("client never saw the open response")
	}
	scenario.SendText("second")
	if !inf.WaitForSentCount(t.Context(), messages.StreamTypeTextDelta, 2, wait) {
		t.Fatal("provider never received the second user turn")
	}

	if err := scenario.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got, want := describeHistory(scenario.Loop.GetConversationHistory()), []string{"user:first", "user:second"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("history after session close:\n got %q\nwant %q", got, want)
	}
}
