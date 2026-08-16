package timeharness

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/platform/clock"
)

const (
	defaultWatchdogTimeout  = 500 * time.Millisecond
	defaultWatchdogInterval = 5 * time.Millisecond
)

type FailureKind string

const (
	FailureStuck       FailureKind = "stuck participant"
	FailureSleep       FailureKind = "forbidden sleep"
	FailureExpectation FailureKind = "expectation"
)

type HarnessError struct {
	Kind                                  FailureKind
	TargetTick                            uint64
	Missing, Sleeping                     []string
	Expectation                           string
	StartingTick, AllowedTicks, FinalTick uint64
}

func (e *HarnessError) Error() string {
	switch e.Kind {
	case FailureExpectation:
		return fmt.Sprintf("expectation %q was not met within %d logical ticks (starting tick %d; final tick %d)", e.Expectation, e.AllowedTicks, e.StartingTick, e.FinalTick)
	case FailureSleep:
		return fmt.Sprintf("time.Sleep is forbidden in a sequenced scenario: participant(s) %s blocked in time.Sleep at target tick %d; missing participants: %s", names(e.Sleeping), e.TargetTick, names(e.Missing))
	default:
		return fmt.Sprintf("timeharness watchdog: target tick %d did not complete; missing participants: %s", e.TargetTick, names(e.Missing))
	}
}

type generation struct {
	tick      uint64
	arrived   map[string]bool
	remaining int
	started   time.Time
}
type Scenario struct {
	mu                 sync.Mutex
	cond               *sync.Cond
	clock              *clock.Deterministic
	base               time.Time
	tickDuration       time.Duration
	participants       map[string]*Participant
	activeParticipants int
	closed             bool
	completed          uint64
	active             *generation
	failure            error
	advanceCalls       int
	watchdogDone       chan struct{}
}
type Participant struct {
	scenario *Scenario
	name     string
	lastTick uint64
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
	s := &Scenario{
		clock: clock.NewDeterministic(base, tickDuration), base: base, tickDuration: tickDuration,
		participants: make(map[string]*Participant),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}
func NewScenario(t testing.TB, base time.Time, tickDuration time.Duration) *Scenario {
	s := New(base, tickDuration)
	if t != nil {
		t.Helper()
		t.Cleanup(s.Close)
	}
	return s
}
func (s *Scenario) Clock() *clock.Deterministic { return s.clock }
func (s *Scenario) CurrentTick() uint64         { return s.clock.Tick() }
func (s *Scenario) Register(name string) (*Participant, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("timeharness participant name cannot be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watchdogDone != nil {
		return nil, fmt.Errorf("timeharness participant %q registered after the first advance", name)
	}
	if _, ok := s.participants[name]; ok {
		return nil, fmt.Errorf("timeharness participant %q is already registered", name)
	}
	p := &Participant{scenario: s, name: name}
	s.participants[name] = p
	s.activeParticipants++
	return p, nil
}
func (p *Participant) Name() string { return p.name }
func (p *Participant) LastTick() uint64 {
	s := p.scenario
	s.mu.Lock()
	defer s.mu.Unlock()
	return p.lastTick
}
func (p *Participant) Run(fn func()) {
	go func() {
		p.bind()
		fn()
	}()
}
func (p *Participant) Complete() {
	s := p.scenario
	s.mu.Lock()
	if !p.complete {
		p.complete = true
		s.activeParticipants--
		if s.active != nil && !s.active.arrived[p.name] {
			s.active.remaining--
		}
		if s.active != nil && s.active.remaining == 0 {
			s.finishLocked()
		}
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}
func (p *Participant) Observe(target uint64) (Observation, error) {
	p.bind()
	s := p.scenario
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.failure == nil && !s.closed {
		if p.complete {
			return Observation{}, fmt.Errorf("timeharness participant %q is complete", p.name)
		}
		if target < p.lastTick {
			return Observation{}, fmt.Errorf("participant %q observed tick %d after tick %d", p.name, target, p.lastTick)
		}
		if target <= s.completed {
			return p.observation(target), nil
		}
		if s.active == nil {
			s.cond.Wait()
			continue
		}
		if target != s.active.tick {
			if target < s.active.tick {
				return Observation{}, fmt.Errorf("participant %q requested tick %d after generation %d started", p.name, target, s.active.tick)
			}
			s.cond.Wait()
			continue
		}
		if !s.active.arrived[p.name] {
			s.active.arrived[p.name], s.active.remaining = true, s.active.remaining-1
		}
		s.cond.Broadcast()
		if s.active.remaining == 0 {
			s.finishLocked()
			continue
		}
		s.cond.Wait()
	}
	if s.failure != nil {
		return Observation{}, s.failure
	}
	return Observation{}, fmt.Errorf("timeharness scenario is closed")
}
func (p *Participant) observation(target uint64) Observation {
	p.lastTick = target
	return Observation{p.name, target, p.scenario.base.Add(time.Duration(target) * p.scenario.tickDuration)}
}

type Observation struct {
	Participant string
	Tick        uint64
	Time        time.Time
}

func (s *Scenario) AdvanceTo(target uint64) (uint64, error) {
	s.mu.Lock()
	s.advanceCalls++
	defer func() { s.advanceCalls--; s.cond.Broadcast(); s.mu.Unlock() }()
	if s.failure != nil {
		return s.clock.Tick(), s.failure
	}
	if s.closed {
		return s.clock.Tick(), fmt.Errorf("timeharness scenario is closed")
	}
	if s.watchdogDone == nil {
		s.watchdogDone = make(chan struct{})
		go s.watchdog(s.watchdogDone)
	}
	for s.failure == nil && s.completed < target {
		if s.active == nil {
			s.startLocked(target)
			continue
		}
		s.cond.Wait()
	}
	if s.failure != nil {
		return s.clock.Tick(), s.failure
	}
	return s.clock.Tick(), nil
}
func (s *Scenario) ExpectWithinTicks(expectation string, allowed uint64, condition func() bool) (uint64, error) {
	start := s.CurrentTick()
	for step := uint64(0); ; step++ {
		if condition != nil && condition() {
			return start + step, nil
		}
		if step == allowed {
			break
		}
		if _, err := s.AdvanceTo(start + step + 1); err != nil {
			return s.CurrentTick(), err
		}
	}
	final := s.CurrentTick()
	return final, &HarnessError{Kind: FailureExpectation, Expectation: expectation, StartingTick: start, AllowedTicks: allowed, FinalTick: final}
}
func (s *Scenario) Close() {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	done := s.watchdogDone
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}
func (s *Scenario) startLocked(target uint64) {
	s.clock.AdvanceTo(target)
	s.active = &generation{tick: target, arrived: make(map[string]bool), remaining: s.activeParticipants, started: time.Now()}
	if s.active.remaining == 0 {
		s.finishLocked()
	}
	s.cond.Broadcast()
}
func (s *Scenario) finishLocked() {
	for name := range s.active.arrived {
		s.participants[name].lastTick = s.active.tick
	}
	s.completed, s.active = s.active.tick, nil
	s.cond.Broadcast()
}
func (s *Scenario) missingLocked() []string {
	if s.active == nil {
		return nil
	}
	missing := make([]string, 0)
	for name, p := range s.participants {
		if !p.complete && !s.active.arrived[name] {
			missing = append(missing, name)
		}
	}
	return missing
}
func (s *Scenario) watchdog(done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(defaultWatchdogInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		if s.closed || s.failure != nil {
			s.mu.Unlock()
			return
		}
		var failure *HarnessError
		if sleeping := s.sleepingLocked(); len(sleeping) > 0 {
			failure = &HarnessError{Kind: FailureSleep, TargetTick: s.clock.Tick(), Missing: s.missingLocked(), Sleeping: sleeping}
		} else if s.active != nil && time.Since(s.active.started) >= defaultWatchdogTimeout {
			failure = &HarnessError{Kind: FailureStuck, TargetTick: s.active.tick, Missing: s.missingLocked()}
		}
		if failure != nil {
			s.failure = failure
			s.cond.Broadcast()
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}
func (s *Scenario) sleepingLocked() []string {
	ids, result := sleepingGoroutines(), []string{}
	for name, p := range s.participants {
		if !p.complete && p.gid.Load() != 0 && ids[p.gid.Load()] {
			result = append(result, name)
		}
	}
	return result
}
func (p *Participant) bind() {
	var stack [64]byte
	n := runtime.Stack(stack[:], false)
	var id uint64
	_, _ = fmt.Sscanf(string(stack[:n]), "goroutine %d ", &id)
	if p.gid.Load() == 0 && id != 0 {
		p.gid.Store(id)
	}
}
func sleepingGoroutines() map[uint64]bool {
	buffer := make([]byte, 128*1024)
	n := runtime.Stack(buffer, true)
	result := make(map[uint64]bool)
	for _, block := range strings.Split(string(buffer[:n]), "\n\n") {
		if !strings.Contains(block, "[sleep]") || !strings.Contains(block, "time.Sleep(") {
			continue
		}
		var id uint64
		_, _ = fmt.Sscanf(block, "goroutine %d ", &id)
		if id != 0 {
			result[id] = true
		}
	}
	return result
}
func names(values []string) string {
	if len(values) == 0 {
		return "<none>"
	}
	return strings.Join(values, ", ")
}
