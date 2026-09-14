package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func TestRunHandlesWakeAndDoneBoundaryFailures(t *testing.T) {
	wakeErr := errors.New("wake failed")
	doneErr := errors.New("done failed")
	tests := []struct {
		name      string
		wake      <-chan struct{}
		done      <-chan struct{}
		onWake    func(context.Context, sessionduration.Loop, sessionduration.Controller) error
		doneError func() error
		want      error
	}{
		{
			name: "wake",
			wake: func() <-chan struct{} {
				wake := make(chan struct{}, 1)
				wake <- struct{}{}
				return wake
			}(),
			onWake: func(context.Context, sessionduration.Loop, sessionduration.Controller) error { return wakeErr },
			want:   wakeErr,
		},
		{
			name: "done",
			done: func() <-chan struct{} {
				done := make(chan struct{})
				close(done)
				return done
			}(),
			doneError: func() error { return doneErr },
			want:      doneErr,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := sessionduration.RunRequest{
				Context:    context.Background(),
				Inferencer: contractInferencer{session: newContractSession()},
				LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
					return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
				},
				Wake:      test.wake,
				OnWake:    test.onWake,
				Done:      test.done,
				DoneError: test.doneError,
				Drain:     func(context.Context, sessionduration.Loop, sessionduration.Controller) error { return nil },
			}
			if err := New().Run(request); !errors.Is(err, test.want) {
				t.Fatalf("Run() = %v, want %v", err, test.want)
			}
		})
	}
}

func TestWaitForLoopNormalizesCancellation(t *testing.T) {
	results := make(chan error, 2)
	results <- context.Canceled
	if err := waitForLoop(results); err != nil {
		t.Fatalf("waitForLoop(cancellation) = %v, want nil", err)
	}
	failure := errors.New("loop failed")
	results <- failure
	if err := waitForLoop(results); !errors.Is(err, failure) {
		t.Fatalf("waitForLoop(failure) = %v, want failure identity", err)
	}
}

type triggerScheduler struct {
	created chan *triggerTimer
}

type triggerTimer struct {
	events chan time.Time
}

func (s *triggerScheduler) NewTimer(time.Duration) sessionduration.Timer {
	timer := &triggerTimer{events: make(chan time.Time, 1)}
	s.created <- timer
	return timer
}
func (t *triggerTimer) C() <-chan time.Time      { return t.events }
func (t *triggerTimer) Stop() bool               { return true }
func (t *triggerTimer) Reset(time.Duration) bool { return true }

func TestRunExpiresAtMaxDurationAndClosesLoop(t *testing.T) {
	scheduler := &triggerScheduler{created: make(chan *triggerTimer, 1)}
	result := make(chan error, 1)
	go func() {
		result <- New().Run(sessionduration.RunRequest{
			Context:     context.Background(),
			Inferencer:  contractInferencer{session: newContractSession()},
			Clock:       scheduler,
			MaxDuration: time.Second,
			LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
				return &idleRunLoopProbe{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}, nil
			},
			Drain: func(context.Context, sessionduration.Loop, sessionduration.Controller) error { return nil },
		})
	}()
	var timer *triggerTimer
	select {
	case timer = <-scheduler.created:
	case <-time.After(time.Second):
		t.Fatal("max-duration timer was not created")
	}
	timer.events <- time.Now()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run after max duration = %v, want bounded clean stop", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not finish after max duration")
	}
}
