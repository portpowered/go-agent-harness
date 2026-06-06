package agentloop

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// interruptibleInferencer is a test double whose first InferStream call emits
// a few partial TEXT.DELTA chunks then blocks until its per-execution context is
// cancelled, simulating a slow in-flight model stream. Subsequent calls emit a
// complete assistant text response.
type interruptibleInferencer struct {
	mu               sync.Mutex
	callCount        int
	firstCallStarted chan struct{} // closed after partial deltas are queued
	resumeResponse   string
}

func newInterruptibleInferencer(resumeResponse string) *interruptibleInferencer {
	return &interruptibleInferencer{
		firstCallStarted: make(chan struct{}),
		resumeResponse:   resumeResponse,
	}
}

func (i *interruptibleInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	i.mu.Lock()
	call := i.callCount
	i.callCount++
	i.mu.Unlock()
	if call == 0 {
		// Block first call until context is cancelled.
		<-ctx.Done()
		return messages.InferenceResult{}, ctx.Err()
	}
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, i.resumeResponse),
	}, nil
}

func (i *interruptibleInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	i.mu.Lock()
	call := i.callCount
	i.callCount++
	i.mu.Unlock()

	ch := make(chan messages.StreamMessage, 16)

	if call == 0 {
		// First call: emit partial deltas, signal the test, then block.
		go func() {
			defer close(ch)
			ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("partial")}
			// Notify the test that partial deltas are in the pipeline.
			close(i.firstCallStarted)
			// Block until the per-execution context is cancelled by the interrupt.
			<-ctx.Done()
		}()
	} else {
		// Subsequent calls: emit a complete response so the loop can terminate.
		go func() {
			defer close(ch)
			ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(i.resumeResponse)}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
		}()
	}
	return ch, nil
}

// TestSendInterrupt_MidModelStream verifies the full interrupt flow when the model
// is mid-stream:
//
//  1. The engine starts inference (pass 1) and emits partial deltas.
//  2. SendInterrupt is called with a follow-up message.
//  3. The InterruptHandler saves the partial response to ConversationBuffer.
//  4. A new inference (pass 2) is dispatched and completes normally.
//  5. The conversation history contains the partial, the follow-up, and the resumed response.
func TestSendInterrupt_MidModelStream(t *testing.T) {
	const resumeResponse = "resumed answer"

	inf := newInterruptibleInferencer(resumeResponse)

	loop, err := New(WithInferencer(inf))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := loop.ExecuteStreaming(ctx, NewExecuteInput("hello"))
	if err != nil {
		t.Fatalf("ExecuteStreaming: %v", err)
	}

	// Wait until the first inference has emitted its partial deltas to the pipeline.
	select {
	case <-inf.firstCallStarted:
	case <-ctx.Done():
		t.Fatal("timed out waiting for first inference to start")
	}

	// Give the engine a few ticks to consume the partial deltas into history.
	time.Sleep(50 * time.Millisecond)

	// Interrupt with a follow-up instruction.
	followUp := messages.NewTextMessage(messages.RoleUser, "new direction")
	if err := loop.SendInterrupt(ctx, &followUp); err != nil {
		t.Fatalf("SendInterrupt: %v", err)
	}

	// Drain the event stream; it should close after the resumed inference completes.
	var textDeltas []string
	for result.EventStream.HasNext() {
		evt := result.EventStream.Response()
		if evt.Type == messages.StreamTypeTextDelta {
			if v, ok := evt.Value.(*messages.TextDeltaValue); ok {
				textDeltas = append(textDeltas, v.Content)
			}
		}
	}

	// The second inference's response must appear in the event stream.
	var gotResumed bool
	for _, chunk := range textDeltas {
		if chunk == resumeResponse {
			gotResumed = true
			break
		}
	}
	if !gotResumed {
		t.Errorf("expected %q in event stream; got deltas: %v", resumeResponse, textDeltas)
	}

	// The inferencer must have been called twice: once for pass 1, once for pass 2.
	inf.mu.Lock()
	callCount := inf.callCount
	inf.mu.Unlock()
	if callCount < 2 {
		t.Errorf("expected at least 2 inferencer calls, got %d", callCount)
	}

	// Conversation history should contain:
	// - The user's initial "hello" message.
	// - Optionally a partial assistant message (if deltas were committed in time).
	// - The follow-up user message ("new direction").
	// - The resumed assistant response.
	history := loop.GetConversationHistory()
	hasFollowUp := false
	hasResumedResponse := false
	for _, msg := range history {
		if msg.Role == messages.RoleUser && msg.TextContent() == "new direction" {
			hasFollowUp = true
		}
		if msg.Role == messages.RoleAssistant && msg.TextContent() == resumeResponse {
			hasResumedResponse = true
		}
	}
	if !hasFollowUp {
		t.Error("expected 'new direction' follow-up message in conversation history")
	}
	if !hasResumedResponse {
		t.Errorf("expected %q in conversation history", resumeResponse)
	}
}

// TestSendInterrupt_StalesDeltasDropped verifies that deltas emitted by the model
// runner after an interrupt (carrying the old LoopPassID) are silently dropped and
// do not pollute the conversation history with duplicate content.
func TestSendInterrupt_StalesDeltasDropped(t *testing.T) {
	// This inferencer mimics a model that keeps emitting deltas after the interrupt
	// by not honouring context cancellation until after emitting extra content.
	const extraContent = "should be dropped"
	const resumeContent = "clean resumed response"

	inf := &stubbornInferencer{
		firstCallStarted: make(chan struct{}),
		extraContent:     extraContent,
		resumeContent:    resumeContent,
	}

	loop, err := New(WithInferencer(inf))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := loop.ExecuteStreaming(ctx, NewExecuteInput("question"))
	if err != nil {
		t.Fatalf("ExecuteStreaming: %v", err)
	}

	select {
	case <-inf.firstCallStarted:
	case <-ctx.Done():
		t.Fatal("timed out waiting for first inference")
	}
	time.Sleep(50 * time.Millisecond)

	if err := loop.SendInterrupt(ctx, nil); err != nil {
		t.Fatalf("SendInterrupt: %v", err)
	}

	// Drain event stream.
	for result.EventStream.HasNext() {
		result.EventStream.Response()
	}

	// The "extra content" emitted with the old pass ID must not appear in history.
	history := loop.GetConversationHistory()
	for _, msg := range history {
		if msg.Role == messages.RoleAssistant && msg.TextContent() == extraContent {
			t.Errorf("stale content %q appeared in conversation history after interrupt", extraContent)
		}
	}

	// The resumed response must appear.
	hasResume := false
	for _, msg := range history {
		if msg.Role == messages.RoleAssistant && msg.TextContent() == resumeContent {
			hasResume = true
		}
	}
	if !hasResume {
		t.Errorf("expected resumed response %q in history", resumeContent)
	}
}

// stubbornInferencer emits a few partial deltas (with the correct LoopPassID set by
// the runner), signals the test, then — after a brief pause — emits more deltas
// that race with the interrupt. The resumed call returns a clean final response.
type stubbornInferencer struct {
	mu               sync.Mutex
	callCount        int
	firstCallStarted chan struct{}
	extraContent     string
	resumeContent    string
}

func (s *stubbornInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	s.mu.Lock()
	s.callCount++
	s.mu.Unlock()
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, s.resumeContent),
	}, nil
}

func (s *stubbornInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	s.mu.Lock()
	call := s.callCount
	s.callCount++
	s.mu.Unlock()

	ch := make(chan messages.StreamMessage, 32)

	if call == 0 {
		go func() {
			defer close(ch)
			ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("initial")}
			close(s.firstCallStarted)
			// Continue emitting after the interrupt signal (racing with cancellation).
			// The runner will tag these with the OLD LoopPassID; they should be dropped.
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(s.extraContent)}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
		}()
	} else {
		go func() {
			defer close(ch)
			ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(s.resumeContent)}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
		}()
	}
	return ch, nil
}
