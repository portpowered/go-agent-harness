package agentruntime

import (
	"errors"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
)

type recordingSessionRuntimeObserver struct {
	observations []SessionRuntimeObservation
}

func (o *recordingSessionRuntimeObserver) ObserveSessionRuntime(observation SessionRuntimeObservation) {
	if o == nil {
		return
	}
	o.observations = append(o.observations, observation)
}

type failingMetricsRecorder struct{}

func (failingMetricsRecorder) Record(metrics.Direction, metrics.Modality, int64) error {
	return errors.New("recorder is intentionally unavailable")
}

func TestSessionRuntimeObservationFinalAccountingIsTerminalCumulativeAndComplete(t *testing.T) {
	runtimeObserver := &recordingSessionRuntimeObserver{}
	runtime := newSessionRuntimeObservationRecorder(runtimeObserver, nil)
	progress := newSessionProgressObserver(nil, failingMetricsRecorder{}, "grok", "scripted-realtime")
	progress.runtime = runtime

	progress.observe(messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("session", "replay")})
	progress.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	progress.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("hello"),
	})
	progress.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Value: messages.NewMessageEndValue(messages.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18, ReasoningTokens: 2}),
	})
	progress.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	progress.observe(messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{1, 2, 3}),
	})
	progress.observe(messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Value: messages.NewMessageEndValue(messages.TokenUsage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8, ReasoningTokens: 1}),
	})

	if err := progress.finish(nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	// Repeated cleanup must not publish another terminal observation.
	if err := progress.finish(errors.New("late cleanup error")); err == nil {
		t.Fatal("second finish unexpectedly changed the returned error")
	}

	if len(runtimeObserver.observations) != 3 {
		t.Fatalf("runtime observations = %d, want two turn boundaries and one terminal observation", len(runtimeObserver.observations))
	}
	for index, observation := range runtimeObserver.observations[:2] {
		if observation.Kind != SessionRuntimeObservationTurnCompleted {
			t.Fatalf("observation %d kind = %q, want turn_completed", index, observation.Kind)
		}
		if observation.FinalAccounting != nil {
			t.Fatalf("non-terminal observation %d unexpectedly carries final accounting: %#v", index, observation.FinalAccounting)
		}
	}

	terminal := runtimeObserver.observations[2]
	if terminal.Kind != SessionRuntimeObservationTerminal || !terminal.Clean || terminal.TurnsCompleted != 2 || terminal.Error != "" {
		t.Fatalf("terminal observation = %#v, want clean terminal after two turns", terminal)
	}
	if terminal.FinalAccounting == nil {
		t.Fatal("terminal observation has no final accounting")
	}
	accounting := terminal.FinalAccounting
	if got, want := accounting.PromptTokens, uint64(14); got != want {
		t.Fatalf("prompt tokens = %d, want %d", got, want)
	}
	if got, want := accounting.CompletionTokens, uint64(12); got != want {
		t.Fatalf("completion tokens = %d, want %d", got, want)
	}
	if got, want := accounting.TotalTokens, uint64(26); got != want {
		t.Fatalf("total tokens = %d, want %d", got, want)
	}
	if got, want := accounting.ReasoningTokens, uint64(3); got != want {
		t.Fatalf("reasoning tokens = %d, want %d", got, want)
	}
	if accounting.UsageSemantics != SessionTokenUsageIncremental {
		t.Fatalf("usage semantics = %q, want %q", accounting.UsageSemantics, SessionTokenUsageIncremental)
	}

	snapshot := accounting.Metrics
	if !reflect.DeepEqual(snapshot.HistogramBounds, metrics.DefaultHistogramBounds()) {
		t.Fatalf("histogram bounds = %v, want %v", snapshot.HistogramBounds, metrics.DefaultHistogramBounds())
	}
	if got, want := len(snapshot.Series), len(metrics.SupportedDirections())*len(metrics.SupportedModalities()); got != want {
		t.Fatalf("snapshot series = %d, want all %d supported series", got, want)
	}
	textSeries := snapshot.SeriesFor(metrics.DirectionOutput, metrics.ModalityText)
	if textSeries.EventCount != 1 || textSeries.TotalBytes != 5 {
		t.Fatalf("output/text = (%d, %d), want (1, 5)", textSeries.EventCount, textSeries.TotalBytes)
	}
	audioSeries := snapshot.SeriesFor(metrics.DirectionOutput, metrics.ModalityAudio)
	if audioSeries.EventCount != 1 || audioSeries.TotalBytes != 3 {
		t.Fatalf("output/audio = (%d, %d), want (1, 3)", audioSeries.EventCount, audioSeries.TotalBytes)
	}
	for _, direction := range metrics.SupportedDirections() {
		for _, modality := range metrics.SupportedModalities() {
			if direction == metrics.DirectionOutput && (modality == metrics.ModalityText || modality == metrics.ModalityAudio) {
				continue
			}
			series := snapshot.SeriesFor(direction, modality)
			if series.EventCount != 0 || series.TotalBytes != 0 || series.Histogram.SampleCount != 0 || series.Histogram.ByteSum != 0 || series.Histogram.OverflowCount != 0 || !reflect.DeepEqual(series.Histogram.BucketCounts, make([]uint64, len(snapshot.HistogramBounds))) {
				t.Fatalf("untouched series %s is not an explicit zero: %#v", metrics.SeriesKey{Direction: direction, Modality: modality}, series)
			}
		}
	}

	// The receiver may retain or mutate its copy without changing the
	// runtime-owned production sink or a later snapshot.
	accounting.Metrics.HistogramBounds[0] = 999
	accounting.Metrics.Series[0].Histogram.BucketCounts[0] = 999
	later := progress.finalAccounting()
	if later.Metrics.HistogramBounds[0] == 999 || later.Metrics.Series[0].Histogram.BucketCounts[0] == 999 {
		t.Fatal("retained final accounting mutation changed the production snapshot")
	}
}

func TestSessionRuntimeObservationFinalAccountingIsPublishedOnError(t *testing.T) {
	runtimeObserver := &recordingSessionRuntimeObserver{}
	progress := newSessionProgressObserver(nil, nil, "grok", "scripted-realtime")
	progress.runtime = newSessionRuntimeObservationRecorder(runtimeObserver, nil)
	progress.observe(messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("partial"),
	})

	wantErr := errors.New("provider disconnected")
	if err := progress.finish(wantErr); !errors.Is(err, wantErr) {
		t.Fatalf("finish error = %v, want %v", err, wantErr)
	}
	if len(runtimeObserver.observations) != 1 {
		t.Fatalf("runtime observations = %d, want one terminal observation", len(runtimeObserver.observations))
	}
	terminal := runtimeObserver.observations[0]
	if terminal.Kind != SessionRuntimeObservationTerminal || terminal.Clean || terminal.Error != wantErr.Error() {
		t.Fatalf("error terminal observation = %#v", terminal)
	}
	if terminal.FinalAccounting == nil {
		t.Fatal("error terminal observation has no final accounting")
	}
	series := terminal.FinalAccounting.Metrics.SeriesFor(metrics.DirectionOutput, metrics.ModalityText)
	if series.EventCount != 1 || series.TotalBytes != uint64(len("partial")) {
		t.Fatalf("error final output/text = (%d, %d), want (1, %d)", series.EventCount, series.TotalBytes, len("partial"))
	}
}

func TestRuntimeToolEvidenceKeepsExecutionIdentity(t *testing.T) {
	observer := &recordingSessionRuntimeObserver{}
	source := platformclock.NewDeterministic(time.Unix(0, 0), time.Millisecond)
	recorder := newSessionRuntimeObservationRecorder(observer, source)
	call := messages.ToolCall{ID: "call-timeout", Name: "youtube_resume"}
	recorder.observeToolCall(call)
	source.Advance()
	recorder.observeToolResult(call, messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "tool execution timed out"}, true)
	if len(observer.observations) != 2 {
		t.Fatalf("observations=%d", len(observer.observations))
	}
	start, end := observer.observations[0], observer.observations[1]
	if start.Kind != "tool_call" || end.Kind != "tool_result" || end.Clean || !end.Timestamp.After(start.Timestamp) {
		t.Fatalf("invalid execution boundaries: %+v %+v", start, end)
	}
	if !strings.Contains(string(end.Payload), call.ID) || !strings.Contains(string(end.Payload), "timed out") {
		t.Fatalf("missing correlated result: %s", end.Payload)
	}
}

func TestAudioTraceDoesNotAccumulateWholeUtterance(t *testing.T) {
	recorder := newSessionRuntimeObservationRecorder(CombineSessionRuntimeObservers(TraceRuntimeObserver{}), platformclock.Real{})
	chunk := make([]byte, 48000)
	for i := 0; i < 128; i++ {
		recorder.audioInput(chunk)
		recorder.providerAudioSent(chunk)
	}
	if len(recorder.inputPayload) != 0 || !recorder.providerBoundaryObserving {
		t.Fatalf("trace retained %d input bytes, provider commits=%v", len(recorder.inputPayload), recorder.providerBoundaryObserving)
	}
	recorder.providerInputCommit()
}
