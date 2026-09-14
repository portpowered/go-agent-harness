package agentruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const sessionRealtimeAudioSampleRate = int(models.SampleRate24000)

type ScheduledAudioInput = audioio.ScheduledAudioInput

type runtimeAudioOutputConfigurer interface {
	SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
}

type runtimeAudioInputConfigurer interface {
	SetSessionAudioInput(models.AudioFormat, models.SampleRate)
}

type sessionAudioRequestProvider interface {
	Request() inference.SessionRequest
}

func resolveSessionSampleRate(opts SessionRunOptions, plan sessionRuntimePlan) (int, error) {
	inputRate := plan.inputAudioSampleRate
	outputRate := plan.outputAudioSampleRate
	if requested, ok := plan.inferencer.(sessionAudioRequestProvider); ok {
		config := requested.Request().Config
		if inputRate <= 0 {
			inputRate = int(config.InputAudioSampleRate)
		}
		if outputRate <= 0 {
			outputRate = int(config.OutputAudioSampleRate)
		}
	}
	resolution, err := wire.NewService().ResolveRates(context.Background(), audioio.RateRequest{
		Provider:           plan.provider,
		Replay:             opts.ReplayPath != "",
		CapturedInputRate:  inputRate,
		CapturedOutputRate: outputRate,
	})
	if err != nil {
		return 0, err
	}
	return resolution.InputRate, nil
}

func configureSessionAudioContract(opts SessionRunOptions, plan *sessionRuntimePlan) error {
	if plan == nil {
		return nil
	}
	rate, err := resolveSessionSampleRate(opts, *plan)
	if err != nil {
		return err
	}
	plan.outputAudioSampleRate = rate
	plan.inputAudioSampleRate = rate
	if configurer, ok := plan.inferencer.(runtimeAudioOutputConfigurer); ok {
		configurer.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate(rate))
	}
	if configurer, ok := plan.inferencer.(runtimeAudioInputConfigurer); ok {
		configurer.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate(rate))
	}
	return nil
}

func convertSessionPCM16(pcm []byte, sourceRate, providerRate int) ([]byte, error) {
	if providerRate == 0 {
		providerRate = sourceRate
	}
	converted, err := wire.NewService().ConvertPCM16(context.Background(), audioio.PCM16Request{
		PCM: pcm, SourceRate: sourceRate, TargetRate: providerRate,
	})
	if err != nil {
		return nil, fmt.Errorf("convert session input from %d Hz to provider rate %d Hz: %w", sourceRate, providerRate, err)
	}
	return converted, nil
}

func convertScheduledInputs(inputs []ScheduledAudioInput, providerRate int) ([]ScheduledAudioInput, error) {
	if inputs == nil {
		return nil, nil
	}
	converted := make([]ScheduledAudioInput, len(inputs))
	for index, input := range inputs {
		pcm, err := convertSessionPCM16(input.PCM, input.SourceSampleRate, providerRate)
		if err != nil {
			return nil, fmt.Errorf("convert scheduled audio input %d: %w", index+1, err)
		}
		converted[index] = input
		converted[index].PCM = pcm
		converted[index].SourceSampleRate = providerRate
	}
	return converted, nil
}

func sendEventDrivenAudioInput(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions, input ScheduledAudioInput) error {
	if len(input.PCM) == 0 {
		return errors.New("event-driven audio input is empty")
	}
	pcm, err := convertSessionPCM16(input.PCM, input.SourceSampleRate, opts.InputAudioSampleRate)
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
