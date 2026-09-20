package live

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audiosubsystem "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems/audio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

// DuplexLoopFactory builds the agent loop from service-owned values instead
// of accepting a CLI-supplied list of agentloop options.
type DuplexLoopFactory struct{}

func NewDuplexLoopFactory() *DuplexLoopFactory { return &DuplexLoopFactory{} }

func (*DuplexLoopFactory) Build(ctx context.Context, inferencer messages.SessionInferencer, config sessionduration.DuplexLoopOptions) (sessionduration.Loop, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if inferencer == nil {
		return nil, errors.New("session execution inferencer is required")
	}

	options := []agentloop.Option{
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(inferencer),
	}
	if ports := config.AudioPorts; ports != nil && (ports.Capture != nil || ports.Playback != nil || ports.Commands != nil) {
		options = append(options, agentloop.WithAudioSubsystem(audiosubsystem.New(*ports)))
	}
	if config.ToolExecutor == nil {
		options = append(options, agentloop.WithToolExecutionDisabled())
	} else {
		definitions := append([]messages.ToolDefinition(nil), config.ToolDefinitions...)
		if len(definitions) > 0 {
			options = append(options, agentloop.WithTools(definitions))
			if config.AdvertiseToolDefinitions {
				options = append(options, agentloop.WithSessionConfig(messages.SessionUpdateConfig{Tools: definitions}))
			}
		}
		options = append(options, agentloop.WithToolExecutor(config.ToolExecutor))
		if policy := config.ToolAcknowledgementPolicy; policy != nil {
			longRunning := make(map[string]struct{}, len(policy.LongRunningToolNames))
			for _, name := range policy.LongRunningToolNames {
				if name != "" {
					longRunning[name] = struct{}{}
				}
			}
			options = append(options, agentloop.WithToolAcknowledgementPolicy(agentloop.ToolAcknowledgementPolicy{
				Threshold: policy.Threshold,
				IsLongRunning: func(name string) bool {
					_, ok := longRunning[name]
					return ok
				},
			}))
		}
	}
	return agentloop.New(options...)
}

var _ sessionduration.DuplexLoopFactory = (*DuplexLoopFactory)(nil)
