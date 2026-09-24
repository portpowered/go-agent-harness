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
		h := newFuzzBufferHarness(t, capacity, len(input))
		for operation, raw := range operations {
			h.apply(fuzzBufferValue{ID: operation, Payload: raw}, raw)
			h.checkInvariants()
		}
		h.drain()
		h.verifyConservation()
	})
}

// fuzzBufferHarness drives one fuzz operation stream against a real buffer
// and records the outcome counters used by the conservation checks.
type fuzzBufferHarness struct {
	t                      *testing.T
	buffer                 *TypedBuffer[fuzzBufferValue]
	readCancellationBuffer *TypedBuffer[fuzzBufferValue]
	closedReadBuffer       *TypedBuffer[fuzzBufferValue]
	closedDone             chan struct{}

	acceptedValues    []fuzzBufferValue
	dropCount         int
	offered           int
	accepted          int
	dropped           int
	cancelled         int
	timedOut          int
	expectedCancelled int
	expectedTimedOut  int
	delivered         int
}

func newFuzzBufferHarness(t *testing.T, capacity, inputLen int) *fuzzBufferHarness {
	h := &fuzzBufferHarness{
		t:                      t,
		buffer:                 NewTypedBuffer[fuzzBufferValue](capacity),
		readCancellationBuffer: NewTypedBuffer[fuzzBufferValue](1),
		closedReadBuffer:       NewTypedBuffer[fuzzBufferValue](1),
		closedDone:             make(chan struct{}),
		acceptedValues:         make([]fuzzBufferValue, 0, inputLen),
	}
	h.buffer.SetOnDrop(func(_ fuzzBufferValue) {
		h.dropCount++
	})
	close(h.closedDone)
	return h
}

func (h *fuzzBufferHarness) apply(value fuzzBufferValue, raw byte) {
	switch raw % 8 {
	case 0, 7: // An open offer is either accepted or explicitly full.
		h.offer(value)
	case 1, 4: // Immediate reads preserve the accepted FIFO sequence.
		h.recordRead(h.buffer.Read())
	case 2: // A pre-closed write is cancellation, never a drop.
		h.writeCancelled(value)
	case 3: // An expired deadline exercises the distinct timeout outcome.
		h.writeTimedOut(value)
	case 5: // Keep a cancelled read empty so its outcome is deterministic.
		h.readCancelled()
	case 6: // The existing done-channel closure surface must terminate.
		value, ok := h.closedReadBuffer.ReadBlocking(h.closedDone)
		if ok || value != (fuzzBufferValue{}) {
			h.t.Fatalf("closed read returned value=%+v ok=%v", value, ok)
		}
	}
}

func (h *fuzzBufferHarness) recordRead(value fuzzBufferValue, ok bool) {
	if !ok {
		return
	}
	if h.delivered >= len(h.acceptedValues) {
		h.t.Fatalf("read value %+v without a preceding accepted value", value)
	}
	if value != h.acceptedValues[h.delivered] {
		h.t.Fatalf("read value %+v at position %d, want %+v", value, h.delivered, h.acceptedValues[h.delivered])
	}
	h.delivered++
}

func (h *fuzzBufferHarness) offer(value fuzzBufferValue) {
	h.offered++
	outcome := h.buffer.WriteContext(context.Background(), value)
	switch outcome.Status {
	case BufferWriteSucceeded:
		if !outcome.OK() || outcome.Err != nil {
			h.t.Fatalf("successful write returned %+v", outcome)
		}
		h.accepted++
		h.acceptedValues = append(h.acceptedValues, value)
	case BufferWriteBufferFull:
		if outcome.OK() || outcome.Err != nil {
			h.t.Fatalf("full write returned %+v", outcome)
		}
		h.dropped++
	case BufferWriteCancelled, BufferWriteTimedOut, BufferWriteStopped:
		fallthrough
	default:
		h.t.Fatalf("open write returned unexpected status %q", outcome.Status)
	}
}

func (h *fuzzBufferHarness) writeCancelled(value fuzzBufferValue) {
	h.expectedCancelled++
	beforeDrops := h.dropCount
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := h.buffer.WriteContext(ctx, value)
	h.cancelled++
	if outcome.Status != BufferWriteCancelled || !errors.Is(outcome.Err, context.Canceled) {
		h.t.Fatalf("cancelled write returned %+v", outcome)
	}
	if h.dropCount != beforeDrops {
		h.t.Fatalf("cancelled write changed drop count from %d to %d", beforeDrops, h.dropCount)
	}
}

func (h *fuzzBufferHarness) writeTimedOut(value fuzzBufferValue) {
	h.expectedTimedOut++
	beforeDrops := h.dropCount
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	outcome := h.buffer.WriteContext(ctx, value)
	cancel()
	h.timedOut++
	if outcome.Status != BufferWriteTimedOut || !errors.Is(outcome.Err, context.DeadlineExceeded) {
		h.t.Fatalf("timed-out write returned %+v", outcome)
	}
	if h.dropCount != beforeDrops {
		h.t.Fatalf("timed-out write changed drop count from %d to %d", beforeDrops, h.dropCount)
	}
}

func (h *fuzzBufferHarness) readCancelled() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	value, err := h.readCancellationBuffer.ReadContext(ctx)
	if !errors.Is(err, context.Canceled) || value != (fuzzBufferValue{}) {
		h.t.Fatalf("cancelled read returned value=%+v err=%v", value, err)
	}
}

func (h *fuzzBufferHarness) checkInvariants() {
	if length := h.buffer.Len(); length > h.buffer.Cap() {
		h.t.Fatalf("buffer length %d exceeded capacity %d", length, h.buffer.Cap())
	}
	if h.buffer.HasData() != (h.buffer.Len() > 0) {
		h.t.Fatalf("HasData disagreed with Len: has_data=%v len=%d", h.buffer.HasData(), h.buffer.Len())
	}
}

func (h *fuzzBufferHarness) drain() {
	for {
		value, ok := h.buffer.Read()
		if !ok {
			return
		}
		h.recordRead(value, true)
	}
}

func (h *fuzzBufferHarness) verifyConservation() {
	if h.accepted != h.delivered {
		h.t.Fatalf("accepted=%d delivered=%d", h.accepted, h.delivered)
	}
	if h.dropped+h.delivered != h.offered {
		h.t.Fatalf("dropped=%d delivered=%d offered=%d", h.dropped, h.delivered, h.offered)
	}
	if h.dropCount != h.dropped {
		h.t.Fatalf("drop callback count=%d, full outcomes=%d", h.dropCount, h.dropped)
	}
	if h.buffer.Len() != 0 || h.buffer.HasData() {
		h.t.Fatalf("buffer was not drained: len=%d has_data=%v", h.buffer.Len(), h.buffer.HasData())
	}
	if h.cancelled != h.expectedCancelled || h.timedOut != h.expectedTimedOut {
		h.t.Fatalf("cancelled=%d (want %d), timed_out=%d (want %d)", h.cancelled, h.expectedCancelled, h.timedOut, h.expectedTimedOut)
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
	run := newConcurrentBufferRun(iteration)

	var producerWG sync.WaitGroup
	producerWG.Add(concurrentProducerCount)
	for producer := 0; producer < concurrentProducerCount; producer++ {
		go func(producer int) {
			defer producerWG.Done()
			run.produce(producer)
		}(producer)
	}

	var consumerWG sync.WaitGroup
	consumerWG.Add(concurrentConsumerCount)
	for consumer := 0; consumer < concurrentConsumerCount; consumer++ {
		go func(consumer int) {
			defer consumerWG.Done()
			run.consume(consumer)
		}(consumer)
	}

	producerWG.Wait()
	close(run.producersDone)
	consumerWG.Wait()

	if maxLen := run.maxLen.Load(); maxLen > int64(run.buffer.Cap()) {
		t.Fatalf("observed buffer length %d exceeded capacity %d", maxLen, run.buffer.Cap())
	}
	if run.buffer.Len() != 0 || run.buffer.HasData() {
		t.Fatalf("concurrent consumers left values: len=%d has_data=%v", run.buffer.Len(), run.buffer.HasData())
	}
	expected, accepted := run.tallyStatuses(t)
	run.verifyDeliveries(t, expected, accepted)
}

// concurrentBufferRun is one iteration of the concurrent producer/consumer
// property: per-producer write outcomes and the consumers' receive ledger.
type concurrentBufferRun struct {
	iteration     int
	buffer        *TypedBuffer[concurrentBufferValue]
	dropCount     atomic.Int64
	maxLen        atomic.Int64
	statuses      [][]BufferWriteStatus
	producersDone chan struct{}
	deliveryMu    sync.Mutex
	delivered     []concurrentBufferValue
}

func newConcurrentBufferRun(iteration int) *concurrentBufferRun {
	run := &concurrentBufferRun{
		iteration:     iteration,
		buffer:        NewTypedBuffer[concurrentBufferValue](concurrentBufferCapacity),
		statuses:      make([][]BufferWriteStatus, concurrentProducerCount),
		producersDone: make(chan struct{}),
		delivered:     make([]concurrentBufferValue, 0, concurrentProducerCount*concurrentValuesPerProducer),
	}
	run.buffer.SetOnDrop(func(_ concurrentBufferValue) {
		run.dropCount.Add(1)
	})
	for producer := range run.statuses {
		run.statuses[producer] = make([]BufferWriteStatus, concurrentValuesPerProducer)
	}
	return run
}

func (run *concurrentBufferRun) produce(producer int) {
	rng := rand.New(rand.NewSource(int64(0x51f15e + run.iteration*97 + producer*13)))
	for sequence := 0; sequence < concurrentValuesPerProducer; sequence++ {
		if rng.Intn(3) == 0 {
			runtime.Gosched()
		}
		value := concurrentBufferValueFor(run.iteration, producer, sequence)
		run.statuses[producer][sequence] = run.buffer.WriteContext(context.Background(), value).Status
		recordBufferMaxLen(run.buffer, &run.maxLen)
		if rng.Intn(4) == 0 {
			runtime.Gosched()
		}
	}
}

func (run *concurrentBufferRun) consume(consumer int) {
	rng := rand.New(rand.NewSource(int64(0x9e3779b9 + run.iteration*101 + consumer*17)))
	for {
		if rng.Intn(3) == 0 {
			runtime.Gosched()
		}

		// Serialize the receive and append so the ledger observes the
		// channel's actual receive order rather than goroutine handoff order.
		run.deliveryMu.Lock()
		value, ok := run.buffer.ReadBlocking(run.producersDone)
		if !ok {
			// Once all producers are done, ReadBlocking may select the done
			// signal while values remain. Drain those values before returning.
			run.drainLocked()
			run.deliveryMu.Unlock()
			return
		}
		run.delivered = append(run.delivered, value)
		recordBufferMaxLen(run.buffer, &run.maxLen)
		run.deliveryMu.Unlock()
		if rng.Intn(4) == 0 {
			runtime.Gosched()
		}
	}
}

// drainLocked must be called with deliveryMu held.
func (run *concurrentBufferRun) drainLocked() {
	for {
		value, ok := run.buffer.Read()
		if !ok {
			return
		}
		run.delivered = append(run.delivered, value)
		recordBufferMaxLen(run.buffer, &run.maxLen)
	}
}

// tallyStatuses checks write-outcome conservation and returns the set of
// accepted values with its size.
func (run *concurrentBufferRun) tallyStatuses(t *testing.T) (map[concurrentBufferValue]struct{}, int) {
	t.Helper()
	expected := make(map[concurrentBufferValue]struct{}, concurrentProducerCount*concurrentValuesPerProducer)
	offered := concurrentProducerCount * concurrentValuesPerProducer
	accepted := 0
	dropped := 0
	for producer, producerStatuses := range run.statuses {
		for sequence, status := range producerStatuses {
			switch status {
			case BufferWriteSucceeded:
				accepted++
				expected[concurrentBufferValueFor(run.iteration, producer, sequence)] = struct{}{}
			case BufferWriteBufferFull:
				dropped++
			case BufferWriteCancelled, BufferWriteTimedOut, BufferWriteStopped:
				fallthrough
			default:
				t.Fatalf("producer %d sequence %d returned unexpected status %q", producer, sequence, status)
			}
		}
	}
	if accepted+dropped != offered {
		t.Fatalf("accepted=%d dropped=%d offered=%d", accepted, dropped, offered)
	}
	if got := int(run.dropCount.Load()); got != dropped {
		t.Fatalf("drop callback count=%d, full outcomes=%d", got, dropped)
	}
	if accepted == 0 || len(run.delivered) != accepted {
		t.Fatalf("accepted=%d delivered=%d", accepted, len(run.delivered))
	}
	return expected, accepted
}

// verifyDeliveries checks that every delivered value was accepted exactly
// once and that each producer's values arrived in sequence order.
func (run *concurrentBufferRun) verifyDeliveries(t *testing.T, expected map[concurrentBufferValue]struct{}, accepted int) {
	t.Helper()
	seen := make(map[concurrentBufferValue]struct{}, len(run.delivered))
	lastSequence := make([]int, concurrentProducerCount)
	for producer := range lastSequence {
		lastSequence[producer] = -1
	}
	for index, value := range run.delivered {
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
		buffer.SetOnDrop(func(_ int) {
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
