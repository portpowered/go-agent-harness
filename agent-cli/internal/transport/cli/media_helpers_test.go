package cli

import (
	runtimeReplay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

// WithReplayFixture explicitly selects replay mode against a recorded session
// fixture. Omitting it keeps today's live transport behavior unchanged.
func WithReplayFixture(fixturePath string) MediaProbeOption {
	return func(c *MediaProbeCommand) { c.ReplayFixture = fixturePath }
}

// WithReplayService supplies the owner of capture admission and replay.
func WithReplayService(service runtimeReplay.Service) MediaProbeOption {
	return func(c *MediaProbeCommand) { c.ReplayService = service }
}

// NewMediaProbeCommand constructs the probe command with an injected source
// probe. The default is used when no dependency is supplied.
func NewMediaProbeCommand(probe ...MediaProbeFunc) *MediaProbeCommand {
	command := &MediaProbeCommand{Timeout: rtc.DefaultMediaSourceTimeout}
	if len(probe) > 0 {
		command.Probe = probe[0]
	}
	return command
}

// NewMediaProbeCommandWithOptions constructs the probe command from options,
// allowing explicit selection of the service-owned replay contract.
func NewMediaProbeCommandWithOptions(options ...MediaProbeOption) *MediaProbeCommand {
	command := &MediaProbeCommand{Timeout: rtc.DefaultMediaSourceTimeout}
	for _, option := range options {
		option(command)
	}
	return command
}
