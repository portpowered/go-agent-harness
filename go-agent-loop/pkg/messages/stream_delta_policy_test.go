package messages

import (
	"context"
	"testing"
	"testing/synctest"
)

func TestMustDeliverClassifiesTurnCorruptingDeltas(t *testing.T) {
	must := []StreamMessage{
		{Type: StreamTypeToolCallStart},
		{Type: StreamTypeToolCallDelta},
		{Type: StreamTypeToolCallEnd},
		{Type: StreamTypeMessageStart},
		{Type: StreamTypeMessageEnd},
		{Type: StreamTypeError},
		{Type: StreamTypeSessionClose},
		{Type: StreamTypeLoopEnd},
		{Type: StreamTypeSystemFullMessage},
		{Type: StreamTypeTextDelta, Role: RoleTool}, // any tool-result delta
	}
	for _, msg := range must {
		if !MustDeliver(msg) {
			t.Errorf("MustDeliver(%s, role %q) = false, want true", msg.Type, msg.Role)
		}
	}
	droppable := []StreamMessage{
		{Type: StreamTypeTextDelta, Role: RoleAssistant},
		{Type: StreamTypeAudioDelta},
		{Type: StreamTypeAudioStart},
		{Type: StreamTypeSessionOpen},
	}
	for _, msg := range droppable {
		if MustDeliver(msg) {
			t.Errorf("MustDeliver(%s, role %q) = true, want false", msg.Type, msg.Role)
		}
	}
}

// An ordinary delta meeting a full buffer is dropped and counted without
// blocking; a must-deliver delta waits for capacity instead.
func TestWriteStreamDeltaDropsOrdinaryButWaitsForMustDeliver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		buf := NewTypedBuffer[StreamMessage](1)
		if outcome := WriteStreamDelta(ctx, buf, StreamMessage{Type: StreamTypeTextDelta}); !outcome.OK() {
			t.Fatalf("first write = %+v, want success", outcome)
		}

		if outcome := WriteStreamDelta(ctx, buf, StreamMessage{Type: StreamTypeAudioDelta}); outcome.Status != BufferWriteBufferFull {
			t.Fatalf("ordinary write to a full buffer = %+v, want buffer_full", outcome)
		}
		if buf.Drops() != 1 {
			t.Fatalf("drops = %d, want the ordinary delta counted", buf.Drops())
		}

		done := make(chan BufferWriteOutcome, 1)
		go func() { done <- WriteStreamDelta(ctx, buf, StreamMessage{Type: StreamTypeMessageEnd}) }()
		synctest.Wait()
		select {
		case outcome := <-done:
			t.Fatalf("must-deliver write returned %+v while the buffer was full, want it to wait", outcome)
		default:
		}
		if _, ok := buf.Read(); !ok {
			t.Fatal("read the queued delta: buffer empty")
		}
		if outcome := <-done; !outcome.OK() {
			t.Fatalf("must-deliver write after capacity freed = %+v, want success", outcome)
		}
		if got, _ := buf.Read(); got.Type != StreamTypeMessageEnd || buf.Drops() != 1 {
			t.Fatalf("delivered %s with %d drops, want MESSAGE.END and no further drop", got.Type, buf.Drops())
		}
	})
}

// A must-deliver write waiting on a full buffer ends with its context, and
// WriteKernelDelta applies the same policy to kernel requests.
func TestWriteKernelDeltaMustDeliverWaitEndsWithContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		buf := NewTypedBuffer[KernelDeltaRequest](1)
		ctx, cancel := context.WithCancel(context.Background())
		buf.TryWrite(KernelDeltaRequest{})
		if outcome := WriteKernelDelta(ctx, buf, KernelDeltaRequest{Delta: StreamMessage{Type: StreamTypeTextDelta}}); outcome.Status != BufferWriteBufferFull {
			t.Fatalf("ordinary kernel write to a full buffer = %+v, want buffer_full", outcome)
		}
		done := make(chan BufferWriteOutcome, 1)
		go func() {
			done <- WriteKernelDelta(ctx, buf, KernelDeltaRequest{Delta: StreamMessage{Type: StreamTypeToolCallEnd}})
		}()
		synctest.Wait()
		cancel()
		if outcome := <-done; outcome.Status != BufferWriteCancelled {
			t.Fatalf("must-deliver kernel write after cancel = %+v, want cancelled", outcome)
		}
	})
}

func TestTypedBufferShedCountsWithoutWriting(t *testing.T) {
	buf := NewTypedBuffer[int](2)
	var observed []int
	buf.SetOnDrop(func(v int) { observed = append(observed, v) })
	if outcome := buf.Shed(7); outcome.Status != BufferWriteBufferFull {
		t.Fatalf("Shed = %+v, want buffer_full", outcome)
	}
	if buf.Len() != 0 || buf.Drops() != 1 || len(observed) != 1 || observed[0] != 7 {
		t.Fatalf("after Shed: len %d drops %d observed %v, want empty buffer, one drop observed", buf.Len(), buf.Drops(), observed)
	}
}
