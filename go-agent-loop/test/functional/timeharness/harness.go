package timeharness

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
)

const defaultWatchdogTimeout = 500 * time.Millisecond

type HarnessError struct {
	Kind                                  string
	TargetTick                            uint64
	Missing, Sleeping                     []string
	Expectation                           string
	StartingTick, AllowedTicks, FinalTick uint64
}

func (e *HarnessError) Error() string {
	if e.Kind == "expectation" {
		return fmt.Sprintf("expectation %q was not met within %d logical ticks (starting tick %d; final tick %d)", e.Expectation, e.AllowedTicks, e.StartingTick, e.FinalTick)
	}
	if e.Kind == "forbidden sleep" {
		return fmt.Sprintf("time.Sleep is forbidden in a sequenced scenario: participant(s) %s blocked in time.Sleep at target tick %d; missing participants: %s", names(e.Sleeping), e.TargetTick, names(e.Missing))
	}
	return fmt.Sprintf("timeharness watchdog: target tick %d did not complete; missing participants: %s", e.TargetTick, names(e.Missing))
}

type generation struct {
	tick      uint64
	arrived   map[string]bool
	remaining int
}
type Scenario struct {
	mu           sync.Mutex
	cond         *sync.Cond
	clock        *clock.Deterministic
	base         time.Time
	tickDuration time.Duration
	participants map[string]*Participant
	completed    uint64
	active       *generation
	failure      error
	advanceCalls int
	started      bool
	watchdog     *time.Timer
}
type Participant struct {
	scenario *Scenario
	name     string
	complete bool
	gid      atomic.Uint64
}

func New(base time.Time, tickDuration time.Duration) *Scenario {
	if base.IsZero() {
		base = time.Unix(0, 0).UTC()
	}
	if tickDuration <= 0 {
		tickDuration = time.Millisecond
	}
	s := &Scenario{clock: clock.NewDeterministic(base, tickDuration), base: base, tickDuration: tickDuration, participants: map[string]*Participant{}}
	s.cond = sync.NewCond(&s.mu)
	return s
}
func (s *Scenario) Clock() *clock.Deterministic { return s.clock }

func (s *Scenario) Register(name string) (*Participant, error) {
	name = strings.TrimSpace(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" || s.failure != nil || s.started || s.participants[name] != nil {
		return nil, fmt.Errorf("timeharness participant %q cannot be registered", name)
	}
	p := &Participant{scenario: s, name: name}
	s.participants[name] = p
	return p, nil
}

func (p *Participant) Run(fn func()) { go func() { p.bind(); fn() }() }

func (p *Participant) Complete() { s := p.scenario; s.mu.Lock(); p.complete = true; s.mu.Unlock() }

func (p *Participant) Observe(target uint64) (Observation, error) {
	s := p.scenario
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if err := s.failure; err != nil {
			return Observation{}, err
		}
		if p.complete {
			return Observation{}, fmt.Errorf("timeharness participant %q is complete", p.name)
		}
		if target <= s.completed {
			return Observation{Participant: p.name, Tick: target, Time: s.base.Add(time.Duration(target) * s.tickDuration)}, nil
		}
		g := s.active
		if g == nil || target != g.tick {
			if g != nil && target < g.tick {
				return Observation{}, fmt.Errorf("participant %q requested tick %d after generation %d started", p.name, target, g.tick)
			}
			s.cond.Wait()
			continue
		}
		if !g.arrived[p.name] {
			g.arrived[p.name], g.remaining = true, g.remaining-1
			s.cond.Broadcast()
		}
		if g.remaining == 0 {
			s.finishLocked()
		} else {
			s.cond.Wait()
		}
	}
}

type Observation struct {
	Participant string
	Tick        uint64
	Time        time.Time
}

func (s *Scenario) AdvanceTo(target uint64) (uint64, error) {
	s.mu.Lock()
	s.advanceCalls++
	s.cond.Broadcast()
	defer func() { s.advanceCalls--; s.cond.Broadcast(); s.mu.Unlock() }()
	if err := s.failure; err != nil {
		return s.clock.Tick(), err
	}
	s.started = true
	for s.failure == nil && s.completed < target {
		if s.active == nil {
			s.startLocked(target)
			continue
		}
		s.cond.Wait()
	}
	return s.clock.Tick(), s.failure
}

func (s *Scenario) ExpectWithinTicks(expectation string, allowed uint64, condition func() bool) (uint64, error) {
	start := s.clock.Tick()
	for step := uint64(0); step <= allowed; step++ {
		if condition != nil && condition() {
			return start + step, nil
		}
		if step == allowed {
			break
		}
		if _, err := s.AdvanceTo(start + step + 1); err != nil {
			return s.clock.Tick(), err
		}
	}
	final := s.clock.Tick()
	return final, &HarnessError{Kind: "expectation", Expectation: expectation, StartingTick: start, AllowedTicks: allowed, FinalTick: final}
}

func (s *Scenario) Close() {
	s.mu.Lock()
	if s.failure == nil {
		s.failure = fmt.Errorf("timeharness scenario is closed")
	}
	if s.watchdog != nil {
		s.watchdog.Stop()
		s.watchdog = nil
	}
	s.cond.Broadcast()
	s.mu.Unlock()
}
func (s *Scenario) startLocked(target uint64) {
	s.clock.AdvanceTo(target)
	active := 0
	for _, p := range s.participants {
		if !p.complete {
			active++
		}
	}
	s.active = &generation{tick: target, arrived: map[string]bool{}, remaining: active}
	if s.active.remaining == 0 {
		s.finishLocked()
	} else {
		s.watchdog = time.AfterFunc(defaultWatchdogTimeout, func() { s.checkWatchdog(target) })
	}
	s.cond.Broadcast()
}
func (s *Scenario) finishLocked() {
	if s.watchdog != nil {
		s.watchdog.Stop()
		s.watchdog = nil
	}
	s.completed, s.active = s.active.tick, nil
	s.cond.Broadcast()
}
func (s *Scenario) checkWatchdog(target uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil || s.active == nil || s.active.tick != target {
		return
	}
	ids, sleeping := sleepingGoroutines(), []string{}
	for name, p := range s.participants {
		if id := p.gid.Load(); !p.complete && id != 0 && ids[id] {
			sleeping = append(sleeping, name)
		}
	}
	if len(sleeping) > 0 {
		s.failure = &HarnessError{Kind: "forbidden sleep", TargetTick: target, Sleeping: sleeping}
	} else {
		missing := []string{}
		for name, p := range s.participants {
			if !p.complete && !s.active.arrived[name] {
				missing = append(missing, name)
			}
		}
		s.failure = &HarnessError{Kind: "stuck participant", TargetTick: target, Missing: missing}
	}
	s.cond.Broadcast()
}
func (p *Participant) bind() {
	var stack [64]byte
	var id uint64
	fmt.Sscanf(string(stack[:runtime.Stack(stack[:], false)]), "goroutine %d ", &id)
	p.gid.CompareAndSwap(0, id)
}
func sleepingGoroutines() map[uint64]bool {
	buffer := make([]byte, 128*1024)
	n := runtime.Stack(buffer, true)
	result := map[uint64]bool{}
	for _, block := range strings.Split(string(buffer[:n]), "\n\n") {
		if !strings.Contains(block, "[sleep]") || !strings.Contains(block, "time.Sleep(") {
			continue
		}
		var id uint64
		fmt.Sscanf(block, "goroutine %d ", &id)
		result[id] = true
	}
	return result
}
func names(values []string) string {
	if len(values) == 0 {
		return "<none>"
	}
	return strings.Join(values, ", ")
}
