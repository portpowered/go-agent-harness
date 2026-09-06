package wire

import (
	"time"

	internalreplay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/replay"
	publicreplay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/replay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// NewReplayClockFactory supplies an isolated deterministic scheduler for each
// replay preparation. It is a graph dependency rather than a host-time
// fallback: the bundle origin is passed in by the replay service.
func NewReplayClockFactory() internalreplay.ClockFactory {
	return func(origin time.Time) *clock.Deterministic {
		return clock.NewDeterministic(origin, time.Millisecond)
	}
}

// NewReplayService installs the headless protocol/tool replay service. The
// runtime factory uses the same OpenAI session adapter and AgentLoop as live
// composition, while all capabilities are replaced by prepared replay seams.
func NewReplayService(clockFactory internalreplay.ClockFactory) publicreplay.Service {
	return internalreplay.New(internalreplay.Dependencies{
		ClockFactory: clockFactory,
		Runtime:      internalreplay.NewOpenAIRuntimeFactory(),
	})
}
