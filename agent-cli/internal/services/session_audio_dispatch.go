package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
)

func sendEventDrivenAudioInput(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions, input ScheduledAudioInput) error {
	if len(input.PCM) == 0 {
		return errors.New("event-driven audio input is empty")
	}
	pcm, err := convertSessionAudioPCM(input.PCM, input.SourceSampleRate, opts.InputAudioSampleRate)
	if err != nil {
		return fmt.Errorf("convert event-driven audio input: %w", err)
	}
	if err := loop.SendAudioInput(ctx, pcm); err != nil {
		return fmt.Errorf("send event-driven audio input: %w", err)
	}
	if opts.observer != nil {
		opts.observer.account(metrics.DirectionInput, metrics.ModalityAudio, len(pcm))
	}
	if !input.EndOfTurn {
		return nil
	}
	if err := loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); err != nil {
		return fmt.Errorf("send event-driven audio input end-of-turn: %w", err)
	}
	if opts.observer != nil {
		opts.observer.armProviderProgress()
	}
	return nil
}
