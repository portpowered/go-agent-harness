package agentruntime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

type livenessTestClock struct {
	mu      sync.Mutex
	timers  []*livenessTestTimer
	created chan struct{}
}

func (c *livenessTestClock) NewTimer(time.Duration) duration.Timer {
	timer := &livenessTestTimer{ch: make(chan time.Time, 1), active: true}
	c.mu.Lock()
	c.timers = append(c.timers, timer)
	created := c.created
	c.mu.Unlock()
	if created != nil {
		select {
		case created <- struct{}{}:
		default:
		}
	}
	return timer
}

func (c *livenessTestClock) latestActiveTimer() *livenessTestTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := len(c.timers) - 1; index >= 0; index-- {
		timer := c.timers[index]
		timer.mu.Lock()
		active := timer.active
		timer.mu.Unlock()
		if active {
			return timer
		}
	}
	return nil
}

func (c *livenessTestClock) fireLatest() bool {
	timer := c.latestActiveTimer()
	return timer != nil && timer.fire()
}

type livenessTestTimer struct {
	mu     sync.Mutex
	ch     chan time.Time
	active bool
}

func (t *livenessTestTimer) C() <-chan time.Time { return t.ch }

func (t *livenessTestTimer) Stop() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	wasActive := t.active
	t.active = false
	t.mu.Unlock()
	return wasActive
}

func (t *livenessTestTimer) fire() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	if !t.active {
		t.mu.Unlock()
		return false
	}
	t.active = false
	t.mu.Unlock()
	t.ch <- time.Time{}
	return true
}

func TestRunAgentLoopSessionWithDuration_WatchdogWakesLoop(t *testing.T) {
	livenessClock := &livenessTestClock{created: make(chan struct{}, 1)}
	observer := newSessionProgressObserver(nil, nil, "test-provider", "test-model")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inferencer := &durationTestInferencer{
		connectedCh: make(chan struct{}),
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("silent-session", "test")},
		},
	}
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- runAgentLoopSession(
			ctx,
			io.Discard,
			inferencer,
			newTestSessionLoopOptions(sessionLoopOptions{
				Prompt:        "wait for a response",
				WaitForClose:  true,
				observer:      observer,
				livenessClock: livenessClock,
			}),
		)
	}()

	select {
	case err := <-runErrCh:
		t.Fatalf("session stopped before watchdog arm: %v", err)
	case <-livenessClock.created:
	case <-time.After(2 * time.Second):
		select {
		case <-inferencer.connectedCh:
		default:
			t.Fatal("session did not connect or arm provider watchdog")
		}
		t.Fatal("session connected but did not arm provider watchdog after dispatch")
	}
	if !livenessClock.fireLatest() {
		t.Fatal("session watchdog timer did not fire")
	}
	select {
	case err := <-runErrCh:
		if !errors.Is(err, duration.ErrProviderLivenessTimeout) {
			t.Fatalf("session error = %v, want timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session loop did not stop after provider watchdog timeout")
	}
}

var _ duration.TimerScheduler = (*livenessTestClock)(nil)
var _ duration.Timer = (*livenessTestTimer)(nil)
