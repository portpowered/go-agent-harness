// Command room-terminal-consumer exercises roomterminal only from a separate
// module. It has no CLI, host configuration, credentials, environment reads,
// provider transport, or hidden runtime initialization.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomterminal"
	roomterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomterminal/wire"
)

const wrongOracleArgument = "wrong-oracle"

type report struct {
	Status             string            `json:"status"`
	Marker             string            `json:"marker"`
	Instances          int               `json:"instances"`
	Independent        bool              `json:"independent"`
	Success            bool              `json:"success"`
	Failure            bool              `json:"failure"`
	Cancellation       bool              `json:"cancellation"`
	RoomBound          bool              `json:"room_bound"`
	Trigger            string            `json:"trigger"`
	DiagnosticFields   map[string]string `json:"diagnostic_fields"`
	RedactionSafe      bool              `json:"redaction_safe"`
	CloseIdempotent    bool              `json:"close_idempotent"`
	NoCrossInstanceMap bool              `json:"no_cross_instance_map"`
}

func main() {
	wrongOracle := len(os.Args) == 2 && os.Args[1] == wrongOracleArgument
	if len(os.Args) > 1 && !wrongOracle {
		fail(fmt.Errorf("unknown argument %q", os.Args[1]))
	}
	result, err := run(wrongOracle)
	if err != nil {
		fail(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		fail(err)
	}
	fmt.Println(string(encoded))
}

func run(wrongOracle bool) (report, error) {
	one := roomterminalwire.NewService()
	two := roomterminalwire.NewService()
	if one == two {
		return report{}, errors.New("Wire returned one shared service instance")
	}
	end := one.MessageEnd("response-1", &messages.MessageEndValue{})
	wantReason := string(messages.TerminalReasonProviderAuthoredCompletion)
	if wrongOracle {
		wantReason = string(messages.TerminalReasonLoopSynthesizedCompletion)
	}
	if end.TerminalReason != wantReason || end.TerminalProvenance != string(messages.TerminalProvenanceProvider) || end.OutputState != string(messages.TerminalOutputComplete) {
		return report{}, fmt.Errorf("wrong-oracle assertion: success=%+v expected_reason=%q", end, wantReason)
	}
	secret := "credential-secret-must-not-cross"
	failureCause := errors.New(secret)
	failure := one.Failure(roomterminal.FailureFacts{Classification: "transport", TerminalReason: string(messages.TerminalReasonTerminalFailure), Provenance: string(messages.TerminalProvenanceProvider), OutputState: string(messages.TerminalOutputPartial), Code: "transport", FailingEvent: "STREAM.ERROR"}, failureCause)
	if !failure.Failure || !errors.Is(failure.Err, failureCause) {
		return report{}, fmt.Errorf("failure identity was not preserved: %+v", failure)
	}
	cancel := one.Cancellation(messages.TerminalOutputNone, false)
	bound := one.Cancellation(messages.TerminalOutputPartial, true)
	trigger := one.BoundTrigger(roomterminal.RoomTerminationMaxDurationReached, true)
	result := roomterminal.ParticipantResult{TerminationTrigger: trigger, TerminationDisposition: roomterminal.ParticipantTerminationDispositionCancelledAfterGrace, Classification: bound.Classification, TerminalReason: bound.TerminalReason, TerminalProvenance: bound.TerminalProvenance, OutputState: bound.OutputState, Reason: "error"}
	fields := one.Fields(result)
	if cancel.Classification != "cancellation" || !bound.RoomBound || trigger != roomterminal.ParticipantTerminationTriggerMaxDurationReachedMidResponse || fields["classification"] != bound.Classification {
		return report{}, fmt.Errorf("terminal projection failed: cancel=%+v bound=%+v trigger=%q fields=%v", cancel, bound, trigger, fields)
	}
	diagnostic := one.Diagnostic(result)
	if diagnostic.Event != roomterminal.DiagnosticEventRoomBound || diagnostic.Fields["termination_trigger"] != trigger {
		return report{}, fmt.Errorf("diagnostic projection failed: %+v", diagnostic)
	}
	encodedDiagnostic, err := json.Marshal(diagnostic)
	if err != nil {
		return report{}, fmt.Errorf("diagnostic encoding: %w", err)
	}
	redactionSafe := !strings.Contains(string(encodedDiagnostic), secret) && !strings.Contains(string(encodedDiagnostic), "provider prose")
	if !redactionSafe {
		return report{}, fmt.Errorf("diagnostic redaction failed: %s", encodedDiagnostic)
	}
	mutated := one.Fields(result)
	mutated["classification"] = "mutated-only-instance-one"
	independentMap := two.Fields(result)["classification"] == bound.Classification
	if !independentMap {
		return report{}, errors.New("service instances shared mutable field state")
	}
	var callbackCount int
	one.RecordBound(roomterminal.BoundDiagnosticRequest{ParticipantID: "participant-1", Result: result, OnDiagnostic: func(string, roomterminal.DiagnosticRecord) { callbackCount++ }})
	if callbackCount != 1 {
		return report{}, fmt.Errorf("bound callback count = %d", callbackCount)
	}
	closeIdempotent := one.Close() == nil && one.Close() == nil && two.Close() == nil && two.Close() == nil
	if !closeIdempotent {
		return report{}, errors.New("close was not idempotent")
	}
	return report{Status: "ok", Marker: "C94_ROOM_TERMINAL_EXTERNAL", Instances: 2, Independent: true, Success: true, Failure: true, Cancellation: true, RoomBound: true, Trigger: trigger, DiagnosticFields: diagnostic.Fields, RedactionSafe: redactionSafe, CloseIdempotent: closeIdempotent, NoCrossInstanceMap: independentMap}, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
