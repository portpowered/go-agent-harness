package messages

import (
	"context"
	"errors"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	maxFuzzInputBytes           = 257
	concurrentBufferIterations  = 100
	concurrentProducerCount     = 4
	concurrentConsumerCount     = 3
	concurrentValuesPerProducer = 24
	concurrentBufferCapacity    = 7
	cancellationRaceIterations  = 100
)

type fuzzBufferValue struct {
	ID      int
	Payload byte
}

// FuzzTypedBufferConservationAndOrdering uses a compact operation stream so
// the native seed corpus is replayed by an ordinary go test as well as by
// longer fuzzing runs. The expected values are built from successful outcomes
// of the real buffer; no second queue implementation is used as the oracle.
func FuzzTypedBufferConservationAndOrdering(f *testing.F) {
	for _, seed := range [][]byte{
		{},
		{0, 0, 1},
		{1, 0, 7, 0, 7, 1},
		{0, 0, 7, 7, 7},
		{2, 2, 3, 5, 6},
		{3, 0, 7, 1, 2, 0, 4, 7, 1, 0},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > maxFuzzInputBytes {
			input = input[:maxFuzzInputBytes]
		}

		capacity := 1
		if len(input) > 0 {
			capacity = 1 + int(input[0]%4)
		}
		operations := input
		if len(input) > 0 {
			operations = input[1:]
		}
		buffer := NewTypedBuffer[fuzzBufferValue](capacity)

		dropCount := 0
		buffer.SetOnDrop(func() {
			dropCount++
		})

		readCancellationBuffer := NewTypedBuffer[fuzzBufferValue](1)
		closedReadBuffer := NewTypedBuffer[fuzzBufferValue](1)
		closedDone := make(chan struct{})
		close(closedDone)

		acceptedValues := make([]fuzzBufferValue, 0, len(input))
		offered := 0
		accepted := 0
		dropped := 0
		cancelled := 0
		timedOut := 0
		expectedCancelled := 0
		expectedTimedOut := 0
		delivered := 0

		recordRead := func(value fuzzBufferValue, ok bool) {
			if !ok {
				return
			}
			if delivered >= len(acceptedValues) {
				t.Fatalf("read value %+v without a preceding accepted value", value)
			}
			if value != acceptedValues[delivered] {
				t.Fatalf("read value %+v at position %d, want %+v", value, delivered, acceptedValues[delivered])
			}
			delivered++
		}

		for operation, raw := range operations {
			value := fuzzBufferValue{ID: operation, Payload: raw}
			switch raw % 8 {
			case 0, 7: // An open offer is either accepted or explicitly full.
				offered++
				outcome := buffer.WriteContext(context.Background(), value)
				switch outcome.Status {
				case BufferWriteSucceeded:
					if !outcome.OK() || outcome.Err != nil {
						t.Fatalf("successful write returned %+v", outcome)
					}
					accepted++
					acceptedValues = append(acceptedValues, value)
				case BufferWriteBufferFull:
					if outcome.OK() || outcome.Err != nil {
						t.Fatalf("full write returned %+v", outcome)
					}
					dropped++
				default:
					t.Fatalf("open write returned unexpected status %q", outcome.Status)
				}
			case 1, 4: // Immediate reads preserve the accepted FIFO sequence.
				recordRead(buffer.Read())
			case 2: // A pre-closed write is cancellation, never a drop.
				expectedCancelled++
				beforeDrops := dropCount
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				outcome := buffer.WriteContext(ctx, value)
				cancelled++
				if outcome.Status != BufferWriteCancelled || !errors.Is(outcome.Err, context.Canceled) {
					t.Fatalf("cancelled write returned %+v", outcome)
				}
				if dropCount != beforeDrops {
					t.Fatalf("cancelled write changed drop count from %d to %d", beforeDrops, dropCount)
				}
			case 3: // An expired deadline exercises the distinct timeout outcome.
				expectedTimedOut++
				beforeDrops := dropCount
				ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
				outcome := buffer.WriteContext(ctx, value)
				cancel()
				timedOut++
				if outcome.Status != BufferWriteTimedOut || !errors.Is(outcome.Err, context.DeadlineExceeded) {
					t.Fatalf("timed-out write returned %+v", outcome)
				}
				if dropCount != beforeDrops {
					t.Fatalf("timed-out write changed drop count from %d to %d", beforeDrops, dropCount)
				}
			case 5: // Keep a cancelled read empty so its outcome is deterministic.
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				value, err := readCancellationBuffer.ReadContext(ctx)
				if !errors.Is(err, context.Canceled) || value != (fuzzBufferValue{}) {
					t.Fatalf("cancelled read returned value=%+v err=%v", value, err)
				}
			case 6: // The existing done-channel closure surface must terminate.
				value, ok := closedReadBuffer.ReadBlocking(closedDone)
				if ok || value != (fuzzBufferValue{}) {
					t.Fatalf("closed read returned value=%+v ok=%v", value, ok)
				}
			}

			if length := buffer.Len(); length > buffer.Cap() {
				t.Fatalf("buffer length %d exceeded capacity %d", length, buffer.Cap())
			}
			if buffer.HasData() != (buffer.Len() > 0) {
				t.Fatalf("HasData disagreed with Len: has_data=%v len=%d", buffer.HasData(), buffer.Len())
			}
		}

		for {
			value, ok := buffer.Read()
			if !ok {
				break
			}
			recordRead(value, true)
		}

		if accepted != delivered {
			t.Fatalf("accepted=%d delivered=%d", accepted, delivered)
		}
		if dropped+delivered != offered {
			t.Fatalf("dropped=%d delivered=%d offered=%d", dropped, delivered, offered)
		}
		if dropCount != dropped {
			t.Fatalf("drop callback count=%d, full outcomes=%d", dropCount, dropped)
		}
		if buffer.Len() != 0 || buffer.HasData() {
			t.Fatalf("buffer was not drained: len=%d has_data=%v", buffer.Len(), buffer.HasData())
		}
		if cancelled != expectedCancelled || timedOut != expectedTimedOut {
			t.Fatalf("cancelled=%d (want %d), timed_out=%d (want %d)", cancelled, expectedCancelled, timedOut, expectedTimedOut)
		}
	})
}

func TestTypedBufferFullDropsNewest(t *testing.T) {
	buffer := NewTypedBuffer[int](1)
	var dropCount atomic.Int64
	buffer.SetOnDrop(func() {
		dropCount.Add(1)
	})

	first := buffer.WriteContext(context.Background(), 41)
	if first.Status != BufferWriteSucceeded || !first.OK() {
		t.Fatalf("first write returned %+v", first)
	}

	result := make(chan BufferWriteOutcome, 1)
	go func() {
		result <- buffer.WriteContext(context.Background(), 99)
	}()

	var newest BufferWriteOutcome
	select {
	case newest = <-result:
	case <-time.After(time.Second):
		t.Fatal("full-buffer write blocked")
	}
	if newest.Status != BufferWriteBufferFull || newest.OK() || newest.Err != nil {
		t.Fatalf("newest write returned %+v, want buffer_full", newest)
	}
	if got := dropCount.Load(); got != 1 {
		t.Fatalf("drop callback count=%d, want 1", got)
	}
	if buffer.Len() != 1 || !buffer.HasData() {
		t.Fatalf("full buffer state len=%d has_data=%v", buffer.Len(), buffer.HasData())
	}

	retained, ok := buffer.Read()
	if !ok || retained != 41 {
		t.Fatalf("retained value=%d ok=%v, want 41", retained, ok)
	}
	if _, ok := buffer.Read(); ok {
		t.Fatal("newest rejected value was delivered")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := buffer.WriteContext(ctx, 123)
	if cancelled.Status != BufferWriteCancelled || !errors.Is(cancelled.Err, context.Canceled) {
		t.Fatalf("cancelled write returned %+v", cancelled)
	}
	if got := dropCount.Load(); got != 1 {
		t.Fatalf("cancelled write changed drop callback count to %d", got)
	}
}

func TestTypedBufferCapacityDefaults(t *testing.T) {
	for _, requested := range []int{0, -1} {
		buffer := NewTypedBuffer[int](requested)
		if buffer.Cap() != 64 {
			t.Errorf("requested capacity %d produced capacity %d, want 64", requested, buffer.Cap())
		}
	}
}

func TestTypedBufferReadBlockingContext(t *testing.T) {
	buffer := NewTypedBuffer[string](1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if value, ok := buffer.ReadBlockingContext(ctx); ok || value != "" {
		t.Fatalf("cancelled ReadBlockingContext returned value=%q ok=%v", value, ok)
	}
}

type cancelOnSecondDoneContext struct {
	done  chan struct{}
	calls atomic.Int32
}

func (c *cancelOnSecondDoneContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *cancelOnSecondDoneContext) Done() <-chan struct{} {
	if c.calls.Add(1) == 2 {
		close(c.done)
	}
	return c.done
}

func (c *cancelOnSecondDoneContext) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

func (c *cancelOnSecondDoneContext) Value(any) any {
	return nil
}

func TestTypedBufferWriteContextCancellationAfterInitialCheck(t *testing.T) {
	buffer := NewTypedBuffer[int](1)
	if outcome := buffer.WriteContext(context.Background(), 41); outcome.Status != BufferWriteSucceeded {
		t.Fatalf("setup write returned %+v", outcome)
	}
	var dropCount atomic.Int64
	buffer.SetOnDrop(func() {
		dropCount.Add(1)
	})

	ctx := &cancelOnSecondDoneContext{done: make(chan struct{})}
	outcome := buffer.WriteContext(ctx, 99)
	if outcome.Status != BufferWriteCancelled || !errors.Is(outcome.Err, context.Canceled) {
		t.Fatalf("write returned %+v, want cancellation after the initial context check", outcome)
	}
	if got := dropCount.Load(); got != 0 {
		t.Fatalf("cancellation invoked %d drop callbacks", got)
	}
	if value, ok := buffer.Read(); !ok || value != 41 {
		t.Fatalf("cancellation disturbed retained value=%d ok=%v", value, ok)
	}
}

type concurrentBufferValue struct {
	Producer int
	Sequence int
	Payload  byte
}

func TestTypedBufferConcurrentProducersConsumers(t *testing.T) {
	for iteration := 0; iteration < concurrentBufferIterations; iteration++ {
		runTypedBufferConcurrentIteration(t, iteration)
	}
}

func runTypedBufferConcurrentIteration(t *testing.T, iteration int) {
	t.Helper()
	buffer := NewTypedBuffer[concurrentBufferValue](concurrentBufferCapacity)
	var dropCount atomic.Int64
	buffer.SetOnDrop(func() {
		dropCount.Add(1)
	})

	statuses := make([][]BufferWriteStatus, concurrentProducerCount)
	for producer := range statuses {
		statuses[producer] = make([]BufferWriteStatus, concurrentValuesPerProducer)
	}
	var maxLen atomic.Int64

	var producerWG sync.WaitGroup
	producerWG.Add(concurrentProducerCount)
	for producer := 0; producer < concurrentProducerCount; producer++ {
		go func(producer int) {
			defer producerWG.Done()
			rng := rand.New(rand.NewSource(int64(0x51f15e + iteration*97 + producer*13)))
			for sequence := 0; sequence < concurrentValuesPerProducer; sequence++ {
				if rng.Intn(3) == 0 {
					runtime.Gosched()
				}
				value := concurrentBufferValueFor(iteration, producer, sequence)
				statuses[producer][sequence] = buffer.WriteContext(context.Background(), value).Status
				recordBufferMaxLen(buffer, &maxLen)
				if rng.Intn(4) == 0 {
					runtime.Gosched()
				}
			}
		}(producer)
	}

	producersDone := make(chan struct{})
	var deliveryMu sync.Mutex
	delivered := make([]concurrentBufferValue, 0, concurrentProducerCount*concurrentValuesPerProducer)
	var consumerWG sync.WaitGroup
	consumerWG.Add(concurrentConsumerCount)
	for consumer := 0; consumer < concurrentConsumerCount; consumer++ {
		go func(consumer int) {
			defer consumerWG.Done()
			rng := rand.New(rand.NewSource(int64(0x9e3779b9 + iteration*101 + consumer*17)))
			for {
				if rng.Intn(3) == 0 {
					runtime.Gosched()
				}

				// Serialize the receive and append so the ledger observes the
				// channel's actual receive order rather than goroutine handoff order.
				deliveryMu.Lock()
				value, ok := buffer.ReadBlocking(producersDone)
				if ok {
					delivered = append(delivered, value)
					recordBufferMaxLen(buffer, &maxLen)
					deliveryMu.Unlock()
					if rng.Intn(4) == 0 {
						runtime.Gosched()
					}
					continue
				}

				// Once all producers are done, ReadBlocking may select the done
				// signal while values remain. Drain those values before returning.
				for {
					value, ok = buffer.Read()
					if !ok {
						break
					}
					delivered = append(delivered, value)
					recordBufferMaxLen(buffer, &maxLen)
				}
				deliveryMu.Unlock()
				return
			}
		}(consumer)
	}

	producerWG.Wait()
	close(producersDone)
	consumerWG.Wait()

	if maxLen := maxLen.Load(); maxLen > int64(buffer.Cap()) {
		t.Fatalf("observed buffer length %d exceeded capacity %d", maxLen, buffer.Cap())
	}
	if buffer.Len() != 0 || buffer.HasData() {
		t.Fatalf("concurrent consumers left values: len=%d has_data=%v", buffer.Len(), buffer.HasData())
	}

	expected := make(map[concurrentBufferValue]struct{}, concurrentProducerCount*concurrentValuesPerProducer)
	offered := concurrentProducerCount * concurrentValuesPerProducer
	accepted := 0
	dropped := 0
	for producer, producerStatuses := range statuses {
		for sequence, status := range producerStatuses {
			switch status {
			case BufferWriteSucceeded:
				accepted++
				expected[concurrentBufferValueFor(iteration, producer, sequence)] = struct{}{}
			case BufferWriteBufferFull:
				dropped++
			default:
				t.Fatalf("producer %d sequence %d returned unexpected status %q", producer, sequence, status)
			}
		}
	}
	if accepted+dropped != offered {
		t.Fatalf("accepted=%d dropped=%d offered=%d", accepted, dropped, offered)
	}
	if got := int(dropCount.Load()); got != dropped {
		t.Fatalf("drop callback count=%d, full outcomes=%d", got, dropped)
	}
	if accepted == 0 || len(delivered) != accepted {
		t.Fatalf("accepted=%d delivered=%d", accepted, len(delivered))
	}

	seen := make(map[concurrentBufferValue]struct{}, len(delivered))
	lastSequence := make([]int, concurrentProducerCount)
	for producer := range lastSequence {
		lastSequence[producer] = -1
	}
	for index, value := range delivered {
		if _, ok := expected[value]; !ok {
			t.Fatalf("delivered unexpected value at index %d: %+v", index, value)
		}
		if _, duplicate := seen[value]; duplicate {
			t.Fatalf("delivered duplicate value at index %d: %+v", index, value)
		}
		seen[value] = struct{}{}
		if value.Sequence <= lastSequence[value.Producer] {
			t.Fatalf("producer %d delivery order moved from %d to %d", value.Producer, lastSequence[value.Producer], value.Sequence)
		}
		lastSequence[value.Producer] = value.Sequence
	}
	if len(seen) != accepted {
		t.Fatalf("unique delivered values=%d, accepted=%d", len(seen), accepted)
	}
}

func concurrentBufferValueFor(iteration, producer, sequence int) concurrentBufferValue {
	return concurrentBufferValue{
		Producer: producer,
		Sequence: sequence,
		Payload:  byte((iteration*29 + producer*47 + sequence*71) % 251),
	}
}

func recordBufferMaxLen[T any](buffer *TypedBuffer[T], maxLen *atomic.Int64) {
	// len(channel) is an atomic observation. Keep the maximum from every
	// synchronization point so the test does not rely only on the final state.
	observed := int64(buffer.Len())
	for {
		previous := maxLen.Load()
		if observed <= previous || maxLen.CompareAndSwap(previous, observed) {
			return
		}
	}
}

func TestTypedBufferCloseDuringWrite(t *testing.T) {
	for iteration := 0; iteration < cancellationRaceIterations; iteration++ {
		buffer := NewTypedBuffer[int](1)
		var dropCount atomic.Int64
		buffer.SetOnDrop(func() {
			dropCount.Add(1)
		})
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		result := make(chan BufferWriteOutcome, 1)
		go func() {
			close(started)
			result <- buffer.WriteContext(ctx, iteration)
		}()
		<-started
		runtime.Gosched()
		cancel()

		var outcome BufferWriteOutcome
		select {
		case outcome = <-result:
		case <-time.After(time.Second):
			t.Fatalf("write did not terminate after cancellation at iteration %d", iteration)
		}
		switch outcome.Status {
		case BufferWriteSucceeded:
			value, ok := buffer.Read()
			if !ok || value != iteration {
				t.Fatalf("successful concurrent write lost value=%d ok=%v at iteration %d", value, ok, iteration)
			}
		case BufferWriteCancelled:
			if !errors.Is(outcome.Err, context.Canceled) {
				t.Fatalf("cancelled concurrent write err=%v at iteration %d", outcome.Err, iteration)
			}
			if _, ok := buffer.Read(); ok {
				t.Fatalf("cancelled concurrent write left a value at iteration %d", iteration)
			}
		default:
			t.Fatalf("concurrent write returned unexpected outcome %+v at iteration %d", outcome, iteration)
		}

		postClosed := buffer.WriteContext(ctx, iteration+1000)
		if postClosed.Status != BufferWriteCancelled || !errors.Is(postClosed.Err, context.Canceled) {
			t.Fatalf("post-closure write returned %+v at iteration %d", postClosed, iteration)
		}
		if got := dropCount.Load(); got != 0 {
			t.Fatalf("cancellation caused %d drop callbacks at iteration %d", got, iteration)
		}
	}
}

type typedBufferReadResult[T any] struct {
	value T
	ok    bool
}

func TestTypedBufferCloseDuringRead(t *testing.T) {
	for iteration := 0; iteration < cancellationRaceIterations; iteration++ {
		buffer := NewTypedBuffer[int](1)
		done := make(chan struct{})
		started := make(chan struct{})
		result := make(chan typedBufferReadResult[int], 1)
		go func() {
			close(started)
			value, ok := buffer.ReadBlocking(done)
			result <- typedBufferReadResult[int]{value: value, ok: ok}
		}()
		<-started
		runtime.Gosched()
		close(done)

		select {
		case read := <-result:
			if read.ok || read.value != 0 {
				t.Fatalf("empty read returned %+v at iteration %d", read, iteration)
			}
		case <-time.After(time.Second):
			t.Fatalf("read did not terminate after done closure at iteration %d", iteration)
		}

		buffer = NewTypedBuffer[int](1)
		if outcome := buffer.WriteContext(context.Background(), iteration+1); outcome.Status != BufferWriteSucceeded {
			t.Fatalf("setup write returned %+v at iteration %d", outcome, iteration)
		}
		done = make(chan struct{})
		started = make(chan struct{})
		result = make(chan typedBufferReadResult[int], 1)
		go func() {
			close(started)
			value, ok := buffer.ReadBlocking(done)
			result <- typedBufferReadResult[int]{value: value, ok: ok}
		}()
		<-started
		runtime.Gosched()
		close(done)

		select {
		case read := <-result:
			if read.ok {
				if read.value != iteration+1 {
					t.Fatalf("read wrong successful value=%d at iteration %d", read.value, iteration)
				}
			} else {
				retained, ok := buffer.Read()
				if !ok || retained != iteration+1 {
					t.Fatalf("done-selected read lost retained value=%d ok=%v at iteration %d", retained, ok, iteration)
				}
			}
		case <-time.After(time.Second):
			t.Fatalf("value read did not terminate after done closure at iteration %d", iteration)
		}
		if _, ok := buffer.Read(); ok {
			t.Fatalf("successful value was delivered more than once at iteration %d", iteration)
		}
	}
}
