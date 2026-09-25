package wire

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

func TestPublicWireFactoriesExposeIndependentContracts(t *testing.T) {
	if NewLiveRecorder(sessiontrace.LiveRecorderOptions{}) == nil {
		t.Fatal("NewLiveRecorder returned nil")
	}
	if NewPlaybackDiagnostics(sessiontrace.PlaybackDiagnosticsOptions{}) == nil {
		t.Fatal("NewPlaybackDiagnostics returned nil")
	}
}

func TestPublicWireLiveRecorderClassifiesProviderMessages(t *testing.T) {
	tests := []struct {
		name string
		msg  messages.StreamMessage
		want sessiontrace.SessionRuntimeObservationKind
	}{
		{"audio", messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant}, sessiontrace.SessionRuntimeObservationAudioOutput},
		{"tool audio", messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleTool}, "tool_result"},
		{"response", messages.StreamMessage{Type: messages.StreamTypeResponseCreate}, sessiontrace.SessionRuntimeObservationResponseCreate},
		{"input", messages.StreamMessage{Type: messages.StreamTypeInputItemAdded}, sessiontrace.SessionRuntimeObservationInputCommit},
		{"user end", messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser}, sessiontrace.SessionRuntimeObservationInputCommit},
		{"turn end", messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant}, sessiontrace.SessionRuntimeObservationTurnCompleted},
		{"close", messages.StreamMessage{Type: messages.StreamTypeSessionClose}, sessiontrace.SessionRuntimeObservationTerminal},
		{"tool call", messages.StreamMessage{Type: messages.StreamTypeToolCallStart}, "tool_call"},
		{"tool result", messages.StreamMessage{Role: messages.RoleTool}, "tool_result"},
		{"ordinary", messages.StreamMessage{Type: messages.StreamTypeTextDelta}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var observed []sessiontrace.RuntimeObservation
			recorder := NewLiveRecorder(sessiontrace.LiveRecorderOptions{Observer: sessiontrace.RuntimeObserverFunc(func(o sessiontrace.RuntimeObservation) { observed = append(observed, o) })})
			if err := recorder.RecordMessage(context.Background(), session.LiveRecord{Message: test.msg}); err != nil {
				t.Fatal(err)
			}
			var got sessiontrace.SessionRuntimeObservationKind
			for _, observation := range observed {
				if observation.Kind != "provider_wire_receive" && observation.Kind != "provider_wire_send" {
					got = observation.Kind
				}
			}
			if got != test.want {
				t.Fatalf("special observation = %q, want %q", got, test.want)
			}
		})
	}
}
