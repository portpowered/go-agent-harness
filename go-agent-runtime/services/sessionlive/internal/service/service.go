// Package service contains the private live-loop implementation.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive"
)

const defaultFirstTurnTimeout = 30 * time.Second

// Service owns the invocation-scoped loop and terminal boundary. It contains
// no provider, device, credential, CLI, or process-global state.
type Service struct{}

var _ sessionlive.Service = (*Service)(nil)

func New() *Service { return &Service{} }

func (s *Service) NewLoop(opts sessionlive.LoopOptions) (*sessionlive.Loop, error) {
	if opts.Inferencer == nil {
		return nil, errors.New("session live inferencer is required")
	}
	loopOpts := []agentloop.Option{agentloop.WithMode(engine.DuplexSession), agentloop.WithSessionInferencer(opts.Inferencer)}
	config, hasConfig := cloneSessionConfig(opts.SessionConfig)
	if opts.Audio != nil {
		loopOpts = append(loopOpts, agentloop.WithAudioSubsystem(opts.Audio))
	}
	if opts.ToolExecutor == nil {
		if hasConfig {
			loopOpts = append(loopOpts, agentloop.WithSessionConfig(*config))
		}
		return newLoop(append(loopOpts, agentloop.WithToolExecutionDisabled())...)
	}
	if len(opts.ToolDefinitions) > 0 {
		definitions := messages.CanonicalToolDefinitions(opts.ToolDefinitions)
		loopOpts = append(loopOpts, agentloop.WithTools(definitions))
		if opts.AdvertiseToolDefinitions {
			if !hasConfig {
				config = &messages.SessionUpdateConfig{}
				hasConfig = true
			}
			config.Tools = messages.CanonicalToolDefinitions(definitions)
		}
	}
	if hasConfig {
		loopOpts = append(loopOpts, agentloop.WithSessionConfig(*config))
	}
	loopOpts = append(loopOpts, agentloop.WithToolExecutor(opts.ToolExecutor))
	if opts.ToolAcknowledgement != nil {
		policy := *opts.ToolAcknowledgement
		policy.IsLongRunning = cloneLongRunningPolicy(policy.IsLongRunning)
		loopOpts = append(loopOpts, agentloop.WithToolAcknowledgementPolicy(agentloop.ToolAcknowledgementPolicy{Threshold: policy.Threshold, IsLongRunning: policy.IsLongRunning}))
	}
	return newLoop(loopOpts...)
}

func cloneSessionConfig(input *sessionlive.SessionUpdateConfig) (*messages.SessionUpdateConfig, bool) {
	if input == nil {
		return nil, false
	}
	config := *input
	config.Modalities = append([]string(nil), input.Modalities...)
	config.Tools = messages.CanonicalToolDefinitions(input.Tools)
	return &config, true
}

func newLoop(opts ...agentloop.Option) (*sessionlive.Loop, error) {
	loop, err := agentloop.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("create session agent loop: %w", err)
	}
	return loop, nil
}

func cloneLongRunningPolicy(policy func(string) bool) func(string) bool {
	if policy == nil {
		return nil
	}
	return func(name string) bool { return policy(name) }
}

func (s *Service) Run(ctx context.Context, opts sessionlive.RunOptions) error {
	if err := validateRun(ctx, opts); err != nil {
		return err
	}
	run, err := newRun(ctx, opts)
	if err != nil {
		return err
	}
	defer run.close()
	return run.loop()
}

func validateRun(ctx context.Context, opts sessionlive.RunOptions) error {
	if ctx == nil {
		return errors.New("session live context is required")
	}
	if opts.Loop == nil {
		return errors.New("session live loop is required")
	}
	if opts.Handler == nil {
		return errors.New("session live message handler is required")
	}
	return nil
}
