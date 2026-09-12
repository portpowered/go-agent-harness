// Package service contains the private provider-progress policy.
package service

import (
	"context"
	"strings"
	"sync"
	"time"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providerliveness"
)

const defaultTimeout = public.DefaultTimeout

type realTimer struct{ timer *time.Timer }

func (t realTimer) C() <-chan time.Time {
	if t.timer == nil {
		return nil
	}
	return t.timer.C
}

func (t realTimer) Stop() bool {
	if t.timer == nil {
		return false
	}
	return t.timer.Stop()
}

type realClock struct{}

func (realClock) NewTimer(duration time.Duration) public.Timer {
	return realTimer{timer: time.NewTimer(duration)}
}

// Service is deliberately private to the providerliveness Wire package.
type Service struct {
	mu sync.Mutex

	clock   public.Clock
	timeout time.Duration
	timer   public.Timer

	generation uint64
	armed      bool
	stopped    bool
	localTools int
	failure    *public.Error

	events  chan struct{}
	control chan struct{}
	stop    chan struct{}
	done    chan struct{}

	onFailure func(error)
}

type timerInstall struct {
	old          public.Timer
	stop         bool
	startWatcher bool
	watcherStop  <-chan struct{}
}

var _ public.Service = (*Service)(nil)

func New(deps public.Dependencies) *Service {
	clock := deps.Clock
	if clock == nil {
		clock = realClock{}
	}
	timeout := deps.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Service{
		clock:     clock,
		timeout:   timeout,
		events:    make(chan struct{}, 1),
		control:   make(chan struct{}, 1),
		onFailure: deps.OnFailure,
	}
}

func (s *Service) ObserveProviderEvent(event public.Event) {
	if s == nil || event.Role == public.EventRoleTool || event.ToolAcknowledgement {
		return
	}
	switch event.Kind {
	case public.EventMessageStart, public.EventAudioStart:
		s.Arm()
	case public.EventProgress, public.EventMessageEnd, public.EventResponseCreate:
		s.Reset()
	default:
		s.Reset()
	}
}

func (s *Service) ObserveProviderDispatch(event public.Event) {
	if s == nil || event.Role == public.EventRoleTool || event.ToolAcknowledgement {
		return
	}
	if event.Kind == public.EventMessageEnd || event.Kind == public.EventResponseCreate {
		s.Arm()
	}
}

func (s *Service) ObserveResponseEnd(end public.ResponseEnd) {
	if s == nil || end.OutputPresent || end.ToolObligation || responseCancellationBoundary(end) {
		return
	}
	if end.TerminalReason != "partial_output" || end.OutputState != "none" || end.Usage.CompletionTokens != 0 {
		return
	}
	s.latch(&public.Error{
		Classification:     public.SilentProviderEmptyResponseClassification,
		ResponseID:         strings.TrimSpace(end.ResponseID),
		TerminalReason:     "terminal_failure",
		TerminalProvenance: "session",
		OutputState:        "none",
		Usage:              end.Usage,
	}, 0, false)
}

func responseCancellationBoundary(end public.ResponseEnd) bool {
	if end.TerminalReason == "cancellation" ||
		(end.TerminalReason == "partial_output" && end.TerminalProvenance == "loop") {
		return true
	}
	status := strings.ToLower(strings.TrimSpace(end.Status))
	return status == "cancelled" || status == "canceled"
}

func (s *Service) Arm() { s.setTimer(false) }

func (s *Service) Reset() { s.setTimer(true) }

func (s *Service) setTimer(onlyIfArmed bool) {
	if s == nil {
		return
	}
	clock, timeout, ok := s.timerRequest(onlyIfArmed)
	if !ok {
		return
	}
	timer := clock.NewTimer(timeout)
	if timer == nil {
		return
	}
	installed := s.installTimer(timer, onlyIfArmed)
	if installed.stop {
		timer.Stop()
		return
	}
	if installed.old != nil {
		installed.old.Stop()
	}
	if installed.startWatcher {
		go s.watch(installed.watcherStop)
	}
}

func (s *Service) timerRequest(onlyIfArmed bool) (public.Clock, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.failure != nil || s.localTools > 0 || (onlyIfArmed && !s.armed) {
		return nil, 0, false
	}
	clock := s.clock
	if clock == nil {
		clock = realClock{}
	}
	return clock, s.timeout, true
}

func (s *Service) installTimer(timer public.Timer, onlyIfArmed bool) timerInstall {
	s.mu.Lock()
	if s.stopped || s.failure != nil || s.localTools > 0 || (onlyIfArmed && !s.armed) {
		s.mu.Unlock()
		return timerInstall{stop: true}
	}
	old := s.timer
	s.timer = timer
	s.armed = true
	s.generation++
	start := s.stop == nil
	if start {
		s.stop = make(chan struct{})
		s.done = make(chan struct{})
	}
	stop := s.stop
	s.signalControlLocked()
	s.mu.Unlock()
	return timerInstall{old: old, startWatcher: start, watcherStop: stop}
}

func (s *Service) watch(stop <-chan struct{}) {
	defer func() {
		s.mu.Lock()
		done := s.done
		s.mu.Unlock()
		if done != nil {
			close(done)
		}
	}()
	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		timerCh := (<-chan time.Time)(nil)
		generation := s.generation
		if s.armed && s.timer != nil {
			timerCh = s.timer.C()
		}
		control := s.control
		s.mu.Unlock()

		select {
		case <-timerCh:
			s.expire(generation)
		case <-control:
		case <-stop:
			return
		}
	}
}

func (s *Service) expire(generation uint64) {
	s.latch(&public.Error{
		Classification:     public.SilentProviderTimeoutClassification,
		TerminalReason:     "terminal_failure",
		TerminalProvenance: "session",
		OutputState:        "none",
	}, generation, true)
}

func (s *Service) latch(failure *public.Error, generation uint64, requireGeneration bool) {
	if s == nil || failure == nil {
		return
	}
	s.mu.Lock()
	if s.stopped || s.failure != nil || (requireGeneration && (!s.armed || s.generation != generation)) {
		s.mu.Unlock()
		return
	}
	s.failure = failure
	s.armed = false
	s.generation++
	timer := s.timer
	s.timer = nil
	onFailure := s.onFailure
	s.signalControlLocked()
	s.mu.Unlock()

	if timer != nil {
		timer.Stop()
	}
	if onFailure != nil {
		onFailure(failure)
	}
	s.mu.Lock()
	s.signalEventLocked()
	s.mu.Unlock()
}

func (s *Service) Disarm() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.armed && s.timer == nil {
		s.mu.Unlock()
		return
	}
	s.armed = false
	s.generation++
	timer := s.timer
	s.timer = nil
	s.signalControlLocked()
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (s *Service) BeginLocalToolExecution() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.localTools++
	s.mu.Unlock()
	s.Disarm()
}

func (s *Service) EndLocalToolExecution() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.localTools > 0 {
		s.localTools--
	}
	s.mu.Unlock()
}

func (s *Service) SetClock(clock public.Clock) {
	if s == nil || clock == nil {
		return
	}
	s.mu.Lock()
	if !s.stopped {
		s.clock = clock
	}
	s.mu.Unlock()
}

func (s *Service) Failure() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		return nil
	}
	return s.failure
}

func (s *Service) Events() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.events
}

func (s *Service) FailureChannel(ctx context.Context) <-chan error {
	if s == nil {
		return nil
	}
	return public.FailureBridge{Events: s.Events(), Failure: s.Failure}.Errors(ctx)
}

func (s *Service) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.armed = false
	s.generation++
	timer := s.timer
	s.timer = nil
	stop := s.stop
	s.stop = nil
	s.signalControlLocked()
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if stop != nil {
		close(stop)
	}
}

func (s *Service) signalControlLocked() {
	select {
	case s.control <- struct{}{}:
	default:
	}
}

func (s *Service) signalEventLocked() {
	select {
	case s.events <- struct{}{}:
	default:
	}
}
