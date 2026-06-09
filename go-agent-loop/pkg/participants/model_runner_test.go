package participants

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

type testInferencer struct {
	responses []messages.InferenceResult
	callCount int
	stream    <-chan messages.StreamMessage
}

func (t *testInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	idx := t.callCount
	if idx >= len(t.responses) {
		idx = len(t.responses) - 1
	}
	t.callCount++
	return t.responses[idx], nil
}

func (t *testInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	if t.stream != nil {
		return t.stream, nil
	}
	return nil, nil
}

// drainModelDeltas reads from DeltaOutbox until MESSAGE.END or ERROR, accumulating
// text and tool calls. Returns assembled text, tool calls, and whether an error delta arrived.
func drainModelDeltas(t *testing.T, ctx context.Context, runner *ModelRunner) (text string, toolCalls []messages.ToolCall, gotErr bool) {
	t.Helper()
	var curToolID, curToolName string
	for {
		delta, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
		if !ok {
			t.Fatal("context cancelled waiting for model delta")
		}
		switch v := delta.Value.(type) {
		case *messages.TextDeltaValue:
			text += v.Content
		case *messages.ToolCallStartValue:
			curToolID = v.ToolCallID
			curToolName = v.Name
		case *messages.ToolCallEndValue:
			id := v.ToolCallID
			if id == "" {
				id = curToolID
			}
			name := v.Name
			if name == "" {
				name = curToolName
			}
			toolCalls = append(toolCalls, messages.ToolCall{ID: id, Name: name, Arguments: v.Arguments})
		case *messages.MessageEndValue:
			_ = v
			return
		case *messages.ErrorValue:
			_ = v
			gotErr = true
			return
		}
	}
}

func TestModelRunner_SimpleInference(t *testing.T) {
	inf := &testInferencer{
		responses: []messages.InferenceResult{
			{
				Message: messages.NewTextMessage(messages.RoleAssistant, "Hello!"),
			},
		},
	}

	runner := NewModelRunner(inf, 10)
	ap := NewActiveParticipant(messages.Model, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.InferenceRequest{
		Messages: []messages.Message{messages.NewTextMessage(messages.RoleUser, "hi")},
	})

	text, _, gotErr := drainModelDeltas(t, ctx, runner)
	if gotErr {
		t.Fatal("unexpected error delta")
	}
	if text != "Hello!" {
		t.Errorf("expected 'Hello!', got %q", text)
	}
}

func TestModelRunner_WithToolCalls(t *testing.T) {
	inf := &testInferencer{
		responses: []messages.InferenceResult{
			{
				Message: messages.NewTextMessage(messages.RoleAssistant, "Hello!"),
				ToolCalls: []messages.ToolCall{
					{ID: "tc1", Name: "get_weather", Arguments: `{"city":"London"}`},
				},
			},
		},
	}

	runner := NewModelRunner(inf, 10)
	ap := NewActiveParticipant(messages.Model, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.InferenceRequest{
		Messages: []messages.Message{messages.NewTextMessage(messages.RoleUser, "weather?")},
		Tools:    []messages.ToolDefinition{{Name: "get_weather"}},
	})

	_, toolCalls, gotErr := drainModelDeltas(t, ctx, runner)
	if gotErr {
		t.Fatal("unexpected error delta")
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Name != "get_weather" {
		t.Errorf("expected tool 'get_weather', got %q", toolCalls[0].Name)
	}
}

func TestModelRunner_NonStreamingFallbackMarksSynthesizedMessageEnd(t *testing.T) {
	inf := &testInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "fallback")},
		},
	}
	runner := NewModelRunner(inf, 10)
	ap := NewActiveParticipant(messages.Model, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.InferenceRequest{
		Messages: []messages.Message{messages.NewTextMessage(messages.RoleUser, "hi")},
	})

	for {
		delta, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
		if !ok {
			t.Fatal("context cancelled waiting for model delta")
		}
		if delta.Type != messages.StreamTypeMessageEnd {
			continue
		}
		value, ok := delta.Value.(*messages.MessageEndValue)
		if !ok {
			t.Fatalf("MESSAGE.END value type = %T, want *MessageEndValue", delta.Value)
		}
		if messages.MessageEndTerminalSource(value) != messages.TerminalSourceLoopSynthesized {
			t.Fatalf("terminal source = %q, want %q", messages.MessageEndTerminalSource(value), messages.TerminalSourceLoopSynthesized)
		}
		return
	}
}

func TestModelRunner_StreamMessageEndDefaultsToProviderAuthored(t *testing.T) {
	stream := make(chan messages.StreamMessage, 1)
	stream <- messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
	close(stream)
	inf := &testInferencer{stream: stream}
	runner := NewModelRunner(inf, 10)
	ap := NewActiveParticipant(messages.Model, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.InferenceRequest{
		Messages: []messages.Message{messages.NewTextMessage(messages.RoleUser, "hi")},
	})

	delta, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
	if !ok {
		t.Fatal("context cancelled waiting for model delta")
	}
	value, ok := delta.Value.(*messages.MessageEndValue)
	if !ok {
		t.Fatalf("MESSAGE.END value type = %T, want *MessageEndValue", delta.Value)
	}
	if messages.MessageEndTerminalSource(value) != messages.TerminalSourceProvider {
		t.Fatalf("terminal source = %q, want %q", messages.MessageEndTerminalSource(value), messages.TerminalSourceProvider)
	}
}

func TestModelRunner_StreamCloseWithoutMessageEndMarksSynthesizedMessageEnd(t *testing.T) {
	stream := make(chan messages.StreamMessage)
	close(stream)
	inf := &testInferencer{stream: stream}
	runner := NewModelRunner(inf, 10)
	ap := NewActiveParticipant(messages.Model, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.InferenceRequest{
		Messages: []messages.Message{messages.NewTextMessage(messages.RoleUser, "hi")},
	})

	delta, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
	if !ok {
		t.Fatal("context cancelled waiting for model delta")
	}
	value, ok := delta.Value.(*messages.MessageEndValue)
	if !ok {
		t.Fatalf("MESSAGE.END value type = %T, want *MessageEndValue", delta.Value)
	}
	if messages.MessageEndTerminalSource(value) != messages.TerminalSourceLoopSynthesized {
		t.Fatalf("terminal source = %q, want %q", messages.MessageEndTerminalSource(value), messages.TerminalSourceLoopSynthesized)
	}
}

func TestModelRunner_ContextCancellation(t *testing.T) {
	inf := &testInferencer{
		responses: []messages.InferenceResult{
			{Message: messages.NewTextMessage(messages.RoleAssistant, "ok")},
		},
	}

	runner := NewModelRunner(inf, 10)
	ap := NewActiveParticipant(messages.Model, runner)

	ctx, cancel := context.WithCancel(context.Background())
	ap.Start(ctx)

	// Cancel immediately - runner should stop
	cancel()
	ap.Stop() // should not hang
}

func TestSessionModelRunner_SessionDoneEmitsSessionClose(t *testing.T) {
	session := newCompletedSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 10, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()

	_ = session.Close()

	delta, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
	if !ok {
		t.Fatal("context cancelled waiting for session close delta")
	}
	if delta.Type != messages.StreamTypeSessionClose {
		t.Fatalf("delta type = %s, want %s", delta.Type, messages.StreamTypeSessionClose)
	}
}

type testSessionInferencer struct {
	session messages.Session
}

func (i *testSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

func TestSessionModelRunner_DrainsPendingMessagesWhenSessionDone(t *testing.T) {
	session := newCompletedSession()
	for i := 0; i < 10; i++ {
		session.recv.Write(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue("x"),
		})
	}
	close(session.done)

	runner := NewSessionModelRunner(&completedSessionInferencer{session: session}, 16, nil)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	textDeltas := 0
	sessionCloses := 0
	for {
		delta, ok := runner.DeltaOutbox.Read()
		if !ok {
			break
		}
		switch delta.Type {
		case messages.StreamTypeTextDelta:
			textDeltas++
		case messages.StreamTypeSessionClose:
			sessionCloses++
		}
	}
	if textDeltas != 10 {
		t.Fatalf("drained %d pending text deltas, want 10", textDeltas)
	}
	if sessionCloses != 1 {
		t.Fatalf("emitted %d session close deltas, want 1", sessionCloses)
	}
}

type completedSessionInferencer struct {
	session *completedSession
}

func (i *completedSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type completedSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}
	once sync.Once
}

func newCompletedSession() *completedSession {
	return &completedSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](16),
		done: make(chan struct{}),
	}
}

func (s *completedSession) Send(context.Context, messages.StreamMessage) bool {
	return true
}

func (s *completedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *completedSession) Done() <-chan struct{} {
	return s.done
}

func (s *completedSession) Close() error {
	s.once.Do(func() {
		select {
		case <-s.done:
			return
		default:
		}
		close(s.done)
	})
	return nil
}
