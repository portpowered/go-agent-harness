package service

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionfailure"
)

func terminalFacts() sessionfailure.Facts {
	return sessionfailure.Facts{Classification: "provider_failure", TerminalReason: string(messages.TerminalReasonTerminalFailure), Provenance: string(messages.TerminalProvenanceProvider), OutputState: string(messages.TerminalOutputNone), FailingEvent: string(messages.StreamTypeError)}
}

func TestNormalizeErrorValue(t *testing.T) {
	typed := errors.New("typed provider failure")
	cases := []struct {
		name           string
		value          *messages.ErrorValue
		accepted       bool
		classification string
		reason         string
		provenance     string
		output         string
	}{
		{name: "defaults", value: &messages.ErrorValue{Message: "failed"}, accepted: true, classification: sessionfailure.ErrorClassUnknown, reason: string(messages.TerminalReasonTerminalFailure), provenance: string(messages.TerminalProvenanceProvider), output: string(messages.TerminalOutputNone)},
		{name: "typed fields", value: &messages.ErrorValue{Message: "failed", Err: typed, Classification: "rate_limited", TerminalReason: messages.TerminalReasonReplayDivergence, TerminalProvenance: messages.TerminalProvenanceReplay, OutputState: messages.TerminalOutputPartial, ErrorType: "quota", Code: "429"}, accepted: true, classification: "rate_limited", reason: string(messages.TerminalReasonReplayDivergence), provenance: string(messages.TerminalProvenanceReplay), output: string(messages.TerminalOutputPartial)},
		{name: "non-terminal", value: messages.NewNonTerminalErrorValue("informational", "response_cancel_not_active"), accepted: false},
		{name: "cancellation reason", value: &messages.ErrorValue{Classification: sessionfailure.ErrorClassCancellation, TerminalReason: messages.TerminalReasonCancellation}, accepted: false},
		{name: "cancellation classification", value: &messages.ErrorValue{Classification: sessionfailure.ErrorClassCancellation}, accepted: false},
		{name: "room cancellation", value: &messages.ErrorValue{Classification: sessionfailure.ErrorClassRoomBoundCancelled}, accepted: false},
	}

	svc := New(sessionfailure.Dependencies{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts, err := svc.NormalizeErrorValue(tc.value)
			if got := facts.FailingEvent != ""; got != tc.accepted {
				t.Fatalf("accepted = %v, want %v; facts = %#v", got, tc.accepted, facts)
			}
			if !tc.accepted {
				if err != nil || facts != (sessionfailure.Facts{}) {
					t.Fatalf("excluded value returned facts=%#v err=%v", facts, err)
				}
				return
			}
			if facts.Classification != tc.classification || facts.TerminalReason != tc.reason || facts.Provenance != tc.provenance || facts.OutputState != tc.output {
				t.Fatalf("normalized facts = %#v", facts)
			}
			if tc.name == "typed fields" && err != typed {
				t.Fatalf("original error identity was not retained: %v", err)
			}
		})
	}
}

func TestFactsFromSessionRunError(t *testing.T) {
	svc := New(sessionfailure.Dependencies{})
	provider := &messages.ErrorValue{Message: "stream failed", Classification: "transport"}
	wrapped := fmt.Errorf("run wrapper: %w", &engine.StreamDeltaError{Value: provider})
	facts := svc.FactsFromSessionRunError(wrapped)
	if facts == nil || facts.Classification != "transport" || facts.FailingEvent != string(messages.StreamTypeError) {
		t.Fatalf("facts from stream error = %#v", facts)
	}
	for _, err := range []error{nil, errors.New("ordinary"), &engine.StreamDeltaError{}} {
		if svc.FactsFromSessionRunError(err) != nil {
			t.Fatalf("unexpected facts from %v", err)
		}
	}
	cancel := &engine.StreamDeltaError{Value: &messages.ErrorValue{Classification: sessionfailure.ErrorClassCancellation}}
	if svc.FactsFromSessionRunError(cancel) != nil {
		t.Fatal("cancellation stream error became failure facts")
	}
}

func TestNormalizeClose(t *testing.T) {
	svc := New(sessionfailure.Dependencies{})
	cases := []struct {
		name     string
		value    *messages.SessionCloseValue
		progress sessionfailure.Progress
		accepted bool
		output   string
	}{
		{name: "provider close partial", value: &messages.SessionCloseValue{Reason: "provider_closed", TerminalReason: messages.TerminalReasonProviderClose}, progress: sessionfailure.Progress{SessionOpened: true, TurnsCompleted: 2}, accepted: true, output: string(messages.TerminalOutputPartial)},
		{name: "provider close no output", value: &messages.SessionCloseValue{Reason: "provider_closed", TerminalReason: messages.TerminalReasonProviderClose}, accepted: true, output: string(messages.TerminalOutputNone)},
		{name: "explicit failure", value: &messages.SessionCloseValue{Reason: "error", TerminalReason: messages.TerminalReasonTerminalFailure, OutputState: messages.TerminalOutputComplete}, accepted: true, output: string(messages.TerminalOutputComplete)},
		{name: "replay divergence", value: &messages.SessionCloseValue{Reason: "replay", TerminalReason: messages.TerminalReasonReplayDivergence}, accepted: true, output: string(messages.TerminalOutputNone)},
		{name: "wrong provider marker", value: &messages.SessionCloseValue{Reason: "transport_closed", TerminalReason: messages.TerminalReasonProviderClose}, accepted: false},
		{name: "normal close", value: &messages.SessionCloseValue{Reason: "client_close", TerminalReason: messages.TerminalReasonSessionClose}, accepted: false},
		{name: "nil", accepted: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := svc.NormalizeClose(tc.value, tc.progress)
			if got := facts.FailingEvent != ""; got != tc.accepted {
				t.Fatalf("accepted = %v, want %v; facts = %#v", got, tc.accepted, facts)
			}
			if tc.accepted && (facts.Classification != sessionfailure.ErrorClassUnknown || facts.Provenance != string(messages.TerminalProvenanceSession) || facts.OutputState != tc.output) {
				t.Fatalf("normalized close facts = %#v", facts)
			}
		})
	}
}

func TestAcceptPublishesBeforeCallbacksAndPreservesCause(t *testing.T) {
	original := errors.New("original provider cause")
	var svc *Service
	var callbackCount atomic.Int32
	svc = New(sessionfailure.Dependencies{Publish: func(observation sessionfailure.Observation) bool {
		callbackCount.Add(1)
		snapshot := svc.Snapshot()
		if snapshot == nil || snapshot.Facts != observation.Facts || snapshot.Err != original {
			t.Errorf("snapshot was not published before callback: snapshot=%#v observation=%#v", snapshot, observation)
		}
		if svc.Accept(terminalFacts(), errors.New("reentrant")) {
			t.Errorf("reentrant observation was accepted")
		}
		return true
	}})
	if !svc.Accept(terminalFacts(), original) {
		t.Fatal("first observation was rejected")
	}
	if callbackCount.Load() != 1 {
		t.Fatalf("publish callbacks = %d, want 1", callbackCount.Load())
	}
	snapshot := svc.Snapshot()
	snapshot.Facts.Classification = "mutated copy"
	if got := svc.Snapshot().Facts.Classification; got == "mutated copy" {
		t.Fatal("snapshot exposed mutable service state")
	}
	if !errors.Is(snapshot.Err, original) {
		t.Fatal("snapshot lost errors.Is identity")
	}
	svc.Clear()
	if svc.Snapshot() != nil {
		t.Fatal("Clear retained accepted observation")
	}
}

func TestAcceptRejectionRollsBack(t *testing.T) {
	var allow atomic.Bool
	svc := New(sessionfailure.Dependencies{Publish: func(sessionfailure.Observation) bool { return allow.Load() }})
	if svc.Accept(terminalFacts(), nil) || svc.Snapshot() != nil {
		t.Fatal("rejected publication retained state")
	}
	allow.Store(true)
	if !svc.Accept(terminalFacts(), nil) || svc.Snapshot() == nil {
		t.Fatal("accepted publication did not retain state")
	}
}

func TestAcceptConcurrentFirstOnly(t *testing.T) {
	svc := New(sessionfailure.Dependencies{})
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if svc.Accept(terminalFacts(), nil) {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || svc.Snapshot() == nil {
		t.Fatalf("concurrent successes = %d, snapshot=%#v", successes.Load(), svc.Snapshot())
	}
}

func TestProjectionOutputAndUnsupportedTool(t *testing.T) {
	var mu sync.Mutex
	var records []sessionfailure.DiagnosticRecord
	svc := New(sessionfailure.Dependencies{Sink: sessionfailure.DiagnosticSinkFunc(func(record sessionfailure.DiagnosticRecord) {
		mu.Lock()
		records = append(records, record)
		mu.Unlock()
	})})
	progress := sessionfailure.Progress{SessionOpened: true, TurnsCompleted: 1}
	for _, kind := range []sessionfailure.Projection{sessionfailure.ProjectionScheduledAudio, sessionfailure.ProjectionUnresolvedTool, sessionfailure.ProjectionImageContinuation, sessionfailure.ProjectionToolContinuation} {
		facts := svc.Projection(kind, "run", progress)
		if facts.Classification != string(kind) || facts.TerminalReason != string(messages.TerminalReasonTerminalFailure) || facts.Provenance != string(messages.TerminalProvenanceSession) || facts.OutputState != string(messages.TerminalOutputPartial) || facts.FailingEvent != "run" {
			t.Fatalf("projection %q = %#v", kind, facts)
		}
	}
	svc.EmitUnsupportedTool(sessionfailure.ToolCall{Name: "lookup", ID: "call-1", TurnIndex: 3})
	mu.Lock()
	defer mu.Unlock()
	if len(records) != 1 {
		t.Fatalf("diagnostic records = %#v", records)
	}
	want := map[string]string{sessionfailure.DiagnosticFieldToolName: "lookup", sessionfailure.DiagnosticFieldToolCallID: "call-1", sessionfailure.DiagnosticFieldFailureClass: sessionfailure.ErrorClassUnsupportedRequest, sessionfailure.DiagnosticFieldFailureReason: sessionfailure.DiagnosticFailureNoToolRunner, sessionfailure.DiagnosticFieldTurnIndex: "3"}
	for key, value := range want {
		if records[0].Event != sessionfailure.DiagnosticEventToolCall || records[0].Fields[key] != value {
			t.Fatalf("diagnostic record = %#v", records[0])
		}
	}
	if got := svc.OutputState(sessionfailure.Progress{SessionOpened: false, TurnsCompleted: 9}); got != string(messages.TerminalOutputNone) {
		t.Fatalf("closed output state = %q", got)
	}
}

func TestNilServiceIsSafe(t *testing.T) {
	var svc *Service
	if svc.Snapshot() != nil || svc.Accept(terminalFacts(), nil) {
		t.Fatal("nil service accepted or exposed state")
	}
	svc.Clear()
	if got := svc.OutputState(sessionfailure.Progress{SessionOpened: true, TurnsCompleted: 1}); got != string(messages.TerminalOutputPartial) {
		t.Fatalf("nil service output state = %q", got)
	}
	if got := svc.Projection(sessionfailure.ProjectionToolContinuation, "error", sessionfailure.Progress{}); got.Classification != sessionfailure.ClassificationToolContinuation {
		t.Fatalf("nil service projection = %#v", got)
	}
	svc.EmitUnsupportedTool(sessionfailure.ToolCall{})
}
