package consumer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	runtimeDevices "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	devicewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	runtimeobs "github.com/portpowered/go-agent-harness/go-audio/pkg/observability"
)

type diagnosticSink struct {
	records []runtimeDevices.DiagnosticRecord
}

func (s *diagnosticSink) RecordDiagnostic(record runtimeDevices.DiagnosticRecord) {
	s.records = append(s.records, record)
}

type observationSink struct {
	samples []runtimeobs.MetricSample
	logs    []runtimeobs.LogRecord
}

func (s *observationSink) Sample(_ context.Context, sample runtimeobs.MetricSample) error {
	s.samples = append(s.samples, sample)
	return errors.New("consumer sink unavailable")
}

func (s *observationSink) Log(_ context.Context, record runtimeobs.LogRecord) error {
	s.logs = append(s.logs, record)
	return errors.New("consumer sink unavailable")
}

func TestPublicObserverServiceWorksWithoutCLIOrPrivateImports(t *testing.T) {
	diagnostics := &diagnosticSink{}
	observations := &observationSink{}
	service := devicewire.NewObserverService(runtimeDevices.ObserverDependencies{
		DiagnosticSink: diagnostics,
		MetricSampler:  observations,
		Logger:         observations,
	})
	stats := audio.PlaybackQueueStats{
		Format:            audio.PCM16DeviceFormat(48_000),
		LatencyTarget:     250 * time.Millisecond,
		CapacitySamples:   12_000,
		QueuedSamples:     720,
		PeakQueuedSamples: 2_400,
		DroppedSamples:    96,
		OverflowEvents:    2,
		RenderedSamples:   1_440,
	}

	observer := service.PlaybackObserver()
	if observer == nil {
		t.Fatal("public playback observer = nil")
	}
	observer("consumer-output", stats)

	if len(diagnostics.records) != 1 {
		t.Fatalf("diagnostic records = %d, want 1", len(diagnostics.records))
	}
	record := diagnostics.records[0]
	if record.Event != runtimeDevices.PlaybackOverflowDiagnosticEvent || record.Fields[runtimeDevices.PlaybackDiagnosticFieldDeviceID] != "consumer-output" || record.Fields[runtimeDevices.PlaybackDiagnosticFieldDroppedSamples] != "96" {
		t.Fatalf("public diagnostic record = %#v", record)
	}
	if len(observations.samples) != 12 || len(observations.logs) != 1 || observations.logs[0].Level != "warn" {
		t.Fatalf("public observation counts = samples:%d logs:%d record:%#v", len(observations.samples), len(observations.logs), observations.logs)
	}

	// A host may omit all sinks. Construction and observation remain safe and
	// do not require a process-global registration or CLI package.
	withoutSinks := devicewire.NewObserverService(runtimeDevices.ObserverDependencies{})
	withoutSinks.PlaybackObserver()("consumer-output", stats)

	secondDiagnostics := &diagnosticSink{}
	second := devicewire.NewObserverService(runtimeDevices.ObserverDependencies{DiagnosticSink: secondDiagnostics})
	second.ReportPlaybackOverflow(runtimeDevices.PlaybackOverflowReport{
		DeviceID: "consumer-output", ParticipantID: "participant-2", Stats: stats,
	})
	if len(secondDiagnostics.records) != 1 || secondDiagnostics.records[0].Fields[runtimeDevices.PlaybackDiagnosticFieldParticipantID] != "participant-2" {
		t.Fatalf("second host diagnostic record = %#v", secondDiagnostics.records)
	}
	if _, ok := record.Fields[runtimeDevices.PlaybackDiagnosticFieldParticipantID]; ok {
		t.Fatal("single-session diagnostic unexpectedly inherited participant attribution")
	}
}
