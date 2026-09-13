package wire

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiondiagnostics"
)

const testResponseA = "response-a"

func TestResponseIdentityRejectsWrongAndStaleTerminals(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
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

func TestResponseFinishRejectsWrongIDWithoutClearingActiveResponse(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	if _, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: testResponseA}); err != nil {
		t.Fatal(err)
	}

	wrong, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseFinish, ResponseID: "response-b"})
	if err != nil {
		t.Fatal(err)
	}
	if wrong.Accepted || service.Snapshot().ActiveResponseID != testResponseA {
		t.Fatalf("wrong finish changed active response: observation=%+v snapshot=%+v", wrong, service.Snapshot())
	}

	finish, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseFinish, ResponseID: testResponseA})
	if err != nil || !finish.Accepted {
		t.Fatalf("matching finish = %+v, err=%v", finish, err)
	}
	if service.Snapshot().ActiveResponse {
		t.Fatal("matching finish left the response active")
	}
}

func TestResponseOpenRejectsForeignIDUntilExplicitBoundary(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	apply := func(event sessiondiagnostics.Event) sessiondiagnostics.Observation {
		t.Helper()
		observation, err := service.Apply(ctx, event)
		if err != nil {
			t.Fatalf("apply %s: %v", event.Kind, err)
		}
		return observation
	}

	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: testResponseA})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseContent})
	foreign := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-b"})
	if foreign.Accepted || foreign.NewResponse || service.Snapshot().ActiveResponseID != testResponseA {
		t.Fatalf("foreign open stole active response: observation=%+v snapshot=%+v", foreign, service.Snapshot())
	}

	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseFinish, ResponseID: testResponseA})
	fresh := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-b"})
	if !fresh.Accepted || !fresh.NewResponse || service.Snapshot().ActiveResponseID != "response-b" {
		t.Fatalf("fresh open after finish = %+v, snapshot=%+v", fresh, service.Snapshot())
	}
}

func TestResponseOpenRejectsForeignIDBeforeAnyBoundary(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	if _, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: testResponseA}); err != nil {
		t.Fatal(err)
	}

	foreign, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-b"})
	if err != nil {
		t.Fatal(err)
	}
	if foreign.Accepted || foreign.NewResponse || service.Snapshot().ActiveResponseID != testResponseA {
		t.Fatalf("foreign open before a boundary stole active response: observation=%+v snapshot=%+v", foreign, service.Snapshot())
	}
}

func TestFinishClearsPurposeAndTerminalBoundary(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	if _, err := service.Apply(ctx, sessiondiagnostics.Event{
		Kind:       sessiondiagnostics.EventResponseOpen,
		ResponseID: "tool-ack",
		Purpose:    sessiondiagnostics.ResponsePurposeToolAcknowledgement,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseFinish, ResponseID: "tool-ack"}); err != nil {
		t.Fatal(err)
	}

	end, err := service.Apply(ctx, sessiondiagnostics.Event{
		Kind:     sessiondiagnostics.EventResponseEnd,
		Output:   true,
		Terminal: &sessiondiagnostics.Terminal{Status: "completed", Reason: "provider_authored_completion"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !end.Candidate || !end.Admitted {
		t.Fatalf("terminal-only normal response after tool acknowledgement was not admitted: %+v", end)
	}
}

func TestResponseEndRejectsDuplicateBeforeFinish(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	apply := func(event sessiondiagnostics.Event) sessiondiagnostics.Observation {
		t.Helper()
		observation, err := service.Apply(ctx, event)
		if err != nil {
			t.Fatalf("apply %s: %v", event.Kind, err)
		}
		return observation
	}

	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: testResponseA})
	first := apply(sessiondiagnostics.Event{
		Kind:       sessiondiagnostics.EventResponseEnd,
		ResponseID: testResponseA,
		Output:     true,
		Terminal:   &sessiondiagnostics.Terminal{Status: "completed", Reason: "provider_authored_completion"},
	})
	if !first.Candidate || !first.Admitted {
		t.Fatalf("first response end = %+v, want admitted candidate", first)
	}

	duplicate := apply(sessiondiagnostics.Event{
		Kind:       sessiondiagnostics.EventResponseEnd,
		ResponseID: testResponseA,
		Output:     true,
		Terminal:   &sessiondiagnostics.Terminal{Status: "completed", Reason: "provider_authored_completion"},
	})
	if duplicate.Accepted || duplicate.Candidate || duplicate.Admitted || !service.Snapshot().ActiveResponse {
		t.Fatalf("duplicate response end was admitted or changed ownership: observation=%+v snapshot=%+v", duplicate, service.Snapshot())
	}
}

func TestResponseContentRejectsUntaggedBoundaryAfterEnd(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	apply := func(event sessiondiagnostics.Event) sessiondiagnostics.Observation {
		t.Helper()
		observation, err := service.Apply(ctx, event)
		if err != nil {
			t.Fatalf("apply %s: %v", event.Kind, err)
		}
		return observation
	}

	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: testResponseA})
	first := apply(sessiondiagnostics.Event{
		Kind:       sessiondiagnostics.EventResponseEnd,
		ResponseID: testResponseA,
		Output:     true,
		Terminal:   &sessiondiagnostics.Terminal{Status: "completed", Reason: "provider_authored_completion"},
	})
	if !first.Candidate || !first.Admitted {
		t.Fatalf("first response end = %+v, want admitted candidate", first)
	}

	content := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseContent})
	if content.Accepted || content.Candidate || content.Admitted {
		t.Fatalf("untagged content reopened the terminal response generation: %+v", content)
	}

	duplicate := apply(sessiondiagnostics.Event{
		Kind:       sessiondiagnostics.EventResponseEnd,
		ResponseID: testResponseA,
		Output:     true,
		Terminal:   &sessiondiagnostics.Terminal{Status: "completed", Reason: "provider_authored_completion"},
	})
	if duplicate.Accepted || duplicate.Candidate || duplicate.Admitted || !service.Snapshot().ActiveResponse {
		t.Fatalf("duplicate response end was admitted or changed ownership: observation=%+v snapshot=%+v", duplicate, service.Snapshot())
	}
}

func TestToolAcknowledgementCannotBeAdmittedAsAssistantTurn(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	if _, err := service.Apply(ctx, sessiondiagnostics.Event{
		Kind:       sessiondiagnostics.EventResponseOpen,
		ResponseID: "tool-ack",
		Purpose:    sessiondiagnostics.ResponsePurposeToolAcknowledgement,
	}); err != nil {
		t.Fatal(err)
	}
	end, err := service.Apply(ctx, sessiondiagnostics.Event{
		Kind:       sessiondiagnostics.EventResponseEnd,
		ResponseID: "tool-ack",
		Output:     true,
		Terminal:   &sessiondiagnostics.Terminal{Status: "completed", Reason: "provider_authored_completion"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if end.Candidate || end.Admitted {
		t.Fatalf("tool acknowledgement was admitted as assistant output: %+v", end)
	}
}

func TestResetRejectsLateResponseFromPreviousGeneration(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	apply := func(event sessiondiagnostics.Event) sessiondiagnostics.Observation {
		t.Helper()
		observation, err := service.Apply(ctx, event)
		if err != nil {
			t.Fatalf("apply %s: %v", event.Kind, err)
		}
		return observation
	}

	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-old"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventReset})
	lateEnd := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, ResponseID: "response-old", Output: true})
	if lateEnd.Accepted || service.Snapshot().ActiveResponse {
		t.Fatalf("late previous-generation end reclaimed lifecycle: observation=%+v snapshot=%+v", lateEnd, service.Snapshot())
	}
	lateOpen := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-old"})
	if lateOpen.Accepted || lateOpen.NewResponse {
		t.Fatalf("late previous-generation open was accepted: %+v", lateOpen)
	}

	fresh := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-new"})
	if !fresh.Accepted || !fresh.NewResponse || service.Snapshot().ActiveResponseID != "response-new" {
		t.Fatalf("fresh generation open = %+v, snapshot=%+v", fresh, service.Snapshot())
	}
}

func TestResetClearsReducerStateWithoutReplacingMutex(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	for _, event := range []sessiondiagnostics.Event{
		{Kind: sessiondiagnostics.EventEnsureScheduled, Count: 1},
		{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-before-reset"},
		{Kind: sessiondiagnostics.EventBindScheduledBoundary, ResponseID: "response-before-reset"},
	} {
		if _, err := service.Apply(ctx, event); err != nil {
			t.Fatalf("apply %s: %v", event.Kind, err)
		}
	}

	service.Reset()
	snapshot := service.Snapshot()
	if snapshot.ActiveResponse || snapshot.ActiveResponseID != "" || len(snapshot.Scheduled) != 0 || len(snapshot.ContinuationStates) != 0 {
		t.Fatalf("reset left lifecycle state: %+v", snapshot)
	}
	opened, err := service.Apply(ctx, sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-after-reset"})
	if err != nil || !opened.NewResponse {
		t.Fatalf("open after reset = %+v, err=%v", opened, err)
	}
}

func TestResetRejectsStaleIDFromRebindingScheduledLifecycle(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
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
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-old"})
	bound := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventBindScheduledBoundary, ResponseID: "response-old"})
	if !bound.Accepted {
		t.Fatalf("initial scheduled binding = %+v, want accepted", bound)
	}
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventReset})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventEnsureScheduled, Count: 1})

	lateBoundary := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventBindScheduledBoundary, ResponseID: "response-old"})
	lateOwner := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventSetScheduledOwner, Index: 0, ResponseID: "response-old"})
	if lateBoundary.Accepted || lateOwner.Accepted {
		t.Fatalf("stale response ID rebound after reset: boundary=%+v owner=%+v", lateBoundary, lateOwner)
	}
	snapshot := service.Snapshot()
	if len(snapshot.Scheduled) != 1 || snapshot.Scheduled[0].Bound || len(snapshot.Scheduled[0].ResponseIDs) != 0 || snapshot.ActiveScheduledSet {
		t.Fatalf("stale scheduled binding mutated reset lifecycle: %+v", snapshot)
	}

	fresh := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventBindScheduledBoundary, ResponseID: "response-new"})
	if !fresh.Accepted || !fresh.HasScheduledIndex || fresh.ScheduledIndex != 0 {
		t.Fatalf("fresh scheduled ID after stale rejection = %+v", fresh)
	}
}

func TestScheduledLifecycleRejectsInvalidDispositionAndDisposedRebind(t *testing.T) {
	for _, terminalDisposition := range []sessiondiagnostics.Disposition{
		sessiondiagnostics.DispositionCompleted,
		sessiondiagnostics.DispositionCancelled,
	} {
		t.Run(string(terminalDisposition), func(t *testing.T) {
			service := NewService(sessiondiagnostics.Options{})
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
			apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventBindScheduledID, Index: 0, ResponseID: "response-original"})
			apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventSetScheduledOwner, Index: 0, ResponseID: "response-original"})

			if _, err := service.Apply(ctx, sessiondiagnostics.Event{
				Kind:        sessiondiagnostics.EventScheduledDisposition,
				ResponseID:  "response-original",
				Disposition: sessiondiagnostics.Disposition("bogus"),
			}); !errors.Is(err, sessiondiagnostics.ErrMalformedSequence) {
				t.Fatalf("invalid disposition error = %v, want ErrMalformedSequence", err)
			}
			if got := service.Snapshot().Scheduled[0].Disposition; got != sessiondiagnostics.DispositionPending {
				t.Fatalf("invalid disposition changed lifecycle to %q", got)
			}

			apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventScheduledDisposition, ResponseID: "response-original", Disposition: terminalDisposition})
			rebind := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventBindScheduledID, Index: 0, ResponseID: "response-rebound"})
			owner := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventSetScheduledOwner, Index: 0, ResponseID: "response-rebound"})
			if rebind.Accepted || owner.Accepted {
				t.Fatalf("disposed lifecycle was rebound: bind=%+v owner=%+v", rebind, owner)
			}
			snapshot := service.Snapshot()
			if snapshot.Scheduled[0].Disposition != terminalDisposition || len(snapshot.Scheduled[0].ResponseIDs) != 1 || snapshot.Scheduled[0].ResponseIDs[0] != "response-original" {
				t.Fatalf("disposed lifecycle mutated during rebind: %+v", snapshot.Scheduled[0])
			}
		})
	}
}

func TestToolContinuationRemainsOneScheduledLifecycle(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
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
	if !end.Candidate || !end.Admitted || end.PendingContinuations != 0 {
		t.Fatalf("continuation end = %+v, want admitted candidate with no pending continuation", end)
	}
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventScheduledDisposition, ResponseID: "response-continuation", Disposition: sessiondiagnostics.DispositionCompleted})
	if got := service.Snapshot().CompletedScheduled; got != 1 {
		t.Fatalf("completed scheduled = %d, want 1", got)
	}
	duplicate := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventScheduledDisposition, ResponseID: "response-continuation", Disposition: sessiondiagnostics.DispositionCompleted})
	if duplicate.Accepted || service.Snapshot().CompletedScheduled != 1 {
		t.Fatalf("duplicate disposition was accepted or changed snapshot: %+v", duplicate)
	}
}

func TestToolContinuationBindsBeforeExplicitRequestObservation(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
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
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventToolCall, ResponseID: "response-tool", CallID: "call-early", ToolName: "lookup"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, ResponseID: "response-tool"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventToolResultAccepted, CallID: "call-early"})

	// The provider response can arrive from the synchronous response.create
	// send before the adapter observes EventContinuationRequested.
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-continuation"})
	bound := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventBindScheduledBoundary, ResponseID: "response-continuation"})
	if !bound.Accepted || !bound.HasScheduledIndex || bound.ScheduledIndex != 0 {
		t.Fatalf("early continuation ownership = %+v, want scheduled index 0", bound)
	}

	terminal := &sessiondiagnostics.Terminal{Status: "failed", ErrorCode: "rate_limit_exceeded", ErrorMessage: "Please try again in 0.01s"}
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, ResponseID: "response-continuation", Terminal: terminal})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventNoteScheduledTerminal, ResponseID: "response-continuation", Terminal: terminal})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventRememberRetry, ResponseID: "response-continuation", Terminal: terminal})
	retry := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventClaimRetry, ResponseID: "response-continuation", Terminal: terminal})
	if !retry.Retry.Accepted || retry.Retry.Delay != 10*time.Millisecond {
		t.Fatalf("early continuation retry = %+v, want accepted 10ms retry", retry.Retry)
	}
}

func TestUnscheduledToolContinuationAdoptsResponseID(t *testing.T) {
	service := NewService(sessiondiagnostics.Options{})
	ctx := context.Background()
	apply := func(event sessiondiagnostics.Event) sessiondiagnostics.Observation {
		t.Helper()
		observation, err := service.Apply(ctx, event)
		if err != nil {
			t.Fatalf("apply %s: %v", event.Kind, err)
		}
		return observation
	}
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-tool"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventToolCall, ResponseID: "response-tool", CallID: "call-1", ToolName: "lookup"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, ResponseID: "response-tool", Output: true})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventToolResultAccepted, CallID: "call-1"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventContinuationRequested})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, Role: sessiondiagnostics.RoleTool, CallID: "call-1"})
	apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseOpen, ResponseID: "response-continuation"})
	end := apply(sessiondiagnostics.Event{Kind: sessiondiagnostics.EventResponseEnd, ResponseID: "response-continuation", Output: true, Terminal: &sessiondiagnostics.Terminal{Status: "completed", Reason: "provider_authored_completion"}})
	if !end.Candidate || !end.Admitted || end.PendingContinuations != 0 {
		t.Fatalf("unscheduled continuation end = %+v", end)
	}
}

func TestRateLimitRetryUsesInjectedSchedulerAndOneBudget(t *testing.T) {
	var scheduled []time.Duration
	service := NewService(sessiondiagnostics.Options{RetryScheduler: func(_ context.Context, delay time.Duration) error {
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
	service := NewService(sessiondiagnostics.Options{})
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
