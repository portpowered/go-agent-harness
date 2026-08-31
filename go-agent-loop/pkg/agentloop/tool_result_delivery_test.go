package agentloop

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// recordingToolSession records everything sent to the provider and lets tests
// feed scripted inbound session events through its Receive buffer.
type recordingToolSession struct {
	mu   sync.Mutex
	sent []messages.StreamMessage
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}
	once sync.Once
}

func newRecordingToolSession() *recordingToolSession {
	return &recordingToolSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](64),
		done: make(chan struct{}),
	}
}

func (s *recordingToolSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	return true
}

func (s *recordingToolSession) sentMessages() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]messages.StreamMessage, len(s.sent))
	copy(out, s.sent)
	return out
}

func (s *recordingToolSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }

func (s *recordingToolSession) Done() <-chan struct{} { return s.done }

func (s *recordingToolSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

type recordingSessionInferencer struct{ session *recordingToolSession }

func (r recordingSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return r.session, nil
}

// cannedExecutor returns a fixed ToolCallResponse per call ID.
type cannedExecutor struct {
	mu        sync.Mutex
	responses map[string]messages.ToolCallResponse
	calls     []string
}

func (e *cannedExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call.ID)
	resp := e.responses[call.ID]
	e.mu.Unlock()
	return resp, nil
}

func (e *cannedExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func waitForSentCount(t *testing.T, s *recordingToolSession, typ messages.StreamMessageType, n int) []messages.StreamMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		matches := 0
		for _, msg := range s.sentMessages() {
			if msg.Type == typ {
				matches++
			}
		}
		if matches >= n {
			return s.sentMessages()
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d %s sends, got %d of %d total", n, typ, matches, len(s.sentMessages()))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func scriptModelToolCalls(s *recordingToolSession, ctx context.Context, calls []messages.ToolCall) {
	s.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()})
	for _, call := range calls {
		s.recv.Write(ctx, messages.StreamMessage{
			Type:       messages.StreamTypeToolCallStart,
			Role:       messages.RoleAssistant,
			ToolCallId: call.ID,
			Value:      messages.NewToolCallStartValue(call.ID, call.Name),
		})
		s.recv.Write(ctx, messages.StreamMessage{
			Type:       messages.StreamTypeToolCallEnd,
			Role:       messages.RoleAssistant,
			ToolCallId: call.ID,
			Value:      messages.NewToolCallEndValue(call.ID, call.Name, call.Arguments),
		})
	}
	s.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
}

func TestDuplexSession_ToolResultsForwardedToSessionSinkOnceInOrder(t *testing.T) {
	session := newRecordingToolSession()
	executor := &cannedExecutor{responses: map[string]messages.ToolCallResponse{
		"tc-1": {ToolCallID: "tc-1", Name: "get_weather", Content: `{"forecast":"sunny"}`},
		"tc-2": {ToolCallID: "tc-2", Name: "get_time", Content: `{"time":"noon"}`},
	}}

	al, err := New(
		WithMode(engine.DuplexSession),
		WithSessionInferencer(recordingSessionInferencer{session: session}),
		WithToolExecutor(executor),
		WithTools([]messages.ToolDefinition{
			{Name: "get_weather", Description: "weather"},
			{Name: "get_time", Description: "time"},
		}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- al.Run(ctx) }()

	scriptModelToolCalls(session, ctx, []messages.ToolCall{
		{ID: "tc-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
		{ID: "tc-2", Name: "get_time", Arguments: `{}`},
	})

	waitForSentCount(t, session, messages.StreamTypeToolCallEnd, 2)
	sent := waitForSentCount(t, session, messages.StreamTypeResponseCreate, 1)

	var forwards []*messages.ToolCallEndValue
	for _, msg := range sent {
		if msg.Type != messages.StreamTypeToolCallEnd {
			continue
		}
		v, ok := msg.Value.(*messages.ToolCallEndValue)
		if !ok {
			t.Fatalf("forward value = %T, want *messages.ToolCallEndValue", msg.Value)
		}
		forwards = append(forwards, v)
	}
	if len(forwards) != 2 {
		t.Fatalf("observed %d TOOLCALL.END forwards, want exactly 2", len(forwards))
	}
	responseCreateIndex := -1
	for index, msg := range sent {
		if msg.Type == messages.StreamTypeResponseCreate {
			responseCreateIndex = index
			break
		}
	}
	if responseCreateIndex < 0 {
		t.Fatal("missing response.create after tool results")
	}
	for index, msg := range sent {
		if msg.Type == messages.StreamTypeToolCallEnd && index > responseCreateIndex {
			t.Fatalf("tool result at index %d was sent after response.create at index %d", index, responseCreateIndex)
		}
	}
	want := []messages.ToolCallEndValue{
		{Type: "tool_use_end", ToolCallID: "tc-1", Name: "get_weather", Arguments: `{"forecast":"sunny"}`},
		{Type: "tool_use_end", ToolCallID: "tc-2", Name: "get_time", Arguments: `{"time":"noon"}`},
	}
	for i, v := range forwards {
		if *v != want[i] {
			t.Fatalf("forward[%d] = %+v, want %+v", i, *v, want[i])
		}
	}

	// A provider that re-surfaces the same call ID must not cause another
	// executor admission or another provider result item.
	scriptModelToolCalls(session, ctx, []messages.ToolCall{
		{ID: "tc-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
	})
	time.Sleep(150 * time.Millisecond)
	if got := executor.callCount(); got != 2 {
		t.Fatalf("executor call count after duplicate provider call = %d, want exactly 2 original admissions", got)
	}
	if got := countSentType(session, messages.StreamTypeToolCallEnd); got != 2 {
		t.Fatalf("tool result count after duplicate provider call = %d, want exactly 2", got)
	}

	// Quiesce and confirm no duplicate delivery of the same tool call IDs.
	time.Sleep(150 * time.Millisecond)
	if got := countSentType(session, messages.StreamTypeToolCallEnd); got != 2 {
		t.Fatalf("after quiescence observed %d TOOLCALL.END forwards, want exactly 2", got)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && err != context.Canceled {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func countSentType(s *recordingToolSession, typ messages.StreamMessageType) int {
	count := 0
	for _, msg := range s.sentMessages() {
		if msg.Type == typ {
			count++
		}
	}
	return count
}

func TestDuplexSession_ZeroToolResultsDeliverNothing(t *testing.T) {
	session := newRecordingToolSession()

	al, err := New(
		WithMode(engine.DuplexSession),
		WithSessionInferencer(recordingSessionInferencer{session: session}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = al.Run(ctx) }()

	// A plain text model turn with no tool calls must not deliver anything to
	// the session sink as a tool result.
	session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()})
	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewTextDeltaValue("no tools here"),
	})
	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})

	time.Sleep(200 * time.Millisecond)
	if got := countSentType(session, messages.StreamTypeToolCallEnd); got != 0 {
		t.Fatalf("tool-result pass without results delivered %d TOOLCALL.END sends, want 0", got)
	}

	cancel()
}
