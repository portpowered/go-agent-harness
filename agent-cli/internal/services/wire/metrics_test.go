package wire

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

type metricsWireRuntime struct {
	request agentsession.Request
}

func (r *metricsWireRuntime) Run(_ context.Context, _ io.Writer, request agentsession.Request) error {
	r.request = request
	if request.MetricsRecorder == nil {
		return nil
	}
	if err := request.MetricsRecorder.Record(metrics.DirectionInput, metrics.ModalityAudio, 8); err != nil {
		return err
	}
	if err := request.MetricsRecorder.Record(metrics.DirectionOutput, metrics.ModalityText, 4); err != nil {
		return err
	}
	return sessiontrace.ErrUnresolvedToolResults
}

type metricsWireReplay struct {
	runtimeReplay.Service
	path       string
	inspection runtimeReplay.CaptureInspection
}

func (r *metricsWireReplay) InspectCapture(_ context.Context, path string) (runtimeReplay.CaptureInspection, error) {
	r.path = path
	return r.inspection, nil
}

func TestMetricsCollectorReconcilesRuntimeAndReplayInspector(t *testing.T) {
	runtime := &metricsWireRuntime{}
	replay := &metricsWireReplay{inspection: runtimeReplay.CaptureInspection{Facts: runtimeReplay.CaptureFacts{
		MetricDeltas: []runtimeReplay.CaptureMetricDelta{
			{Direction: metrics.DirectionInput, Modality: metrics.ModalityAudio, Bytes: 11},
			{Direction: metrics.DirectionInput, Modality: metrics.ModalityAudio, Bytes: 0},
			{Direction: metrics.DirectionOutput, Modality: metrics.ModalityText, Bytes: 4},
			{Direction: metrics.DirectionOutput, Modality: metrics.ModalityAudio, Bytes: -2},
		},
	}}}
	series, err := NewMetricsCollector(runtime, replay).Collect(context.Background(), "fixture.capture", "summarize")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if replay.path != "fixture.capture" {
		t.Fatalf("InspectCapture path = %q, want fixture.capture", replay.path)
	}
	if runtime.request.ReplayPath != "fixture.capture" || runtime.request.Prompt != "summarize" || runtime.request.MetricsRecorder == nil {
		t.Fatalf("runtime request = %+v, want admitted replay request and metrics recorder", runtime.request)
	}
	got := make(map[string]struct{ observed, reported int64 }, len(series))
	for _, item := range series {
		got[item.Direction+"/"+item.Modality] = struct{ observed, reported int64 }{item.ObservedDeltas, item.ReportedTotal}
	}
	inputAudio := got["input/audio"]
	if inputAudio.observed != 11 || inputAudio.reported != 8 {
		t.Fatalf("input audio metrics = %+v, want observed 11 and reported 8", inputAudio)
	}
	outputText := got["output/text"]
	if outputText.observed != 4 || outputText.reported != 4 {
		t.Fatalf("output text metrics = %+v, want observed and reported 4", outputText)
	}
	outputAudio, ok := got["output/audio"]
	if !ok || outputAudio.observed != 0 || outputAudio.reported != 0 {
		t.Fatalf("output audio metrics = %+v, want non-positive capture deltas ignored", outputAudio)
	}
}

func TestMetricsCollectorRequiresReplayService(t *testing.T) {
	_, err := NewMetricsCollector(&metricsWireRuntime{}, nil).Collect(context.Background(), "fixture.capture", "summarize")
	if err == nil || !strings.Contains(err.Error(), "replay service") {
		t.Fatalf("Collect error = %v, want explicit missing replay-service failure", err)
	}
}
