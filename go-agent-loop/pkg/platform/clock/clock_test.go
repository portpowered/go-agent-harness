package clock

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestS11SourceConformance(t *testing.T) {
	base := time.Date(2026, time.January, 2, 3, 4, 5, 6, time.FixedZone("test", 90*60))
	deterministic := NewDeterministic(base, 10*time.Millisecond)

	tests := []struct {
		name   string
		source Source
	}{
		{name: "real", source: Real{}},
		{name: "deterministic", source: deterministic},
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

			if test.name == "deterministic" && second != base {
				t.Fatalf("initial deterministic time: got %v, want %v", second, base)
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
			switch value % 3 {
			case 0:
				modelTick = clock.Advance()
			case 1:
				modelTick = clock.AdvanceTo(target)
			case 2:
				modelTick = clock.AdvanceTo(target / 2)
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
