package services

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionProgressObserver_AdmitsOnlyResponsesWithOutput(t *testing.T) {
	tests := []struct {
		name            string
		setup           func(*sessionProgressObserver)
		events          []messages.StreamMessage
		wantTurns       int
		wantTurnRecords int
		wantInputText   uint64
		wantOutputText  uint64
		wantOutputAudio uint64
		wantOutputTool  uint64
	}{
		{
			name: "empty response after text input",
			setup: func(observer *sessionProgressObserver) {
				observer.noteUserTextInput("opening question")
			},
			events: []messages.StreamMessage{
				responseMessageStart(),
				responseMessageEnd(),
			},
			wantTurns:       0,
			wantTurnRecords: 0,
			wantInputText:   uint64(len("opening question")),
		},
		{
			name: "text response",
			events: []messages.StreamMessage{
				responseMessageStart(),
				{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("hello")},
				responseMessageEnd(),
			},
			wantTurns:       1,
			wantTurnRecords: 1,
			wantOutputText:  uint64(len("hello")),
		},
		{
			name: "audio response",
			events: []messages.StreamMessage{
				responseMessageStart(),
				{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{1, 2, 3, 4})},
				responseMessageEnd(),
			},
			wantTurns:       1,
			wantTurnRecords: 1,
			wantOutputAudio: 4,
		},
		{
			name: "actionable tool-only response",
			setup: func(observer *sessionProgressObserver) {
				// A tool-only response is terminal only when no session executor
				// owns the call. Tool-enabled sessions retain their existing
				// intermediate-call/continuation contract.
				observer.setToolResultsEnabled(false)
			},
			events: []messages.StreamMessage{
				responseMessageStart(),
				{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue("call-1", "lookup", `{"city":"Lisbon"}`)},
				responseMessageEnd(),
			},
			wantTurns:       1,
			wantTurnRecords: 1,
			wantOutputTool:  uint64(len(`{"city":"Lisbon"}`)),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := &diagnosticRecordSink{}
			observer := newSessionProgressObserver(sink, nil, "test", "test-model")
			if test.setup != nil {
				test.setup(observer)
			}
			for _, event := range test.events {
				observer.observe(event)
			}

			if observer.turnsCompleted != test.wantTurns {
				t.Fatalf("completed turns = %d, want %d", observer.turnsCompleted, test.wantTurns)
			}
			if got := len(sink.events(SessionDiagnosticEventTurn)); got != test.wantTurnRecords {
				t.Fatalf("turn diagnostic records = %d, want %d", got, test.wantTurnRecords)
			}
			if observer.lastMessageEndAdmitted() != (test.wantTurns > 0) {
				t.Fatalf("last MESSAGE.END admitted = %t, want %t", observer.lastMessageEndAdmitted(), test.wantTurns > 0)
			}
			if observer.totals.inputText != test.wantInputText {
				t.Fatalf("input text bytes = %d, want %d", observer.totals.inputText, test.wantInputText)
			}
			if observer.totals.outText != test.wantOutputText {
				t.Fatalf("output text bytes = %d, want %d", observer.totals.outText, test.wantOutputText)
			}
			if observer.totals.outAudio != test.wantOutputAudio {
				t.Fatalf("output audio bytes = %d, want %d", observer.totals.outAudio, test.wantOutputAudio)
			}
			if observer.totals.outTool != test.wantOutputTool {
				t.Fatalf("output tool bytes = %d, want %d", observer.totals.outTool, test.wantOutputTool)
			}
			if test.wantTurns == 0 && observer.assistantResponseCompleted() {
				t.Fatal("empty response was marked as a completed assistant response")
			}
		})
	}
}

func TestSessionProgressObserver_ClassifiesExplicitEmptyPartialResponse(t *testing.T) {
	sink := &diagnosticRecordSink{}
	observer := newSessionProgressObserver(sink, nil, "test-provider", "test-model")
	observer.observe(messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("session-empty", "test"),
	})
	observer.observe(responseMessageStart())
	observer.observe(messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		Role:       messages.RoleAssistant,
		ResponseID: "response-empty",
		Value: messages.NewMessageEndValueWithTerminal(
			messages.TokenUsage{},
			messages.TerminalReasonPartialOutput,
			messages.TerminalProvenanceProvider,
			messages.TerminalOutputNone,
		),
	})

	if observer.turnsCompleted != 0 {
		t.Fatalf("empty partial response completed turns = %d, want 0", observer.turnsCompleted)
	}
	livenessErr := observer.livenessFailure()
	if livenessErr == nil || !errors.Is(livenessErr, ErrSilentProviderEmptyResponse) {
		t.Fatalf("liveness error = %v, want ErrSilentProviderEmptyResponse", livenessErr)
	}
	var typedErr *SessionLivenessError
	if !errors.As(livenessErr, &typedErr) || typedErr.ResponseID != "response-empty" {
		t.Fatalf("liveness error = %#v, want typed response-empty error", livenessErr)
	}

	if err := observer.finish(livenessErr); err == nil || !errors.Is(err, ErrSilentProviderEmptyResponse) {
		t.Fatalf("finish error = %v, want ErrSilentProviderEmptyResponse", err)
	}
	records := sink.events(SessionDiagnosticEventFailure)
	if len(records) != 1 {
		t.Fatalf("session failure records = %d, want exactly one", len(records))
	}
	want := map[string]string{
		fieldClassification:     SessionSilentProviderEmptyResponseClassification,
		fieldTerminalReason:     string(messages.TerminalReasonTerminalFailure),
		fieldTerminalProvenance: string(messages.TerminalProvenanceSession),
		fieldOutputState:        string(messages.TerminalOutputNone),
		fieldTurnsCompleted:     "0",
		fieldFailingEvent:       string(messages.StreamTypeMessageEnd),
	}
	for key, wantValue := range want {
		if got := records[0].Fields[key]; got != wantValue {
			t.Fatalf("failure field %q = %q, want %q (fields: %v)", key, got, wantValue, records[0].Fields)
		}
	}
}

func TestSessionProgressObserver_DoesNotClassifyCancellationOrToolContinuationAsSilent(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*sessionProgressObserver)
		value *messages.MessageEndValue
	}{
		{
			name: "loop cancellation",
			value: messages.NewMessageEndValueWithTerminal(
				messages.TokenUsage{},
				messages.TerminalReasonPartialOutput,
				messages.TerminalProvenanceLoop,
				messages.TerminalOutputNone,
			),
		},
		{
			name: "pending tool continuation",
			setup: func(observer *sessionProgressObserver) {
				observer.toolStateMu.Lock()
				observer.toolContinuations["call-1"] = &toolContinuationState{resultAccepted: true}
				observer.toolStateMu.Unlock()
			},
			value: messages.NewMessageEndValueWithTerminal(
				messages.TokenUsage{},
				messages.TerminalReasonPartialOutput,
				messages.TerminalProvenanceProvider,
				messages.TerminalOutputNone,
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := newSessionProgressObserver(nil, nil, "test-provider", "test-model")
			if test.setup != nil {
				test.setup(observer)
			}
			observer.observe(responseMessageStart())
			observer.observe(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: test.value})
			if err := observer.livenessFailure(); err != nil {
				t.Fatalf("unexpected silent-provider classification: %v", err)
			}
		})
	}
}

func TestRunRoom_ExplicitEmptyPartialResponseTerminatesParticipant(t *testing.T) {
	ids := []string{"silent", "viable"}
	emptyPartialResponse := []messages.StreamMessage{
		roomTestSessionOpen("silent"),
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{
			Type:       messages.StreamTypeMessageEnd,
			Role:       messages.RoleAssistant,
			ResponseID: "silent-response",
			Value: messages.NewMessageEndValueWithTerminal(
				messages.TokenUsage{},
				messages.TerminalReasonPartialOutput,
				messages.TerminalProvenanceProvider,
				messages.TerminalOutputNone,
			),
		},
	}
	inferencers := map[string]*roomTestInferencer{
		"silent": {events: emptyPartialResponse, closeStarted: make(chan struct{})},
		"viable": {events: append([]messages.StreamMessage{roomTestSessionOpen("viable")}, append(roomTestResponse("hello"), roomTestSessionClose("viable", "complete"))...)},
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.Manifest.Room.MaxDuration = 5 * time.Second
	var diagnosticMu sync.Mutex
	var failureRecord *SessionDiagnosticRecord
	opts.OnDiagnostic = func(participantID string, record SessionDiagnosticRecord) {
		if participantID != "silent" || record.Event != SessionDiagnosticEventFailure {
			return
		}
		diagnosticMu.Lock()
		copy := record
		failureRecord = &copy
		diagnosticMu.Unlock()
	}
	result, err := RunRoomWithResult(context.Background(), io.Discard, opts)
	if err != nil {
		t.Fatalf("room after isolated empty participant returned an error: %v", err)
	}
	if result.Reason != RoomTerminationStopped {
		t.Fatalf("room reason = %q, want stopped after the viable participant ended", result.Reason)
	}
	participant, ok := result.Participants["silent"]
	if !ok {
		t.Fatal("room result is missing silent participant")
	}
	if participant.Reason != ParticipantTerminationError {
		t.Fatalf("silent participant reason = %q, want error", participant.Reason)
	}
	if participant.Classification != SessionSilentProviderEmptyResponseClassification {
		t.Fatalf("silent participant classification = %q, want %q", participant.Classification, SessionSilentProviderEmptyResponseClassification)
	}
	if participant.TerminalReason != string(messages.TerminalReasonTerminalFailure) || participant.TerminalProvenance != string(messages.TerminalProvenanceSession) || participant.OutputState != string(messages.TerminalOutputNone) {
		t.Fatalf("silent participant terminal metadata = (%q, %q, %q), want terminal failure/session/none", participant.TerminalReason, participant.TerminalProvenance, participant.OutputState)
	}
	if participant.TurnsCompleted != 0 {
		t.Fatalf("silent participant turns = %d, want 0", participant.TurnsCompleted)
	}
	if !strings.Contains(participant.Error, SessionSilentProviderEmptyResponseClassification) {
		t.Fatalf("silent participant error = %q, want classification", participant.Error)
	}
	diagnosticMu.Lock()
	gotFailure := failureRecord
	diagnosticMu.Unlock()
	if gotFailure == nil {
		t.Fatal("silent participant did not emit a session failure diagnostic")
	}
	if got := gotFailure.Fields[fieldClassification]; got != SessionSilentProviderEmptyResponseClassification {
		t.Fatalf("room diagnostic classification = %q, want %q", got, SessionSilentProviderEmptyResponseClassification)
	}
	if got := gotFailure.Fields[fieldTerminalReason]; got != string(messages.TerminalReasonTerminalFailure) {
		t.Fatalf("room diagnostic terminal reason = %q, want terminal_failure", got)
	}
	if got := gotFailure.Fields[fieldTerminalProvenance]; got != string(messages.TerminalProvenanceSession) {
		t.Fatalf("room diagnostic provenance = %q, want session", got)
	}
	if got := gotFailure.Fields[fieldOutputState]; got != string(messages.TerminalOutputNone) {
		t.Fatalf("room diagnostic output state = %q, want none", got)
	}
}

func TestRunRoom_EmptyResponseDoesNotAdvanceTurnsOrMaxTurns(t *testing.T) {
	ids := []string{"customer", "assistant"}
	emptyResponse := func(id string) []messages.StreamMessage {
		return []messages.StreamMessage{
			roomTestSessionOpen(id),
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			roomTestMessageEnd(),
		}
	}
	inferencers := map[string]*roomTestInferencer{
		"customer":  {events: emptyResponse("customer"), disconnect: true},
		"assistant": {events: emptyResponse("assistant"), disconnect: true},
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.Manifest.Room.MaxTurns = 1

	var diagnosticMu sync.Mutex
	diagnosticTurns := make(map[string]int, len(ids))
	opts.OnDiagnostic = func(participantID string, record SessionDiagnosticRecord) {
		if record.Event != SessionDiagnosticEventTurn {
			return
		}
		diagnosticMu.Lock()
		diagnosticTurns[participantID]++
		diagnosticMu.Unlock()
	}

	result, err := RunRoomWithResult(context.Background(), io.Discard, opts)
	if err != nil {
		t.Fatalf("empty-response room: %v", err)
	}
	if result.Reason == RoomTerminationMaxTurnsReached {
		t.Fatal("empty response satisfied the room MaxTurns target")
	}
	if result.Reason != RoomTerminationStopped {
		t.Fatalf("empty-response room reason = %q, want clean stopped termination", result.Reason)
	}
	for _, id := range ids {
		participant, ok := result.Participants[id]
		if !ok {
			t.Fatalf("room result is missing participant %q", id)
		}
		if participant.TurnsCompleted != 0 {
			t.Fatalf("participant %q completed turns = %d, want 0", id, participant.TurnsCompleted)
		}
	}
	diagnosticMu.Lock()
	defer diagnosticMu.Unlock()
	if len(diagnosticTurns) != 0 {
		t.Fatalf("empty response emitted completed-turn diagnostics: %v", diagnosticTurns)
	}
}

func responseMessageStart() messages.StreamMessage {
	return messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageStartValue(),
	}
}

func responseMessageEnd() messages.StreamMessage {
	return messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Role:  messages.RoleAssistant,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	}
}
