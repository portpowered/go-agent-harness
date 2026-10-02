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

// requestHistories renders every inference request. It fails the test if
// one ends on an assistant message: providers treat a trailing assistant
// message as a prefill, and current Claude models reject it.
func (g *gatedFirstResponseInferencer) requestHistories(t *testing.T) [][]string {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([][]string, 0, len(g.requests))
	for index, req := range g.requests {
		if n := len(req.Messages); n == 0 || req.Messages[n-1].Role == messages.RoleAssistant {
			t.Errorf("inference request %d ends on an assistant message: %q", index, describeTurnHistory(req.Messages))
		}
		out = append(out, describeTurnHistory(req.Messages))
	}
	return out
}

// gatedToolExecutor blocks each call until gate closes and signals started
// when the first call begins.
type gatedToolExecutor struct {
	started chan struct{}
	gate    chan struct{}
	once    sync.Once
	calls   int
}

func (e *gatedToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls++
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.gate:
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Content: "ok"}, nil
}

// runUserTurnDuring executes "first", runs await until the exchange is at the
// point under test, sends "second", waits until the engine has handled it,
// then runs release to let the exchange finish. It returns the conversation
// history after the loop ended.
func runUserTurnDuring(t *testing.T, inf *gatedFirstResponseInferencer, exec messages.ToolExecutor, await func(Stream), release func()) []string {
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
	await(result.EventStream)
	if err := loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, "second")}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Every loop goroutine is idle again only once the engine has handled
	// the second user turn; the exchange under test is still open.
	synctest.Wait()
	release()
	for result.EventStream.HasNext() {
		result.EventStream.Response()
	}
	return describeTurnHistory(loop.GetConversationHistory())
}

// awaitEvent returns an await step that consumes the stream up to an event
// of type seen.
func awaitEvent(t *testing.T, seen messages.StreamMessageType) func(Stream) {
	t.Helper()
	return func(stream Stream) {
		t.Helper()
		for {
			if !stream.HasNext() {
				t.Fatalf("event stream ended before %s", seen)
			}
			if stream.Response().Type == seen {
				return
			}
		}
	}
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
		history := runUserTurnDuring(t, inf, exec, awaitEvent(t, messages.StreamTypeToolCallEnd), func() { close(inf.gate) })

		if got := exec.executed.Load(); got != 1 {
			t.Fatalf("tool executions: got %d, want 1 (the client saw call-x complete)", got)
		}
		assertTurnOrder(t, inf, history, []string{"user:first", "assistant:tool_call=call-x", "tool:result=call-x", "user:second", "assistant:second answer"})
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
		history := runUserTurnDuring(t, inf, &countingToolExecutor{}, awaitEvent(t, messages.StreamTypeTextEnd), func() { close(inf.gate) })

		assertTurnOrder(t, inf, history, []string{"user:first", "assistant:first answer", "user:second", "assistant:second answer"})
	})
}

// TestSendBeforeToolResultFollowsTheResult pins the gap between a tool-call
// response's MESSAGE.END and its tool result: a user turn landing there must
// not split the call from its result ([tool_call, user, tool] is rejected by
// Chat Completions and Anthropic). It joins history after the result, and
// the tool continuation answers it.
func TestSendBeforeToolResultFollowsTheResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inf := &gatedFirstResponseInferencer{gate: make(chan struct{}), first: []messages.StreamMessage{
			{Type: messages.StreamTypeToolCallStart, Value: messages.NewToolCallStartValue("call-x", "lookup")},
			{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("call-x", "lookup", "{}")},
		}}
		close(inf.gate) // the tool-call response completes at once
		exec := &gatedToolExecutor{started: make(chan struct{}), gate: make(chan struct{})}
		history := runUserTurnDuring(t, inf, exec, func(Stream) { <-exec.started }, func() { close(exec.gate) })

		if exec.calls != 1 {
			t.Fatalf("tool executions: got %d, want 1", exec.calls)
		}
		assertTurnOrder(t, inf, history, []string{"user:first", "assistant:tool_call=call-x", "tool:result=call-x", "user:second", "assistant:second answer"})
	})
}

// assertTurnOrder checks the final history and that exactly two inference
// requests ran: the first turn, then one request over everything before the
// last answer, so the second user turn was answered exactly once.
func assertTurnOrder(t *testing.T, inf *gatedFirstResponseInferencer, history, want []string) {
	t.Helper()
	if fmt.Sprint(history) != fmt.Sprint(want) {
		t.Fatalf("history:\n got %q\nwant %q", history, want)
	}
	requests := inf.requestHistories(t)
	wantRequests := [][]string{want[:1], want[:len(want)-1]}
	if fmt.Sprint(requests) != fmt.Sprint(wantRequests) {
		t.Fatalf("inference requests:\n got %q\nwant %q", requests, wantRequests)
	}
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
