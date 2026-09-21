package durationrun

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
	if err := validateDuplexLoopInputs(ctx, inferencer); err != nil {
		return nil, err
	}

	options := []agentloop.Option{
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(inferencer),
	}
	options = appendAudioSubsystem(options, config.AudioPorts)
	options = appendToolExecution(options, config)
	return agentloop.New(options...)
}

func validateDuplexLoopInputs(ctx context.Context, inferencer messages.SessionInferencer) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if inferencer == nil {
		return errors.New("session execution inferencer is required")
	}
	return nil
}

func appendAudioSubsystem(options []agentloop.Option, ports *audiosubsystem.Ports) []agentloop.Option {
	if ports != nil && (ports.Capture != nil || ports.Playback != nil || ports.Commands != nil) {
		options = append(options, agentloop.WithAudioSubsystem(audiosubsystem.New(*ports)))
	}
	return options
}

func appendToolExecution(options []agentloop.Option, config sessionduration.DuplexLoopOptions) []agentloop.Option {
	if config.ToolExecutor == nil {
		return append(options, agentloop.WithToolExecutionDisabled())
	}
	definitions := append([]messages.ToolDefinition(nil), config.ToolDefinitions...)
	if len(definitions) > 0 {
		options = append(options, agentloop.WithTools(definitions))
		if config.AdvertiseToolDefinitions {
			options = append(options, agentloop.WithSessionConfig(messages.SessionUpdateConfig{Tools: definitions}))
		}
	}
	options = append(options, agentloop.WithToolExecutor(config.ToolExecutor))
	if policy := config.ToolAcknowledgementPolicy; policy != nil {
		options = append(options, toolAcknowledgementOption(policy))
	}
	return options
}

func toolAcknowledgementOption(policy *sessionduration.DuplexToolAcknowledgementPolicy) agentloop.Option {
	longRunning := make(map[string]struct{}, len(policy.LongRunningToolNames))
	for _, name := range policy.LongRunningToolNames {
		if name != "" {
			longRunning[name] = struct{}{}
		}
	}
	return agentloop.WithToolAcknowledgementPolicy(agentloop.ToolAcknowledgementPolicy{
		Threshold: policy.Threshold,
		IsLongRunning: func(name string) bool {
			_, ok := longRunning[name]
			return ok
		},
	})
}

var _ sessionduration.DuplexLoopFactory = (*DuplexLoopFactory)(nil)
