package wire

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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

func TestPublicWireReporterAndTerminationContracts(t *testing.T) {
	reporter := NewReporter()
	if reporter == nil {
		t.Fatal("NewReporter returned nil")
	}
	ctx := WithReporter(context.Background(), reporter)
	if got := ReporterFromContext(ctx); got != reporter {
		t.Fatal("reporter context did not round-trip the public reporter")
	}
	if !HasIndependentFailure(errors.New("provider failed")) || !IsCancellation(context.Canceled) {
		t.Fatal("Wire error classification did not preserve provider and cancellation behavior")
	}

	boundary := NewTerminationBoundary(sessionterminal.TerminationOptions{})
	if boundary == nil {
		t.Fatal("NewTerminationBoundary returned nil")
	}
	if err := boundary.Terminate(nil); err == nil {
		t.Fatal("termination boundary accepted a missing straggler drain")
	}
}

func TestPublicTerminalContractPreservesDurationBoundaryAndFormatsTerminalMetadata(t *testing.T) {
	service := NewService()
	primary := errors.New("duration expired")
	lifecycle := sessionterminal.LifecycleSnapshot{
		UnresolvedToolResultCallIDs:  []string{"call-1"},
		UnresolvedToolResultStatuses: map[string]string{"call-1": "rejected"},
	}
	if err := service.Enrich(sessionterminal.Request{RunError: primary, DurationExpired: true, Lifecycle: lifecycle}); !errors.Is(err, primary) || errors.Is(err, sessionterminal.ErrUnresolvedToolResults) {
		t.Fatalf("duration boundary enrichment = %v, want original expiry without an incomplete-tool error", err)
	}
	enriched := service.Enrich(sessionterminal.Request{RunError: primary, Lifecycle: lifecycle})
	var unresolved *sessionterminal.UnresolvedToolResultsError
	if !errors.Is(enriched, primary) || !errors.Is(enriched, sessionterminal.ErrUnresolvedToolResults) || !errors.As(enriched, &unresolved) || len(unresolved.UnresolvedCallIDs()) != 1 || unresolved.UnresolvedCallIDs()[0] != "call-1" {
		t.Fatalf("ordinary terminal enrichment = %v, want primary and unresolved tool call", enriched)
	}

	value := messages.NewSessionCloseValueWithTerminal("session-1", "expired", string(sessionterminal.MaxDurationReason), sessionterminal.MaxDurationReason, messages.TerminalProvenanceLoop, messages.TerminalOutputPartial)
	var output bytes.Buffer
	if err := service.WriteSessionClose(&output, value, false); err != nil {
		t.Fatalf("WriteSessionClose: %v", err)
	}
	want := "[session closed: expired]\n[session terminal: classification=max_duration terminal_reason=max_duration terminal_provenance=loop output_state=partial]\n"
	if output.String() != want {
		t.Fatalf("terminal transcript = %q, want %q", output.String(), want)
	}
	fields := service.ErrorFields(&messages.ErrorValue{
		Message:            "provider rejected input",
		Classification:     "provider_rejected",
		TerminalReason:     messages.TerminalReasonTerminalFailure,
		TerminalProvenance: messages.TerminalProvenanceProvider,
		OutputState:        messages.TerminalOutputNone,
		ErrorType:          "invalid_request",
		Code:               "bad_parameter",
	})
	for _, want := range []string{"classification=provider_rejected", "terminal_reason=terminal_failure", "error_type=invalid_request", "code=bad_parameter"} {
		if !bytes.Contains([]byte(fields), []byte(want)) {
			t.Fatalf("public error fields %q do not contain %q", fields, want)
		}
	}
}
