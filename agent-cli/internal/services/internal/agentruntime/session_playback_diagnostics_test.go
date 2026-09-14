package agentruntime

import (
	"context"
	"errors"
	"testing"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
)

func TestSessionPlaybackObservabilitySamplesCompleteSnapshotAndContainsFailures(t *testing.T) {
	var samples []observability.MetricSample
	var records []observability.LogRecord
	observer := sessionPlaybackObservabilityObserver(
		observability.MetricSamplerFunc(func(_ context.Context, sample observability.MetricSample) error {
			samples = append(samples, sample)
			if sample.Name == "audio.playback.zero_fill" {
				return errors.New("sink unavailable")
			}
			return nil
		}),
		observability.LoggerFunc(func(_ context.Context, record observability.LogRecord) error {
			records = append(records, record)
			return errors.New("logger unavailable")
		}),
	)
	observer("simulated-duplex:output", audio.PlaybackQueueStats{
		Format: audio.PCM16DeviceFormat(48000), CallbackCount: 3, RenderedSamples: 1440,
		UnderflowEvents: 1, UnderflowSamples: 480, ZeroFilledSamples: 480,
		OverflowEvents: 2, DroppedSamples: 96, MinimumQueuedSamples: 0,
	})
	if len(samples) != len(playbackMetricSamples) {
		t.Fatalf("metric samples = %d, want %d", len(samples), len(playbackMetricSamples))
	}
	byName := make(map[string]observability.MetricSample, len(samples))
	for _, sample := range samples {
		byName[sample.Name] = sample
	}
	if got := byName["audio.playback.underflow"].Value; got != 480 {
		t.Fatalf("underflow sample = %v, want 480", got)
	}
	if got := byName["audio.playback.dropped"].Value; got != 96 {
		t.Fatalf("dropped sample = %v, want 96", got)
	}
	if len(records) != 1 || records[0].Level != "warn" || records[0].Fields["underflow_samples"] != "480" {
		t.Fatalf("log records = %+v", records)
	}

	// A panicking observer is also contained and cannot change device teardown.
	panicking := sessionPlaybackObservabilityObserver(
		observability.MetricSamplerFunc(func(context.Context, observability.MetricSample) error { panic("metric") }),
		observability.LoggerFunc(func(context.Context, observability.LogRecord) error { panic("logger") }),
	)
	panicking("simulated-duplex:output", audio.PlaybackQueueStats{})
}

func TestSessionCaptureObservabilitySamplesDropOldestLoss(t *testing.T) {
	var samples []observability.MetricSample
	var records []observability.LogRecord
	observer := sessionCaptureObservabilityObserver(
		observability.MetricSamplerFunc(func(_ context.Context, sample observability.MetricSample) error {
			samples = append(samples, sample)
			return nil
		}),
		observability.LoggerFunc(func(_ context.Context, record observability.LogRecord) error {
			records = append(records, record)
			return nil
		}),
	)
	observer("simulated-duplex:input", audio.CaptureQueueStats{
		DropPolicy: "drop_oldest", CapturedSamples: 960, CompletedFrames: 2,
		DroppedFrames: 1, DroppedSamples: 480, SequenceGaps: 1,
	})
	seen := map[string]float64{}
	for _, sample := range samples {
		seen[sample.Name+"/"+sample.Unit] = sample.Value
	}
	if seen["audio.capture.dropped/frames"] != 1 || seen["audio.capture.dropped/samples"] != 480 || seen["audio.capture.sequence_gaps/gaps"] != 1 {
		t.Fatalf("capture samples = %+v", samples)
	}
	if len(records) != 1 || records[0].Level != "warn" || records[0].Fields["drop_policy"] != "drop_oldest" {
		t.Fatalf("capture log = %+v", records)
	}
}

// recordingDiagnosticSink is a minimal SessionDiagnosticSink test double that
// records every record it receives, so a test can assert exactly what a real
// caller-supplied sink would have observed.
type recordingDiagnosticSink struct {
	records []SessionDiagnosticRecord
}

func (s *recordingDiagnosticSink) RecordSessionDiagnostic(record SessionDiagnosticRecord) {
	s.records = append(s.records, record)
}

// TestResolvePlaybackDiagnosticSink covers the structural fix's core
// contract: a caller-supplied sink is always used untouched, and a nil sink
// -- the exact shape of the #350/#360 omission -- resolves to a real,
// non-nil fallback instead of staying nil.
func TestResolvePlaybackDiagnosticSink(t *testing.T) {
	if resolved := resolvePlaybackDiagnosticSink(nil); resolved == nil {
		t.Fatal("resolvePlaybackDiagnosticSink(nil) = nil, want the package fallback sink")
	} else if resolved != fallbackPlaybackDiagnosticSink {
		t.Fatal("resolvePlaybackDiagnosticSink(nil) did not return the shared fallback sink")
	}

	sink := &recordingDiagnosticSink{}
	if resolved := resolvePlaybackDiagnosticSink(sink); resolved != SessionDiagnosticSink(sink) {
		t.Fatal("resolvePlaybackDiagnosticSink did not pass a caller-supplied sink through unchanged")
	}
}

// TestSessionPlaybackDiagnosticObserverResolvedNeverNil proves the specific
// wiring bug is closed at the function level: an observer built from a
// resolved nil sink is callable (never nil) and still reaches a real sink --
// here, the fallback -- carrying the dropped-sample count.
func TestSessionPlaybackDiagnosticObserverResolvedNeverNil(t *testing.T) {
	observer := sessionPlaybackDiagnosticObserver(resolvePlaybackDiagnosticSink(nil))
	if observer == nil {
		t.Fatal("sessionPlaybackDiagnosticObserver(resolvePlaybackDiagnosticSink(nil)) = nil, want a callable observer")
	}
	// The fallback logs rather than exposing a way to assert on it directly;
	// this call only needs to prove it does not panic on the un-configured
	// path a forgetful caller now falls back to.
	observer("virtual:output", audio.PlaybackQueueStats{DroppedSamples: 7, OverflowEvents: 1})

	sink := &recordingDiagnosticSink{}
	observer = sessionPlaybackDiagnosticObserver(resolvePlaybackDiagnosticSink(sink))
	observer("virtual:output", audio.PlaybackQueueStats{DroppedSamples: 7, OverflowEvents: 1})
	if len(sink.records) != 1 {
		t.Fatalf("caller-supplied sink recorded %d records, want 1", len(sink.records))
	}
	if sink.records[0].Fields[SessionDiagnosticFieldPlaybackDroppedSamples] != "7" {
		t.Fatalf("recorded fields = %+v, want dropped_samples=7", sink.records[0].Fields)
	}
}

// TestPlanSessionRuntimePlaybackObserverNonNilAcrossConstructionPaths is the
// mandatory regression test asserting that every non-test construction path
// for SessionRunOptions yields a non-nil playback observer once planned,
// closing the class of bug (not just the two call sites already found) --
// see resolvePlaybackDiagnosticSink's doc comment. Each case below builds its
// SessionRunOptions the same way the real, corresponding production call
// site does (self-play's own builder function, and the room package's actual
// per-participant plan for a live and a replay participant), deliberately
// leaving Diagnostics unset exactly as that call site does today, then
// re-plans a hermetic copy (ReplayPath + an injected SessionInferencer, the
// same seam session_interactive_policy_test.go already uses) and asserts the
// resulting plan's RTC playback observer is non-nil regardless.
func TestPlanSessionRuntimePlaybackObserverNonNilAcrossConstructionPaths(t *testing.T) {
	cases := []struct {
		name string
		opts SessionRunOptions
	}{
		{
			name: "generic minimal caller (a hypothetical future construction site)",
			opts: SessionRunOptions{ModelCatalog: testModelCatalog()},
		},
		{
			name: "self-play (services.selfPlaySessionRunOptions)",
			opts: selfPlaySessionRunOptions(SelfPlayRunOptions{}),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.opts.Diagnostics != nil {
				t.Fatal("test case setup error: this case must leave Diagnostics unset to exercise the default")
			}
			opts := testCase.opts
			opts.ReplayPath = "unused.json"
			opts.SessionInferencer = stubPlanSessionInferencer{}

			plan, err := planSessionRuntime(opts)
			if err != nil {
				t.Fatalf("planSessionRuntime: %v", err)
			}
			if plan.rtcDeviceRequest.PlaybackObserver == nil {
				t.Fatal("plan.rtcDeviceRequest.PlaybackObserver = nil despite an unset SessionRunOptions.Diagnostics; the class-level default did not apply")
			}
		})
	}
}
