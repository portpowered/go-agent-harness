package fleet

import (
	"context"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe/replay"
	serviceProbes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

// DefaultExecutorConfig composes the production entry executor: replay
// entries run through the offline probe path and live entries through the
// existing live session runtime.
type DefaultExecutorConfig struct {
	// ReplayPath is the --replay fixture file or directory; it is required
	// only when the manifest contains replay entries.
	ReplayPath string
	Replay     runtimeReplay.Service
	Metrics    serviceProbes.MetricsCollector
	Corpus     replay.Corpus
	// Live is used for live entries. Its Replay, Metrics, and Corpus fields
	// are filled from this configuration.
	Live LiveExecutor
}

// NewDefaultExecutor builds only the transports the manifest uses, so a
// replay-only manifest never needs live session options and vice versa.
func NewDefaultExecutor(ctx context.Context, manifest Manifest, config DefaultExecutorConfig) (EntryExecutor, error) {
	executors := make(map[Transport]EntryExecutor, len(manifest.Transports))
	if manifest.HasTransport(TransportReplay) {
		replayExecutor, err := newReplayExecutor(ctx, config)
		if err != nil {
			return nil, err
		}
		executors[TransportReplay] = replayExecutor
	}
	if manifest.HasTransport(TransportLive) {
		live := config.Live
		live.Replay, live.Metrics, live.Corpus = config.Replay, config.Metrics, config.Corpus
		executors[TransportLive] = live.Execute
	}
	return func(ctx context.Context, entry Entry) (EntryOutcome, error) {
		executor, ok := executors[entry.Transport]
		if !ok {
			return EntryOutcome{}, fmt.Errorf("fleet entry %q has unsupported transport %q", entry.ID, entry.Transport)
		}
		return executor(ctx, entry)
	}, nil
}

// HasTransport reports whether any entry uses the transport.
func (m Manifest) HasTransport(want Transport) bool {
	for _, entry := range m.Entries {
		if entry.Transport == want {
			return true
		}
	}
	return false
}

func newReplayExecutor(ctx context.Context, config DefaultExecutorConfig) (EntryExecutor, error) {
	if strings.TrimSpace(config.ReplayPath) == "" {
		return nil, fmt.Errorf("--replay <fixture-path-or-dir> is required for replay fleet entries")
	}
	if config.Replay == nil {
		return nil, fmt.Errorf("fleet replay service is required")
	}
	fixtures, err := replay.LoadFixtures(config.ReplayPath)
	if err != nil {
		return nil, err
	}
	for name, fixture := range fixtures {
		if _, err := config.Replay.InspectCapture(ctx, fixture); err != nil {
			return nil, fmt.Errorf("invalid replay fixture %q (%s): %w", fixture, name, err)
		}
	}
	executor := replay.Executor{Service: config.Replay, Fixtures: fixtures, Metrics: config.Metrics, Corpus: config.Corpus}
	return func(ctx context.Context, entry Entry) (EntryOutcome, error) {
		return runEntry(ctx, entry, executor.Exec)
	}, nil
}
