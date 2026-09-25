package wire

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
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
}

func testPublicWireLiveness(t *testing.T) {
	if NewCancellationIntent() == nil {
		t.Fatal("wire did not construct a cancellation intent")
	}
	intent := NewCancellationIntent()
	if intent.SIGINTReceived() {
		t.Fatal("new cancellation intent already reports SIGINT")
	}
	intent.MarkSIGINT()
	if !intent.SIGINTReceived() {
		t.Fatal("cancellation intent did not retain SIGINT")
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
	path := "fixture.capture"
	collector := NewReplayMetricsCollector(sessiontrace.MetricsCollectorOptions{
		Clock: clock.Real{},
		ReplayInspector: metricsCaptureInspector{facts: replay.CaptureFacts{MetricDeltas: []replay.CaptureMetricDelta{
			{Direction: metrics.DirectionOutput, Modality: metrics.ModalityText, Bytes: 5},
			{Direction: metrics.DirectionOutput, Modality: metrics.ModalityAudio, Bytes: 3},
			{Direction: metrics.DirectionOutput, Modality: metrics.ModalityTool, Bytes: 5},
			{Direction: metrics.DirectionInput, Modality: metrics.ModalityAudio, Bytes: 3},
			{Direction: metrics.DirectionInput, Modality: metrics.ModalityText, Bytes: 3},
		}}},
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

type metricsCaptureInspector struct{ facts replay.CaptureFacts }

func (i metricsCaptureInspector) InspectCapture(context.Context, string) (replay.CaptureInspection, error) {
	return replay.CaptureInspection{Facts: i.facts}, nil
}
