package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

type metricsCollector struct {
	options       sessiontrace.MetricsCollectorOptions
	replayService replay.Service
}

func NewReplayMetricsCollector(options sessiontrace.MetricsCollectorOptions, replayService replay.Service) sessiontrace.MetricsCollector {
	return metricsCollector{options: options, replayService: replayService}
}

func (c metricsCollector) Collect(ctx context.Context, fixture, prompt string) ([]probe.MetricsSeries, error) {
	if c.options.Clock == nil {
		return nil, fmt.Errorf("metrics collector requires an injected clock")
	}
	if c.options.FactoryReady != nil && !c.options.FactoryReady() {
		return nil, fmt.Errorf("metrics collector requires an injected session runtime factory")
	}
	if c.options.Runner == nil {
		return nil, fmt.Errorf("metrics collector requires an injected replay runner")
	}
	if c.replayService == nil {
		return nil, fmt.Errorf("metrics collector requires the replay service")
	}
	snapshot, err := c.options.Runner(ctx, fixture, prompt)
	if err != nil {
		return nil, fmt.Errorf("replay %s for metrics: %w", fixture, err)
	}
	observed, err := observedFixtureDeltaSums(ctx, c.replayService, fixture)
	if err != nil {
		return nil, err
	}
	series := make([]probe.MetricsSeries, 0, len(snapshot.Series))
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
		series = append(series, probe.MetricsSeries{Direction: parts[0], Modality: parts[1], ObservedDeltas: deltaSum})
	}
	return series, nil
}

func observedFixtureDeltaSums(ctx context.Context, replayService replay.Service, fixture string) (map[string]int64, error) {
	inspection, err := replayService.InspectCapture(ctx, fixture)
	if err != nil {
		return nil, fmt.Errorf("inspect replay fixture %q: %w", fixture, err)
	}
	sums := make(map[string]int64, len(inspection.Facts.MetricDeltas))
	for _, delta := range inspection.Facts.MetricDeltas {
		key := string(delta.Direction) + "/" + string(delta.Modality)
		sums[key] += delta.Bytes
	}
	return sums, nil
}
