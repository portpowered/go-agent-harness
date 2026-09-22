package wire

import (
	"context"
	"errors"
	"io"

	internalruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	runtimetrace "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	runtimetracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// NewProbeMetrics installs the production metrics reconciliation service
// in the CLI graph. The transport receives only this narrow interface and
// cannot construct a session runtime or metrics sink itself.
func NewProbeMetrics(clockSource clock.Source, factory internalruntime.SessionRuntimeFactory) serviceprobes.MetricsCollector {
	return runtimetracewire.NewReplayMetricsCollector(runtimetrace.MetricsCollectorOptions{
		Clock:        clockSource,
		FactoryReady: func() bool { return internalruntime.SessionRuntimeFactoryConfigured(factory) },
		Runner: func(ctx context.Context, fixture, prompt string) (metrics.Snapshot, error) {
			sink, err := metrics.NewInMemorySink()
			if err != nil {
				return metrics.Snapshot{}, err
			}
			err = internalruntime.RunSessionWithRuntimeFactory(ctx, io.Discard, internalruntime.SessionRunOptions{
				ReplayPath: fixture, Prompt: prompt, Clock: clockSource, AudioService: audioiowire.NewService(), MetricsRecorder: sink,
			}, factory)
			if errors.Is(err, runtimetrace.ErrUnresolvedToolResults) {
				err = nil
			}
			return sink.Snapshot(), err
		},
	})
}
