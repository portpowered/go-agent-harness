package agentruntime

import (
	"context"
	"fmt"
	"io"
	"strings"

	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimereplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// NewMetricsCollector returns the production metrics reconciliation service.
// It uses the same RunSession path as live sessions, with only a replay
// capture and in-memory metrics sink supplied by the caller.
func NewMetricsCollector(audioService audioio.Service, clockSource clock.Source, factory SessionRuntimeFactory, replayService runtimereplay.Service) serviceprobes.MetricsCollector {
	return metricsCollector{audioService: audioService, clock: clockSource, factory: factory, replayService: replayService}
}

type metricsCollector struct {
	audioService  audioio.Service
	clock         clock.Source
	factory       SessionRuntimeFactory
	replayService runtimereplay.Service
}

func (c metricsCollector) Collect(ctx context.Context, fixture, prompt string) ([]serviceprobes.MetricsSeries, error) {
	if c.clock == nil {
		return nil, fmt.Errorf("metrics collector requires an injected clock")
	}
	if !c.factory.configured() {
		return nil, fmt.Errorf("metrics collector requires an injected session runtime factory")
	}
	if c.replayService == nil {
		return nil, fmt.Errorf("metrics collector requires an injected replay service")
	}
	sink, err := metrics.NewInMemorySink()
	if err != nil {
		return nil, fmt.Errorf("construct metrics sink: %w", err)
	}
	if err := RunSession(ctx, io.Discard, SessionRunOptions{
		AudioService:    c.audioService,
		ReplayPath:      fixture,
		Prompt:          prompt,
		Clock:           c.clock,
		runtimeFactory:  c.factory,
		ReplayService:   c.replayService,
		MetricsRecorder: sink,
	}); err != nil {
		return nil, fmt.Errorf("replay %s for metrics: %w", fixture, err)
	}
	snapshot := sink.Snapshot()
	inspection, err := c.replayService.InspectCapture(ctx, fixture)
	if err != nil {
		return nil, fmt.Errorf("inspect replay fixture %q: %w", fixture, err)
	}
	observed := observedCaptureDeltaSums(inspection.Facts.MetricDeltas)
	series := make([]serviceprobes.MetricsSeries, 0, len(snapshot.Series))
	for _, entry := range snapshot.Series {
		key := string(entry.Direction) + "/" + string(entry.Modality)
		series = append(series, probe.MetricsSeries{
			Direction:      string(entry.Direction),
			Modality:       string(entry.Modality),
			ObservedDeltas: observed[key],
			ReportedTotal:  int64(entry.TotalBytes),
		})
		delete(observed, key)
	}
	for key, deltaSum := range observed {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			continue
		}
		series = append(series, probe.MetricsSeries{
			Direction:      parts[0],
			Modality:       parts[1],
			ObservedDeltas: deltaSum,
		})
	}
	return series, nil
}

// observedCaptureDeltaSums is deliberately independent from the runtime
// observer. The replay service derives these wire-level facts while admitting
// the capture, so the metrics service does not reopen or reinterpret the
// capture through a second package.
func observedCaptureDeltaSums(deltas []runtimereplay.CaptureMetricDelta) map[string]int64 {
	sums := map[string]int64{}
	for _, delta := range deltas {
		if delta.Bytes > 0 {
			sums[string(delta.Direction)+"/"+string(delta.Modality)] += delta.Bytes
		}
	}
	return sums
}
