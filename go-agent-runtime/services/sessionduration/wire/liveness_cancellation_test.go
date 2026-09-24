package wire

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestPublicCancellationDisarmsTimerCreatedConcurrently(t *testing.T) {
	release := make(chan struct{})
	scheduler := &publicGatedScheduler{
		entered: make(chan struct{}), release: release, created: make(chan *publicManualTimer, 2),
	}
	controller, err := NewService().Begin(sessionduration.Options{
		Liveness:      sessionduration.LivenessOptions{Enabled: true, Timeout: time.Millisecond},
		LivenessClock: scheduler,
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	t.Cleanup(func() {
		if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
			t.Errorf("Finalize: %v", err)
		}
	})

	observed := make(chan struct{})
	go func() {
		controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "cancelled"})
		close(observed)
	}()
	select {
	case <-scheduler.entered:
	case <-time.After(time.Second):
		t.Fatal("liveness timer creation did not block")
	}
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Role: messages.RoleUser, ResponseID: "cancelled"})
	close(release)
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("response observation did not finish after timer creation resumed")
	}

	cancelledTimer := receiveGatedPublicTimer(t, scheduler)
	cancelledTimer.Fire()
	select {
	case err := <-controller.Errors():
		t.Fatalf("response cancelled during timer creation triggered liveness: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Role: messages.RoleAssistant, ResponseID: "next"})
	nextTimer := receiveGatedPublicTimer(t, scheduler)
	nextTimer.Fire()
	select {
	case err := <-controller.Errors():
		var typed *sessionduration.LivenessError
		if !errors.Is(err, sessionduration.ErrProviderLivenessTimeout) || !errors.As(err, &typed) || typed.ResponseID != "next" {
			t.Fatalf("next response liveness error = %v, want timeout for next", err)
		}
	case <-time.After(time.Second):
		t.Fatal("new response did not restart liveness after cancellation")
	}
}

type publicGatedScheduler struct {
	entered chan struct{}
	release chan struct{}
	created chan *publicManualTimer
	calls   int
}

func (s *publicGatedScheduler) NewTimer(duration time.Duration) sessionduration.Timer {
	if s.calls == 0 {
		s.calls++
		close(s.entered)
		<-s.release
	}
	timer := &publicManualTimer{duration: duration, events: make(chan time.Time, 1)}
	s.created <- timer
	return timer
}

func receiveGatedPublicTimer(t *testing.T, scheduler *publicGatedScheduler) *publicManualTimer {
	t.Helper()
	select {
	case timer := <-scheduler.created:
		return timer
	case <-time.After(time.Second):
		t.Fatal("session duration timer was not created")
		return nil
	}
}
