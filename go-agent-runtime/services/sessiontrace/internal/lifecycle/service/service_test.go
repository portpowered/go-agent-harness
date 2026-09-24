package service

import (
	"context"
	"errors"
	"testing"
	"time"

	lifecycle "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/internal/lifecycle"
)

func applyEvent(t *testing.T, service lifecycle.Service, event lifecycle.Event) lifecycle.Observation {
	t.Helper()
	observation, err := service.Apply(context.Background(), event)
	if err != nil {
		t.Fatalf("Apply(%+v): %v", event, err)
	}
	return observation
}

func terminal(status, reason, code, message string) *lifecycle.Terminal {
	return &lifecycle.Terminal{
		Status:        status,
		Reason:        reason,
		ErrorCode:     code,
		ErrorMessage:  message,
		StatusDetails: " code=" + code + ",message=" + message + "\x00  ",
	}
}

func TestResponseLifecyclePreservesIdentityAndRejectsLateEvents(t *testing.T) {
	service := New(lifecycle.Options{})

	opened := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOpen, ResponseID: " response-1 "})
	if !opened.NewResponse || opened.ResponseID != "response-1" {
		t.Fatalf("open = %+v, want a normalized new response", opened)
	}
	if belongs := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseBelongs, ResponseID: "foreign"}); belongs.Accepted {
		t.Fatalf("foreign response belongs = %+v, want rejection", belongs)
	}
	if content := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseContent, ResponseID: "foreign"}); content.Accepted {
		t.Fatalf("foreign content = %+v, want rejection", content)
	}
	if content := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseContent, ResponseID: "response-1"}); !content.Accepted {
		t.Fatalf("owned content = %+v, want acceptance", content)
	}

	ended := applyEvent(t, service, lifecycle.Event{
		Kind:       lifecycle.EventResponseEnd,
		ResponseID: "response-1",
		Output:     true,
		Terminal:   terminal("completed", "provider_authored_completion", "", ""),
	})
	if !ended.Candidate || ended.ResponseID != "response-1" {
		t.Fatalf("end = %+v, want admitted response-1", ended)
	}
	if lateContent := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseContent, ResponseID: "response-1"}); lateContent.Accepted {
		t.Fatalf("late content = %+v, want rejection after MESSAGE.END", lateContent)
	}
	if duplicate := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseEnd, ResponseID: "response-1", Output: true}); duplicate.Accepted {
		t.Fatalf("duplicate end = %+v, want ignored", duplicate)
	}
	if finished := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseFinish, ResponseID: "response-1"}); !finished.Accepted {
		t.Fatalf("finish = %+v, want acceptance", finished)
	}
	if staleBoundary := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseContentBoundary, ResponseID: "response-1"}); staleBoundary.Accepted {
		t.Fatalf("stale content boundary = %+v, want rejection after finish", staleBoundary)
	}
	if owns := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOwnsEnd}); !owns.Accepted || !owns.OwnsResponse {
		t.Fatalf("unidentified end ownership = %+v, want an idle boundary", owns)
	}
	if late := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOpen, ResponseID: "response-1"}); late.NewResponse {
		t.Fatalf("late response reopened lifecycle: %+v", late)
	}

	service.Reset()
	if reopened := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOpen, ResponseID: "response-1"}); reopened.NewResponse {
		t.Fatalf("retired response reopened after reset: %+v", reopened)
	}
}

func TestResponseLifecycleAdoptsUnidentifiedResponseAndControlsReplacement(t *testing.T) {
	service := New(lifecycle.Options{})
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOpen})
	if content := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseContent}); !content.Accepted {
		t.Fatalf("unidentified response content = %+v, want acceptance", content)
	}
	if adopted := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseAdopt, ResponseID: "late-id"}); !adopted.Accepted {
		t.Fatalf("adopt = %+v, want acceptance", adopted)
	}
	if belongs := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseBelongs, ResponseID: "late-id"}); !belongs.Accepted {
		t.Fatalf("adopted response belongs = %+v, want acceptance", belongs)
	}
	if foreign := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOpen, ResponseID: "foreign"}); foreign.NewResponse {
		t.Fatalf("foreign active response replaced lifecycle: %+v", foreign)
	}
	if end := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseEnd, ResponseID: "late-id", Output: true}); !end.Candidate {
		t.Fatalf("adopted response end = %+v, want candidate", end)
	}
	if replacement := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOpen, ResponseID: "replacement"}); !replacement.NewResponse {
		t.Fatalf("replacement open = %+v, want new response", replacement)
	}
	if content := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseContentBoundary, ResponseID: "replacement"}); !content.Accepted {
		t.Fatalf("replacement content boundary = %+v, want acceptance", content)
	}
	if owns := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOwnsEnd, ResponseID: "replacement"}); !owns.OwnsResponse {
		t.Fatalf("replacement end ownership = %+v, want ownership", owns)
	}
}

func TestToolContinuationRequiresAcceptedResultAndRetainsTerminalFacts(t *testing.T) {
	service := New(lifecycle.Options{})
	if orphan := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventToolResultAccepted, CallID: "orphan"}); orphan.Accepted {
		t.Fatalf("orphan result = %+v, want rejection", orphan)
	}
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOpen, ResponseID: "tool-response"})
	if call := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventToolCall, ResponseID: "tool-response", CallID: "call-1", ToolName: "lookup"}); !call.Accepted {
		t.Fatalf("tool call = %+v, want acceptance", call)
	}
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventToolResultAccepted, CallID: "call-1"})
	if requested := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventContinuationRequested, CallID: "call-1"}); !requested.Accepted || requested.PendingContinuations != 1 {
		t.Fatalf("continuation request = %+v, want one pending continuation", requested)
	}
	if malformed := func() error {
		_, err := service.Apply(context.Background(), lifecycle.Event{Kind: lifecycle.EventContinuationRequested, CallID: "call-1"})
		return err
	}(); !errors.Is(malformed, lifecycle.ErrMalformedSequence) {
		t.Fatalf("duplicate continuation error = %v, want malformed sequence", malformed)
	}
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseEnd, ResponseID: "tool-response", Output: false})
	if opened := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOpen, ResponseID: "continuation-response"}); !opened.NewResponse {
		t.Fatalf("continuation open = %+v, want new response", opened)
	}
	ended := applyEvent(t, service, lifecycle.Event{
		Kind:       lifecycle.EventResponseEnd,
		ResponseID: "continuation-response",
		Output:     true,
		Terminal:   terminal("completed", "provider_authored_completion", "", ""),
	})
	if !ended.Candidate || ended.PendingContinuations != 0 {
		t.Fatalf("continuation end = %+v, want complete candidate", ended)
	}
	states := service.Snapshot().ContinuationStates
	if len(states) != 1 || !states[0].ContinuationComplete || states[0].ToolName != "lookup" {
		t.Fatalf("continuation snapshot = %+v, want completed lookup state", states)
	}
}

func TestToolLifecycleReconcilesRejectedResultBeforeCompletion(t *testing.T) {
	service := New(lifecycle.Options{})
	orphan := applyEvent(t, service, lifecycle.Event{
		Kind:       lifecycle.EventToolCall,
		ResponseID: "foreign-response",
		CallID:     "call-1",
		ToolName:   "lookup",
	})
	if orphan.Accepted {
		t.Fatalf("foreign tool call = %+v, want rejection", orphan)
	}
	rejected := applyEvent(t, service, lifecycle.Event{
		Kind:         lifecycle.EventToolResultRejected,
		CallID:       "call-1",
		ResultStatus: "cancelled",
	})
	if !rejected.Accepted {
		t.Fatalf("tool result rejection = %+v, want accepted lifecycle evidence", rejected)
	}
	snapshot := service.Snapshot().ContinuationStates
	if len(snapshot) != 1 || !snapshot[0].ProviderCallObserved || !snapshot[0].ResultRejected || snapshot[0].ResultRejectionStatus != "cancelled" {
		t.Fatalf("rejected continuation = %+v, want retained provider failure", snapshot)
	}
	if accepted := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventToolResultAccepted, CallID: "call-1"}); !accepted.Accepted {
		t.Fatalf("later tool result acceptance = %+v", accepted)
	}
	if completed := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventToolResponseComplete, CallID: "call-1"}); !completed.Accepted {
		t.Fatalf("tool response completion = %+v", completed)
	}
	snapshot = service.Snapshot().ContinuationStates
	if len(snapshot) != 1 || !snapshot[0].ToolResponseComplete || snapshot[0].ResultRejected || snapshot[0].ResultRejectionStatus != "" {
		t.Fatalf("completed continuation = %+v, want accepted completed result", snapshot)
	}
}

func TestScheduledLifecycleClearsTerminalFailureWhenRetryIsDispatched(t *testing.T) {
	service := New(lifecycle.Options{})
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventEnsureScheduled, Count: 1})
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventBindScheduledBoundary, ResponseID: "scheduled-1"})
	failure := terminal("failed", "terminal_failure", "rate_limit_exceeded", "Please try again in 2s")
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventNoteScheduledTerminal, ResponseID: "scheduled-1", Terminal: failure})
	remembered := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventRememberRetry, ResponseID: "scheduled-1", Terminal: failure})
	if !remembered.Accepted || !remembered.Retry.Accepted {
		t.Fatalf("retry candidate = %+v, want an eligible retry", remembered)
	}
	dispatched := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventRetryDispatched})
	if !dispatched.Accepted || !dispatched.HasScheduledIndex || dispatched.ScheduledIndex != 0 {
		t.Fatalf("retry dispatch = %+v, want scheduled index 0", dispatched)
	}
	snapshot := service.Snapshot()
	if len(snapshot.Scheduled) != 1 || !snapshot.Scheduled[0].RetryUsed || !snapshot.Scheduled[0].RetryPending || snapshot.Scheduled[0].TerminalFailure {
		t.Fatalf("retry-dispatched schedule = %+v, want pending retry without stale failure", snapshot.Scheduled)
	}
}

func TestScheduledLifecycleHandlesDispositionAndRetryBoundaries(t *testing.T) {
	var scheduledDelay time.Duration
	service := New(lifecycle.Options{RetryScheduler: func(_ context.Context, delay time.Duration) error {
		scheduledDelay = delay
		return nil
	}})
	if invalid := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventEnsureScheduled, Count: -1}); invalid.Accepted {
		t.Fatalf("negative schedule = %+v, want rejection", invalid)
	}
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventEnsureScheduled, Count: 2})
	if bound := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventBindScheduledBoundary, ResponseID: "scheduled-1"}); !bound.HasScheduledIndex || bound.ScheduledIndex != 0 {
		t.Fatalf("first scheduled bind = %+v, want index 0", bound)
	}
	if conflict := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventBindScheduledID, Index: 1, ResponseID: "scheduled-1"}); conflict.Accepted {
		t.Fatalf("duplicate schedule owner = %+v, want rejection", conflict)
	}
	failure := terminal("failed", "terminal_failure", "rate_limit_exceeded", "Please try again in 3.5s")
	if noted := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventNoteScheduledTerminal, ResponseID: "scheduled-1", Terminal: failure}); !noted.Accepted {
		t.Fatalf("scheduled failure = %+v, want acceptance", noted)
	}
	remembered := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventRememberRetry, ResponseID: "scheduled-1", Terminal: failure})
	if !remembered.Accepted || remembered.Retry.Delay != 3500*time.Millisecond {
		t.Fatalf("retry candidate = %+v, want 3.5s delay", remembered)
	}
	claimed, err := service.Apply(context.Background(), lifecycle.Event{Kind: lifecycle.EventClaimRetry, ResponseID: "scheduled-1", Terminal: failure})
	if err != nil || !claimed.Retry.Accepted || scheduledDelay != 3500*time.Millisecond {
		t.Fatalf("retry claim = %+v, err=%v, scheduled delay=%s", claimed, err, scheduledDelay)
	}
	if rebound := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventBindScheduledBoundary, ResponseID: "scheduled-2"}); !rebound.Accepted {
		t.Fatalf("retry rebound = %+v, want acceptance", rebound)
	}
	if exhausted, err := service.Apply(context.Background(), lifecycle.Event{Kind: lifecycle.EventClaimRetry, ResponseID: "scheduled-2", Terminal: failure}); !errors.Is(err, lifecycle.ErrRetryExhausted) || !exhausted.Retry.Exhausted {
		t.Fatalf("second retry = %+v, err=%v, want exhausted", exhausted, err)
	}
	if disposition, err := service.Apply(context.Background(), lifecycle.Event{Kind: lifecycle.EventScheduledDisposition, ResponseID: "scheduled-2", Disposition: lifecycle.DispositionCompleted}); err != nil || !disposition.Accepted {
		t.Fatalf("completed disposition = %+v, err=%v", disposition, err)
	}
	if invalid, err := service.Apply(context.Background(), lifecycle.Event{Kind: lifecycle.EventScheduledDisposition, ResponseID: "scheduled-1", Disposition: lifecycle.Disposition("unknown")}); !errors.Is(err, lifecycle.ErrMalformedSequence) || invalid.Accepted {
		t.Fatalf("invalid disposition = %+v, err=%v", invalid, err)
	}
}

func TestScheduledLifecycleSupportsUnidentifiedCompletionAndSnapshotCopies(t *testing.T) {
	service := New(lifecycle.Options{})
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventEnsureScheduled, Count: 1})
	if disposition, err := service.Apply(context.Background(), lifecycle.Event{Kind: lifecycle.EventScheduledDisposition, Disposition: lifecycle.DispositionCompleted}); err != nil || !disposition.Accepted {
		t.Fatalf("unidentified disposition = %+v, err=%v", disposition, err)
	}
	snapshot := service.Snapshot()
	if len(snapshot.Scheduled) != 1 || snapshot.CompletedScheduled != 1 {
		t.Fatalf("snapshot = %+v, want one completed scheduled response", snapshot)
	}
	snapshot.Scheduled[0].ResponseIDs = append(snapshot.Scheduled[0].ResponseIDs, "mutated")
	if len(service.Snapshot().Scheduled[0].ResponseIDs) != 0 {
		t.Fatal("snapshot exposed mutable scheduled response IDs")
	}
}

func TestLifecycleCloseIsBoundedAndResetRetiresAllKnownIDs(t *testing.T) {
	service := New(lifecycle.Options{})
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOpen, ResponseID: "active"})
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := service.Snapshot(); !got.Closed || got.ActiveResponse {
		t.Fatalf("closed snapshot = %+v, want closed and inactive", got)
	}
	closedErr := func() error {
		_, err := service.Apply(context.Background(), lifecycle.Event{Kind: lifecycle.EventResponseOpen, ResponseID: "late"})
		return err
	}()
	if !errors.Is(closedErr, lifecycle.ErrClosed) || closedErr.Error() != lifecycle.ErrClosed.Error() {
		t.Fatalf("closed apply error = %v, want stable ErrClosed", closedErr)
	}
	var nilService *reducer
	if err := nilService.Close(); err != nil || !nilService.Snapshot().Closed {
		t.Fatalf("nil service close/snapshot = %v, %+v", err, nilService.Snapshot())
	}
}

func TestScheduledLifecycleBindsTerminalOnlyRetryAndAllowsReplacementBoundary(t *testing.T) {
	service := New(lifecycle.Options{})
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventEnsureScheduled, Count: 2})
	if bound := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventBindScheduledBoundary, ResponseID: "scheduled-1"}); !bound.Accepted {
		t.Fatalf("initial schedule bind = %+v, want acceptance", bound)
	}
	failure := terminal("failed", "terminal_failure", "rate_limit_exceeded", "Please try again in 2s")
	applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventNoteScheduledTerminal, ResponseID: "scheduled-1", Terminal: failure})
	if retry := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventRememberRetry, ResponseID: "scheduled-1", Terminal: failure}); !retry.Retry.Accepted && !retry.Accepted {
		t.Fatalf("retry candidate = %+v, want acceptance", retry)
	}
	if claimed, err := service.Apply(context.Background(), lifecycle.Event{Kind: lifecycle.EventClaimRetry, ResponseID: "scheduled-1", Terminal: failure}); err != nil || !claimed.Retry.Accepted {
		t.Fatalf("retry claim = %+v, err=%v", claimed, err)
	}
	if rebound := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventBindScheduledTerminalOnly, ResponseID: "scheduled-2"}); !rebound.Accepted {
		t.Fatalf("terminal-only retry bind = %+v, want acceptance", rebound)
	}
	if replacement := applyEvent(t, service, lifecycle.Event{Kind: lifecycle.EventResponseOpen, ResponseID: "replacement"}); !replacement.Accepted || replacement.NewResponse {
		t.Fatalf("replacement response = %+v, want adoption of the terminal-only active boundary", replacement)
	}
	if snapshot := service.Snapshot(); snapshot.ActiveResponseID != "replacement" || !snapshot.ActiveResponse {
		t.Fatalf("replacement snapshot = %+v, want replacement active", snapshot)
	}
}
