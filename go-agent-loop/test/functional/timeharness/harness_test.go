package timeharness

import (
	"errors"
	"testing"
	"time"
)

func TestScenarioLifecycleExactTicksAndExpectations(t *testing.T) {
	base := time.Date(2026, time.August, 16, 10, 11, 12, 13, time.UTC)
	s := testScenario(base, 7*time.Millisecond)
	defer s.Close()
	if got := s.Clock().Tick(); got != 0 {
		t.Fatalf("scenario started at tick %d", got)
	}
	left, right := register(s, "left"), register(s, "right")
	for _, name := range []string{" ", "left"} {
		if _, err := s.Register(name); err == nil {
			t.Fatalf("invalid name %q accepted", name)
		}
	}
	observations := make(chan Observation, 4)
	for _, p := range []*Participant{left, right} {
		p := p
		p.Run(func() { runTicks(p, observations, 2, nil) })
	}
	tick, err := s.ExpectWithinTicks("both peers reached tick two", 3, func() bool { return s.Clock().Tick() == 2 })
	if err != nil || tick != 2 {
		t.Fatalf("success: tick=%d err=%v", tick, err)
	}
	for range 4 {
		o := <-observations
		if o.Tick == 0 || o.Tick > 2 || o.Time != base.Add(time.Duration(o.Tick)*7*time.Millisecond) {
			t.Fatalf("observation: %+v", o)
		}
	}
	if _, err = s.Register("late"); err == nil {
		t.Fatal("registration remained open after advancement")
	}
	final, err := s.ExpectWithinTicks("never happens", 2, func() bool { return false })
	if err == nil || final != 4 {
		t.Fatalf("failure: tick=%d err=%v", final, err)
	}
	var expectation *HarnessError
	if !errors.As(err, &expectation) || expectation.StartingTick != 2 || expectation.AllowedTicks != 2 || expectation.FinalTick != 4 {
		t.Fatalf("details: %+v", err)
	}
}

func TestBarrierWithheldParticipantAndConcurrentAdvances(t *testing.T) {
	s := testScenario(time.Unix(50, 0).UTC(), time.Millisecond)
	defer s.Close()
	participants := make([]*Participant, 8)
	for i := range participants {
		participants[i] = register(s, "peer-"+string(rune('a'+i)))
	}
	started, events := make(chan struct{}, len(participants)), make(chan Observation, 16)
	hold := make(chan struct{})
	for _, p := range participants {
		p := p
		p.Run(func() { started <- struct{}{}; runTicks(p, events, 2, hold) })
	}
	for range participants {
		<-started
	}
	results := make(chan error, 4)
	advance := func(tick uint64) { _, err := s.AdvanceTo(tick); results <- err }
	go advance(1)
	waitState(t, s, func(s *Scenario) bool { return s.active != nil && s.active.tick == 1 && s.advanceCalls == 1 })
	waitState(t, s, func(s *Scenario) bool {
		return len(s.active.arrived) == len(participants)-1 && !s.active.arrived["peer-h"]
	})
	for range 3 {
		go advance(2)
	}
	waitState(t, s, func(s *Scenario) bool { return s.advanceCalls == 4 })
	select {
	case o := <-events:
		t.Fatalf("participant crossed barrier early: %+v", o)
	case err := <-results:
		t.Fatalf("advance returned early: %v", err)
	default:
	}
	close(hold)
	counts := map[uint64]int{}
	for range 16 {
		counts[(<-events).Tick]++
	}
	if counts[1] != 8 || counts[2] != 8 {
		t.Fatalf("event counts: %v", counts)
	}
	for range 4 {
		<-results
	}
}

func testScenario(b time.Time, d time.Duration) *Scenario { return New(b, d) }
func register(s *Scenario, n string) *Participant         { p, _ := s.Register(n); return p }

func runTicks(p *Participant, out chan<- Observation, ticks int, hold <-chan struct{}) {
	if hold != nil && p.name == "peer-h" {
		<-hold
	}
	for i := 1; i <= ticks; i++ {
		o, _ := p.Observe(uint64(i))
		out <- o
	}
	p.Complete()
}
func waitState(t *testing.T, s *Scenario, ready func(*Scenario) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for !ready(s) && s.failure == nil {
		s.cond.Wait()
	}
	if s.failure != nil {
		t.Fatalf("scenario failed while synchronizing test: %v", s.failure)
	}
}
