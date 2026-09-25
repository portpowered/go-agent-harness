package agentloop

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// londonWeatherReply is the scripted assistant reply shared by weather tests.
const londonWeatherReply = "The weather in London is sunny."

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

func (e *cannedExecutor) callIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
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

// assertDuplicateProviderCallIgnored re-surfaces the already-completed call
// tc-1, which must cause neither another executor admission nor another
// provider result item. A fresh call tc-3 scripted after it is a sequence
// barrier: once its result is delivered, the duplicate ahead of it on the
// same ordered provider stream was handled too.
func assertDuplicateProviderCallIgnored(ctx context.Context, t *testing.T, session *recordingToolSession, executor *cannedExecutor) {
	t.Helper()
	scriptModelToolCalls(session, ctx, []messages.ToolCall{
		{ID: "tc-1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
	})
	scriptModelToolCalls(session, ctx, []messages.ToolCall{
		{ID: "tc-3", Name: "get_time", Arguments: `{}`},
	})
	waitForSentCount(t, session, messages.StreamTypeToolCallEnd, 3)
	// Calls of one turn execute concurrently, so compare admissions as a set.
	admitted := executor.callIDs()
	sort.Strings(admitted)
	if got := strings.Join(admitted, ","); got != "tc-1,tc-2,tc-3" {
		t.Fatalf("executor admissions after duplicate provider call = %v, want tc-1, tc-2 and tc-3 once each", admitted)
	}
	delivered := map[string]int{}
	for _, msg := range session.sentMessages() {
		if v, ok := msg.Value.(*messages.ToolCallEndValue); ok && msg.Type == messages.StreamTypeToolCallEnd {
			delivered[v.ToolCallID]++
		}
	}
	if delivered["tc-1"] != 1 || delivered["tc-2"] != 1 || delivered["tc-3"] != 1 || len(delivered) != 3 {
		t.Fatalf("tool results delivered per call = %v, want each call exactly once", delivered)
	}
}

func TestDuplexSession_ToolResultsForwardedToSessionSinkOnceInOrder(t *testing.T) {
	session := newRecordingToolSession()
	executor := &cannedExecutor{responses: map[string]messages.ToolCallResponse{
		"tc-1": {ToolCallID: "tc-1", Name: "get_weather", Content: `{"forecast":"sunny"}`},
		"tc-2": {ToolCallID: "tc-2", Name: "get_time", Content: `{"time":"noon"}`},
		"tc-3": {ToolCallID: "tc-3", Name: "get_time", Content: `{"time":"one"}`},
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

	assertDuplicateProviderCallIgnored(ctx, t, session, executor)

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

func TestDuplexSession_ZeroToolResultsDeliverNothing(t *testing.T) {
	session := newRecordingToolSession()
	executor := &cannedExecutor{responses: map[string]messages.ToolCallResponse{
		"tc-barrier": {ToolCallID: "tc-barrier", Name: "get_time", Content: `{"time":"noon"}`},
	}}

	al, err := New(
		WithMode(engine.DuplexSession),
		WithSessionInferencer(recordingSessionInferencer{session: session}),
		WithToolExecutor(executor),
		WithTools([]messages.ToolDefinition{{Name: "get_time", Description: "time"}}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- al.Run(ctx) }()

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

	// A tool turn scripted after the text turn is a sequence barrier: once
	// its single result is delivered, the text turn ahead of it on the same
	// ordered stream was fully handled and must have delivered nothing.
	scriptModelToolCalls(session, ctx, []messages.ToolCall{{ID: "tc-barrier", Name: "get_time", Arguments: `{}`}})
	sent := waitForSentCount(t, session, messages.StreamTypeToolCallEnd, 1)
	var delivered []string
	for _, msg := range sent {
		if v, ok := msg.Value.(*messages.ToolCallEndValue); ok && msg.Type == messages.StreamTypeToolCallEnd {
			delivered = append(delivered, v.ToolCallID)
		}
	}
	if len(delivered) != 1 || delivered[0] != "tc-barrier" {
		t.Fatalf("TOOLCALL.END sends = %v, want only the barrier call's result (the text turn must deliver none)", delivered)
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}
