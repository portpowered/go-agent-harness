package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/providerliveness"
)

type testTimer struct {
	mu     sync.Mutex
	ch     chan time.Time
	active bool
}

func newTestTimer() *testTimer           { return &testTimer{ch: make(chan time.Time, 1), active: true} }
func (t *testTimer) C() <-chan time.Time { return t.ch }
func (t *testTimer) Stop() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}
func (t *testTimer) Fire() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	active := t.active
	t.active = false
	t.mu.Unlock()
	if !active {
		return false
	}
	t.ch <- time.Time{}
	return true
}
func (t *testTimer) FireLate() { t.ch <- time.Time{} }
func (t *testTimer) Active() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active
}

type testClock struct {
	mu     sync.Mutex
	timers []*testTimer
	nil    bool
}

func (c *testClock) NewTimer(time.Duration) public.Timer {
	if c == nil || c.nil {
		return nil
	}
	timer := newTestTimer()
	c.mu.Lock()
	c.timers = append(c.timers, timer)
	c.mu.Unlock()
	return timer
}
func (c *testClock) Latest() *testTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.timers) - 1; i >= 0; i-- {
		if c.timers[i].Active() {
			return c.timers[i]
		}
	}
	return nil
}
func (c *testClock) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

func waitFailure(t *testing.T, s public.Service) error {
	t.Helper()
	select {
	case <-s.Events():
		return s.Failure()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for liveness failure")
		return nil
	}
}

func emptyEnd(responseID string) public.ResponseEnd {
	return public.ResponseEnd{
		Event:              public.Event{Kind: public.EventMessageEnd, ResponseID: responseID},
		TerminalReason:     "partial_output",
		TerminalProvenance: "provider",
		OutputState:        "none",
		Usage:              public.TokenUsage{PromptTokens: 11, CompletionTokens: 0, TotalTokens: 11, ReasoningTokens: 3},
	}
}

func TestServiceEmptyResponsePreservesTypedFacts(t *testing.T) {
	clock := &testClock{}
	service := New(public.Dependencies{Clock: clock})
	defer service.Stop()
	service.ObserveResponseEnd(emptyEnd("response-17"))
	err := waitFailure(t, service)
	if !errors.Is(err, public.ErrSilentProviderEmptyResponse) {
		t.Fatalf("error = %v, want empty-response sentinel", err)
	}
	var typed *public.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error = %T, want typed provider error", err)
	}
	if typed.ResponseID != "response-17" || typed.Usage.PromptTokens != 11 || typed.Usage.ReasoningTokens != 3 {
		t.Fatalf("facts = %#v, want response identity and usage preserved", typed)
	}
}

func TestServiceEmptyResponseExclusions(t *testing.T) {
	cases := []struct {
		name string
		edit func(*public.ResponseEnd)
	}{
		{"output-present", func(end *public.ResponseEnd) { end.OutputPresent = true }},
		{"tool-obligation", func(end *public.ResponseEnd) { end.ToolObligation = true }},
		{"cancel-reason", func(end *public.ResponseEnd) { end.TerminalReason = "cancellation" }},
		{"loop-cancel", func(end *public.ResponseEnd) { end.TerminalProvenance = "loop" }},
		{"cancel-status", func(end *public.ResponseEnd) { end.Status = "cancelled" }},
		{"usage-present", func(end *public.ResponseEnd) { end.Usage.CompletionTokens = 1 }},
		{"non-partial", func(end *public.ResponseEnd) { end.TerminalReason = "completed" }},
		{"output-state", func(end *public.ResponseEnd) { end.OutputState = "text" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service := New(public.Dependencies{Clock: &testClock{}})
			defer service.Stop()
			end := emptyEnd(test.name)
			test.edit(&end)
			service.ObserveResponseEnd(end)
			select {
			case <-service.Events():
				t.Fatal("excluded response published a liveness wake")
			case <-time.After(20 * time.Millisecond):
			}
			if err := service.Failure(); err != nil {
				t.Fatalf("excluded response produced %v", err)
			}
		})
	}
}

func TestServiceArmingResetAndStaleGeneration(t *testing.T) {
	clock := &testClock{}
	service := New(public.Dependencies{Clock: clock})
	defer service.Stop()
	service.ObserveProviderDispatch(public.Event{Kind: public.EventResponseCreate})
	first := clock.Latest()
	if first == nil || clock.Count() != 1 {
		t.Fatalf("initial timers = %d, want one", clock.Count())
	}
	service.mu.Lock()
	firstGeneration := service.generation
	service.mu.Unlock()
	service.ObserveProviderEvent(public.Event{Kind: public.EventProgress})
	second := clock.Latest()
	if second == nil || second == first || clock.Count() != 2 {
		t.Fatalf("reset timers = %d, want replacement", clock.Count())
	}
	service.expire(firstGeneration)
	select {
	case <-service.Events():
		t.Fatal("obsolete timer generation published a failure")
	case <-time.After(20 * time.Millisecond):
	}
	first.FireLate()
	select {
	case <-service.Events():
		t.Fatal("obsolete timer generation published a failure")
	case <-time.After(20 * time.Millisecond):
	}
	if !second.Fire() {
		t.Fatal("current timer was not active")
	}
	if err := waitFailure(t, service); !errors.Is(err, public.ErrSilentProviderTimeout) {
		t.Fatalf("error = %v, want timeout", err)
	}
}

func TestServiceArmsExpectedProviderBoundaries(t *testing.T) {
	for _, kind := range []public.EventKind{public.EventMessageStart, public.EventAudioStart} {
		t.Run(string(kind), func(t *testing.T) {
			clock := &testClock{}
			service := New(public.Dependencies{Clock: clock})
			defer service.Stop()
			service.ObserveProviderEvent(public.Event{Kind: kind})
			if clock.Count() != 1 || clock.Latest() == nil {
				t.Fatalf("timer count = %d, want one", clock.Count())
			}
		})
	}
	clock := &testClock{}
	service := New(public.Dependencies{Clock: clock})
	defer service.Stop()
	service.ObserveProviderEvent(public.Event{Kind: public.EventProgress})
	if clock.Count() != 0 {
		t.Fatal("unarmed progress created a watchdog")
	}
}

func TestServiceSuppressesLocalToolAndRetainsFirstCause(t *testing.T) {
	clock := &testClock{}
	service := New(public.Dependencies{Clock: clock})
	defer service.Stop()
	service.Arm()
	service.BeginLocalToolExecution()
	if timer := clock.Latest(); timer != nil {
		t.Fatal("local tool left watchdog armed")
	}
	service.EndLocalToolExecution()
	if clock.Latest() != nil {
		t.Fatal("ending local tool reopened watchdog without provider obligation")
	}
	service.ObserveProviderDispatch(public.Event{Kind: public.EventResponseCreate})
	current := clock.Latest()
	if current == nil || !current.Fire() {
		t.Fatal("continuation watchdog did not arm")
	}
	timeout := waitFailure(t, service)
	if !errors.Is(timeout, public.ErrSilentProviderTimeout) {
		t.Fatalf("first error = %v, want timeout", timeout)
	}
	service.ObserveResponseEnd(emptyEnd("late-empty"))
	if got := service.Failure(); !errors.Is(got, public.ErrSilentProviderTimeout) {
		t.Fatalf("first cause changed to %v", got)
	}
}

func TestServiceFirstCauseEmptyWinsAndNilClockIsSafe(t *testing.T) {
	service := New(public.Dependencies{Clock: &testClock{nil: true}})
	defer service.Stop()
	service.Arm()
	service.ObserveResponseEnd(emptyEnd("empty-first"))
	if err := waitFailure(t, service); !errors.Is(err, public.ErrSilentProviderEmptyResponse) {
		t.Fatalf("error = %v, want empty response", err)
	}

	quiet := New(public.Dependencies{Clock: &testClock{nil: true}})
	defer quiet.Stop()
	quiet.ObserveProviderDispatch(public.Event{Kind: public.EventResponseCreate})
	if err := quiet.Failure(); err != nil {
		t.Fatalf("nil timer seam produced %v", err)
	}
}

func TestServiceStopIsIdempotentAndWatcherExits(t *testing.T) {
	clock := &testClock{}
	service := New(public.Dependencies{Clock: clock})
	service.Arm()
	service.mu.Lock()
	done := service.done
	service.mu.Unlock()
	service.Stop()
	service.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher survived Stop")
	}
}

func TestFailureChannelHonorsParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan struct{})
	called := false
	out := public.FailureBridge{Events: events, Failure: func() error { called = true; return errors.New("unexpected") }}.Errors(ctx)
	cancel()
	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("cancellation emitted an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failure bridge survived cancellation")
	}
	if called {
		t.Fatal("failure callback ran after parent cancellation")
	}
}
