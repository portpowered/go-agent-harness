package wire

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
)

func TestNewServiceReturnsIndependentUsableServices(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil {
		t.Fatal("Wire returned nil service")
	}
	if got := first.CancellationOutputState(sessionterminal.OutputSnapshot{}); got != "none" {
		t.Fatalf("first service output state = %q", got)
	}
	if got := second.CancellationOutputState(sessionterminal.OutputSnapshot{TurnsCompleted: 1}); got != "partial" {
		t.Fatalf("second service output state = %q", got)
	}
}

func TestReporterContextAndIndependentFailureClassification(t *testing.T) {
	reporter := NewReporter()
	if reporter == nil {
		t.Fatal("NewReporter returned nil")
	}
	ctx := WithReporter(context.Background(), reporter)
	if got := ReporterFromContext(ctx); got != reporter {
		t.Fatal("ReporterFromContext did not return the invocation reporter")
	}
	if got := ReporterFromContext(context.Background()); got != nil {
		t.Fatalf("ReporterFromContext without a reporter = %v, want nil", got)
	}
	if HasIndependentFailure(errors.Join(context.Canceled, sessionterminal.ErrDurationExpired)) {
		t.Fatal("cancellation and planned expiry classified as independent failures")
	}
	if !HasIndependentFailure(errors.Join(context.Canceled, errors.New("artifact flush failed"))) {
		t.Fatal("artifact failure was hidden by a cancellation cause")
	}
}

func TestTerminationBoundaryPreservesCleanupOrderAndRunsOnce(t *testing.T) {
	var calls []string
	boundary := NewTerminationBoundary(sessionterminal.TerminationOptions{
		QuiesceUpstream: func() error { calls = append(calls, "quiesce"); return nil },
		WaitForStragglers: func() error {
			calls = append(calls, "drain")
			return nil
		},
		StopOwnedResources: func() error { calls = append(calls, "stop"); return nil },
		FlushBuffered:      func() error { calls = append(calls, "flush"); return nil },
	})
	primary := errors.New("session failed")
	first := boundary.Terminate(primary)
	second := boundary.Terminate(errors.New("later failure"))
	if !errors.Is(first, primary) || second != first {
		t.Fatalf("termination results = (%v, %v), want stable primary failure", first, second)
	}
	want := []string{"quiesce", "drain", "stop", "flush"}
	if len(calls) != len(want) {
		t.Fatalf("cleanup calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("cleanup calls = %v, want %v", calls, want)
		}
	}
}
