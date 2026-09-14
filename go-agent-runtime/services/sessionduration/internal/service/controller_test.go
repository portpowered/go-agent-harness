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
	deadline := time.After(time.Second)
	for !controller.TerminalWritten() {
		select {
		case <-deadline:
			t.Fatal("controller did not expire")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if got := controller.Observe(messages.StreamMessage{Type: messages.StreamTypeTextDelta}); got.Accepted {
		t.Fatal("late output was admitted after expiry")
	}
	if err := controller.Expire(); err != nil {
		if !errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
			t.Fatalf("second Expire: %v", err)
		}
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
	_, _ = controller.Finalize(context.Background(), sessionduration.FinalizeRequest{})
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
	controller.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart})
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
	_, _ = controller.Finalize(context.Background(), sessionduration.FinalizeRequest{})
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
	_, _ = controller.Finalize(context.Background(), sessionduration.FinalizeRequest{})
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
	result, finalErr := controller.Finalize(context.Background(), sessionduration.FinalizeRequest{
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

type artifactLifecycleFunc struct {
	accept func(messages.StreamMessage) error
	flush  func() error
	close  func() error
}

func (f artifactLifecycleFunc) Accept(msg messages.StreamMessage) error { return f.accept(msg) }
func (f artifactLifecycleFunc) Flush() error                            { return f.flush() }
func (f artifactLifecycleFunc) Close() error                            { return f.close() }
