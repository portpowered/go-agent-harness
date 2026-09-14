package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"

	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
	metricsreplaywire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// NewMetricsCollector returns the production metrics reconciliation service.
// It uses the same RunSession path as live sessions, with only a replay
// capture and in-memory metrics sink supplied by the caller.
func NewMetricsCollector(clockSource clock.Source, factory SessionRuntimeFactory) serviceprobes.MetricsCollector {
	return metricsCollectorAdapter{service: metricsreplaywire.NewService(metricsreplay.Dependencies{
		Clock:   clockSource,
		Runner:  sessionMetricsReplayRunner{factory: factory},
		Loader:  sessionMetricsFixtureLoader{},
		NewSink: newSessionMetricsSink,
	})}
}

type metricsCollectorAdapter struct {
	service metricsreplay.Service
}

func (a metricsCollectorAdapter) Collect(ctx context.Context, fixture, prompt string) ([]serviceprobes.MetricsSeries, error) {
	series, err := a.service.Collect(ctx, fixture, prompt)
	if err != nil {
		return nil, err
	}
	result := make([]serviceprobes.MetricsSeries, 0, len(series))
	for _, entry := range series {
		result = append(result, serviceprobes.MetricsSeries{
			Direction:      string(entry.Direction),
			Modality:       string(entry.Modality),
			ObservedDeltas: entry.ObservedDeltas,
			ReportedTotal:  entry.ReportedTotal,
		})
	}
	return result, nil
}

type sessionMetricsReplayRunner struct {
	factory SessionRuntimeFactory
}

func (r sessionMetricsReplayRunner) Run(ctx context.Context, request metricsreplay.RunRequest) error {
	if !r.factory.configured() {
		return fmt.Errorf("metrics collector requires an injected session runtime factory")
	}
	return RunSession(ctx, io.Discard, SessionRunOptions{
		ReplayPath:      request.Fixture,
		Prompt:          request.Prompt,
		Clock:           request.Clock,
		runtimeFactory:  r.factory,
		MetricsRecorder: sessionMetricsRecorder{recorder: request.Recorder},
	})
}

type sessionMetricsRecorder struct {
	recorder metricsreplay.Recorder
}

func (r sessionMetricsRecorder) Record(direction metrics.Direction, modality metrics.Modality, bytes int64) error {
	if r.recorder == nil {
		return errors.New("metrics replay recorder is nil")
	}
	return r.recorder.Record(metricsreplay.Direction(direction), metricsreplay.Modality(modality), bytes)
}

type sessionMetricsFixtureLoader struct{}

func (sessionMetricsFixtureLoader) Load(ctx context.Context, fixture string) (metricsreplay.Fixture, error) {
	if err := ctx.Err(); err != nil {
		return metricsreplay.Fixture{}, err
	}
	capture, err := gatewaytesting.LoadSessionCapture(fixture)
	if err != nil {
		return metricsreplay.Fixture{}, fmt.Errorf("load replay fixture %q: %w", fixture, err)
	}
	records := make([]metricsreplay.Record, 0, len(capture.Records))
	for _, record := range capture.Records {
		payload := record.Payload
		if len(payload) == 0 {
			payload = record.Data
		}
		records = append(records, metricsreplay.Record{
			Sequence:    record.Sequence,
			Direction:   metricsreplay.WireDirection(record.Direction),
			Type:        record.Type,
			PayloadType: record.PayloadType,
			Payload:     append([]byte(nil), payload...),
		})
	}
	return metricsreplay.Fixture{Records: records}, nil
}

func newSessionMetricsSink() (metricsreplay.Sink, error) {
	sink, err := metrics.NewInMemorySink()
	if err != nil {
		return nil, err
	}
	return sessionMetricsSink{sink: sink}, nil
}

type sessionMetricsSink struct {
	sink *metrics.InMemorySink
}

func (s sessionMetricsSink) Record(direction metricsreplay.Direction, modality metricsreplay.Modality, bytes int64) error {
	return s.sink.Record(metrics.Direction(direction), metrics.Modality(modality), bytes)
}

func (s sessionMetricsSink) Snapshot() (metricsreplay.Snapshot, error) {
	snapshot := s.sink.Snapshot()
	series := make([]metricsreplay.SnapshotSeries, 0, len(snapshot.Series))
	for _, entry := range snapshot.Series {
		if entry.TotalBytes > uint64(^uint64(0)>>1) {
			return metricsreplay.Snapshot{}, fmt.Errorf("metrics total for %s/%s exceeds int64", entry.Direction, entry.Modality)
		}
		series = append(series, metricsreplay.SnapshotSeries{
			Direction:  metricsreplay.Direction(entry.Direction),
			Modality:   metricsreplay.Modality(entry.Modality),
			TotalBytes: int64(entry.TotalBytes),
		})
	}
	return metricsreplay.Snapshot{Series: series}, nil
}

func (s sessionMetricsSink) Close() error { return nil }
