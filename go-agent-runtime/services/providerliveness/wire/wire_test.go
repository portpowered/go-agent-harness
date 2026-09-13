package wire

import (
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providerliveness"
)

type wireTimer struct {
	ch     chan time.Time
	active bool
}

func newWireTimer() *wireTimer           { return &wireTimer{ch: make(chan time.Time, 1), active: true} }
func (t *wireTimer) C() <-chan time.Time { return t.ch }
func (t *wireTimer) Stop() bool          { active := t.active; t.active = false; return active }
func (t *wireTimer) Fire() bool {
	if !t.active {
		return false
	}
	t.active = false
	t.ch <- time.Time{}
	return true
}

type wireClock struct{ timers []*wireTimer }

func (c *wireClock) NewTimer(time.Duration) providerliveness.Timer {
	timer := newWireTimer()
	c.timers = append(c.timers, timer)
	return timer
}
func (c *wireClock) latest() *wireTimer {
	for i := len(c.timers) - 1; i >= 0; i-- {
		if c.timers[i].active {
			return c.timers[i]
		}
	}
	return nil
}

func TestWireConstructsIndependentServices(t *testing.T) {
	clockOne, clockTwo := &wireClock{}, &wireClock{}
	first := NewService(providerliveness.Dependencies{Clock: clockOne})
	second := NewService(providerliveness.Dependencies{Clock: clockTwo})
	defer first.Stop()
	defer second.Stop()
	first.ObserveProviderDispatch(providerliveness.Event{Kind: providerliveness.EventResponseCreate})
	if clockOne.latest() == nil || clockTwo.latest() != nil {
		t.Fatal("Wire services did not keep timer state isolated")
	}
	if !clockOne.latest().Fire() {
		t.Fatal("first watchdog did not fire")
	}
	select {
	case <-first.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("first service did not publish timeout")
	}
	if !errors.Is(first.Failure(), providerliveness.ErrSilentProviderTimeout) {
		t.Fatalf("first failure = %v", first.Failure())
	}
	if second.Failure() != nil {
		t.Fatalf("second service inherited first failure: %v", second.Failure())
	}
}
