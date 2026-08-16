package timeharness

import (
	"errors"
	"testing"
	"time"
)

func TestScenarioLifecycleExactTicksAndExpectations(t *testing.T) {
	base := time.Date(2026, time.August, 16, 10, 11, 12, 13, time.UTC)
	s := NewScenario(t, base, 7*time.Millisecond)
	if s.CurrentTick() != 0 || s.Clock() == nil {
		t.Fatal("scenario did not start at tick zero")
	}
	left, right := register(t, s, "left"), register(t, s, "right")
	if _, err := s.Register(" "); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := s.Register("left"); err == nil {
		t.Fatal("duplicate name accepted")
	}
	observations := make(chan Observation, 4)
	for _, p := range []*Participant{left, right} {
		p := p
		p.Run(func() { runTicks(p, nil, observations, observations) })
	}
	tick, err := s.ExpectWithinTicks("both peers reached tick two", 3, func() bool { return left.LastTick() == 2 })
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
	s := NewScenario(t, time.Unix(50, 0).UTC(), time.Millisecond)
	participants := make([]*Participant, 8)
	for i := range participants {
		participants[i] = register(t, s, "peer-"+string(rune('a'+i)))
	}
	started, events := make(chan struct{}, 8), make(chan Observation, 16)
	hold := make(chan struct{})
	for _, p := range participants {
		p := p
		p.Run(func() {
			started <- struct{}{}
			var wait <-chan struct{}
			if p.Name() == "peer-h" {
				wait = hold
			}
			runTicks(p, wait, events, events)
		})
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
	default:
	}
	select {
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
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}
func register(t *testing.T, s *Scenario, name string) *Participant {
	t.Helper()
	p, err := s.Register(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func runTicks(p *Participant, wait <-chan struct{}, out ...chan<- Observation) {
	if wait != nil {
		<-wait
	}
	for i, ch := range out {
		o, err := p.Observe(uint64(i + 1))
		if err != nil {
			panic(err)
		}
		ch <- o
	}
	p.Complete()
}
func waitState(t *testing.T, s *Scenario, ready func(*Scenario) bool) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for !ready(s) && s.failure == nil {
		s.cond.Wait()
	}
	if s.failure != nil {
		t.Fatalf("scenario failed while synchronizing test: %v", s.failure)
	}
}
