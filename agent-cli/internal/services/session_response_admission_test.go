package services

import (
	"context"
	"io"
	"sync"
	"testing"

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
	if err == nil {
		t.Fatal("empty-response room returned clean success")
	}
	if result.Reason == RoomTerminationMaxTurnsReached {
		t.Fatal("empty response satisfied the room MaxTurns target")
	}
	if result.Reason != RoomTerminationFailed {
		t.Fatalf("empty-response room reason = %q, want failed termination", result.Reason)
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
