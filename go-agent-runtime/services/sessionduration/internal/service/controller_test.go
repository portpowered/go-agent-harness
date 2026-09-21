package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestBeginRejectsNegativeDurationBeforeTimingOrPublication(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(1, 0), time.Millisecond)
	controller, err := New().Begin(sessionduration.Options{
		Clock:       clock,
		MaxDuration: -time.Second,
		Publication: sessionduration.Publication{Write: func(messages.StreamMessage) error { t.Fatal("publication occurred"); return nil }},
	})
	if controller != nil {
		t.Fatal("negative duration returned a controller")
	}
	var durationErr *sessionduration.InvalidDurationError
	if !errors.As(err, &durationErr) || !errors.Is(err, sessionduration.ErrInvalidDuration) {
		t.Fatalf("error = %v, want typed invalid-duration identity", err)
	}
}

func TestControllerExpiresOnceAndRejectsLateOutput(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(2, 0), time.Millisecond)
	var mu sync.Mutex
	var writes []messages.StreamMessage
	controller, err := New().Begin(sessionduration.Options{
		Context:     context.Background(),
		Clock:       clock,
		MaxDuration: 5 * time.Millisecond,
		Publication: sessionduration.Publication{Write: func(msg messages.StreamMessage) error {
			mu.Lock()
			writes = append(writes, msg)
			mu.Unlock()
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	clock.AdvanceBy(5 * time.Millisecond)
	select {
	case err := <-controller.Errors():
		if !errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
			t.Fatalf("expiry error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("controller did not expire")
	}
	if got := controller.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); got.Accepted {
		t.Fatal("late output was admitted after expiry")
	}
	if err := controller.Expire(); !errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
		t.Fatalf("second Expire: %v", err)
	}
	if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(writes) != 1 {
		t.Fatalf("max-duration publications = %d, want one", len(writes))
	}
	value, ok := writes[0].Value.(*messages.SessionCloseValue)
	if !ok || value.OutputState != messages.TerminalOutputPartial || value.TerminalReason != messages.TerminalReason("max_duration") {
		t.Fatalf("publication = %#v, want partial max_duration terminal", writes[0].Value)
	}
}

func TestControllerLivenessUsesGenerationAndPreservesTypedCause(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(3, 0), time.Millisecond)
	controller, err := New().Begin(sessionduration.Options{
		Context:  context.Background(),
		Clock:    clock,
		Liveness: sessionduration.LivenessOptions{Enabled: true, Timeout: 5 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.ExpectProviderProgress()
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	clock.AdvanceBy(4 * time.Millisecond)
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta})
	clock.AdvanceBy(4 * time.Millisecond)
	select {
	case got := <-controller.Errors():
		t.Fatalf("stale liveness timer reported %v", got)
	default:
	}
	clock.AdvanceBy(time.Millisecond)
	var got error
	deadline := time.After(time.Second)
	for got == nil {
		select {
		case got = <-controller.Errors():
		case <-deadline:
			t.Fatal("liveness error was not reported")
		}
	}
	if !errors.Is(got, sessionduration.ErrProviderLivenessTimeout) {
		t.Fatalf("liveness error = %v, want timeout identity", got)
	}
	var typed *sessionduration.LivenessError
	if !errors.As(got, &typed) || typed.Classification != "silent_provider_timeout" {
		t.Fatalf("liveness error = %T/%v, want typed timeout", got, got)
	}
	if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

func TestControllerArbitratesFirstCauseOnce(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Unix(30, 0), time.Millisecond)
	var causes []error
	causeCalled := make(chan struct{}, 2)
	controller, err := New().Begin(sessionduration.Options{
		Clock:       clock,
		MaxDuration: time.Second,
		Liveness:    sessionduration.LivenessOptions{Enabled: true, Timeout: 5 * time.Millisecond},
		FirstCause: func(cause error) {
			causes = append(causes, cause)
			causeCalled <- struct{}{}
		},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
	clock.AdvanceBy(5 * time.Millisecond)
	select {
	case <-controller.Errors():
	case <-time.After(time.Second):
		t.Fatal("liveness failure was not reported")
	}
	select {
	case <-causeCalled:
	case <-time.After(time.Second):
		t.Fatal("first-cause callback was not called")
	}
	if err := controller.Expire(); !errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
		t.Fatalf("Expire: %v", err)
	}
	if len(causes) != 1 || !errors.Is(causes[0], sessionduration.ErrProviderLivenessTimeout) {
		t.Fatalf("first causes = %v, want one provider-timeout cause", causes)
	}
	if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

func TestControllerRetryIsBoundedAndNeverSleeps(t *testing.T) {
	controller, err := New().Begin(sessionduration.Options{Clock: platformclock.NewDeterministic(time.Unix(4, 0), time.Millisecond), Retry: sessionduration.RetryPolicy{Enabled: true, MaxRetries: 1, DefaultDelay: time.Second, MaxDelay: 3 * time.Second}})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	terminal := &messages.MessageEndValue{Status: "failed", ProviderErrorCode: "rate_limit_exceeded", ProviderErrorMessage: "please try again in 9s"}
	decision := controller.Retry(sessionduration.RetryRequest{Terminal: terminal})
	if !decision.Eligible || decision.Delay != 3*time.Second {
		t.Fatalf("retry decision = %+v, want eligible capped at 3s", decision)
	}
	if exhausted := controller.Retry(sessionduration.RetryRequest{Terminal: terminal}); !exhausted.Exhausted || exhausted.Eligible {
		t.Fatalf("second retry decision = %+v, want exhausted", exhausted)
	}
	if _, err := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
}

func TestControllerFinalizePreservesPrimaryAndCleanupErrors(t *testing.T) {
	primary := errors.New("provider cause")
	drainErr := errors.New("drain cause")
	closeErr := errors.New("close cause")
	artifactErr := errors.New("artifact cause")
	controller, err := New().Begin(sessionduration.Options{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	order := make([]string, 0, 4)
	var finalizationContext context.Context
	result, finalErr := controller.Finalize(finalizationContext, sessionduration.FinalizeRequest{
		Primary: primary,
		Drain:   func(context.Context) error { order = append(order, "drain"); return drainErr },
		Close:   func() error { order = append(order, "close"); return closeErr },
		Artifacts: artifactLifecycleFunc{
			accept: func(messages.StreamMessage) error { return nil },
			flush:  func() error { order = append(order, "flush"); return artifactErr },
			close:  func() error { order = append(order, "artifact-close"); return nil },
		},
	})
	if !errors.Is(finalErr, primary) || !errors.Is(finalErr, drainErr) || !errors.Is(finalErr, closeErr) || !errors.Is(finalErr, artifactErr) {
		t.Fatalf("final error = %v, lost cleanup identity", finalErr)
	}
	if got, want := result.OutputState, messages.TerminalOutputNone; got != want {
		t.Fatalf("result output state = %q, want %q", got, want)
	}
	if got, want := order, []string{"drain", "close", "flush", "artifact-close"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("cleanup order = %v, want %v", got, want)
	}
}

func TestControllerBoundedDrainReportsSchedulerFailure(t *testing.T) {
	deltas := messages.NewTypedBuffer[messages.StreamMessage](1)
	if !deltas.Write(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant}) {
		t.Fatal("could not queue loop delta")
	}
	controller, err := New().Begin(sessionduration.Options{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, err = controller.Finalize(context.Background(), sessionduration.FinalizeRequest{
		DrainLoop:   &idleRunLoopProbe{deltas: deltas},
		DrainPolicy: sessionduration.DrainPolicy{Clock: nilTimerScheduler{}},
	})
	if err == nil {
		t.Fatal("Finalize unexpectedly hid a nil drain scheduler")
	}
}

func TestControllerFinalizationPreservesPanicIdentity(t *testing.T) {
	controller, err := New().Begin(sessionduration.Options{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, err = controller.Finalize(context.Background(), sessionduration.FinalizeRequest{
		Close: func() error { panic("close panic") },
	})
	if !errors.Is(err, sessionduration.ErrFinalizationPanic) {
		t.Fatalf("Finalize error = %v, want ErrFinalizationPanic identity", err)
	}
}

func TestControllerFinalizationDrainOutlivesCallerCancellationWithBound(t *testing.T) {
	callerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller, err := New().Begin(sessionduration.Options{Context: callerCtx})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	cancel()

	var gotDeadline bool
	_, err = controller.Finalize(callerCtx, sessionduration.FinalizeRequest{
		DrainPolicy: sessionduration.DrainPolicy{WallSafety: time.Second},
		Drain: func(drainCtx context.Context) error {
			_, gotDeadline = drainCtx.Deadline()
			return drainCtx.Err()
		},
	})
	if err != nil {
		t.Fatalf("Finalize after caller cancellation: %v", err)
	}
	if !gotDeadline {
		t.Fatal("finalization drain context has no bounded deadline")
	}
}

type artifactLifecycleFunc struct {
	accept func(messages.StreamMessage) error
	flush  func() error
	close  func() error
}

func (f artifactLifecycleFunc) Accept(msg messages.StreamMessage) error { return f.accept(msg) }
func (f artifactLifecycleFunc) Flush() error                            { return f.flush() }
func (f artifactLifecycleFunc) Close() error                            { return f.close() }

func TestControllerFinalizationDrainsLoopUntilQuiet(t *testing.T) {
	scheduler := &triggerScheduler{created: make(chan *triggerTimer, 1)}
	deltas := messages.NewTypedBuffer[messages.StreamMessage](2)
	first := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("first")}
	second := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("second")}
	if !deltas.Write(context.Background(), first) {
		t.Fatal("could not queue first loop delta")
	}
	var published []messages.StreamMessage
	controller, err := New().Begin(sessionduration.Options{Publication: sessionduration.Publication{Write: func(msg messages.StreamMessage) error {
		published = append(published, msg)
		return nil
	}}})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, finalizeErr := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{
			DrainLoop: &idleRunLoopProbe{deltas: deltas},
			DrainPolicy: sessionduration.DrainPolicy{
				Clock:       scheduler,
				QuietPeriod: time.Second,
				WallSafety:  time.Second,
			},
		})
		done <- finalizeErr
	}()
	select {
	case <-scheduler.created:
	case <-time.After(time.Second):
		t.Fatal("bounded drain did not create its quiet timer")
	}
	if !deltas.Write(context.Background(), second) {
		t.Fatal("could not queue late loop delta")
	}
	var quietTimer *triggerTimer
	select {
	case quietTimer = <-scheduler.created:
	case <-time.After(time.Second):
		t.Fatal("bounded drain did not reset its quiet timer")
	}
	quietTimer.events <- time.Now()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Finalize: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bounded drain did not finish at quiet timer")
	}
	if len(published) != 2 || published[0].Value == nil || published[1].Value == nil {
		t.Fatalf("published loop deltas = %+v, want both queued deltas", published)
	}
}
