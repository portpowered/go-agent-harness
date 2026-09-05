package clock

import (
	"context"
	"errors"
	"math"
	"runtime"
	"sync"
	"testing"
	"time"
)

type contextValueKey string

func TestS11SourceConformance(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 5, 6, time.FixedZone("test", 90*60))
	deterministic := NewDeterministic(base, 10*time.Millisecond)

	tests := []struct {
		name          string
		source        Source
		deterministic *Deterministic
	}{
		{name: "real", source: Real{}},
		{name: "deterministic", source: deterministic, deterministic: deterministic},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := test.source.Now()
			second := test.source.Now()
			if first.IsZero() || second.IsZero() {
				t.Fatal("source returned a zero timestamp")
			}
			if second.Before(first) {
				t.Fatalf("source moved backward: first=%v second=%v", first, second)
			}

			if test.deterministic == nil {
				return
			}
			if second != base {
				t.Fatalf("initial deterministic time: got %v, want %v", second, base)
			}

			const advancedTick = uint64(3)
			if got := test.deterministic.AdvanceTo(advancedTick); got != advancedTick {
				t.Fatalf("deterministic AdvanceTo: got %d, want %d", got, advancedTick)
			}
			advanced := test.source.Now()
			want := base.Add(time.Duration(advancedTick) * 10 * time.Millisecond)
			if advanced != want {
				t.Fatalf("advanced deterministic time: got %v, want %v", advanced, want)
			}
			if advanced.Before(second) {
				t.Fatalf("deterministic source moved backward: first=%v second=%v", second, advanced)
			}
			if got := test.deterministic.Tick(); got != advancedTick {
				t.Fatalf("deterministic tick: got %d, want %d", got, advancedTick)
			}
		})
	}
}

func TestDeterministicNormalizationAndAdvancement(t *testing.T) {
	clock := NewDeterministic(time.Time{}, 0)
	epoch := time.Unix(0, 0).UTC()

	if got := clock.Tick(); got != 0 {
		t.Fatalf("initial tick: got %d, want 0", got)
	}
	if got := clock.Now(); got != epoch {
		t.Fatalf("normalized base: got %v, want %v", got, epoch)
	}
	if got := clock.Advance(); got != 1 {
		t.Fatalf("Advance: got %d, want 1", got)
	}
	if got, want := clock.Now(), epoch.Add(time.Millisecond); got != want {
		t.Fatalf("advanced time: got %v, want %v", got, want)
	}
	if got := clock.AdvanceTo(5); got != 5 {
		t.Fatalf("AdvanceTo: got %d, want 5", got)
	}
	if got := clock.AdvanceTo(3); got != 5 {
		t.Fatalf("backward AdvanceTo: got %d, want 5", got)
	}
	if got := clock.Tick(); got != 5 {
		t.Fatalf("final tick: got %d, want 5", got)
	}
}

func TestDeterministicPreservesBaseAndExactMapping(t *testing.T) {
	base := time.Date(2030, time.June, 7, 8, 9, 10, 11, time.FixedZone("base", -4*60*60))
	tickDuration := 37 * time.Microsecond
	clock := NewDeterministic(base, tickDuration)

	for _, tick := range []uint64{0, 1, 4, 17, 99} {
		if got := clock.AdvanceTo(tick); got != tick {
			t.Fatalf("AdvanceTo(%d): got %d", tick, got)
		}
		want := base.Add(time.Duration(tick) * tickDuration)
		first := clock.Now()
		second := clock.Now()
		if first != want || second != want {
			t.Fatalf("tick %d: got %v and %v, want %v", tick, first, second, want)
		}
		if first != second {
			t.Fatalf("tick %d reread changed: %v != %v", tick, first, second)
		}
	}
}

func TestDeterministicSaturatesAtMaximumTick(t *testing.T) {
	clock := NewDeterministic(time.Unix(0, 0).UTC(), time.Millisecond)
	maximum := uint64(math.MaxUint64)

	if got := clock.AdvanceTo(maximum); got != maximum {
		t.Fatalf("AdvanceTo(max): got %d, want %d", got, maximum)
	}
	atMaximum := clock.Now()
	if got := clock.Advance(); got != maximum {
		t.Fatalf("Advance at max: got %d, want %d", got, maximum)
	}
	if got := clock.AdvanceTo(0); got != maximum {
		t.Fatalf("AdvanceTo(0) at max: got %d, want %d", got, maximum)
	}
	if got := clock.Now(); got.Before(atMaximum) {
		t.Fatalf("timestamp moved backward at max tick: before=%v after=%v", atMaximum, got)
	}
}

func TestEnsure(t *testing.T) {
	fallback := Ensure(nil)
	if fallback == nil {
		t.Fatal("Ensure(nil) returned nil")
	}
	if _, ok := fallback.(Real); !ok {
		t.Fatalf("Ensure(nil) returned %T, want Real", fallback)
	}
	if fallback.Now().IsZero() {
		t.Fatal("Ensure(nil) returned unusable source")
	}

	supplied := NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	if got := Ensure(supplied); got != supplied {
		t.Fatal("Ensure replaced supplied source")
	}
}

func TestDeterministicTimerFiresAtLogicalDeadline(t *testing.T) {
	clock := NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	timer := clock.NewTimer(3 * time.Second)

	select {
	case <-timer.C():
		t.Fatal("timer fired before its logical deadline")
	default:
	}
	clock.AdvanceTo(2)
	select {
	case <-timer.C():
		t.Fatal("timer fired before its logical deadline")
	default:
	}
	clock.Advance()
	select {
	case <-timer.C():
	default:
		t.Fatal("timer did not fire at its logical deadline")
	}
	if timer.Stop() {
		t.Fatal("Stop reported an active timer after it fired")
	}
}

func TestDeterministicTimerStopPreventsDelivery(t *testing.T) {
	clock := NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	timer := clock.NewTimer(time.Second)
	if !timer.Stop() {
		t.Fatal("Stop reported an inactive timer")
	}
	clock.Advance()
	select {
	case <-timer.C():
		t.Fatal("stopped timer delivered a tick")
	default:
	}
}

func TestDeterministicTimersOrderByDeadlineAndCreation(t *testing.T) {
	base := time.Unix(42, 0).UTC()
	clock := NewDeterministic(base, time.Second)
	late := clock.NewTimer(30 * time.Millisecond)
	early := clock.NewTimer(10 * time.Millisecond)
	middle := clock.NewTimer(20 * time.Millisecond)
	earlyTie := clock.NewTimer(20 * time.Millisecond)

	clock.AdvanceBy(30 * time.Millisecond)
	for name, timer := range map[string]Timer{
		"early": early, "middle": middle, "late": late, "early tie": earlyTie,
	} {
		select {
		case <-timer.C():
		default:
			t.Fatalf("%s timer did not fire", name)
		}
	}

	// Read the timers in their expected delivery order. A heap plus a creation
	// sequence makes equal deadlines deterministic instead of map-order based.
	clock = NewDeterministic(base, time.Second)
	first := clock.NewTimer(10 * time.Millisecond)
	second := clock.NewTimer(10 * time.Millisecond)
	third := clock.NewTimer(10 * time.Millisecond)
	clock.AdvanceBy(10 * time.Millisecond)
	for index, timer := range []Timer{first, second, third} {
		select {
		case <-timer.C():
		default:
			t.Fatalf("timer %d did not fire", index)
		}
	}
}

func TestDeterministicTimerUsesPreciseElapsedTimeWithoutTick(t *testing.T) {
	base := time.Unix(42, 0).UTC()
	clock := NewDeterministic(base, time.Second)
	timer := clock.NewTimer(1500 * time.Microsecond)

	clock.AdvanceBy(1499 * time.Microsecond)
	select {
	case <-timer.C():
		t.Fatal("timer fired before precise elapsed deadline")
	default:
	}
	if got := clock.Tick(); got != 0 {
		t.Fatalf("AdvanceBy changed logical tick: got %d, want 0", got)
	}
	clock.AdvanceBy(time.Microsecond)
	select {
	case got := <-timer.C():
		want := base.Add(1500 * time.Microsecond)
		if got != want {
			t.Fatalf("timer timestamp: got %v, want %v", got, want)
		}
	default:
		t.Fatal("timer did not fire at precise elapsed deadline")
	}
}

func TestDeterministicTimerStopIsIdempotentAndRaceSafe(t *testing.T) {
	clock := NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	timer := clock.NewTimer(time.Second)
	if !timer.Stop() {
		t.Fatal("first Stop reported an inactive timer")
	}
	if timer.Stop() {
		t.Fatal("second Stop reported an active timer")
	}
	clock.AdvanceBy(time.Second)
	select {
	case <-timer.C():
		t.Fatal("stopped timer delivered a tick")
	default:
	}
}

func TestDeterministicWaitHonorsCancellationAndVirtualAdvance(t *testing.T) {
	clock := NewDeterministic(time.Unix(42, 0).UTC(), time.Second)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := clock.Wait(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait cancelled error: got %v, want context.Canceled", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- clock.Wait(ctx, 3*time.Millisecond) }()
	waitForDeterministicTimers(t, clock, 1)
	clock.AdvanceBy(3 * time.Millisecond)
	for attempts := 0; attempts < 10000; attempts++ {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("virtual Wait error: %v", err)
			}
			return
		default:
			runtime.Gosched()
		}
	}
	t.Fatal("virtual Wait did not return after virtual advance")
}

func TestDeterministicContextDeadlineAndParentCancellation(t *testing.T) {
	base := time.Unix(42, 0).UTC()
	clock := NewDeterministic(base, time.Second)
	parent := context.WithValue(context.Background(), contextValueKey("key"), "value")
	ctx, cancel := clock.WithTimeout(parent, 5*time.Millisecond)
	defer cancel()
	if got := ctx.Value(contextValueKey("key")); got != "value" {
		t.Fatalf("context value was not preserved: got %v", got)
	}
	if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(base.Add(5*time.Millisecond)) {
		t.Fatalf("context deadline: got %v (ok=%v)", deadline, ok)
	}
	clock.AdvanceBy(4 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatal("context expired before virtual deadline")
	default:
	}
	clock.AdvanceBy(time.Millisecond)
	if !waitForContextDone(ctx) {
		t.Fatal("context did not expire after virtual deadline")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("context error: got %v, want DeadlineExceeded", ctx.Err())
	}

	parentCancel, cancelParent := context.WithCancel(context.Background())
	parentCtx, cancelChild := clock.WithTimeout(parentCancel, time.Hour)
	cancelParent()
	if !waitForContextDone(parentCtx) {
		t.Fatal("parent cancellation did not propagate")
	}
	if !errors.Is(parentCtx.Err(), context.Canceled) {
		t.Fatalf("parent cancellation error: got %v", parentCtx.Err())
	}
	cancelChild()
}

func TestDeterministicConcurrentTimerLifecycleAndAdvancement(t *testing.T) {
	clock := NewDeterministic(time.Unix(42, 0).UTC(), time.Microsecond)
	const workers = 8
	const iterations = 500
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func(worker int) {
			defer group.Done()
			<-start
			for i := 0; i < iterations; i++ {
				timer := clock.NewTimer(time.Duration((worker+i)%17) * time.Microsecond)
				if (worker+i)%2 == 0 {
					timer.Stop()
				}
			}
		}(worker)
	}
	group.Add(1)
	go func() {
		defer group.Done()
		<-start
		for i := 0; i < workers*iterations; i++ {
			clock.AdvanceBy(time.Microsecond)
		}
	}()
	close(start)
	group.Wait()
	clock.AdvanceBy(time.Hour)
}

func TestRequireTimerSourceDoesNotFallbackToHostTime(t *testing.T) {
	var source Source = timestampOnly{}
	if _, err := RequireTimerSource(source); !errors.Is(err, ErrTimerSourceUnavailable) {
		t.Fatalf("RequireTimerSource error: got %v, want ErrTimerSourceUnavailable", err)
	}
	if err := Wait(context.Background(), source, time.Second); !errors.Is(err, ErrTimerSourceUnavailable) {
		t.Fatalf("Wait unsupported source error: got %v, want ErrTimerSourceUnavailable", err)
	}
}

type timestampOnly struct{}

func (timestampOnly) Now() time.Time { return time.Unix(42, 0).UTC() }

func waitForDeterministicTimers(t *testing.T, clock *Deterministic, count int) {
	t.Helper()
	for attempts := 0; attempts < 10000; attempts++ {
		clock.advanceMu.Lock()
		ready := clock.timers.Len() >= count
		clock.advanceMu.Unlock()
		if ready {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timers did not register")
}

func waitForContextDone(ctx context.Context) bool {
	for attempts := 0; attempts < 10000; attempts++ {
		select {
		case <-ctx.Done():
			return true
		default:
			runtime.Gosched()
		}
	}
	return false
}

func FuzzS7DeterministicMapping(f *testing.F) {
	f.Add(int64(42), int64(time.Microsecond), []byte{0, 1, 5, 2, 9})
	f.Add(int64(-100), int64(time.Second), []byte{3, 7, 1})
	f.Add(int64(0), int64(0), []byte{4, 2, 8})

	f.Fuzz(func(t *testing.T, baseSeconds, durationNanos int64, sequence []byte) {
		if len(sequence) > 32 {
			sequence = sequence[:32]
		}
		base := time.Unix(baseSeconds, 123).UTC()
		duration := time.Duration(durationNanos)
		if duration <= 0 {
			duration = time.Millisecond
		}
		if duration > time.Hour {
			duration = duration%time.Hour + time.Nanosecond
		}

		clock := NewDeterministic(base, duration)
		modelTick := uint64(0)
		for index, value := range sequence {
			target := uint64(value) + uint64(index)
			var returnedTick uint64
			switch value % 3 {
			case 0:
				if modelTick < math.MaxUint64 {
					modelTick++
				}
				returnedTick = clock.Advance()
			case 1:
				if target > modelTick {
					modelTick = target
				}
				returnedTick = clock.AdvanceTo(target)
			case 2:
				target /= 2
				if target > modelTick {
					modelTick = target
				}
				returnedTick = clock.AdvanceTo(target)
			}

			if returnedTick != modelTick {
				t.Fatalf("step %d: returned tick got %d, want %d", index, returnedTick, modelTick)
			}

			if got := clock.Tick(); got != modelTick {
				t.Fatalf("step %d: tick got %d, want %d", index, got, modelTick)
			}
			want := base.Add(time.Duration(modelTick) * duration)
			first := clock.Now()
			second := clock.Now()
			if first != want || second != want {
				t.Fatalf("step %d at tick %d: got %v and %v, want %v", index, modelTick, first, second, want)
			}
		}
	})
}

func TestS8ConcurrentReadersAndAdvancers(t *testing.T) {
	base := time.Unix(100, 0).UTC()
	tickDuration := time.Microsecond
	clock := NewDeterministic(base, tickDuration)
	const (
		readerCount   = 6
		advancerCount = 4
		iterations    = 500
	)

	start := make(chan struct{})
	var readers sync.WaitGroup
	var advancers sync.WaitGroup
	readerErrors := make(chan string, readerCount)

	for reader := 0; reader < readerCount; reader++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			var previous time.Time
			for i := 0; i < iterations; i++ {
				observed := clock.Now()
				if i > 0 && observed.Before(previous) {
					readerErrors <- "reader observed time moving backward"
					return
				}
				previous = observed
				if observed.Before(base) {
					readerErrors <- "reader observed time before base"
					return
				}
				delta := observed.Sub(base)
				if delta%tickDuration != 0 {
					readerErrors <- "reader observed a timestamp off the tick lattice"
					return
				}
			}
		}()
	}

	for advancer := 0; advancer < advancerCount; advancer++ {
		advancers.Add(1)
		go func(id int) {
			defer advancers.Done()
			<-start
			for i := 0; i < iterations; i++ {
				if id%2 == 0 {
					clock.Advance()
					continue
				}
				clock.AdvanceTo(uint64(id*iterations + i))
			}
		}(advancer)
	}

	close(start)
	readers.Wait()
	advancers.Wait()
	close(readerErrors)
	for message := range readerErrors {
		t.Fatal(message)
	}

	if got := clock.Tick(); got == 0 {
		t.Fatal("concurrent advancers did not advance the clock")
	}

	// Once advancement stops, concurrent readers must resolve the same tick to
	// byte-for-byte identical timestamps.
	expectedTick := clock.Tick()
	expectedTime := base.Add(time.Duration(expectedTick) * tickDuration)
	reads := make(chan time.Time, readerCount)
	var stableReaders sync.WaitGroup
	for i := 0; i < readerCount; i++ {
		stableReaders.Add(1)
		go func() {
			defer stableReaders.Done()
			reads <- clock.Now()
		}()
	}
	stableReaders.Wait()
	close(reads)
	for got := range reads {
		if got != expectedTime {
			t.Fatalf("equal-tick read: got %v, want %v", got, expectedTime)
		}
	}
}
