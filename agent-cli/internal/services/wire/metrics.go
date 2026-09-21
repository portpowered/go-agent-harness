package wire

import (
	internalruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	serviceprobes "github.com/portpowered/go-agent-harness/agent-cli/internal/services/probes"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	runtimereplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// NewMetricsCollector installs the production metrics reconciliation service
// in the CLI graph. The transport receives only this narrow interface and
// cannot construct a session runtime or metrics sink itself.
func NewMetricsCollector(audioService audioio.Service, clockSource clock.Source, factory internalruntime.SessionRuntimeFactory, replayService runtimereplay.Service) serviceprobes.MetricsCollector {
	return internalruntime.NewMetricsCollector(audioService, clockSource, factory, replayService)
}
