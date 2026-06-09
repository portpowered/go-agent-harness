package functional

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems"
)

// retryInferencer implements messages.Inferencer. On the first InferStream it
// sends a few TEXT.DELTA events then an ERROR, so the engine sees
// InferenceResponse.Error. On the second InferStream it succeeds with full text.
type retryInferencer struct {
	callCount int
}

func (r *retryInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	r.callCount++
	if r.callCount == 1 {
		return messages.InferenceResult{}, nil
	}
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, "Final answer."),
	}, nil
}

func (r *retryInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, 16)
	go func() {
		defer close(ch)
		r.callCount++
		if r.callCount == 1 {
			// First call: emit partial text then error.
			ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, ActorProvidedIndex: 0, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: 0, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("par")}
			ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: 0, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("tial")}
			ch <- messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue("first attempt failed")}
			return
		}
		// Second call: full success.
		ch <- messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, ActorProvidedIndex: 0, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: 0, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("Final answer.")}
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, ActorProvidedIndex: 0, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()}
		ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ActorProvidedIndex: 0, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
	}()
	return ch, nil
}

// assistantTextFromDeltas concatenates all TEXT.DELTA content for assistant role.
func assistantTextFromDeltas(deltas []messages.StreamMessage) string {
	var b []byte
	for _, d := range deltas {
		if d.Role != messages.RoleAssistant {
			continue
		}
		if v, ok := d.Value.(*messages.TextDeltaValue); ok {
			b = append(b, v.Content...)
		}
	}
	return string(b)
}

// newRetryTestEngine creates an engine with a retryInferencer and returns the
// engine plus kernel runner. The engine is seeded with a single user message.
func newRetryTestEngine() (*engine.Engine, *participants.KernelRunner) {
	bufCap := 64
	inf := &retryInferencer{}
	modelRunner := participants.NewModelRunner(inf, bufCap)
	toolRunner := participants.NewToolRunner(&messages.DefaultToolExecutor{}, bufCap)
	userRunner := participants.NewUserRunner(bufCap)
	kernelRunner := participants.NewKernelRunner(nil, bufCap)

	coord := subsystems.NewCoordinator(nil)
	coordDelta := subsystems.NewCoordinatorDelta(*kernelRunner.DeltaInbox, nil)
	hlps := []subsystems.Subsystem{coord, coordDelta}

	eng := engine.NewEngine(engine.ModeAskOnce, nil, hlps, modelRunner, toolRunner, userRunner, kernelRunner, nil)
	eng.AddMessages([]messages.Message{messages.NewTextMessage(messages.RoleUser, "hello")})

	return eng, kernelRunner
}

// waitForLoopEnd reads delta events until LOOP.END or timeout, returning all
// deltas collected. Fails the test on timeout.
func waitForLoopEnd(t *testing.T, eventCh <-chan messages.StreamMessage, timeout time.Duration) []messages.StreamMessage {
	t.Helper()
	timer := time.After(timeout)
	var deltas []messages.StreamMessage
	for {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				return deltas
			}
			deltas = append(deltas, ev)
			if _, ok := ev.Value.(*messages.LoopEndValue); ok {
				return deltas
			}
		case <-timer:
			t.Fatal("timeout waiting for LOOP.END")
			return nil
		}
	}
}

// TestRetryDelta_TrimOnErrorThenRetry verifies that when inference fails after
// emitting some TEXT.DELTA, the engine trims ConversationDeltaBuffer back to
// ModelDeltaStartIndex and sets CurrentModelDeltaCount = 0, so a retry
// (second RunHotLoop without re-adding the user message) overwrites from that
// index and the final deltas contain the successful response text exactly once.
func TestRetryDelta_TrimOnErrorThenRetry(t *testing.T) {
	const successText = "Final answer."

	eng, kernelRunner := newRetryTestEngine()

	// First RunHotLoop: model sends partial deltas then error; engine should
	// trim ConversationDeltaBuffer and return the error.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	err1 := eng.RunHotLoop(ctx1)
	cancel1()
	if err1 == nil {
		t.Fatal("first RunHotLoop expected to fail with error, got nil")
	}

	// Verify delta buffer was trimmed: no assistant deltas should remain.
	for _, d := range eng.State().LoopState.History.ConversationDeltaBuffer {
		if d.Role == messages.RoleAssistant {
			t.Fatalf("stale assistant delta found in ConversationDeltaBuffer after error rollback: type=%v", d.Type)
		}
	}

	// Create event reader for second run so we can detect LOOP.END.
	eventCh := kernelRunner.NewDeltaEventReader(64)

	// Retry: run hot loop again (retryInferencer succeeds on second call).
	ctx2, cancel2 := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.RunHotLoop(ctx2) }()

	waitForLoopEnd(t, eventCh, 5*time.Second)
	cancel2()
	err2 := <-done
	if err2 != nil && err2 != context.Canceled {
		t.Fatalf("second RunHotLoop (retry): %v", err2)
	}

	// Assert assistant TEXT.DELTA content appears exactly once (the success response).
	deltas := eng.State().LoopState.History.ConversationDeltaBuffer
	assistantText := assistantTextFromDeltas(deltas)
	if assistantText != successText {
		t.Errorf("assistant text from deltas: got %q, want %q (duplicate or wrong content)", assistantText, successText)
	}
}

// TestRetryDelta_StreamNoDuplicateMessages verifies that when inference fails
// after emitting some TEXT.DELTA and the hot loop is run again (retry), the
// delta events delivered to the kernel's event reader for the retry run do not
// contain any stale content from the failed first run.
func TestRetryDelta_StreamNoDuplicateMessages(t *testing.T) {
	const successText = "Final answer."

	eng, kernelRunner := newRetryTestEngine()

	// First RunHotLoop: fails after partial deltas.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	err1 := eng.RunHotLoop(ctx1)
	cancel1()
	if err1 == nil {
		t.Fatal("first RunHotLoop expected to fail with error, got nil")
	}

	// Create a fresh event reader for the retry run.
	eventCh := kernelRunner.NewDeltaEventReader(64)

	// Retry: second RunHotLoop succeeds.
	ctx2, cancel2 := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.RunHotLoop(ctx2) }()

	retryDeltas := waitForLoopEnd(t, eventCh, 5*time.Second)
	cancel2()
	<-done

	// Verify no partial deltas from first run leaked into second run's stream.
	text := assistantTextFromDeltas(retryDeltas)
	if text != successText {
		t.Errorf("retry stream text: got %q, want %q (stale content leaked)", text, successText)
	}

	for _, d := range retryDeltas {
		if v, ok := d.Value.(*messages.TextDeltaValue); ok && d.Role == messages.RoleAssistant {
			if v.Content == "par" || v.Content == "tial" {
				t.Errorf("stale delta content %q from failed run found in retry stream", v.Content)
			}
		}
	}
}

// TestRetryDelta_PreservesConversationContext verifies that after a failed
// inference and successful retry, the original user message is preserved in
// ConversationBuffer and the final conversation contains both the user message
// and the assistant's successful response.
func TestRetryDelta_PreservesConversationContext(t *testing.T) {
	const userMessage = "hello"
	const successText = "Final answer."

	eng, kernelRunner := newRetryTestEngine()

	// First RunHotLoop: fails.
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	err1 := eng.RunHotLoop(ctx1)
	cancel1()
	if err1 == nil {
		t.Fatal("first RunHotLoop expected to fail")
	}

	// Verify user message is preserved after error.
	convBuf := eng.State().LoopState.History.ConversationBuffer
	if len(convBuf) == 0 {
		t.Fatal("ConversationBuffer is empty after error, expected user message")
	}
	if convBuf[0].Role != messages.RoleUser || convBuf[0].TextContent() != userMessage {
		t.Errorf("message 0 after error: got role=%v text=%q, want role=user text=%q",
			convBuf[0].Role, convBuf[0].TextContent(), userMessage)
	}

	// Retry: second RunHotLoop succeeds.
	eventCh := kernelRunner.NewDeltaEventReader(64)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.RunHotLoop(ctx2) }()

	waitForLoopEnd(t, eventCh, 5*time.Second)
	cancel2()
	<-done

	// Verify conversation contains user message + assistant response.
	convBuf = eng.State().LoopState.History.ConversationBuffer
	if len(convBuf) < 2 {
		t.Fatalf("ConversationBuffer has %d messages, expected at least 2", len(convBuf))
	}
	if convBuf[0].Role != messages.RoleUser || convBuf[0].TextContent() != userMessage {
		t.Errorf("message 0: got role=%v text=%q, want role=user text=%q",
			convBuf[0].Role, convBuf[0].TextContent(), userMessage)
	}
	if convBuf[1].Role != messages.RoleAssistant || convBuf[1].TextContent() != successText {
		t.Errorf("message 1: got role=%v text=%q, want role=assistant text=%q",
			convBuf[1].Role, convBuf[1].TextContent(), successText)
	}
}
