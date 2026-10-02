package agentloop

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// gatedFirstResponseInferencer streams its first response up to a gate, so a
// test can send a user turn while that response is still open, then finishes
// it. Every later call answers "second answer".
type gatedFirstResponseInferencer struct {
	first []messages.StreamMessage // first response's deltas before the gate
	gate  chan struct{}

	mu       sync.Mutex
	requests []messages.InferenceRequest
}

func (g *gatedFirstResponseInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, fmt.Errorf("gatedFirstResponseInferencer: streaming only")
}

func (g *gatedFirstResponseInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	g.mu.Lock()
	call := len(g.requests)
	g.requests = append(g.requests, req)
	g.mu.Unlock()
	ch := make(chan messages.StreamMessage, 16)
	go func() {
		defer close(ch)
		ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}
		if call == 0 {
			for _, delta := range g.first {
				ch <- delta
			}
			select {
			case <-g.gate:
			case <-ctx.Done():
				return
			}
		} else {
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("second answer")}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()}
		}
		ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
	}()
	return ch, nil
}

func (g *gatedFirstResponseInferencer) requestHistories() [][]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([][]string, 0, len(g.requests))
	for _, req := range g.requests {
		out = append(out, describeTurnHistory(req.Messages))
	}
	return out
}

// runUserTurnDuringOpenResponse executes "first", waits until the client saw
// the first response's last pre-gate delta, sends "second" while that
// response is still open, and only then lets the response finish. It returns
// the conversation history after the loop ended.
func runUserTurnDuringOpenResponse(t *testing.T, inf *gatedFirstResponseInferencer, exec messages.ToolExecutor, seen messages.StreamMessageType) []string {
	t.Helper()
	loop, err := New(WithInferencer(inf), WithToolExecutor(exec))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result, err := loop.ExecuteStreaming(ctx, NewExecuteInput("first"))
	if err != nil {
		t.Fatalf("ExecuteStreaming: %v", err)
	}
	for sawOpenResponse := false; !sawOpenResponse; {
		if !result.EventStream.HasNext() {
			t.Fatalf("event stream ended before the open response's %s", seen)
		}
		sawOpenResponse = result.EventStream.Response().Type == seen
	}
	if err := loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, "second")}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Every loop goroutine is idle again only once the engine has recorded
	// and dispatched the second user turn; the first response is still open.
	synctest.Wait()
	close(inf.gate)
	for result.EventStream.HasNext() {
		result.EventStream.Response()
	}
	return describeTurnHistory(loop.GetConversationHistory())
}

// TestSendDuringToolCallResponseStillExecutesToolCall pins the turn-based
// form of the dropped tool call: a user turn sent while a response that
// already streamed a complete tool call is still open must not retire that
// response. Its tool call executes, and the continuation answers both turns.
func TestSendDuringToolCallResponseStillExecutesToolCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inf := &gatedFirstResponseInferencer{gate: make(chan struct{}), first: []messages.StreamMessage{
			{Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue("call-x", "lookup")},
			{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("call-x", "lookup", "{}")},
		}}
		exec := &countingToolExecutor{}
		history := runUserTurnDuringOpenResponse(t, inf, exec, messages.StreamTypeToolCallEnd)

		if got := exec.executed.Load(); got != 1 {
			t.Fatalf("tool executions: got %d, want 1 (the client saw call-x complete)", got)
		}
		want := []string{"user:first", "user:second", "assistant:tool_call=call-x", "tool:result=call-x", "assistant:second answer"}
		if fmt.Sprint(history) != fmt.Sprint(want) {
			t.Fatalf("history:\n got %q\nwant %q", history, want)
		}
		requests := inf.requestHistories()
		wantContinuation := want[:4]
		if len(requests) != 2 || fmt.Sprint(requests[1]) != fmt.Sprint(wantContinuation) {
			t.Fatalf("inference requests: got %q, want the first turn and then the tool continuation %q", requests, wantContinuation)
		}
	})
}

// TestSendDuringFinalResponseAnswersBothTurns pins the text form: a user turn
// sent while a final text response is still open neither discards that
// response nor goes unanswered. The open response completes, and the loop
// then answers the deferred turn instead of ending.
func TestSendDuringFinalResponseAnswersBothTurns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inf := &gatedFirstResponseInferencer{gate: make(chan struct{}), first: []messages.StreamMessage{
			{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()},
			{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("first answer")},
			{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()},
		}}
		history := runUserTurnDuringOpenResponse(t, inf, &countingToolExecutor{}, messages.StreamTypeTextEnd)

		want := []string{"user:first", "user:second", "assistant:first answer", "assistant:second answer"}
		if fmt.Sprint(history) != fmt.Sprint(want) {
			t.Fatalf("history:\n got %q\nwant %q", history, want)
		}
		requests := inf.requestHistories()
		if len(requests) != 2 || fmt.Sprint(requests[1]) != fmt.Sprint(want[:3]) {
			t.Fatalf("inference requests: got %q, want the deferred turn answered over %q", requests, want[:3])
		}
	})
}

// describeTurnHistory renders each message as role plus its identifying
// content: text, tool call IDs, or the tool result's call ID.
func describeTurnHistory(history []messages.Message) []string {
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
