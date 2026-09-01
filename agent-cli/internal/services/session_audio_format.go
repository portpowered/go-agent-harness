package services

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const sessionRealtimeAudioSampleRate = int(models.SampleRate24000)

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

func sessionAudioOutputRequested(opts SessionRunOptions) bool {
	return opts.AudioOutputRequested || opts.RTCDeviceBinding.outputSelected()
}

func sessionAudioInputDeviceRequested(opts SessionRunOptions) bool {
	return opts.RTCDeviceBinding.inputSelected()
}

func sessionOutputAudioSampleRate(opts SessionRunOptions, plan sessionRuntimePlan) int {
	if plan.outputAudioSampleRate > 0 {
		return plan.outputAudioSampleRate
	}
	if requested, ok := plan.inferencer.(sessionAudioRequestProvider); ok {
		if rate := int(requested.Request().Config.OutputAudioSampleRate); rate > 0 {
			return rate
		}
	}
	// A caller-supplied inferencer owns its media contract unless it exposes a
	// concrete request rate. This keeps low-level test/integration seams
	// compatible with their existing 16 kHz virtual devices while the normal
	// provider constructors still receive the explicit realtime rate below.
	if opts.SessionInferencer == nil && opts.ReplayPath == "" && sessionAudioOutputRequested(opts) && (plan.provider == sessionProviderOpenAI || plan.provider == sessionProviderGrok) {
		return sessionRealtimeAudioSampleRate
	}
	return audio.SampleRate
}

func sessionInputAudioSampleRate(opts SessionRunOptions, plan sessionRuntimePlan) int {
	if plan.inputAudioSampleRate > 0 {
		return plan.inputAudioSampleRate
	}
	if requested, ok := plan.inferencer.(sessionAudioRequestProvider); ok {
		if rate := int(requested.Request().Config.InputAudioSampleRate); rate > 0 {
			return rate
		}
	}
	if opts.SessionInferencer == nil && opts.ReplayPath == "" && sessionAudioInputDeviceRequested(opts) && (plan.provider == sessionProviderOpenAI || plan.provider == sessionProviderGrok) {
		return sessionRealtimeAudioSampleRate
	}
	return audio.SampleRate
}

func configureSessionAudioOutput(opts SessionRunOptions, plan *sessionRuntimePlan) {
	if plan == nil {
		return
	}
	rate := sessionOutputAudioSampleRate(opts, *plan)
	plan.outputAudioSampleRate = rate
	inputRate := sessionInputAudioSampleRate(opts, *plan)
	plan.inputAudioSampleRate = inputRate
	if sessionAudioOutputRequested(opts) {
		if configurer, ok := plan.inferencer.(sessionAudioOutputConfigurer); ok {
			configurer.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate(rate))
		}
	}
	if sessionAudioOutputRequested(opts) || sessionAudioInputDeviceRequested(opts) {
		if configurer, ok := plan.inferencer.(sessionAudioInputConfigurer); ok {
			configurer.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate(inputRate))
		}
	}
}
