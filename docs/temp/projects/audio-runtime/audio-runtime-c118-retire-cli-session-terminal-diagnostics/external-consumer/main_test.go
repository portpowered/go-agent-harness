package consumer_test

import (
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	terminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
)

func TestExternalConsumerUsesOnlyPublicTerminalContract(t *testing.T) {
	first := terminalwire.NewService()
	second := terminalwire.NewService()
	if first == nil || second == nil || first == second {
		t.Fatal("Wire did not construct two independent service values")
	}

	cause := errors.New("external provider cause")
	result := first.Finalize(sessionterminal.Request{
		RunError:       cause,
		Provider:       "provider",
		Model:          "model",
		TurnsCompleted: 1,
		Output:         sessionterminal.OutputSnapshot{SawSessionOpen: true, TurnsCompleted: 1},
		Lifecycle: sessionterminal.LifecycleSnapshot{
			PendingToolContinuationIDs: []string{"call-z", "call-a"},
			PendingContinuationCallIDs: []string{"call-z", "call-a"},
			FailureHints:               []string{sessionterminal.FailureHintToolContinuationIncomplete},
			PendingContinuations: sessionterminal.ContinuationSnapshot{
				Codes: map[string]string{"call-z": "z-code", "call-a": "a-code"},
			},
		},
		Usage: sessionterminal.TokenSnapshot{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5, Seen: true},
	})
	if !errors.Is(result.Error, cause) {
		t.Fatalf("errors.Is identity lost: %v", result.Error)
	}
	failure := record(t, result, sessionterminal.EventFailure)
	if failure.Fields[sessionterminal.FieldClassification] != "tool_continuation" || failure.Fields[sessionterminal.FieldPendingContinuationCodes] != "call-a=a-code, call-z=z-code" {
		t.Fatalf("unstable public failure: %#v", failure.Fields)
	}
	if result.Accounting == nil || result.Accounting.PromptTokens != 2 || result.Accounting.UsageSemantics != sessionterminal.TokenUsageIncremental {
		t.Fatalf("missing public accounting: %#v", result.Accounting)
	}

	cancelled := second.Finalize(sessionterminal.Request{
		UserCancelled:  true,
		TurnsCompleted: 0,
		Lifecycle: sessionterminal.LifecycleSnapshot{
			PendingContinuationCallIDs: []string{"call-a"},
		},
	})
	terminal := record(t, cancelled, sessionterminal.EventTerminal)
	if terminal.Fields[sessionterminal.FieldTerminalReason] != "cancellation" || terminal.Fields[sessionterminal.FieldOutputState] != "none" {
		t.Fatalf("unexpected cancellation projection: %#v", terminal.Fields)
	}
	if hasEvent(cancelled, sessionterminal.EventFailure) {
		t.Fatal("cancellation became a failure")
	}
	if got := second.CancellationOutputState(sessionterminal.OutputSnapshot{ResponseOutputTextBytes: 1}); got != "partial" {
		t.Fatalf("output-state policy = %q", got)
	}
}

func record(t *testing.T, result sessionterminal.Result, event string) sessionterminal.Record {
	t.Helper()
	for _, record := range result.Records {
		if record.Event == event {
			return record
		}
	}
	t.Fatalf("missing %s in %#v", event, result.Records)
	return sessionterminal.Record{}
}

func hasEvent(result sessionterminal.Result, event string) bool {
	for _, record := range result.Records {
		if record.Event == event {
			return true
		}
	}
	return false
}
