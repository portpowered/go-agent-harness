package runner_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationservice "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/internal/service"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestPublicDurationServicePreservesLoopCreationFailure(t *testing.T) {
	want := errors.New("loop unavailable")
	service := durationservice.New()
	_, err := service.RunWithResult(sessionduration.RunRequest{
		Context:    context.Background(),
		Inferencer: unusedInferencer{},
		LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
			return nil, want
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("RunWithResult() error = %v, want to preserve %v", err, want)
	}
}

func TestPublicServiceRunReportsBoundedSessionUpdatedTimeout(t *testing.T) {
	configuredFailure := errors.New("session updated was not acknowledged")
	for _, test := range []struct {
		name       string
		timeoutErr error
		want       error
	}{
		{name: "configured cause", timeoutErr: configuredFailure, want: configuredFailure},
		{name: "default cause", want: errors.New("session updated acknowledgement timed out")},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheduler := clock.NewDeterministic(time.Unix(0, 0), time.Millisecond)
			service := durationservice.New()
			loop := &sessionUpdatedLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}
			request := sessionduration.RunRequest{
				Context:    context.Background(),
				Clock:      scheduler,
				Inferencer: unusedInferencer{},
				LoopFactory: func(context.Context, sessionduration.AdmissionInferencer, sessionduration.Controller) (sessionduration.Loop, error) {
					return loop, nil
				},
				SessionUpdated: sessionduration.SessionUpdatedWait{
					Timeout: time.Millisecond, Pending: func() bool { return true }, TimeoutError: test.timeoutErr,
				},
				Effects: sessionduration.RunEffects{SessionOpened: func(context.Context, sessionduration.Loop) error {
					scheduler.AdvanceBy(time.Millisecond)
					return nil
				}},
			}
			_, err := service.RunWithResult(request)
			if test.timeoutErr != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("RunWithResult() error = %v, want configured cause %v", err, test.want)
				}
				return
			}
			if err == nil || err.Error() != test.want.Error() {
				t.Fatalf("RunWithResult() error = %v, want default timeout %v", err, test.want)
			}
		})
	}
}

func TestPublicServiceFinalizeRechecksPendingAfterDrainingLateOutput(t *testing.T) {
	ctx := context.Background()
	scheduler := &drainTestScheduler{created: make(chan *drainTestTimer, 2)}
	published := make(chan messages.StreamMessage, 1)
	var pending atomic.Bool
	controller, err := durationservice.New().Begin(sessionduration.Options{
		Context: ctx, Clock: scheduler,
		Publication: sessionduration.Publication{Write: func(msg messages.StreamMessage) error {
			published <- msg
			pending.Store(true)
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	loop := &drainOutputLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}
	want := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("drain me")}
	checkingPending := make(chan struct{})
	continuePendingCheck := make(chan struct{})
	var firstCheck sync.Once
	checkPending := func() bool {
		firstCheck.Do(func() {
			close(checkingPending)
			<-continuePendingCheck
		})
		return pending.Load()
	}
	closed := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, finalizeErr := controller.Finalize(ctx, sessionduration.FinalizeRequest{
			DrainLoop: loop,
			DrainPolicy: sessionduration.DrainPolicy{
				Clock: scheduler, QuietPeriod: time.Millisecond, WallSafety: time.Second,
				Pending: checkPending,
			},
			Close: func() error { close(closed); return nil },
		})
		finished <- finalizeErr
	}()
	var first *drainTestTimer
	select {
	case first = <-scheduler.created:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not start the drain quiet timer")
	}
	first.events <- time.Time{}
	select {
	case <-checkingPending:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not check pending work at the quiet boundary")
	}
	if !loop.deltas.Write(ctx, want) {
		t.Fatal("could not queue late output for shutdown drain")
	}
	close(continuePendingCheck)
	select {
	case got := <-published:
		if got != want {
			t.Fatalf("drained output = %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not publish late loop output")
	}
	var second *drainTestTimer
	select {
	case second = <-scheduler.created:
	case <-time.After(time.Second):
		t.Fatal("drained output did not trigger a second pending-work check")
	}
	select {
	case <-closed:
		t.Fatal("session closed while drain work remained pending")
	default:
	}
	pending.Store(false)
	second.events <- time.Time{}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("Finalize: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Finalize did not complete after pending output drained")
	}
	select {
	case <-closed:
	default:
		t.Fatal("session was not closed after the drain quiet period")
	}
}

func TestPublicServiceFinalizeReturnsDrainPublicationFailureAndCloses(t *testing.T) {
	wantErr := errors.New("output writer failed")
	closed := false
	controller, err := durationservice.New().Begin(sessionduration.Options{
		Context: context.Background(),
		Publication: sessionduration.Publication{Write: func(messages.StreamMessage) error {
			return wantErr
		}},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	loop := &drainOutputLoop{deltas: messages.NewTypedBuffer[messages.StreamMessage](1)}
	message := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("final output")}
	if !loop.deltas.Write(context.Background(), message) {
		t.Fatal("could not queue output for shutdown drain")
	}
	_, err = controller.Finalize(context.Background(), sessionduration.FinalizeRequest{
		DrainLoop: loop,
		Close:     func() error { closed = true; return nil },
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Finalize() error = %v, want publication error %v", err, wantErr)
	}
	if !closed {
		t.Fatal("session was not closed after drain publication failed")
	}
}

func TestPublicServiceBeginRequiresSchedulerForBoundedDuration(t *testing.T) {
	_, err := durationservice.New().Begin(sessionduration.Options{
		Context: context.Background(), MaxDuration: time.Second,
	})
	if !errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
		t.Fatalf("Begin() error = %v, want missing scheduler error", err)
	}
}

func TestPublicServiceReportsUnavailableLivenessSchedulers(t *testing.T) {
	ctx := context.Background()
	controller, err := durationservice.New().Begin(sessionduration.Options{
		Context: ctx,
		Clock:   nilTimerScheduler{},
		Liveness: sessionduration.LivenessOptions{
			Enabled: true, RequireFirstResponse: true,
		},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ExpectProviderProgress()
	select {
	case got := <-controller.Errors():
		if !errors.Is(got, sessionduration.ErrSchedulerUnavailable) {
			t.Fatalf("liveness arm error = %v, want unavailable scheduler", got)
		}
	case <-time.After(time.Second):
		t.Fatal("missing liveness timer was not reported")
	}
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Role: messages.RoleSystem})
	select {
	case got := <-controller.Errors():
		if !errors.Is(got, sessionduration.ErrSchedulerUnavailable) {
			t.Fatalf("first-response arm error = %v, want unavailable scheduler", got)
		}
	case <-time.After(time.Second):
		t.Fatal("missing first-response timer was not reported")
	}
	if _, err := controller.Finalize(ctx, sessionduration.FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

type nilTimerScheduler struct{}

func (nilTimerScheduler) NewTimer(time.Duration) sessionduration.Timer { return nil }

type drainTestScheduler struct{ created chan *drainTestTimer }

func (s *drainTestScheduler) NewTimer(time.Duration) sessionduration.Timer {
	timer := &drainTestTimer{events: make(chan time.Time, 1)}
	s.created <- timer
	return timer
}

type drainTestTimer struct{ events chan time.Time }

func (t *drainTestTimer) C() <-chan time.Time { return t.events }
func (*drainTestTimer) Stop() bool            { return true }

type drainOutputLoop struct {
	deltas *messages.TypedBuffer[messages.StreamMessage]
}

func (*drainOutputLoop) Run(context.Context) error { return nil }
func (l *drainOutputLoop) Deltas() *messages.TypedBuffer[messages.StreamMessage] {
	return l.deltas
}
func (*drainOutputLoop) Send(context.Context, []messages.Message) error { return nil }

type sessionUpdatedLoop struct {
	deltas *messages.TypedBuffer[messages.StreamMessage]
}

func (l *sessionUpdatedLoop) Run(ctx context.Context) error {
	if !l.deltas.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Role: messages.RoleSystem, Value: messages.NewSessionOpenValue("session-1", "audio")}) {
		return errors.New("publish session-open event")
	}
	<-ctx.Done()
	return ctx.Err()
}

func (l *sessionUpdatedLoop) Deltas() *messages.TypedBuffer[messages.StreamMessage] { return l.deltas }

func (*sessionUpdatedLoop) Send(context.Context, []messages.Message) error { return nil }

type unusedInferencer struct{}

func (unusedInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, errors.New("provider connection was not expected")
}
