package wire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	serviceRuntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentruntime"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceProbes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

// NewMetricsCollector reconciles runtime metrics with service-derived capture
// deltas. Replay admission and capture inspection stay behind the replay
// contract; execution uses the same public session runtime as ordinary calls.
func NewMetricsCollector(sessionRuntime serviceRuntime.Runtime, replayService runtimeReplay.Service) serviceProbes.MetricsCollector {
	return metricsCollector{runtime: sessionRuntime, replayService: replayService}
}

type metricsCollector struct {
	runtime       serviceRuntime.Runtime
	replayService runtimeReplay.Service
}

func (c metricsCollector) Collect(ctx context.Context, fixture, prompt string) ([]serviceProbes.MetricsSeries, error) {
	if c.runtime == nil {
		return nil, errors.New("metrics collector requires a session runtime")
	}
	if c.replayService == nil {
		return nil, errors.New("metrics collector requires a replay service")
	}
	sink, err := metrics.NewInMemorySink()
	if err != nil {
		return nil, fmt.Errorf("construct metrics sink: %w", err)
	}
	if err := c.runtime.Run(ctx, io.Discard, serviceSession.Request{
		ReplayPath: fixture, Prompt: prompt, MetricsRecorder: sink,
	}); err != nil && !errors.Is(err, sessiontrace.ErrUnresolvedToolResults) {
		return nil, fmt.Errorf("replay %s for metrics: %w", fixture, err)
	}
	inspection, err := c.replayService.InspectCapture(ctx, fixture)
	if err != nil {
		return nil, fmt.Errorf("inspect replay fixture %q: %w", fixture, err)
	}
	observed := observedCaptureDeltaSums(inspection.Facts.MetricDeltas)
	snapshot := sink.Snapshot()
	series := make([]serviceProbes.MetricsSeries, 0, len(snapshot.Series)+len(observed))
	for _, entry := range snapshot.Series {
		key := string(entry.Direction) + "/" + string(entry.Modality)
		series = append(series, probe.MetricsSeries{
			Direction: string(entry.Direction), Modality: string(entry.Modality),
			ObservedDeltas: observed[key], ReportedTotal: int64(entry.TotalBytes),
		})
		delete(observed, key)
	}
	keys := make([]string, 0, len(observed))
	for key := range observed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) == 2 {
			series = append(series, probe.MetricsSeries{Direction: parts[0], Modality: parts[1], ObservedDeltas: observed[key]})
		}
	}
	return series, nil
}

func observedCaptureDeltaSums(deltas []runtimeReplay.CaptureMetricDelta) map[string]int64 {
	sums := map[string]int64{}
	for _, delta := range deltas {
		if delta.Bytes > 0 {
			sums[string(delta.Direction)+"/"+string(delta.Modality)] += delta.Bytes
		}
	}
	return sums
}
