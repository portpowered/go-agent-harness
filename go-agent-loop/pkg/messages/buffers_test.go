package messages

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTypedBuffer_WriteAndRead(t *testing.T) {
	buf := NewTypedBuffer[string](10)
	ok := buf.Write(context.Background(), "hello")
	if !ok {
		t.Fatal("Write should succeed")
	}

	data, ok := buf.Read()
	if !ok {
		t.Fatal("Read should succeed after Write")
	}
	if data != "hello" {
		t.Errorf("expected 'hello', got %q", data)
	}
}

func TestTypedBuffer_ReadEmpty(t *testing.T) {
	buf := NewTypedBuffer[string](10)
	_, ok := buf.Read()
	if ok {
		t.Error("Read from empty buffer should return false")
	}
}

func TestTypedBuffer_HasData(t *testing.T) {
	buf := NewTypedBuffer[int](10)
	if buf.HasData() {
		t.Error("empty buffer should not have data")
	}

	buf.Write(context.Background(), 42)
	if !buf.HasData() {
		t.Error("buffer should have data after write")
	}
}

func TestTypedBuffer_WriteFull(t *testing.T) {
	buf := NewTypedBuffer[string](1)
	buf.Write(context.Background(), "first")
	ok := buf.Write(context.Background(), "second")
	if ok {
		t.Error("Write to full buffer should return false")
	}
}

func TestTypedBuffer_WriteTerminalEvictsWhenFull(t *testing.T) {
	buf := NewTypedBuffer[string](1)
	var drops int
	buf.SetOnDrop(func(_ string) { drops++ })
	if !buf.Write(context.Background(), "ordinary") {
		t.Fatal("ordinary write should succeed")
	}

	if !buf.WriteTerminal("terminal") {
		t.Fatal("terminal write should succeed when buffer is full")
	}
	if drops != 1 {
		t.Fatalf("drop callback count = %d, want 1 evicted ordinary record", drops)
	}
	if got := buf.Drops(); got != 1 {
		t.Fatalf("buffer drop count = %d, want 1 evicted ordinary record", got)
	}
	if got := buf.Len(); got != 1 {
		t.Fatalf("buffer length = %d, want 1", got)
	}
	got, ok := buf.Read()
	if !ok || got != "terminal" {
		t.Fatalf("Read = (%q, %t), want terminal record", got, ok)
	}
}

func TestTypedBuffer_WriteContextOutcomes(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		buf := NewTypedBuffer[string](1)
		outcome := buf.WriteContext(context.Background(), "first")
		if outcome.Status != BufferWriteSucceeded || !outcome.OK() {
			t.Fatalf("outcome = %+v, want success", outcome)
		}
	})

	t.Run("buffer full", func(t *testing.T) {
		buf := NewTypedBuffer[string](1)
		if !buf.Write(context.Background(), "first") {
			t.Fatal("initial write failed")
		}
		outcome := buf.WriteContext(context.Background(), "second")
		if outcome.Status != BufferWriteBufferFull || outcome.OK() {
			t.Fatalf("outcome = %+v, want buffer full", outcome)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		buf := NewTypedBuffer[string](1)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		outcome := buf.WriteContext(ctx, "value")
		if outcome.Status != BufferWriteCancelled {
			t.Fatalf("status = %q, want %q", outcome.Status, BufferWriteCancelled)
		}
		if !errors.Is(outcome.Err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", outcome.Err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		buf := NewTypedBuffer[string](1)
		ctx, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()
		outcome := buf.WriteContext(ctx, "value")
		if outcome.Status != BufferWriteTimedOut {
			t.Fatalf("status = %q, want %q", outcome.Status, BufferWriteTimedOut)
		}
		if !errors.Is(outcome.Err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", outcome.Err)
		}
	})
}

func TestTypedBuffer_OnDropCalledWhenFull(t *testing.T) {
	buf := NewTypedBuffer[string](1)
	dropCount := 0
	buf.SetOnDrop(func(_ string) {
		dropCount++
	})

	buf.Write(context.Background(), "first")
	buf.Write(context.Background(), "second") // should trigger OnDrop
	buf.Write(context.Background(), "third")  // should trigger OnDrop again

	if dropCount != 2 {
		t.Errorf("expected OnDrop called 2 times, got %d", dropCount)
	}
}

func TestTypedBuffer_OnDropNotCalledOnSuccess(t *testing.T) {
	buf := NewTypedBuffer[string](10)
	called := false
	buf.SetOnDrop(func(_ string) {
		called = true
	})

	buf.Write(context.Background(), "hello")
	if called {
		t.Error("OnDrop should not fire on successful write")
	}
}

func TestTypedBuffer_OnDropNotCalledOnContextCancel(t *testing.T) {
	buf := NewTypedBuffer[string](10)
	called := false
	buf.SetOnDrop(func(_ string) {
		called = true
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	buf.Write(ctx, "hello")

	if called {
		t.Error("OnDrop should not fire on context cancellation")
	}
}

func TestTypedBuffer_OnDropNilSafe(t *testing.T) {
	buf := NewTypedBuffer[string](1)
	// No OnDrop set — should not panic
	buf.Write(context.Background(), "first")
	buf.Write(context.Background(), "second") // buffer full, no callback set
}

func TestTypedBuffer_DropCountsWithoutObserver(t *testing.T) {
	buf := NewTypedBuffer[string](1)
	// No OnDrop registered: the built-in counter is the only evidence.
	if !buf.Write(context.Background(), "first") {
		t.Fatal("initial write failed")
	}
	for i := range 3 {
		if buf.Write(context.Background(), "overflow") {
			t.Fatalf("write %d succeeded on a full buffer", i)
		}
	}
	if got := buf.Drops(); got != 3 {
		t.Fatalf("Drops() = %d, want 3", got)
	}
	// Draining the buffer must not reset or change the cumulative count.
	if _, ok := buf.Read(); !ok {
		t.Fatal("read from filled buffer failed")
	}
	if !buf.Write(context.Background(), "after drain") {
		t.Fatal("write after drain failed")
	}
	if got := buf.Drops(); got != 3 {
		t.Fatalf("Drops() after drain = %d, want still 3", got)
	}
}

func TestTypedBuffer_DropCountOnlyOnBufferFullPath(t *testing.T) {
	buf := NewTypedBuffer[string](1)
	if !buf.Write(context.Background(), "kept") {
		t.Fatal("successful write reported failure")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if outcome := buf.WriteContext(cancelled, "cancelled"); outcome.Status != BufferWriteCancelled {
		t.Fatalf("cancelled write returned %+v", outcome)
	}
	timedOut, timeout := context.WithTimeout(context.Background(), 0)
	defer timeout()
	if outcome := buf.WriteContext(timedOut, "timed out"); outcome.Status != BufferWriteTimedOut {
		t.Fatalf("timed-out write returned %+v", outcome)
	}
	if got := buf.Drops(); got != 0 {
		t.Fatalf("Drops() = %d after success/cancel/timeout writes, want 0", got)
	}
	if outcome := buf.WriteContext(context.Background(), "dropped"); outcome.Status != BufferWriteBufferFull {
		t.Fatalf("buffer-full write returned %+v", outcome)
	}
	if got := buf.Drops(); got != 1 {
		t.Fatalf("Drops() = %d after one buffer-full write, want 1", got)
	}
}

func TestTypedBuffer_ConcurrentWritersAccurateDropCount(t *testing.T) {
	const (
		writers    = 8
		writesEach = 200
	)
	buf := NewTypedBuffer[int](4)
	var delivered atomic.Int64
	var observed atomic.Int64
	buf.SetOnDrop(func(_ int) { observed.Add(1) })

	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range writesEach {
				if buf.Write(context.Background(), i) {
					delivered.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	want := int64(writers * writesEach)
	if got := buf.Drops(); got != observed.Load() {
		t.Fatalf("Drops() = %d disagrees with observer count %d", got, observed.Load())
	}
	if total := delivered.Load() + buf.Drops(); total != want {
		t.Fatalf("delivered %d + dropped %d = %d, want %d", delivered.Load(), buf.Drops(), total, want)
	}
}

func TestTypedBuffer_WriteCancelledContext(t *testing.T) {
	buf := NewTypedBuffer[string](10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ok := buf.Write(ctx, "hello")
	if ok {
		t.Error("Write with cancelled context should return false")
	}
}

func TestTypedBuffer_ReadBlocking(t *testing.T) {
	buf := NewTypedBuffer[string](10)
	done := make(chan struct{})

	go func() {
		time.Sleep(10 * time.Millisecond)
		buf.Write(context.Background(), "delayed")
	}()

	data, ok := buf.ReadBlocking(done)
	if !ok {
		t.Fatal("ReadBlocking should succeed")
	}
	if data != "delayed" {
		t.Errorf("expected 'delayed', got %q", data)
	}
}

func TestTypedBuffer_ReadBlockingCancelled(t *testing.T) {
	buf := NewTypedBuffer[string](10)
	done := make(chan struct{})
	close(done)

	_, ok := buf.ReadBlocking(done)
	if ok {
		t.Error("ReadBlocking should return false when done is closed")
	}
}

func TestTypedBuffer_ReadContextReturnsData(t *testing.T) {
	buf := NewTypedBuffer[string](10)
	if !buf.Write(context.Background(), "ready") {
		t.Fatal("Write should succeed")
	}

	data, err := buf.ReadContext(context.Background())
	if err != nil {
		t.Fatalf("ReadContext returned error: %v", err)
	}
	if data != "ready" {
		t.Errorf("expected 'ready', got %q", data)
	}
}

func TestTypedBuffer_ReadContextCancelledBeforeRead(t *testing.T) {
	buf := NewTypedBuffer[string](10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	data, err := buf.ReadContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if data != "" {
		t.Errorf("expected zero value on cancellation, got %q", data)
	}
}

func TestTypedBuffer_ReadContextCancelledWhileBlocked(t *testing.T) {
	buf := NewTypedBuffer[string](10)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)

	go func() {
		_, err := buf.ReadContext(ctx)
		result <- err
	}()
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReadContext did not return after context cancellation")
	}
}

func TestTypedBuffer_Chan(t *testing.T) {
	buf := NewTypedBuffer[int](10)
	buf.Write(context.Background(), 99)

	select {
	case v := <-buf.Chan():
		if v != 99 {
			t.Errorf("expected 99, got %d", v)
		}
	default:
		t.Error("Chan should have data available")
	}
}

func TestTypedBuffer_StructType(t *testing.T) {
	type request struct {
		ID   string
		Data int
	}
	buf := NewTypedBuffer[request](10)
	buf.Write(context.Background(), request{ID: "abc", Data: 42})

	data, ok := buf.Read()
	if !ok {
		t.Fatal("Read should succeed")
	}
	if data.ID != "abc" || data.Data != 42 {
		t.Errorf("unexpected data: %+v", data)
	}
}

type boolOnlySession struct {
	ok bool
}

func (s boolOnlySession) Send(context.Context, StreamMessage) bool {
	return s.ok
}

func (s boolOnlySession) Receive() *TypedBuffer[StreamMessage] {
	return NewTypedBuffer[StreamMessage](1)
}

func (s boolOnlySession) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (s boolOnlySession) Close() error {
	return nil
}

type outcomeSession struct {
	outcome SessionSendOutcome
}

func (s outcomeSession) Send(context.Context, StreamMessage) bool {
	return s.outcome.OK()
}

func (s outcomeSession) SendWithOutcome(context.Context, StreamMessage) SessionSendOutcome {
	return s.outcome
}

func (s outcomeSession) Receive() *TypedBuffer[StreamMessage] {
	return NewTypedBuffer[StreamMessage](1)
}

func (s outcomeSession) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (s outcomeSession) Close() error {
	return nil
}

func TestSendSessionWithOutcome(t *testing.T) {
	msg := StreamMessage{Type: StreamTypeTextDelta, Value: NewTextDeltaValue("hello")}

	success := SendSessionWithOutcome(context.Background(), boolOnlySession{ok: true}, msg)
	if success.Status != SessionSendSucceeded || !success.OK() {
		t.Fatalf("success = %+v, want succeeded", success)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := SendSessionWithOutcome(ctx, boolOnlySession{ok: false}, msg)
	if cancelled.Status != SessionSendCancelled {
		t.Fatalf("cancelled status = %q, want %q", cancelled.Status, SessionSendCancelled)
	}
	if !errors.Is(cancelled.Err, context.Canceled) {
		t.Fatalf("cancelled err = %v, want context.Canceled", cancelled.Err)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 0)
	defer cancel()
	timedOut := SendSessionWithOutcome(ctx, boolOnlySession{ok: false}, msg)
	if timedOut.Status != SessionSendTimedOut {
		t.Fatalf("timeout status = %q, want %q", timedOut.Status, SessionSendTimedOut)
	}
	if !errors.Is(timedOut.Err, context.DeadlineExceeded) {
		t.Fatalf("timeout err = %v, want context.DeadlineExceeded", timedOut.Err)
	}

	for _, status := range []SessionSendStatus{SessionSendBufferFull, SessionSendClosed, SessionSendTerminalFailure} {
		outcome := SendSessionWithOutcome(context.Background(), outcomeSession{
			outcome: SessionSendOutcome{Status: status},
		}, msg)
		if outcome.Status != status || outcome.OK() {
			t.Fatalf("outcome status = %q, want %q", outcome.Status, status)
		}
	}
}
