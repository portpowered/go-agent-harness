package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiorate"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// Service is the private, stateless implementation of audiorate.Service.
type Service struct{}

// New constructs an inert audio-rate service without external setup.
func New() *Service { return &Service{} }

// ConvertPCM validates and converts little-endian mono PCM16 to providerRate.
// A zero source rate means the caller has already supplied provider-rate PCM;
// a zero provider rate selects the source rate or the default rate.
func (s *Service) ConvertPCM(ctx context.Context, pcm []byte, sourceRate, providerRate int) ([]byte, error) {
	ctx = backgroundIfNil(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("%w: %w: got %d bytes", audiorate.ErrPCM16Truncated, codec.ErrPCM16OddLength, len(pcm))
	}
	targetRate := resolvedTargetRate(sourceRate, providerRate)
	if err := validateRates(sourceRate, targetRate); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sourceRate == 0 || sourceRate == targetRate {
		return pcm, nil
	}
	samples, err := codec.DecodePCM16(pcm)
	if err != nil {
		return nil, fmt.Errorf("decode session PCM16: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	converted, err := wavio.Resample(samples, sourceRate, targetRate)
	if err != nil {
		return nil, fmt.Errorf("resample session PCM16 from %d Hz to %d Hz: %w", sourceRate, targetRate, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return codec.EncodePCM16(converted), nil
}

// ConvertScheduledAudioInputs converts every scheduled item without changing
// its ordering or turn metadata. A failed item discards the partial result.
func (s *Service) ConvertScheduledAudioInputs(ctx context.Context, inputs []audiorate.ScheduledAudioInput, providerRate int) ([]audiorate.ScheduledAudioInput, error) {
	ctx = backgroundIfNil(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if providerRate != 0 {
		if err := validateRate(providerRate); err != nil {
			return nil, err
		}
	}
	if inputs == nil {
		return nil, nil
	}
	converted := make([]audiorate.ScheduledAudioInput, len(inputs))
	for index, input := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pcm, err := s.ConvertPCM(ctx, input.PCM, input.SourceSampleRate, providerRate)
		if err != nil {
			return nil, fmt.Errorf("convert scheduled audio input %d: %w", index+1, err)
		}
		converted[index] = input
		converted[index].PCM = pcm
		converted[index].SourceSampleRate = resolvedTargetRate(input.SourceSampleRate, providerRate)
	}
	return converted, nil
}

// ResolveSampleRate resolves captured/requested duplex rates before provider
// setup. Explicit facts win over defaults and asymmetric rates are rejected.
func (s *Service) ResolveSampleRate(ctx context.Context, request audiorate.RateResolutionRequest) (int, error) {
	ctx = backgroundIfNil(ctx)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	inputRate := request.CapturedInputRate
	outputRate := request.CapturedOutputRate
	if inputRate <= 0 {
		inputRate = request.RequestedInputRate
	}
	if outputRate <= 0 {
		outputRate = request.RequestedOutputRate
	}
	if inputRate > 0 && outputRate > 0 && inputRate != outputRate {
		return 0, fmt.Errorf("%w: input=%d Hz output=%d Hz", audiorate.ErrSampleRateConflict, inputRate, outputRate)
	}
	rate := inputRate
	if rate <= 0 {
		rate = outputRate
	}
	if rate <= 0 {
		rate = audiorate.DefaultSampleRate
		if !request.Replay && isRealtimeProvider(request.Provider) {
			rate = audiorate.RealtimeSampleRate
		}
	}
	if err := validateRate(rate); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return rate, nil
}

// ConfigureSessionAudioContract resolves one rate and applies the provider
// setters only after all validation has succeeded.
func (s *Service) ConfigureSessionAudioContract(ctx context.Context, request audiorate.ConfigureRequest) (int, error) {
	ctx = backgroundIfNil(ctx)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	rate, err := s.ResolveSampleRate(ctx, request.Resolution)
	if err != nil {
		return 0, err
	}
	if request.Output != nil {
		request.Output.SetSessionAudioOutput(audiorate.AudioFormatPCM16, rate)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if request.Input != nil {
		request.Input.SetSessionAudioInput(audiorate.AudioFormatPCM16, rate)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return rate, nil
}

func backgroundIfNil(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func resolvedTargetRate(sourceRate, providerRate int) int {
	if providerRate != 0 {
		return providerRate
	}
	if sourceRate != 0 {
		return sourceRate
	}
	return audiorate.DefaultSampleRate
}

func validateRates(sourceRate, targetRate int) error {
	if sourceRate == 0 {
		return validateRate(targetRate)
	}
	if _, err := wavio.Resample(nil, sourceRate, targetRate); err != nil {
		return unsupportedRateError(err)
	}
	return nil
}

func validateRate(rate int) error {
	if _, err := wavio.Resample(nil, rate, rate); err != nil {
		return unsupportedRateError(err)
	}
	return nil
}

func unsupportedRateError(err error) error {
	return fmt.Errorf("%w: %w", audiorate.ErrUnsupportedSampleRate, err)
}

func isRealtimeProvider(provider string) bool {
	provider = strings.TrimSpace(provider)
	return strings.EqualFold(provider, audiorate.ProviderOpenAI) || strings.EqualFold(provider, audiorate.ProviderGrok)
}
