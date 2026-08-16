package timeharness

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestScenarioClockLifecycleAndExactTimestamps(t *testing.T) {
	base := time.Date(2026, time.August, 16, 10, 11, 12, 13, time.UTC)
	s := NewScenario(t, base, 7*time.Millisecond)
	if got := s.CurrentTick(); got != 0 {
		t.Fatalf("initial tick: got %d", got)
	}
	if got := s.Clock(); got == nil {
		t.Fatal("scenario did not create a shared clock")
	}
	left, err := s.Register("left")
	if err != nil {
		t.Fatal(err)
	}
	right, err := s.Register("right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(" "); err == nil {
		t.Fatal("empty participant name was accepted")
	}
	if _, err := s.Register("left"); err == nil {
		t.Fatal("duplicate participant name was accepted")
	}

	observations := make(chan Observation, 4)
	for _, participant := range []*Participant{left, right} {
		participant := participant
		participant.Run(func() {
			for tick := uint64(1); tick <= 2; tick++ {
				observation, observeErr := participant.Observe(tick)
				if observeErr != nil {
					return
				}
				observations <- observation
			}
			participant.Complete()
		})
	}
	for tick := uint64(1); tick <= 2; tick++ {
		if _, err := s.AdvanceTo(tick); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			observation := <-observations
			if observation.Tick != tick || observation.Time != base.Add(time.Duration(tick)*7*time.Millisecond) {
				t.Fatalf("observation: got %+v at tick %d", observation, tick)
			}
		}
	}
	if _, err := s.Register("late"); err == nil {
		t.Fatal("participant registration remained open after advancement")
	}
}

func TestBarrierWithheldParticipantAndConcurrentAdvances(t *testing.T) {
	s := NewScenario(t, time.Unix(50, 0).UTC(), time.Millisecond, WithWatchdogTimeout(time.Second))
	participants := make([]*Participant, 0, 8)
	for i := 0; i < 8; i++ {
		participant, err := s.Register("peer-" + string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		participants = append(participants, participant)
	}
	started := make(chan string, len(participants))
	afterOne := make(chan Observation, len(participants))
	afterTwo := make(chan Observation, len(participants))
	withhold := make(chan struct{})
	for _, participant := range participants {
		participant := participant
		participant.Run(func() {
			started <- participant.Name()
			if participant.Name() == "peer-h" {
				<-withhold
			}
			first, err := participant.Observe(1)
			if err != nil {
				return
			}
			afterOne <- first
			second, err := participant.Observe(2)
			if err == nil {
				afterTwo <- second
			}
			participant.Complete()
		})
	}
	seen := make(map[string]bool)
	for len(seen) < len(participants) {
		seen[<-started] = true
	}

	advanceResults := make(chan error, 4)
	go func() { _, err := s.AdvanceTo(1); advanceResults <- err }()
	for spins := 0; s.CurrentTick() == 0 && spins < 10000; spins++ {
		runtime.Gosched()
	}
	if s.CurrentTick() != 1 {
		t.Fatalf("advance did not start generation 1; tick=%d", s.CurrentTick())
	}
	for i := 0; i < 3; i++ {
		go func() { _, err := s.AdvanceTo(2); advanceResults <- err }()
	}
	runtime.Gosched()
	select {
	case observation := <-afterOne:
		t.Fatalf("participant crossed withheld barrier early: %+v", observation)
	default:
	}
	select {
	case err := <-advanceResults:
		t.Fatalf("advance returned before withheld peer: %v", err)
	default:
	}
	close(withhold)

	for i := 0; i < len(participants); i++ {
		observation := <-afterOne
		if observation.Tick != 1 {
			t.Fatalf("first observation: got %+v", observation)
		}
	}
	for i := 0; i < 4; i++ {
		if err := <-advanceResults; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < len(participants); i++ {
		observation := <-afterTwo
		if observation.Tick != 2 {
			t.Fatalf("second observation: got %+v", observation)
		}
	}
}

func TestExpectWithinTicksReportsExactSuccessAndFailure(t *testing.T) {
	s := NewScenario(t, time.Unix(0, 0).UTC(), time.Millisecond)
	participant, err := s.Register("observer")
	if err != nil {
		t.Fatal(err)
	}
	participant.Run(func() {
		for tick := uint64(1); tick <= 2; tick++ {
			if _, observeErr := participant.Observe(tick); observeErr != nil {
				return
			}
		}
		participant.Complete()
	})
	tick, err := s.ExpectWithinTicks("observer reached tick two", 3, func() bool { return participant.LastTick() == 2 })
	if err != nil || tick != 2 {
		t.Fatalf("expectation: tick=%d err=%v", tick, err)
	}

	final, err := s.ExpectWithinTicks("never happens", 2, func() bool { return false })
	if err == nil {
		t.Fatal("bounded expectation unexpectedly passed")
	}
	if final != 4 {
		t.Fatalf("final tick: got %d, want 4", final)
	}
	var expectationErr *HarnessError
	if !errors.As(err, &expectationErr) {
		t.Fatalf("expectation error type: %T %v", err, err)
	}
	if expectationErr.StartingTick != 2 || expectationErr.AllowedTicks != 2 || expectationErr.FinalTick != 4 {
		t.Fatalf("expectation details: %+v", expectationErr)
	}
}
