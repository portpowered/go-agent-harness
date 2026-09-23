package wire

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestNewServiceBuildsIndependentFactories(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil {
		t.Fatalf("services = %v, %v; want constructed services", first, second)
	}
}

func TestPublicWireFactoriesExposeIndependentContracts(t *testing.T) {
	t.Run("constructors", testPublicWireConstructors)
	t.Run("diagnostics", testPublicWireDiagnostics)
	t.Run("liveness", testPublicWireLiveness)
}

func testPublicWireConstructors(t *testing.T) {
	if NewLiveRecorder(sessiontrace.LiveRecorderOptions{}) == nil {
		t.Fatal("NewLiveRecorder returned nil")
	}
	if NewRuntimeRecorder(nil, clock.Real{}) != nil {
		t.Fatal("NewRuntimeRecorder accepted a nil observer")
	}
	if NewProviderWireDialer(nil, nil, clock.Real{}) != nil {
		t.Fatal("NewProviderWireDialer accepted nil dependencies")
	}
	if _, err := NewReplayMetricsCollector(sessiontrace.MetricsCollectorOptions{}).Collect(context.Background(), "fixture", "prompt"); err == nil {
		t.Fatal("NewReplayMetricsCollector returned a nil error for missing dependencies")
	}
	if NewPlaybackDiagnostics(sessiontrace.PlaybackDiagnosticsOptions{}) == nil {
		t.Fatal("NewPlaybackDiagnostics returned nil")
	}
	if NewObserver(sessiontrace.NewObserverOptions{}) == nil {
		t.Fatal("NewObserver returned nil")
	}
}

func testPublicWireDiagnostics(t *testing.T) {
	called := 0
	sink := sessiontrace.DiagnosticFunc(func(sessiontrace.DiagnosticRecord) { called++ })
	if CombineDiagnosticSinks(nil) != nil {
		t.Fatal("CombineDiagnosticSinks(nil) returned a sink")
	}
	if combined := CombineDiagnosticSinks(sink); combined == nil {
		t.Fatal("CombineDiagnosticSinks lost its only sink")
	} else {
		combined.RecordSessionDiagnostic(sessiontrace.DiagnosticRecord{Event: "test"})
	}
	if combined := CombineDiagnosticSinks(sink, sink); combined == nil {
		t.Fatal("CombineDiagnosticSinks lost its fanout")
	} else {
		combined.RecordSessionDiagnostic(sessiontrace.DiagnosticRecord{Event: "test"})
	}
	if called != 3 {
		t.Fatalf("diagnostic fanout calls = %d, want 3", called)
	}

	firstErr := errors.New("first")
	first := make(chan error, 1)
	first <- firstErr
	if got := <-MergeErrorChannels(context.Background(), first, nil); !errors.Is(got, firstErr) {
		t.Fatalf("merged error = %v, want first error", got)
	}
	first, second := make(chan error, 1), make(chan error, 1)
	first <- nil
	second <- firstErr
	close(first)
	close(second)
	merged := MergeErrorChannels(context.Background(), first, second)
	if got := <-merged; !errors.Is(got, firstErr) {
		t.Fatalf("merged concurrent error = %v, want first failure", got)
	}
	if _, ok := <-merged; ok {
		t.Fatal("merged concurrent error channel remained open")
	}
}

func testPublicWireLiveness(t *testing.T) {
	if NewCancellationIntent() == nil || LivenessClockFromSource(clock.Real{}) == nil {
		t.Fatal("wire did not construct cancellation/liveness dependencies")
	}
	intent := NewCancellationIntent()
	if intent.SIGINTReceived() {
		t.Fatal("new cancellation intent already reports SIGINT")
	}
	intent.MarkSIGINT()
	if !intent.SIGINTReceived() {
		t.Fatal("cancellation intent did not retain SIGINT")
	}
	livenessErr := &sessiontrace.LivenessError{
		Classification:     "timeout",
		TerminalReason:     messages.TerminalReasonTerminalFailure,
		TerminalProvenance: messages.TerminalProvenanceSession,
		OutputState:        messages.TerminalOutputNone,
	}
	classification, reason, provenance, output := LivenessMetadata(livenessErr)
	if classification != "timeout" || reason != messages.TerminalReasonTerminalFailure || provenance != messages.TerminalProvenanceSession || output != messages.TerminalOutputNone {
		t.Fatalf("liveness metadata = %q/%q/%q/%q", classification, reason, provenance, output)
	}
	if classification, reason, provenance, output := LivenessMetadata(errors.New("ordinary")); classification != "" || reason != "" || provenance != "" || output != "" {
		t.Fatalf("ordinary error metadata = %q/%q/%q/%q, want empty", classification, reason, provenance, output)
	}
	if got := OutputStateForProgress(false, 2); got != string(messages.TerminalOutputNone) {
		t.Fatalf("closed progress output state = %q", got)
	}
	if got := OutputStateForProgress(true, 1); got != string(messages.TerminalOutputPartial) {
		t.Fatalf("active progress output state = %q", got)
	}
	if unresolved := NewUnresolvedToolResultsError([]string{"b", "a"}, map[string]messages.SessionSendStatus{"a": messages.SessionSendTimedOut}); unresolved == nil || unresolved.Error() == "" {
		t.Fatal("NewUnresolvedToolResultsError did not return a typed error")
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

func TestPublicWireReplayMetricsReconcilesAllWireModalities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.session.json")
	capture := gatewaytesting.SessionCapture{Version: gatewaytesting.SessionCaptureVersion, Records: []gatewaytesting.CapturedSessionEvent{
		{Sequence: 1, Direction: gatewaytesting.DirectionServerToClient, Type: "response.output_text.delta", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"response.output_text.delta","delta":"hello"}`)},
		{Sequence: 2, Direction: gatewaytesting.DirectionClientToServer, Type: "input_audio_buffer.append", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"input_audio_buffer.append","audio":"AQID"}`)},
		{Sequence: 3, Direction: gatewaytesting.DirectionServerToClient, Type: "response.audio.delta", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"response.audio.delta","delta":"AQID"}`)},
		{Sequence: 4, Direction: gatewaytesting.DirectionServerToClient, Type: "response.function_call_arguments.delta", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"response.function_call_arguments.delta","call_id":"call-1","delta":"x"}`)},
		{Sequence: 5, Direction: gatewaytesting.DirectionServerToClient, Type: "response.function_call_arguments.done", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"response.function_call_arguments.done","call_id":"call-2","arguments":"args"}`)},
		{Sequence: 6, Direction: gatewaytesting.DirectionClientToServer, Type: "conversation.item.create", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage, Payload: json.RawMessage(`{"type":"conversation.item.create","item":{"content":[{"type":"input_text","text":"hey"}]}}`)},
	}}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	collector := NewReplayMetricsCollector(sessiontrace.MetricsCollectorOptions{
		Clock: clock.Real{},
		Runner: func(context.Context, string, string) (metrics.Snapshot, error) {
			return metrics.Snapshot{Series: []metrics.SeriesSnapshot{
				{Direction: metrics.DirectionOutput, Modality: metrics.ModalityText, TotalBytes: 5},
				{Direction: metrics.DirectionOutput, Modality: metrics.ModalityAudio, TotalBytes: 3},
				{Direction: metrics.DirectionOutput, Modality: metrics.ModalityTool, TotalBytes: 5},
				{Direction: metrics.DirectionInput, Modality: metrics.ModalityAudio, TotalBytes: 3},
				{Direction: metrics.DirectionInput, Modality: metrics.ModalityText, TotalBytes: 3},
			}}, nil
		},
	})
	series, err := collector.Collect(context.Background(), path, "prompt")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int64{"output/text": {5, 5}, "output/audio": {3, 3}, "output/tool": {5, 5}, "input/audio": {3, 3}, "input/text": {3, 3}}
	got := make(map[string][2]int64, len(series))
	for _, entry := range series {
		got[string(entry.Direction)+"/"+string(entry.Modality)] = [2]int64{entry.ObservedDeltas, entry.ReportedTotal}
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Fatalf("metrics[%s] = %v, want %v", key, got[key], expected)
		}
	}
}
