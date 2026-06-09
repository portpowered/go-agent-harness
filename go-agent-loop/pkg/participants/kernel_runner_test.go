package participants

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// --- Delta dispatch to deltaEventCh ---

func TestKernelRunner_DispatchDeltaToDeltaEventCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	delta := messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("hello"),
	}
	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta:  delta,
	})

	select {
	case got := <-evCh:
		if got.Type != messages.StreamTypeTextDelta {
			t.Errorf("Type: got %s, want %s", got.Type, messages.StreamTypeTextDelta)
		}
		v, ok := got.Value.(*messages.TextDeltaValue)
		if !ok {
			t.Fatalf("Value: got %T, want *messages.TextDeltaValue", got.Value)
		}
		if v.Content != "hello" {
			t.Errorf("Content: got %q, want %q", v.Content, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delta on deltaEventCh")
	}
}

func TestKernelRunner_SystemFullMessageNotSentToDeltaEventCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeSystemFullMessage,
			Value: messages.NewInferenceResultValue("model", messages.NewTextMessage(messages.RoleAssistant, "hi")),
		},
	})

	select {
	case got := <-evCh:
		t.Errorf("SYSTEM.FULL_MESSAGE should not be sent to deltaEventCh, got %v", got)
	case <-time.After(50 * time.Millisecond):
		// expected: nothing on deltaEventCh
	}
}

func TestKernelRunner_NoDeltaEventChNoPanic(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	// Do NOT call NewDeltaEventReader — deltaEventCh is nil.
	// Dispatching should not panic.
	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue("no listener"),
		},
	})
}

func TestKernelRunner_MultipleDeltaTypesDispatched(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	deltas := []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("data")},
		{Type: messages.StreamTypeTextEnd, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	}

	for _, d := range deltas {
		kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
			Source: messages.Model,
			Delta:  d,
		})
	}

	for i, want := range deltas {
		select {
		case got := <-evCh:
			if got.Type != want.Type {
				t.Errorf("delta[%d].Type: got %s, want %s", i, got.Type, want.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for delta[%d]", i)
		}
	}
}

// --- Message dispatch to messageOutCh ---

func TestKernelRunner_DispatchFullMessageToMessageOutCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch

	msg := messages.NewTextMessage(messages.RoleAssistant, "response")
	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeSystemFullMessage,
			Value: messages.NewInferenceResultValue("model", msg),
		},
	})

	select {
	case got := <-ch:
		if got.Source != messages.Model {
			t.Errorf("Source: got %s, want %s", got.Source, messages.Model)
		}
		if got.Message.TextContent() != "response" {
			t.Errorf("Message text: got %q, want %q", got.Message.TextContent(), "response")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message on messageOutCh")
	}
}

func TestKernelRunner_NoMessageOutChNoPanic(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	// messageOutCh is nil — should not panic.
	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Tool,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeSystemFullMessage,
			Value: messages.NewInferenceResultValue("tool", messages.NewTextMessage(messages.RoleTool, "result")),
		},
	})
}

func TestKernelRunner_LoopEndClosesMessageOutCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch

	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.System,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeLoopEnd,
			Value: messages.NewLoopEndValue(),
		},
	})

	// Channel should be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("messageOutCh should be closed after LOOP.END, but received a value")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: messageOutCh not closed after LOOP.END")
	}
}

func TestKernelRunner_LoopEndClosesDeltaEventCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.System,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeLoopEnd,
			Value: messages.NewLoopEndValue(),
		},
	})

	// LOOP.END is forwarded to deltaEventCh before the channel is closed
	// (it passes the StreamTypeSystemFullMessage filter).
	select {
	case got := <-evCh:
		if got.Type != messages.StreamTypeLoopEnd {
			t.Errorf("expected LOOP.END delta, got %s", got.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for LOOP.END delta")
	}

	// After the LOOP.END delta, the channel should be closed.
	select {
	case _, ok := <-evCh:
		if ok {
			t.Error("deltaEventCh should be closed after LOOP.END, but received a value")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: deltaEventCh not closed after LOOP.END")
	}
}

func TestKernelRunner_LoopEndNilsChannels(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch
	kr.NewDeltaEventReader(8)

	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.System,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeLoopEnd,
			Value: messages.NewLoopEndValue(),
		},
	})

	// Internal channels should be nil after LOOP.END.
	if kr.messageOutCh != nil {
		t.Error("messageOutCh should be nil after LOOP.END")
	}
	if kr.deltaEventCh != nil {
		t.Error("deltaEventCh should be nil after LOOP.END")
	}
}

// --- closeStreamWithError behavior ---

func TestKernelRunner_CloseStreamWithErrorClosesMessageOutCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch

	kr.closeStreamWithError(errors.New("test error"))

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("messageOutCh should be closed after closeStreamWithError")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: messageOutCh not closed")
	}
}

func TestKernelRunner_CloseStreamWithErrorClosesDeltaEventCh(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	kr.closeStreamWithError(errors.New("test error"))

	select {
	case _, ok := <-evCh:
		if ok {
			t.Error("deltaEventCh should be closed after closeStreamWithError")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out: deltaEventCh not closed")
	}
}

func TestKernelRunner_CloseStreamWithErrorDrainsDeltaInbox(t *testing.T) {
	kr := NewKernelRunner(nil, 8)

	// Write some deltas to DeltaInbox before closing.
	ctx := context.Background()
	kr.DeltaInbox.Write(ctx, messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta:  messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("stale1")},
	})
	kr.DeltaInbox.Write(ctx, messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta:  messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("stale2")},
	})

	kr.closeStreamWithError(errors.New("retry"))

	// DeltaInbox should be empty after drain.
	if _, ok := kr.DeltaInbox.Read(); ok {
		t.Error("DeltaInbox should be drained after closeStreamWithError")
	}
}

func TestKernelRunner_CloseStreamWithErrorNilChannelsSafe(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	// Both messageOutCh and deltaEventCh are nil — should not panic.
	kr.closeStreamWithError(errors.New("no channels"))
}

func TestKernelRunner_CloseStreamWithErrorNilsChannels(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch
	kr.NewDeltaEventReader(8)

	kr.closeStreamWithError(errors.New("err"))

	if kr.messageOutCh != nil {
		t.Error("messageOutCh should be nil after closeStreamWithError")
	}
	if kr.deltaEventCh != nil {
		t.Error("deltaEventCh should be nil after closeStreamWithError")
	}
}

// --- NewDeltaEventReader channel creation and replacement ---

func TestKernelRunner_NewDeltaEventReaderCreatesChannel(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(4)
	if evCh == nil {
		t.Fatal("NewDeltaEventReader should return a non-nil channel")
	}
	if cap(evCh) != 4 {
		t.Errorf("channel capacity: got %d, want 4", cap(evCh))
	}
}

func TestKernelRunner_NewDeltaEventReaderReplacesChannel(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	first := kr.NewDeltaEventReader(4)
	second := kr.NewDeltaEventReader(8)

	// The internal deltaEventCh should now be the second channel.
	// Dispatch a delta and verify it arrives on the second channel.
	kr.dispatchDelta(context.Background(), messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue("replaced"),
		},
	})

	select {
	case got := <-second:
		if got.Type != messages.StreamTypeTextDelta {
			t.Errorf("Type: got %s, want %s", got.Type, messages.StreamTypeTextDelta)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting on second channel")
	}

	// First channel should have nothing.
	select {
	case <-first:
		t.Error("first channel should not receive after replacement")
	default:
		// expected
	}
}

func TestKernelRunner_NewDeltaEventReaderCapacity(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch16 := kr.NewDeltaEventReader(16)
	if cap(ch16) != 16 {
		t.Errorf("capacity: got %d, want 16", cap(ch16))
	}
}

// --- Tick with context cancellation ---

func TestKernelRunner_TickCancelledContext(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	ch := make(chan messages.KernelMessageRequest, 8)
	kr.messageOutCh = ch
	kr.NewDeltaEventReader(8)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := kr.Tick(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}

	// Channels should be closed by closeStreamWithError.
	if kr.messageOutCh != nil {
		t.Error("messageOutCh should be nil after context cancellation")
	}
	if kr.deltaEventCh != nil {
		t.Error("deltaEventCh should be nil after context cancellation")
	}
}

func TestKernelRunner_TickProcessesDelta(t *testing.T) {
	kr := NewKernelRunner(nil, 8)
	evCh := kr.NewDeltaEventReader(8)

	ctx := context.Background()
	kr.DeltaInbox.Write(ctx, messages.KernelDeltaRequest{
		Source: messages.Model,
		Delta: messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue("tick data"),
		},
	})

	// Use a timeout context to prevent hanging.
	tickCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := kr.Tick(tickCtx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case got := <-evCh:
		if got.Type != messages.StreamTypeTextDelta {
			t.Errorf("Type: got %s, want %s", got.Type, messages.StreamTypeTextDelta)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for delta from Tick")
	}
}
