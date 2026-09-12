package consumer

import (
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providerliveness"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providerliveness/wire"
)

type timer struct {
	ch     chan time.Time
	active bool
}

func newTimer() *timer               { return &timer{ch: make(chan time.Time, 1), active: true} }
func (t *timer) C() <-chan time.Time { return t.ch }
func (t *timer) Stop() bool          { active := t.active; t.active = false; return active }
func (t *timer) fire() bool {
	if !t.active {
		return false
	}
	t.active = false
	t.ch <- time.Time{}
	return true
}

type clock struct{ timers []*timer }

func (c *clock) NewTimer(time.Duration) providerliveness.Timer {
	timer := newTimer()
	c.timers = append(c.timers, timer)
	return timer
}
func (c *clock) latest() *timer {
	for i := len(c.timers) - 1; i >= 0; i-- {
		if c.timers[i].active {
			return c.timers[i]
		}
	}
	return nil
}

func TestExternalConsumerUsesOnlyPublicProviderLiveness(t *testing.T) {
	serviceClock := &clock{}
	service := wire.NewService(providerliveness.Dependencies{Clock: serviceClock})
	defer service.Stop()
	service.ObserveResponseEnd(providerliveness.ResponseEnd{
		Event:              providerliveness.Event{Kind: providerliveness.EventMessageEnd, ResponseID: "external-response"},
		TerminalReason:     "partial_output",
		TerminalProvenance: "provider",
		OutputState:        "none",
		Usage:              providerliveness.TokenUsage{PromptTokens: 5, TotalTokens: 5},
	})
	select {
	case <-service.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("external empty response did not publish")
	}
	var typed *providerliveness.Error
	if !errors.Is(service.Failure(), providerliveness.ErrSilentProviderEmptyResponse) || !errors.As(service.Failure(), &typed) {
		t.Fatalf("failure = %v", service.Failure())
	}
	if typed.ResponseID != "external-response" || typed.Usage.PromptTokens != 5 {
		t.Fatalf("facts = %#v", typed)
	}

	cancelled := wire.NewService(providerliveness.Dependencies{Clock: &clock{}})
	defer cancelled.Stop()
	cancelled.ObserveResponseEnd(providerliveness.ResponseEnd{Event: providerliveness.Event{Kind: providerliveness.EventMessageEnd}, TerminalReason: "cancellation", OutputState: "none"})
	if cancelled.Failure() != nil {
		t.Fatalf("cancelled empty response classified as %v", cancelled.Failure())
	}

	timeoutClock := &clock{}
	timed := wire.NewService(providerliveness.Dependencies{Clock: timeoutClock})
	defer timed.Stop()
	timed.ObserveProviderDispatch(providerliveness.Event{Kind: providerliveness.EventResponseCreate})
	if timeoutClock.latest() == nil || !timeoutClock.latest().fire() {
		t.Fatal("external timeout did not arm")
	}
	select {
	case <-timed.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("external timeout did not publish")
	}
	if !errors.Is(timed.Failure(), providerliveness.ErrSilentProviderTimeout) {
		t.Fatalf("timeout failure = %v", timed.Failure())
	}
}
