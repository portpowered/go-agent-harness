package sessiondiagnostics_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics/wire"
)

func TestResponseIdentityRejectsWrongAndStaleTerminals(t *testing.T) {
	service := wire.NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	open, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-current"})
	if err != nil || !open.NewResponse {
		t.Fatalf("open = %+v, err=%v", open, err)
	}
	wrong, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOwnsEnd, ResponseID: "response-foreign"})
	if err != nil || wrong.OwnsResponse {
		t.Fatalf("wrong terminal ownership = %+v, err=%v", wrong, err)
	}
	if _, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseFinish, ResponseID: "response-current"}); err != nil {
		t.Fatal(err)
	}
	stale, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-current"})
	if err != nil || stale.Accepted {
		t.Fatalf("stale response was reclaimed: %+v, err=%v", stale, err)
	}
}

func TestToolContinuationRemainsOneScheduledLifecycle(t *testing.T) {
	service := wire.NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	apply := func(event sessiondiagnostics.Event) sessiondiagnostics.Observation {
		t.Helper()
		observation, err := service.Apply(ctx, event)
		if err != nil {
			t.Fatalf("apply %s: %v", event.Kind, err)
		}
		return observation
	}
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventEnsureScheduled, Count: 1})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-tool"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventBindScheduledBoundary, ResponseID: "response-tool"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventToolCall, ResponseID: "response-tool", CallID: "call-1", ToolName: "lookup"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, ResponseID: "response-tool", Output: true})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventToolResultAccepted, CallID: "call-1"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventContinuationRequested})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, Role: sessiondiagnostics.RoleTool, CallID: "call-1"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-continuation"})
	bound := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventBindScheduledBoundary, ResponseID: "response-continuation"})
	if !bound.HasScheduledIndex || bound.ScheduledIndex != 0 {
		t.Fatalf("continuation consumed wrong scheduled slot: %+v", bound)
	}
	end := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, ResponseID: "response-continuation", Output: true, Terminal: &sessiondiagnostics.Terminal{Status: "completed", Reason: "provider_authored_completion"}})
	if !end.Candidate || end.PendingContinuations != 0 {
		t.Fatalf("continuation end = %+v, want candidate with no pending continuation", end)
	}
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventScheduledDisposition, ResponseID: "response-continuation", Disposition: sessiondiagnostics.DispositionCompleted})
	if got := service.Snapshot().CompletedScheduled; got != 1 {
		t.Fatalf("completed scheduled = %d, want 1", got)
	}
	duplicate := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventScheduledDisposition, ResponseID: "response-continuation", Disposition: sessiondiagnostics.DispositionCompleted})
	if duplicate.Accepted && service.Snapshot().CompletedScheduled != 1 {
		t.Fatalf("duplicate disposition changed snapshot: %+v", duplicate)
	}
}

func TestRateLimitRetryUsesInjectedSchedulerAndOneBudget(t *testing.T) {
	var scheduled []time.Duration
	service := wire.NewService(sessiondiagnostics.Options{RetryScheduler: func(_ context.Context, delay time.Duration) error {
		scheduled = append(scheduled, delay)
		return nil
	}})
	ctx := context.Background()
	apply := func(event sessiondiagnostics.Event) sessiondiagnostics.Observation {
		t.Helper()
		observation, err := service.Apply(ctx, event)
		if err != nil {
			t.Fatalf("apply %s: %v", event.Kind, err)
		}
		return observation
	}
	terminal := &sessiondiagnostics.Terminal{Status: "failed", ErrorCode: "rate_limit_exceeded", ErrorMessage: "Please try again in 0.01s"}
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventEnsureScheduled, Count: 1})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "failed"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventBindScheduledBoundary, ResponseID: "failed"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, ResponseID: "failed", Output: false, Terminal: terminal})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventNoteScheduledTerminal, ResponseID: "failed", Terminal: terminal})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventRememberRetry, ResponseID: "failed", Terminal: terminal})
	retry, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventClaimRetry, ResponseID: "failed", Terminal: terminal})
	if err != nil || !retry.Retry.Accepted || retry.Retry.Delay != 10*time.Millisecond {
		t.Fatalf("retry = %+v, err=%v", retry, err)
	}
	if len(scheduled) != 1 || scheduled[0] != 10*time.Millisecond {
		t.Fatalf("scheduler calls = %v, want one 10ms call", scheduled)
	}
	if _, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventClaimRetry, ResponseID: "failed", Terminal: terminal}); !errors.Is(err, sessiondiagnostics.ErrRetryExhausted) {
		t.Fatalf("second retry error = %v, want ErrRetryExhausted", err)
	}
}

func TestCloseIsIdempotentAndRejectsEvents(t *testing.T) {
	service := wire.NewService(sessiondiagnostics.Options{})
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "closed"}); !errors.Is(err, sessiondiagnostics.ErrClosed) {
		t.Fatalf("closed service error = %v, want ErrClosed", err)
	}
}
