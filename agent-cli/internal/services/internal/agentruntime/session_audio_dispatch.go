package agentruntime

import (
	"context"
	"errors"
	"fmt"

	public "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
)

func (d *Dispatcher) prepareAudioInterruptions(ctx context.Context, options SessionRunOptions, request public.Request) (SessionRunOptions, error) {
	if len(request.AudioInterrupts) == 0 {
		return options, nil
	}
	if options.BrowserWatch == nil {
		return SessionRunOptions{}, closeSessionAudioInterruptionCapability(options, errors.New("--audio-interrupt requires an enabled WebMCP session capability"))
	}
	inputs, err := prepareScheduledAudioInputsContext(ctx, request.AudioInterrupts)
	if err != nil {
		return SessionRunOptions{}, closeSessionAudioInterruptionCapability(options, fmt.Errorf("prepare --audio-interrupt: %w", err))
	}
	interruptions, _ := StartSessionAudioInterruptionsOnBrowserTool(ctx, options.BrowserWatch(ctx), request.AudioInterruptTool, inputs)
	options.AudioInterruptions = interruptions
	return options, nil
}

func closeSessionAudioInterruptionCapability(options SessionRunOptions, err error) error {
	if options.CapabilityClose == nil {
		return err
	}
	return errors.Join(err, options.CapabilityClose())
}

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
