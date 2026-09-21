package agentruntime

import (
	"errors"
	"fmt"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const sessionRealtimeAudioSampleRate = int(models.SampleRate24000)

var ErrSessionAudioSampleRateConflict = errors.New("session input and output sample rates conflict")

// sessionAudioOutputConfigurer is deliberately smaller than the provider or
// inference implementation. It lets the runtime planner apply the resolved
// output contract to the concrete gateway bridge without coupling the service
// package to a provider implementation.
type sessionAudioOutputConfigurer interface {
	SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
}

type sessionAudioInputConfigurer interface {
	SetSessionAudioInput(models.AudioFormat, models.SampleRate)
}

type sessionAudioRequestProvider interface {
	Request() inference.SessionRequest
}

func resolveSessionAudioSampleRate(opts SessionRunOptions, plan sessionRuntimePlan) (int, error) {
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
	if inputRate > 0 && outputRate > 0 && inputRate != outputRate {
		return 0, fmt.Errorf("%w: input=%d Hz output=%d Hz", ErrSessionAudioSampleRateConflict, inputRate, outputRate)
	}
	if inputRate > 0 {
		return inputRate, nil
	}
	if outputRate > 0 {
		return outputRate, nil
	}
	// Realtime provider plans own one explicit 24 kHz duplex contract even when
	// the caller supplies the inferencer. Legacy 16 kHz seams retain that rate
	// only by declaring it explicitly in their request or captured handshake;
	// native sources and sinks are converted at the harness boundary. A
	// replayed capture that never declared its own handshake rate is not
	// talking to a live provider enforcing a minimum, so it keeps the
	// harness compatibility default instead of silently reinterpreting an
	// undeclared hermetic fixture at the live realtime rate.
	if opts.ReplayPath == "" && (plan.provider == sessionProviderOpenAI || plan.provider == sessionProviderGrok) {
		return sessionRealtimeAudioSampleRate, nil
	}
	return audio.SampleRate, nil
}

func configureSessionAudioContract(opts SessionRunOptions, plan *sessionRuntimePlan) error {
	if plan == nil {
		return nil
	}
	rate, err := resolveSessionAudioSampleRate(opts, *plan)
	if err != nil {
		return err
	}
	plan.outputAudioSampleRate = rate
	plan.inputAudioSampleRate = rate
	if configurer, ok := plan.inferencer.(sessionAudioOutputConfigurer); ok {
		configurer.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate(rate))
	}
	if configurer, ok := plan.inferencer.(sessionAudioInputConfigurer); ok {
		configurer.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate(rate))
	}
	return nil
}
