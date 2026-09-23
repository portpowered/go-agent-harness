package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
	failurewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure/wire"
)

func assertContract() (map[string]any, error) {
	var first sessionfailure.Service
	first = failurewire.NewService(sessionfailure.Dependencies{Publish: func(observation sessionfailure.Observation) bool {
		snapshot := first.Snapshot()
		if snapshot == nil || snapshot.Facts != observation.Facts {
			return false
		}
		return true
	}})
	second := failurewire.NewService(sessionfailure.Dependencies{})
	if first == nil || second == nil {
		return nil, fmt.Errorf("Wire returned a nil service")
	}
	if second.Snapshot() != nil {
		return nil, fmt.Errorf("independent Wire service inherited first state")
	}
	original := &os.PathError{Op: "provider", Path: "session", Err: errors.New("consumer provider failure")}
	facts, cause := first.NormalizeErrorValue(&messages.ErrorValue{Message: "provider failed", Err: original, Code: "E_CONSUMER"})
	if facts.Classification != sessionfailure.ErrorClassUnknown || facts.TerminalReason != string(messages.TerminalReasonTerminalFailure) || cause != original {
		return nil, fmt.Errorf("normalization mismatch: facts=%#v cause=%v", facts, cause)
	}
	if !first.Accept(facts, cause) {
		return nil, fmt.Errorf("first observation was rejected")
	}
	snapshot := first.Snapshot()
	var pathError *os.PathError
	if snapshot == nil || !errors.Is(snapshot.Err, original) || !errors.As(snapshot.Err, &pathError) || pathError != original || snapshot.Facts.Code != "E_CONSUMER" {
		return nil, fmt.Errorf("original error or provider code was lost: %#v", snapshot)
	}
	nonTerminal, _ := first.NormalizeErrorValue(messages.NewNonTerminalErrorValue("informational", "notice"))
	if nonTerminal.FailingEvent != "" || first.Accept(nonTerminal, nil) {
		return nil, fmt.Errorf("non-terminal diagnostic became a failure")
	}
	cancelled, _ := first.NormalizeErrorValue(&messages.ErrorValue{Classification: sessionfailure.ErrorClassCancellation})
	if cancelled.FailingEvent != "" || first.Accept(cancelled, nil) {
		return nil, fmt.Errorf("cancellation became a failure")
	}
	closeFacts := second.NormalizeClose(&messages.SessionCloseValue{Reason: "provider_closed", TerminalReason: messages.TerminalReasonProviderClose}, sessionfailure.Progress{SessionOpened: true, TurnsCompleted: 1})
	if closeFacts.OutputState != string(messages.TerminalOutputPartial) || closeFacts.Provenance != string(messages.TerminalProvenanceSession) {
		return nil, fmt.Errorf("provider close mismatch: %#v", closeFacts)
	}
	projection := second.Projection(sessionfailure.ProjectionToolContinuation, "run", sessionfailure.Progress{SessionOpened: true, TurnsCompleted: 1})
	if projection.Classification != sessionfailure.ClassificationToolContinuation || projection.OutputState != string(messages.TerminalOutputPartial) {
		return nil, fmt.Errorf("projection mismatch: %#v", projection)
	}
	return map[string]any{"status": "accepted", "wire": true, "cli_imports": false, "private_imports": false, "original_error": true, "nonterminal_excluded": true, "cancellation_excluded": true, "provider_close_output": closeFacts.OutputState, "projection": projection.Classification}, nil
}

func main() {
	report, err := assertContract()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
