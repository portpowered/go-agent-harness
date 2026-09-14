package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomterminal"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestProvenanceTable(t *testing.T) {
	s := New()
	cases := []struct {
		name, disposition, reason, want string
	}{
		{"provider", "completed", string(messages.TerminalReasonProviderAuthoredCompletion), string(messages.TerminalProvenanceProvider)},
		{"loop", "completed", string(messages.TerminalReasonLoopSynthesizedCompletion), string(messages.TerminalProvenanceLoop)},
		{"provider close", "completed", string(messages.TerminalReasonProviderClose), string(messages.TerminalProvenanceSession)},
		{"replay complete", "completed", string(messages.TerminalReasonReplayComplete), string(messages.TerminalProvenanceReplay)},
		{"replay divergence", "completed", string(messages.TerminalReasonReplayDivergence), string(messages.TerminalProvenanceReplay)},
		{"replay incomplete", "completed", string(messages.TerminalReasonReplayIncomplete), string(messages.TerminalProvenanceReplay)},
		{"cancellation", "completed", string(messages.TerminalReasonCancellation), string(messages.TerminalProvenanceLoop)},
		{"partial", "completed", string(messages.TerminalReasonPartialOutput), string(messages.TerminalProvenanceLoop)},
		{"failure", "completed", string(messages.TerminalReasonTerminalFailure), string(messages.TerminalProvenanceSession)},
		{"bound disposition", roomterminal.ParticipantTerminationDispositionCancelledAfterGrace, "unknown", string(messages.TerminalProvenanceRoom)},
		{"stopped fallback", roomterminal.ParticipantTerminationDispositionStopped, "unknown", string(messages.TerminalProvenanceLoop)},
		{"completed fallback", roomterminal.ParticipantTerminationDispositionCompleted, "unknown", string(messages.TerminalProvenanceSession)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.Provenance(tc.disposition, tc.reason); got != tc.want {
				t.Fatalf("provenance = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMessageEndAndCancellationProjection(t *testing.T) {
	s := New()
	defaultEnd := s.MessageEnd("response-1", &messages.MessageEndValue{})
	if defaultEnd.ResponseID != "response-1" || defaultEnd.TerminalReason != string(messages.TerminalReasonProviderAuthoredCompletion) || defaultEnd.TerminalProvenance != string(messages.TerminalProvenanceProvider) || defaultEnd.OutputState != string(messages.TerminalOutputComplete) {
		t.Fatalf("default message end = %+v", defaultEnd)
	}
	customEnd := s.MessageEnd("response-2", &messages.MessageEndValue{TerminalReason: messages.TerminalReasonReplayComplete, TerminalProvenance: messages.TerminalProvenanceReplay, OutputState: messages.TerminalOutputPartial})
	if customEnd.ResponseID != "response-2" || customEnd.TerminalReason != string(messages.TerminalReasonReplayComplete) || customEnd.TerminalProvenance != string(messages.TerminalProvenanceReplay) || customEnd.OutputState != string(messages.TerminalOutputPartial) {
		t.Fatalf("custom message end = %+v", customEnd)
	}
	if got := s.MessageEnd("ignored", nil); got != (roomterminal.Observation{}) {
		t.Fatalf("nil message end = %+v, want zero", got)
	}
	for _, tc := range []struct {
		name, class, provenance string
		bound                   bool
	}{
		{"user", providers.ErrorClassCancellation, string(messages.TerminalProvenanceCLI), false},
		{"room", providers.ErrorClassRoomBoundCancelled, string(messages.TerminalProvenanceRoom), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := s.Cancellation(messages.TerminalOutputPartial, tc.bound)
			if got.Classification != tc.class || got.TerminalReason != string(messages.TerminalReasonCancellation) || got.TerminalProvenance != tc.provenance || got.OutputState != string(messages.TerminalOutputPartial) || got.RoomBound != tc.bound {
				t.Fatalf("cancellation = %+v", got)
			}
		})
	}
	if got := s.Cancellation("", false).OutputState; got != string(messages.TerminalOutputNone) {
		t.Fatalf("empty cancellation output = %q", got)
	}
}

func TestFailureProjectionPreservesIdentity(t *testing.T) {
	s := New()
	underlying := errors.Join(providers.ErrTransport, errors.New("provider secret"))
	facts := roomterminal.FailureFacts{Classification: providers.ErrorClassTransport, TerminalReason: string(messages.TerminalReasonTerminalFailure), Provenance: string(messages.TerminalProvenanceProvider), OutputState: string(messages.TerminalOutputPartial), Code: "transport_code", FailingEvent: "STREAM.ERROR"}
	got := s.Failure(facts, underlying)
	if !got.Failure || !errors.Is(got.Err, providers.ErrTransport) || !errors.Is(got.Err, underlying) || got.Code != "transport_code" || got.FailingEvent != "STREAM.ERROR" {
		t.Fatalf("failure = %+v, errors.Is transport=%t", got, errors.Is(got.Err, providers.ErrTransport))
	}
	if fallback := s.Failure(roomterminal.FailureFacts{ErrorType: "typed provider error"}, nil); fallback.Err == nil || fallback.Err.Error() != "typed provider error" {
		t.Fatalf("typed error fallback = %+v", fallback)
	}
	if fallback := s.Failure(roomterminal.FailureFacts{}, nil); fallback.Err == nil || fallback.Err.Error() != "session stream error" {
		t.Fatalf("generic error fallback = %+v", fallback)
	}
	if empty := s.FailureOptional(nil, underlying); empty != (roomterminal.Observation{}) {
		t.Fatalf("nil optional failure = %+v", empty)
	}
	if _, ok := s.FinalObservation(roomterminal.FinalObservationRequest{}); ok {
		t.Fatal("empty request produced an observation")
	}
}

func TestFinalObservationPrecedence(t *testing.T) {
	s := New()
	underlying := errors.Join(providers.ErrTransport, errors.New("provider secret"))
	facts := roomterminal.FailureFacts{Classification: providers.ErrorClassTransport, TerminalReason: string(messages.TerminalReasonTerminalFailure), Provenance: string(messages.TerminalProvenanceProvider), OutputState: string(messages.TerminalOutputPartial)}
	final, ok := s.FinalObservation(roomterminal.FinalObservationRequest{
		Failure: &facts, FailureError: underlying, RunError: context.Canceled, CancellationOnly: true, RoomBoundCancellation: true, UserCancelled: true,
	})
	if !ok || !final.Failure || final.Classification != facts.Classification || !errors.Is(final.Err, providers.ErrTransport) {
		t.Fatalf("failure precedence = ok:%t observation:%+v", ok, final)
	}
	unknown, ok := s.FinalObservation(roomterminal.FinalObservationRequest{RunError: providers.ErrTransport, SawSessionOpen: true, TurnsCompleted: 1, FallbackProvenance: string(messages.TerminalProvenanceCLI), FallbackFailingEvent: "SESSION.RUN"})
	if !ok || !unknown.Failure || unknown.Classification != providers.ErrorClassTransport || unknown.OutputState != string(messages.TerminalOutputPartial) || unknown.FailingEvent != "SESSION.RUN" || unknown.TerminalProvenance != string(messages.TerminalProvenanceCLI) {
		t.Fatalf("unknown run failure = ok:%t observation:%+v", ok, unknown)
	}
	room, ok := s.FinalObservation(roomterminal.FinalObservationRequest{RunError: context.Canceled, CancellationOnly: true, RoomBoundCancellation: true, UserCancelled: true})
	if !ok || room.Classification != providers.ErrorClassRoomBoundCancelled || !room.RoomBound {
		t.Fatalf("room cancellation precedence = ok:%t observation:%+v", ok, room)
	}
	user, ok := s.FinalObservation(roomterminal.FinalObservationRequest{RunError: context.Canceled, CancellationOnly: true, UserCancelled: true})
	if !ok || user.Classification != providers.ErrorClassCancellation || user.RoomBound {
		t.Fatalf("user cancellation = ok:%t observation:%+v", ok, user)
	}
}

func TestTriggerParticipantAndDiagnosticProjection(t *testing.T) {
	s := New()
	for _, tc := range []struct{ reason, want string }{
		{roomterminal.RoomTerminationMaxTurnsReached, roomterminal.ParticipantTerminationTriggerMaxTurnsReached},
		{roomterminal.RoomTerminationMaxDurationReached, roomterminal.ParticipantTerminationTriggerMaxDurationReached},
		{"stopped", "stopped"},
	} {
		if got := s.BoundTrigger(tc.reason, false); got != tc.want {
			t.Fatalf("trigger %q = %q, want %q", tc.reason, got, tc.want)
		}
		if tc.reason != "stopped" {
			if got := s.BoundTrigger(tc.reason, true); !strings.HasSuffix(got, "_mid_response") {
				t.Fatalf("mid-response trigger %q = %q", tc.reason, got)
			}
		}
	}
	result := roomterminal.ParticipantResult{TerminationTrigger: roomterminal.ParticipantTerminationTriggerMaxDurationReachedMidResponse, TerminationDisposition: roomterminal.ParticipantTerminationDispositionCancelledAfterGrace, Classification: providers.ErrorClassRoomBoundCancelled, TerminalReason: string(messages.TerminalReasonCancellation), TerminalProvenance: string(messages.TerminalProvenanceRoom), OutputState: string(messages.TerminalOutputPartial), Reason: "error"}
	wantFields := map[string]string{"termination_trigger": result.TerminationTrigger, "termination_disposition": result.TerminationDisposition, "classification": result.Classification, "terminal_reason": result.TerminalReason, "terminal_provenance": result.TerminalProvenance, "output_state": result.OutputState, "reason": result.Reason}
	if got := s.Fields(result); !reflect.DeepEqual(got, wantFields) {
		t.Fatalf("fields = %#v, want %#v", got, wantFields)
	}
	diagnostic := s.Diagnostic(result)
	if diagnostic.Event != roomterminal.DiagnosticEventRoomBound || !reflect.DeepEqual(diagnostic.Fields, wantFields) {
		t.Fatalf("diagnostic = %+v", diagnostic)
	}
	if !s.IsBoundTrigger(result.TerminationTrigger) || s.IsBoundTrigger("participant_completion") {
		t.Fatal("room-bound trigger predicate mismatch")
	}
}

func TestRecordBoundDiagnosticIsBoundedAndIndependent(t *testing.T) {
	s := New()
	result := roomterminal.ParticipantResult{TerminationTrigger: roomterminal.ParticipantTerminationTriggerMaxTurnsReached}
	var participant, timeline, callback roomterminal.DiagnosticRecord
	s.RecordBound(roomterminal.BoundDiagnosticRequest{
		ParticipantID: "participant-1", Result: result,
		RecordParticipant: func(_ string, got roomterminal.DiagnosticRecord) {
			participant = got
			got.Fields["mutated"] = "callback-only"
		},
		RecordTimeline: func(event, _ string, fields map[string]string) {
			timeline = roomterminal.DiagnosticRecord{Event: event, Fields: fields}
		},
		OnDiagnostic: func(_ string, got roomterminal.DiagnosticRecord) { callback = got },
	})
	if participant.Event != roomterminal.DiagnosticEventRoomBound || timeline.Event != roomterminal.DiagnosticEventRoomBound || callback.Event != roomterminal.DiagnosticEventRoomBound {
		t.Fatalf("callback records = participant:%+v timeline:%+v callback:%+v", participant, timeline, callback)
	}
	if _, ok := timeline.Fields["mutated"]; ok {
		t.Fatal("timeline callback map shared mutable state")
	}
	if _, ok := callback.Fields["mutated"]; ok {
		t.Fatal("diagnostic callback maps shared mutable state")
	}
	var calls int
	s.RecordBound(roomterminal.BoundDiagnosticRequest{ParticipantID: "ignored", Result: roomterminal.ParticipantResult{TerminationTrigger: "participant_completion"}, OnDiagnostic: func(string, roomterminal.DiagnosticRecord) { calls++ }})
	if calls != 0 {
		t.Fatal("non-bound result emitted a diagnostic")
	}
	if err := s.Close(); err != nil || s.Close() != nil {
		t.Fatalf("close errors = %v", err)
	}
}
