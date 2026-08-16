// Package timeharness coordinates functional tests on deterministic logical ticks.
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

// FailureKind identifies a terminal diagnostic emitted by a scenario.
type FailureKind string

const (
	FailureStuck       FailureKind = "stuck participant"
	FailureSleep       FailureKind = "forbidden sleep"
	FailureExpectation FailureKind = "expectation"
)

// HarnessError preserves the logical details that caused a scenario to fail.
type HarnessError struct {
	Kind         FailureKind
	TargetTick   uint64
	Missing      []string
	Sleeping     []string
	Expectation  string
	StartingTick uint64
	AllowedTicks uint64
	FinalTick    uint64
}

func (e *HarnessError) Error() string {
	if e.Kind == FailureExpectation {
		return fmt.Sprintf("expectation %q was not met within %d logical ticks (starting tick %d; final tick %d)", e.Expectation, e.AllowedTicks, e.StartingTick, e.FinalTick)
	}
	if e.Kind == FailureSleep {
		return fmt.Sprintf("time.Sleep is forbidden in a sequenced scenario: participant(s) %s blocked in time.Sleep at target tick %d; missing participants: %s", names(e.Sleeping), e.TargetTick, names(e.Missing))
	}
	return fmt.Sprintf("timeharness watchdog: target tick %d did not complete; missing participants: %s", e.TargetTick, names(e.Missing))
}

// Observation is the stable result returned after a participant crosses a tick.
type Observation struct {
	Participant string
	Tick        uint64
	Time        time.Time
}

// Option configures diagnostic behavior. Watchdog durations never participate
// in scenario ordering or latency assertions.
type Option func(*Scenario)

// WithWatchdogTimeout sets the wall-clock deadlock diagnostic limit.
func WithWatchdogTimeout(limit time.Duration) Option {
	return func(s *Scenario) {
		if limit > 0 {
			s.watchdogTimeout = limit
		}
	}
}

type generation struct {
	tick      uint64
	arrived   map[string]bool
	startedAt time.Time
}

// Scenario owns one deterministic clock and one fixed participant set.
type Scenario struct {
	mu               sync.Mutex
	cond             *sync.Cond
	clock            *clock.Deterministic
	base             time.Time
	tickDuration     time.Duration
	participants     map[string]*Participant
	order            []string
	started          bool
	completedTick    uint64
	active           *generation
	failure          error
	closed           bool
	watchdogTimeout  time.Duration
	watchdogInterval time.Duration
	watchdogDone     chan struct{}
}

// Participant is a named member of a Scenario. Registration is explicit and
// must happen before the first call to AdvanceTo.
type Participant struct {
	scenario *Scenario
	name     string
	lastTick uint64
	complete bool
	gid      atomic.Uint64
}

// New creates a scenario without coupling it to testing.T. Call Close when
// the scenario is no longer needed.
func New(base time.Time, tickDuration time.Duration, options ...Option) *Scenario {
	if base.IsZero() {
		base = time.Unix(0, 0).UTC()
	}
	if tickDuration <= 0 {
		tickDuration = time.Millisecond
	}
	s := &Scenario{
		clock:            clock.NewDeterministic(base, tickDuration),
		base:             base,
		tickDuration:     tickDuration,
		participants:     make(map[string]*Participant),
		watchdogTimeout:  defaultWatchdogTimeout,
		watchdogInterval: defaultWatchdogInterval,
	}
	s.cond = sync.NewCond(&s.mu)
	for _, option := range options {
		option(s)
	}
	return s
}

// NewScenario creates a scenario and arranges deterministic cleanup with t.
func NewScenario(t testing.TB, base time.Time, tickDuration time.Duration, options ...Option) *Scenario {
	s := New(base, tickDuration, options...)
	if t != nil {
		t.Helper()
		t.Cleanup(s.Close)
	}
	return s
}

// Clock returns the one platform deterministic clock shared by the scenario.
func (s *Scenario) Clock() *clock.Deterministic { return s.clock }

// CurrentTick returns the current logical tick.
func (s *Scenario) CurrentTick() uint64 { return s.clock.Tick() }

// TimeAt returns the timestamp defined by the scenario's base and tick lattice.
func (s *Scenario) TimeAt(tick uint64) time.Time {
	if tick == 0 {
		return s.base
	}
	const maxDuration = time.Duration(1<<63 - 1)
	if tick > uint64(maxDuration)/uint64(s.tickDuration) {
		return s.base.Add(maxDuration)
	}
	return s.base.Add(time.Duration(tick) * s.tickDuration)
}

// Register adds one uniquely named participant before logical advancement.
func (s *Scenario) Register(name string) (*Participant, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("timeharness participant name cannot be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil, fmt.Errorf("timeharness participant %q registered after the first advance", name)
	}
	if _, exists := s.participants[name]; exists {
		return nil, fmt.Errorf("timeharness participant %q is already registered", name)
	}
	p := &Participant{scenario: s, name: name}
	s.participants[name] = p
	s.order = append(s.order, name)
	return p, nil
}

// Name returns the stable participant name.
func (p *Participant) Name() string { return p.name }

// LastTick returns the last generation acknowledged by the participant.
func (p *Participant) LastTick() uint64 {
	s := p.scenario
	s.mu.Lock()
	defer s.mu.Unlock()
	return p.lastTick
}

// Run starts fn on the participant's managed goroutine. Completion remains
// explicit: fn should call Complete after its final observation.
func (p *Participant) Run(fn func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		p.bind()
		defer close(done)
		if fn != nil {
			fn()
		}
	}()
	return done
}

// Complete explicitly removes the participant from future barrier membership.
func (p *Participant) Complete() {
	s := p.scenario
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.complete {
		return
	}
	p.complete = true
	if s.active != nil && s.allArrivedLocked() {
		s.finishLocked()
	}
	s.cond.Broadcast()
}

// Observe acknowledges target and waits until every active peer acknowledges
// the same generation. The returned timestamp is derived from target, not
// from a later generation that may be coalesced concurrently.
func (p *Participant) Observe(target uint64) (Observation, error) {
	p.bind()
	s := p.scenario
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.failure != nil {
			return Observation{}, s.failure
		}
		if s.closed {
			return Observation{}, fmt.Errorf("timeharness scenario is closed")
		}
		if p.complete {
			return Observation{}, fmt.Errorf("timeharness participant %q is complete", p.name)
		}
		if target < p.lastTick {
			return Observation{}, fmt.Errorf("participant %q observed tick %d after tick %d", p.name, target, p.lastTick)
		}
		if target <= s.completedTick {
			p.lastTick = target
			return Observation{Participant: p.name, Tick: target, Time: s.TimeAt(target)}, nil
		}
		if s.active == nil {
			s.cond.Wait()
			continue
		}
		if target < s.active.tick {
			return Observation{}, fmt.Errorf("participant %q requested tick %d, but generation %d is already active", p.name, target, s.active.tick)
		}
		if target > s.active.tick {
			s.cond.Wait()
			continue
		}
		s.active.arrived[p.name] = true
		if s.allArrivedLocked() {
			s.finishLocked()
		}
		if s.failure != nil {
			return Observation{}, s.failure
		}
		if s.completedTick >= target {
			p.lastTick = target
			return Observation{Participant: p.name, Tick: target, Time: s.TimeAt(target)}, nil
		}
		s.cond.Wait()
	}
}

// AdvanceTo moves logical time forward and waits for the corresponding
// generation barrier. Concurrent later targets serialize behind an incomplete
// active generation.
func (s *Scenario) AdvanceTo(target uint64) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return s.clock.Tick(), s.failure
	}
	if s.closed {
		return s.clock.Tick(), fmt.Errorf("timeharness scenario is closed")
	}
	if !s.started {
		s.started = true
		s.startWatchdogLocked()
	}
	for s.failure == nil && s.completedTick < target {
		if s.active == nil && target > s.clock.Tick() {
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

// ExpectWithinTicks checks condition at the current tick, then advances at
// most allowed logical ticks. It returns the exact successful tick.
func (s *Scenario) ExpectWithinTicks(expectation string, allowed uint64, condition func() bool) (uint64, error) {
	start := s.CurrentTick()
	for step := uint64(0); ; step++ {
		if condition != nil && condition() {
			return start + step, nil
		}
		if step == allowed {
			break
		}
		target := start + step + 1
		if target < start {
			target = ^uint64(0)
		}
		if _, err := s.AdvanceTo(target); err != nil {
			return s.CurrentTick(), err
		}
	}
	final := s.CurrentTick()
	return final, &HarnessError{Kind: FailureExpectation, Expectation: expectation, StartingTick: start, AllowedTicks: allowed, FinalTick: final}
}

// Close stops diagnostic work and wakes blocked participants.
func (s *Scenario) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cond.Broadcast()
	}
	done := s.watchdogDone
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (s *Scenario) startWatchdogLocked() {
	s.watchdogDone = make(chan struct{})
	go s.watchdog(s.watchdogDone)
}

func (s *Scenario) startLocked(target uint64) {
	s.clock.AdvanceTo(target)
	s.active = &generation{tick: target, arrived: make(map[string]bool), startedAt: time.Now()}
	if s.allArrivedLocked() {
		s.finishLocked()
	}
	s.cond.Broadcast()
}

func (s *Scenario) finishLocked() {
	if s.active == nil || !s.allArrivedLocked() {
		return
	}
	finished := s.active
	for name := range finished.arrived {
		s.participants[name].lastTick = finished.tick
	}
	s.completedTick = finished.tick
	s.active = nil
	s.cond.Broadcast()
}

func (s *Scenario) allArrivedLocked() bool {
	if s.active == nil {
		return false
	}
	for _, name := range s.order {
		if !s.participants[name].complete && !s.active.arrived[name] {
			return false
		}
	}
	return true
}

func (s *Scenario) missingLocked() []string {
	if s.active == nil {
		return nil
	}
	missing := make([]string, 0)
	for _, name := range s.order {
		if !s.participants[name].complete && !s.active.arrived[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

func (s *Scenario) watchdog(done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(s.watchdogInterval)
	defer ticker.Stop()
	for {
		<-ticker.C
		s.mu.Lock()
		if s.closed || s.failure != nil {
			s.mu.Unlock()
			return
		}
		sleeping := s.sleepingLocked()
		if len(sleeping) > 0 {
			target := s.clock.Tick()
			s.failure = &HarnessError{Kind: FailureSleep, TargetTick: target, Missing: s.missingLocked(), Sleeping: sleeping}
			s.cond.Broadcast()
			s.mu.Unlock()
			return
		}
		if s.active != nil && time.Since(s.active.startedAt) >= s.watchdogTimeout {
			s.failure = &HarnessError{Kind: FailureStuck, TargetTick: s.active.tick, Missing: s.missingLocked()}
			s.cond.Broadcast()
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

func (s *Scenario) sleepingLocked() []string {
	ids := sleepingGoroutines()
	var result []string
	for _, name := range s.order {
		p := s.participants[name]
		if !p.complete && p.gid.Load() != 0 && ids[p.gid.Load()] {
			result = append(result, name)
		}
	}
	return result
}

func (p *Participant) bind() {
	if p.gid.Load() != 0 {
		return
	}
	var stack [64]byte
	n := runtime.Stack(stack[:], false)
	var id uint64
	_, _ = fmt.Sscanf(string(stack[:n]), "goroutine %d ", &id)
	if id != 0 {
		p.gid.CompareAndSwap(0, id)
	}
}

func sleepingGoroutines() map[uint64]bool {
	buffer := make([]byte, 128*1024)
	n := runtime.Stack(buffer, true)
	result := make(map[uint64]bool)
	for _, block := range strings.Split(string(buffer[:n]), "\n\n") {
		lines := strings.SplitN(block, "\n", 2)
		if len(lines) != 2 || !strings.Contains(lines[0], "[sleep]") || !strings.Contains(lines[1], "time.Sleep(") {
			continue
		}
		var id uint64
		if _, err := fmt.Sscanf(lines[0], "goroutine %d ", &id); err == nil && id != 0 {
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
