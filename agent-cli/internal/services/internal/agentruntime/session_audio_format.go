package agentruntime

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate"
	audioratewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const sessionRealtimeAudioSampleRate = audiorate.RealtimeSampleRate

// Deprecated: use audiorate.ErrSampleRateConflict at the public boundary.
var ErrSessionAudioSampleRateConflict = audiorate.ErrSampleRateConflict

type sessionAudioOutputConfigurer interface {
	SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
}

type sessionAudioInputConfigurer interface {
	SetSessionAudioInput(models.AudioFormat, models.SampleRate)
}

type sessionAudioRequestProvider interface {
	Request() inference.SessionRequest
}

type audiorateOutputAdapter struct{ target sessionAudioOutputConfigurer }

func (a audiorateOutputAdapter) SetSessionAudioOutput(_ audiorate.AudioFormat, rate int) {
	a.target.SetSessionAudioOutput(models.AudioFormatPCM16, models.SampleRate(rate))
}

type audiorateInputAdapter struct{ target sessionAudioInputConfigurer }

func (a audiorateInputAdapter) SetSessionAudioInput(_ audiorate.AudioFormat, rate int) {
	a.target.SetSessionAudioInput(models.AudioFormatPCM16, models.SampleRate(rate))
}

// Deprecated: retained as a source-compatible rate-resolution adapter.
func resolveSessionAudioSampleRate(opts SessionRunOptions, plan sessionRuntimePlan) (int, error) {
	return audioratewire.NewService().ResolveSampleRate(context.Background(), sessionAudioRateRequest(opts, plan))
}

func sessionAudioRateRequest(opts SessionRunOptions, plan sessionRuntimePlan) audiorate.RateResolutionRequest {
	request := audiorate.RateResolutionRequest{
		Provider:           plan.provider,
		Replay:             opts.ReplayPath != "",
		CapturedInputRate:  plan.inputAudioSampleRate,
		CapturedOutputRate: plan.outputAudioSampleRate,
	}
	if requested, ok := plan.inferencer.(sessionAudioRequestProvider); ok {
		config := requested.Request().Config
		request.RequestedInputRate = int(config.InputAudioSampleRate)
		request.RequestedOutputRate = int(config.OutputAudioSampleRate)
	}
	return request
}

// Deprecated: retained as a source-compatible configuration adapter.
func configureSessionAudioContract(opts SessionRunOptions, plan *sessionRuntimePlan) error {
	if plan == nil {
		return nil
	}
	request := audiorate.ConfigureRequest{Resolution: sessionAudioRateRequest(opts, *plan)}
	if configurer, ok := plan.inferencer.(sessionAudioOutputConfigurer); ok {
		request.Output = audiorateOutputAdapter{target: configurer}
	}
	if configurer, ok := plan.inferencer.(sessionAudioInputConfigurer); ok {
		request.Input = audiorateInputAdapter{target: configurer}
	}
	rate, err := audioratewire.NewService().ConfigureSessionAudioContract(context.Background(), request)
	if err != nil {
		return err
	}
	plan.inputAudioSampleRate, plan.outputAudioSampleRate = rate, rate
	return nil
}
