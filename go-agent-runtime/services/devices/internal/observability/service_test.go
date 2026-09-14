package observability

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	runtimeobs "github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
)

type diagnosticFunc func(runtimeDevices.DiagnosticRecord)

func (f diagnosticFunc) RecordDiagnostic(record runtimeDevices.DiagnosticRecord) { f(record) }

type observationCollector struct {
	mu      sync.Mutex
	samples []runtimeobs.MetricSample
	logs    []runtimeobs.LogRecord
	err     error
	panic   bool
}

func (c *observationCollector) Sample(_ context.Context, sample runtimeobs.MetricSample) error {
	if c.panic {
		panic("metric collector panic")
	}
	c.mu.Lock()
	c.samples = append(c.samples, sample)
	c.mu.Unlock()
	return c.err
}

func (c *observationCollector) Log(_ context.Context, record runtimeobs.LogRecord) error {
	if c.panic {
		panic("log collector panic")
	}
	c.mu.Lock()
	c.logs = append(c.logs, record)
	c.mu.Unlock()
	return c.err
}

func (c *observationCollector) snapshot() ([]runtimeobs.MetricSample, []runtimeobs.LogRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	samples := append([]runtimeobs.MetricSample(nil), c.samples...)
	logs := append([]runtimeobs.LogRecord(nil), c.logs...)
	return samples, logs
}

func playbackTestStats() audio.PlaybackQueueStats {
	return audio.PlaybackQueueStats{
		Format:               audio.PCM16DeviceFormat(48_000),
		LatencyTarget:        250 * 1_000_000,
		CapacitySamples:      12_000,
		QueuedSamples:        720,
		PeakQueuedSamples:    2_400,
		DroppedSamples:       96,
		OverflowEvents:       2,
		DiscardedSamples:     14,
		DiscardEvents:        1,
		CallbackCount:        3,
		RenderedSamples:      1_440,
		UnderflowEvents:      1,
		UnderflowSamples:     480,
		ZeroFilledSamples:    480,
		MinimumQueuedSamples: 120,
	}
}

func TestPlaybackObserverPreservesAllProjectionFields(t *testing.T) {
	var diagnostic runtimeDevices.DiagnosticRecord
	diagnostics := diagnosticFunc(func(record runtimeDevices.DiagnosticRecord) {
		diagnostic = record
	})
	collector := &observationCollector{err: errors.New("sink unavailable")}
	service := New(runtimeDevices.ObserverDependencies{
		DiagnosticSink: diagnostics,
		MetricSampler:  collector,
		Logger:         collector,
	})

	service.PlaybackObserver()("virtual:output", playbackTestStats())

	if diagnostic.Event != runtimeDevices.PlaybackOverflowDiagnosticEvent {
		t.Fatalf("diagnostic event = %q, want %q", diagnostic.Event, runtimeDevices.PlaybackOverflowDiagnosticEvent)
	}
	wantFields := map[string]string{
		runtimeDevices.PlaybackDiagnosticFieldDeviceID:            "virtual:output",
		runtimeDevices.PlaybackDiagnosticFieldSampleRate:          "48000",
		runtimeDevices.PlaybackDiagnosticFieldChannels:            "1",
		runtimeDevices.PlaybackDiagnosticFieldLatencyTargetMillis: "250",
		runtimeDevices.PlaybackDiagnosticFieldCapacitySamples:     "12000",
		runtimeDevices.PlaybackDiagnosticFieldQueuedSamples:       "720",
		runtimeDevices.PlaybackDiagnosticFieldPeakQueuedSamples:   "2400",
		runtimeDevices.PlaybackDiagnosticFieldDroppedSamples:      "96",
		runtimeDevices.PlaybackDiagnosticFieldOverflowEvents:      "2",
	}
	if !reflect.DeepEqual(diagnostic.Fields, wantFields) {
		t.Fatalf("diagnostic fields = %#v, want %#v", diagnostic.Fields, wantFields)
	}

	samples, logs := collector.snapshot()
	if len(samples) != 12 {
		t.Fatalf("playback samples = %d, want 12", len(samples))
	}
	wantSamples := map[string]struct {
		kind  string
		unit  string
		value float64
	}{
		"audio.playback.queue.depth":   {"gauge", "samples", 720},
		"audio.playback.queue.peak":    {"gauge", "samples", 2400},
		"audio.playback.rendered":      {"counter", "samples", 1440},
		"audio.playback.callbacks":     {"counter", "callbacks", 3},
		"audio.playback.underflows":    {"counter", "events", 1},
		"audio.playback.underflow":     {"counter", "samples", 480},
		"audio.playback.zero_fill":     {"counter", "samples", 480},
		"audio.playback.overflows":     {"counter", "events", 2},
		"audio.playback.dropped":       {"counter", "samples", 96},
		"audio.playback.discards":      {"counter", "events", 1},
		"audio.playback.discarded":     {"counter", "samples", 14},
		"audio.playback.queue.minimum": {"gauge", "samples", 120},
	}
	for _, sample := range samples {
		want, ok := wantSamples[sample.Name]
		if !ok || sample.Kind != want.kind || sample.Unit != want.unit || sample.Value != want.value {
			t.Errorf("unexpected playback sample: %#v", sample)
		}
		if sample.Fields["device_id"] != "virtual:output" || sample.Fields["sample_rate"] != "48000" || sample.Fields["channels"] != "1" {
			t.Errorf("playback sample fields = %#v", sample.Fields)
		}
	}
	if len(logs) != 1 || logs[0].Level != logLevelWarn || logs[0].Message != runtimeDevices.PlaybackSnapshotLogMessage {
		t.Fatalf("playback logs = %#v", logs)
	}
	for key, want := range map[string]string{
		"device_id": "virtual:output", "sample_rate": "48000", "channels": "1",
		"latency_target_ms": "250", "capacity_samples": "12000", "queued_samples": "720",
		"peak_queued_samples": "2400", "dropped_samples": "96", "overflow_events": "2",
		"underflow_events": "1", "underflow_samples": "480", "zero_filled_samples": "480",
		"rendered_samples": "1440",
	} {
		if got := logs[0].Fields[key]; got != want {
			t.Errorf("playback log field %q = %q, want %q", key, got, want)
		}
	}
}

func TestPlaybackObserverIsSilentForNonOverflowAndUsesInfo(t *testing.T) {
	diagnosticCalls := 0
	collector := &observationCollector{}
	service := New(runtimeDevices.ObserverDependencies{
		DiagnosticSink: diagnosticFunc(func(runtimeDevices.DiagnosticRecord) { diagnosticCalls++ }),
		MetricSampler:  collector,
		Logger:         collector,
	})
	stats := audio.PlaybackQueueStats{Format: audio.PCM16DeviceFormat(24_000), RenderedSamples: 480}
	service.PlaybackObserver()("virtual:output", stats)
	if diagnosticCalls != 0 {
		t.Fatalf("diagnostic calls = %d, want 0 without overflow", diagnosticCalls)
	}
	_, logs := collector.snapshot()
	if len(logs) != 1 || logs[0].Level != logLevelInfo {
		t.Fatalf("non-overflow logs = %#v, want one info record", logs)
	}
}

func TestCaptureObserverPreservesFrameAndSampleLossUnits(t *testing.T) {
	collector := &observationCollector{}
	service := New(runtimeDevices.ObserverDependencies{MetricSampler: collector, Logger: collector})
	service.CaptureObserver()("virtual:input", audio.CaptureQueueStats{
		QueuedSamples: 120, HighWaterSamples: 960, CapturedSamples: 1_920,
		CompletedFrames: 4, DroppedFrames: 2, DroppedSamples: 480,
		DropPolicy: "drop_oldest", SequenceGaps: 1,
	})
	samples, logs := collector.snapshot()
	if len(samples) != 7 {
		t.Fatalf("capture samples = %d, want 7", len(samples))
	}
	seen := map[string]float64{}
	for _, sample := range samples {
		seen[sample.Name+"/"+sample.Unit] = sample.Value
		if sample.Fields["device_id"] != "virtual:input" || sample.Fields["drop_policy"] != "drop_oldest" {
			t.Errorf("capture sample fields = %#v", sample.Fields)
		}
	}
	for key, want := range map[string]float64{
		"audio.capture.queue.depth/samples": 120, "audio.capture.queue.peak/samples": 960,
		"audio.capture.captured/samples": 1_920, "audio.capture.frames/frames": 4,
		"audio.capture.dropped/frames": 2, "audio.capture.dropped/samples": 480,
		"audio.capture.sequence_gaps/gaps": 1,
	} {
		if seen[key] != want {
			t.Errorf("capture sample %q = %v, want %v", key, seen[key], want)
		}
	}
	if len(logs) != 1 || logs[0].Level != logLevelWarn || logs[0].Message != runtimeDevices.CaptureSnapshotLogMessage {
		t.Fatalf("capture logs = %#v", logs)
	}
	for key, want := range map[string]string{"device_id": "virtual:input", "drop_policy": "drop_oldest", "dropped_frames": "2", "dropped_samples": "480", "sequence_gaps": "1"} {
		if logs[0].Fields[key] != want {
			t.Errorf("capture log field %q = %q, want %q", key, logs[0].Fields[key], want)
		}
	}
}

func TestObserverServiceContainsSinkFailuresAndPanics(t *testing.T) {
	service := New(runtimeDevices.ObserverDependencies{
		DiagnosticSink: diagnosticFunc(func(runtimeDevices.DiagnosticRecord) { panic("diagnostic panic") }),
		MetricSampler:  &observationCollector{panic: true},
		Logger:         &observationCollector{panic: true},
	})
	service.PlaybackObserver()("virtual:output", playbackTestStats())
	service.CaptureObserver()("virtual:input", audio.CaptureQueueStats{DroppedSamples: 1})
	service.ReportPlaybackOverflow(runtimeDevices.PlaybackOverflowReport{DeviceID: "virtual:output", ParticipantID: "customer", Stats: playbackTestStats()})
}

func TestObserverFanoutFiltersNilPreservesOrderAndContinuesAfterPanic(t *testing.T) {
	service := New(runtimeDevices.ObserverDependencies{})
	var order []string
	combined := service.CombinePlaybackObservers(
		nil,
		func(string, audio.PlaybackQueueStats) { order = append(order, "first"); panic("first observer") },
		func(string, audio.PlaybackQueueStats) { order = append(order, "second") },
	)
	if combined == nil {
		t.Fatal("combined playback observer = nil with active observers")
	}
	combined("virtual:output", audio.PlaybackQueueStats{})
	if !reflect.DeepEqual(order, []string{"first", "second"}) {
		t.Fatalf("observer order = %v, want [first second]", order)
	}
	if service.CombinePlaybackObservers(nil) != nil || service.CombineCaptureObservers(nil) != nil || service.CombinePlaybackReceiptObservers(nil) != nil {
		t.Fatal("all-nil fanout must return nil")
	}
}

func TestObserverFanoutPassesIndependentSnapshotsConcurrently(t *testing.T) {
	service := New(runtimeDevices.ObserverDependencies{})
	const calls = 32
	var mu sync.Mutex
	seen := make([]string, 0, calls*2)
	combined := service.CombineCaptureObservers(
		func(id string, stats audio.CaptureQueueStats) {
			stats.QueuedSamples = 999
			mu.Lock()
			seen = append(seen, id)
			mu.Unlock()
		},
		func(id string, stats audio.CaptureQueueStats) {
			if stats.QueuedSamples != 7 {
				t.Errorf("second observer saw mutated queue depth %d", stats.QueuedSamples)
			}
			mu.Lock()
			seen = append(seen, id+"-second")
			mu.Unlock()
		},
	)
	var wg sync.WaitGroup
	for index := 0; index < calls; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			combined("virtual:input", audio.CaptureQueueStats{QueuedSamples: 7})
		}()
	}
	wg.Wait()
	if len(seen) != calls*2 {
		t.Fatalf("observer calls = %d, want %d", len(seen), calls*2)
	}
}

func TestReceiptFanoutPreservesOrderAndContainsPanic(t *testing.T) {
	service := New(runtimeDevices.ObserverDependencies{})
	var order []string
	combined := service.CombinePlaybackReceiptObservers(
		func(audio.PlaybackReceipt) { order = append(order, "first"); panic("receipt observer") },
		func(receipt audio.PlaybackReceipt) {
			if !receipt.Applied || receipt.CommandID != 9 {
				t.Errorf("receipt = %#v", receipt)
			}
			order = append(order, "second")
		},
	)
	combined(audio.PlaybackReceipt{CommandID: 9, Applied: true})
	if !reflect.DeepEqual(order, []string{"first", "second"}) {
		t.Fatalf("receipt order = %v, want [first second]", order)
	}
}

func TestDiagnosticSnapshotIsNotSharedWithSink(t *testing.T) {
	var received runtimeDevices.DiagnosticRecord
	service := New(runtimeDevices.ObserverDependencies{DiagnosticSink: diagnosticFunc(func(record runtimeDevices.DiagnosticRecord) {
		received = record
		record.Fields["device_id"] = "changed"
	})})
	service.ReportPlaybackOverflow(runtimeDevices.PlaybackOverflowReport{DeviceID: "original", Stats: audio.PlaybackQueueStats{DroppedSamples: 1}})
	if received.Fields["device_id"] != "changed" {
		t.Fatalf("sink did not receive an ownable field map: %#v", received.Fields)
	}
	second := New(runtimeDevices.ObserverDependencies{DiagnosticSink: diagnosticFunc(func(record runtimeDevices.DiagnosticRecord) {
		if record.Fields["device_id"] != "original" {
			t.Errorf("new observer service inherited sink mutation: %#v", record.Fields)
		}
	})})
	second.ReportPlaybackOverflow(runtimeDevices.PlaybackOverflowReport{DeviceID: "original", Stats: audio.PlaybackQueueStats{DroppedSamples: 1}})
}
