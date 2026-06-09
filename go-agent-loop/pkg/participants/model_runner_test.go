package participants

import (
	"context"
	"errors"
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

type streamInferencer struct {
	deltas []messages.StreamMessage
}

func (s *streamInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, nil
}

type controlledStreamInferencer struct {
	stream chan messages.StreamMessage
	ready  chan struct{}
	once   sync.Once
}

func (s *controlledStreamInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, nil
}

func (s *controlledStreamInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	if s.ready != nil {
		s.once.Do(func() { close(s.ready) })
	}
	return s.stream, nil
}

type streamErrorInferencer struct {
	err error
}

type cancelThenClosedStreamInferencer struct {
	ready chan struct{}
	once  sync.Once
}

func (s *streamErrorInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, nil
}

func (s *streamErrorInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return nil, s.err
}

func (s *cancelThenClosedStreamInferencer) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, nil
}

func (s *cancelThenClosedStreamInferencer) InferStream(ctx context.Context, _ messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	if s.ready != nil {
		s.once.Do(func() { close(s.ready) })
	}
	<-ctx.Done()
	ch := make(chan messages.StreamMessage)
	close(ch)
	return ch, nil
}

func (s *streamInferencer) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, len(s.deltas))
	for _, delta := range s.deltas {
		ch <- delta
	}
	close(ch)
	return ch, nil
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

func drainTerminalDelta(t *testing.T, ctx context.Context, runner *ModelRunner) messages.StreamMessage {
	t.Helper()
	for {
		delta, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
		if !ok {
			t.Fatal("context cancelled waiting for terminal model delta")
		}
		switch delta.Value.(type) {
		case *messages.MessageEndValue, *messages.ErrorValue:
			return delta
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

func TestModelRunner_ProviderAuthoredCompletionGetsTerminalMetadata(t *testing.T) {
	inf := &streamInferencer{
		deltas: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("done")},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
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

	terminal := drainTerminalDelta(t, ctx, runner)
	value, ok := terminal.Value.(*messages.MessageEndValue)
	if !ok {
		t.Fatalf("terminal value = %T, want *messages.MessageEndValue", terminal.Value)
	}
	if value.TerminalReason != messages.TerminalReasonProviderAuthoredCompletion {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonProviderAuthoredCompletion)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceProvider {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceProvider)
	}
	if value.OutputState != messages.TerminalOutputComplete {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputComplete)
	}
}

func TestModelRunner_LoopSynthesizedCompletionGetsTerminalMetadata(t *testing.T) {
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

	terminal := drainTerminalDelta(t, ctx, runner)
	value, ok := terminal.Value.(*messages.MessageEndValue)
	if !ok {
		t.Fatalf("terminal value = %T, want *messages.MessageEndValue", terminal.Value)
	}
	if value.TerminalReason != messages.TerminalReasonLoopSynthesizedCompletion {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonLoopSynthesizedCompletion)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceLoop {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceLoop)
	}
	if value.OutputState != messages.TerminalOutputComplete {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputComplete)
	}
}

func TestModelRunner_ProviderCloseGetsDistinctTerminalMetadata(t *testing.T) {
	inf := &streamInferencer{
		deltas: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("partial")},
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

	terminal := drainTerminalDelta(t, ctx, runner)
	value, ok := terminal.Value.(*messages.MessageEndValue)
	if !ok {
		t.Fatalf("terminal value = %T, want *messages.MessageEndValue", terminal.Value)
	}
	if value.TerminalReason != messages.TerminalReasonProviderClose {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonProviderClose)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceProvider {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceProvider)
	}
	if value.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputPartial)
	}
}

func TestModelRunner_TerminalFailureAfterPartialOutputGetsTerminalMetadata(t *testing.T) {
	inf := &streamInferencer{
		deltas: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("partial")},
			{Type: messages.StreamTypeError, Role: messages.RoleAssistant, Value: messages.NewErrorValue("provider failed")},
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

	terminal := drainTerminalDelta(t, ctx, runner)
	value, ok := terminal.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("terminal value = %T, want *messages.ErrorValue", terminal.Value)
	}
	if value.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonTerminalFailure)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceProvider {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceProvider)
	}
	if value.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputPartial)
	}
}

func TestModelRunner_CancellationGetsDistinctTerminalError(t *testing.T) {
	inf := &controlledStreamInferencer{stream: make(chan messages.StreamMessage), ready: make(chan struct{})}

	runner := NewModelRunner(inf, 10)
	ap := NewActiveParticipant(messages.Model, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.InferenceRequest{
		Messages: []messages.Message{messages.NewTextMessage(messages.RoleUser, "hi")},
	})
	<-inf.ready
	runner.CancelCurrentExecution()

	terminal := drainTerminalDelta(t, ctx, runner)
	value, ok := terminal.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("terminal value = %T, want *messages.ErrorValue", terminal.Value)
	}
	if !errors.Is(value.Err, context.Canceled) {
		t.Fatalf("terminal error should preserve context.Canceled, got %v", value.Err)
	}
	if value.Classification != string(messages.TerminalReasonCancellation) {
		t.Fatalf("classification = %q, want %q", value.Classification, messages.TerminalReasonCancellation)
	}
	if value.TerminalReason != messages.TerminalReasonCancellation {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonCancellation)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceLoop {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceLoop)
	}
	if value.OutputState != messages.TerminalOutputNone {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputNone)
	}
	if value.TerminalReason == messages.TerminalReasonProviderClose || value.TerminalReason == messages.TerminalReasonTerminalFailure {
		t.Fatalf("cancellation terminal reason should not collapse into %q", value.TerminalReason)
	}
}

func TestModelRunner_CancellationTakesPrecedenceWhenProviderClosesStream(t *testing.T) {
	inf := &cancelThenClosedStreamInferencer{ready: make(chan struct{})}

	runner := NewModelRunner(inf, 10)
	ap := NewActiveParticipant(messages.Model, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.InferenceRequest{
		Messages: []messages.Message{messages.NewTextMessage(messages.RoleUser, "hi")},
	})
	<-inf.ready
	runner.CancelCurrentExecution()

	terminal := drainTerminalDelta(t, ctx, runner)
	value, ok := terminal.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("terminal value = %T, want *messages.ErrorValue", terminal.Value)
	}
	if !errors.Is(value.Err, context.Canceled) {
		t.Fatalf("terminal error should preserve context.Canceled, got %v", value.Err)
	}
	if value.TerminalReason != messages.TerminalReasonCancellation {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonCancellation)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceLoop {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceLoop)
	}
	if value.OutputState != messages.TerminalOutputNone {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputNone)
	}
}

func TestModelRunner_PreStreamCancellationGetsDistinctTerminalError(t *testing.T) {
	inf := &streamErrorInferencer{err: context.Canceled}

	runner := NewModelRunner(inf, 10)
	ap := NewActiveParticipant(messages.Model, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.InferenceRequest{
		Messages: []messages.Message{messages.NewTextMessage(messages.RoleUser, "hi")},
	})

	terminal := drainTerminalDelta(t, ctx, runner)
	value, ok := terminal.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("terminal value = %T, want *messages.ErrorValue", terminal.Value)
	}
	if !errors.Is(value.Err, context.Canceled) {
		t.Fatalf("terminal error should preserve context.Canceled, got %v", value.Err)
	}
	if value.TerminalReason != messages.TerminalReasonCancellation {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonCancellation)
	}
	if value.OutputState != messages.TerminalOutputNone {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputNone)
	}
}

func TestModelRunner_CancellationAfterPartialOutputReportsPartialState(t *testing.T) {
	stream := make(chan messages.StreamMessage, 2)
	stream <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}
	stream <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("partial")}
	inf := &controlledStreamInferencer{stream: stream, ready: make(chan struct{})}

	runner := NewModelRunner(inf, 10)
	ap := NewActiveParticipant(messages.Model, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap.Start(ctx)
	defer ap.Stop()

	runner.Inbox.Write(ctx, messages.InferenceRequest{
		Messages: []messages.Message{messages.NewTextMessage(messages.RoleUser, "hi")},
	})
	<-inf.ready

	for {
		delta, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
		if !ok {
			t.Fatal("context cancelled waiting for partial delta")
		}
		if delta.Type == messages.StreamTypeTextDelta {
			break
		}
	}
	runner.CancelCurrentExecution()

	terminal := drainTerminalDelta(t, ctx, runner)
	value, ok := terminal.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("terminal value = %T, want *messages.ErrorValue", terminal.Value)
	}
	if !errors.Is(value.Err, context.Canceled) {
		t.Fatalf("terminal error should preserve context.Canceled, got %v", value.Err)
	}
	if value.TerminalReason != messages.TerminalReasonCancellation {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonCancellation)
	}
	if value.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputPartial)
	}
	if value.Classification != string(messages.TerminalReasonCancellation) {
		t.Fatalf("classification = %q, want %q", value.Classification, messages.TerminalReasonCancellation)
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

func TestModelRunner_StreamCloseWithoutMessageEndMarksProviderClose(t *testing.T) {
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
	if messages.MessageEndTerminalSource(value) != messages.TerminalSourceProvider {
		t.Fatalf("terminal source = %q, want %q", messages.MessageEndTerminalSource(value), messages.TerminalSourceProvider)
	}
	if value.TerminalReason != messages.TerminalReasonProviderClose {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonProviderClose)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceProvider {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceProvider)
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
	value, ok := delta.Value.(*messages.SessionCloseValue)
	if !ok {
		t.Fatalf("delta value = %T, want *messages.SessionCloseValue", delta.Value)
	}
	if value.TerminalReason != messages.TerminalReasonProviderClose {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonProviderClose)
	}
	if value.Classification != string(messages.TerminalReasonProviderClose) {
		t.Fatalf("classification = %q, want %q", value.Classification, messages.TerminalReasonProviderClose)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceSession {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceSession)
	}
	if value.OutputState != messages.TerminalOutputNotApplicable {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputNotApplicable)
	}
}

func TestSessionModelRunner_NormalizesProviderSessionCloseMetadata(t *testing.T) {
	session := newCompletedSession()
	session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValue("session-1", "closed"),
	})
	close(session.done)

	runner := NewSessionModelRunner(&completedSessionInferencer{session: session}, 16, nil)
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	delta, ok := runner.DeltaOutbox.Read()
	if !ok {
		t.Fatal("expected session close delta")
	}
	value, ok := delta.Value.(*messages.SessionCloseValue)
	if !ok {
		t.Fatalf("delta value = %T, want *messages.SessionCloseValue", delta.Value)
	}
	if value.TerminalReason != messages.TerminalReasonSessionClose {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonSessionClose)
	}
	if value.Classification != string(messages.TerminalReasonSessionClose) {
		t.Fatalf("classification = %q, want %q", value.Classification, messages.TerminalReasonSessionClose)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceSession {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceSession)
	}
	if value.OutputState != messages.TerminalOutputNotApplicable {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputNotApplicable)
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
