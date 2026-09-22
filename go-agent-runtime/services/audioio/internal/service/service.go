// Package service is the private implementation of audioio.Service.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

type Service struct{}

func New() *Service { return &Service{} }

func (s *Service) ResolveRates(ctx context.Context, request audioio.RateRequest) (audioio.RateResolution, error) {
	if ctx == nil {
		return audioio.RateResolution{}, errors.New("audio rate resolution context is required")
	}
	if err := ctx.Err(); err != nil {
		return audioio.RateResolution{}, err
	}
	inRate, outRate := request.CapturedInputRate, request.CapturedOutputRate
	if inRate <= 0 {
		inRate = request.RequestedInputRate
	}
	if outRate <= 0 {
		outRate = request.RequestedOutputRate
	}
	if inRate > 0 && outRate > 0 && inRate != outRate {
		return audioio.RateResolution{}, fmt.Errorf("%w: input=%d output=%d", audioio.ErrSampleRateConflict, inRate, outRate)
	}
	rate := inRate
	if rate <= 0 {
		rate = outRate
	}
	if rate <= 0 {
		rate = audioio.DefaultSampleRate
		if !request.Replay && (strings.EqualFold(strings.TrimSpace(request.Provider), audioio.ProviderOpenAI) || strings.EqualFold(strings.TrimSpace(request.Provider), audioio.ProviderGrok)) {
			rate = audioio.RealtimeSampleRate
		}
	}
	if err := validateRate(rate); err != nil {
		return audioio.RateResolution{}, err
	}
	return audioio.RateResolution{InputRate: rate, OutputRate: rate}, nil
}

func (s *Service) ConvertPCM16(ctx context.Context, request audioio.PCM16Request) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("audio PCM conversion context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sourceRate, targetRate, err := resolveConversionRates(request)
	if err != nil {
		return nil, err
	}
	return convertPCM16Payload(ctx, request, sourceRate, targetRate)
}

func (s *Service) ConvertScheduledInputs(ctx context.Context, inputs []audioio.ScheduledAudioInput, providerRate int) ([]audioio.ScheduledAudioInput, error) {
	if inputs == nil {
		return nil, nil
	}
	converted := make([]audioio.ScheduledAudioInput, len(inputs))
	for index, input := range inputs {
		pcm, err := s.ConvertPCM16(ctx, audioio.PCM16Request{
			PCM: input.PCM, SourceRate: input.SourceSampleRate, TargetRate: providerRate,
		})
		if err != nil {
			return nil, fmt.Errorf("convert scheduled audio input %d: convert session input from %d Hz to provider rate %d Hz: %w", index+1, input.SourceSampleRate, providerRate, err)
		}
		converted[index] = input
		converted[index].PCM = pcm
		converted[index].SourceSampleRate = providerRate
	}
	return converted, nil
}

func resolveConversionRates(request audioio.PCM16Request) (int, int, error) {
	sourceRate, targetRate := request.SourceRate, request.TargetRate
	if targetRate == 0 {
		targetRate = sourceRate
	}
	if targetRate == 0 {
		targetRate = audioio.DefaultSampleRate
	}
	if sourceRate == 0 {
		sourceRate = targetRate
	}
	if err := validateRate(sourceRate); err != nil {
		return 0, 0, err
	}
	if err := validateRate(targetRate); err != nil {
		return 0, 0, err
	}
	return sourceRate, targetRate, nil
}

func convertPCM16Payload(ctx context.Context, request audioio.PCM16Request, sourceRate, targetRate int) ([]byte, error) {
	// A zero source rate is the injected/replay contract: the payload is
	// already encoded at the resolved provider rate. Preserve that payload
	// verbatim, including compatibility fixtures that use a marker byte.
	if request.SourceRate == 0 {
		return request.PCM, nil
	}
	if len(request.PCM)%2 != 0 {
		return nil, fmt.Errorf("%w: %w", audioio.ErrPCM16Truncated, codec.ErrPCM16OddLength)
	}
	sourceChannels, targetChannels := request.SourceChannels, request.TargetChannels
	if sourceChannels == 0 {
		sourceChannels = sharedaudio.Channels
	}
	if targetChannels == 0 {
		targetChannels = sharedaudio.Channels
	}
	if sourceRate == targetRate && sourceChannels == targetChannels {
		return request.PCM, nil
	}
	converted, err := sharedaudio.ConvertPCM16Bytes(request.PCM, sourceChannels, sourceRate, targetChannels, targetRate)
	if err != nil {
		return nil, fmt.Errorf("convert PCM16: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return converted, nil
}

func (s *Service) OpenInput(ctx context.Context, request audioio.InputRequest) (audioio.Input, error) {
	return newInput(ctx, request)
}

func (s *Service) OpenOutput(ctx context.Context, request audioio.OutputRequest) (audioio.Output, error) {
	return newOutput(ctx, request)
}

func (s *Service) ApplyVoicePCM16(ctx context.Context, request audioio.VoicePCMRequest) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("audio voice transformation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(request.PCM)%2 != 0 {
		return nil, fmt.Errorf("%w: %w", audioio.ErrPCM16Truncated, codec.ErrPCM16OddLength)
	}
	normalizer := sharedaudio.NewLoudnessNormalizer(sharedaudio.LoudnessNormalizerConfig{GainDB: s.VoiceGainDB(request.Voice)})
	return normalizer.ProcessBytes(request.PCM), nil
}

func (s *Service) NewTimer(source platformclock.Source, duration time.Duration) (platformclock.Timer, error) {
	timerSource, err := s.NewClock(source)
	if err != nil {
		return nil, err
	}
	timer := timerSource.NewTimer(duration)
	if timer == nil {
		return nil, fmt.Errorf("clock: timer source returned nil timer")
	}
	return timer, nil
}

func (s *Service) NewClock(source platformclock.Source) (platformclock.TimerSource, error) {
	if source == nil {
		source = platformclock.Real{}
	}
	timerSource, err := platformclock.RequireTimerSource(source)
	if err != nil {
		return nil, err
	}
	return timerSource, nil
}

func (s *Service) ResolveTranscription(request audioio.TranscriptionRequest) audioio.TranscriptionConfig {
	if request.Override != nil {
		return *request.Override
	}
	if !request.AcceptsAudioInput || request.Replay || !strings.EqualFold(strings.TrimSpace(request.Provider), audioio.ProviderOpenAI) {
		return audioio.TranscriptionConfig{}
	}
	model := strings.TrimSpace(request.Model)
	if request.Disabled {
		return audioio.TranscriptionConfig{Model: model}
	}
	if model == "" {
		model = audioio.DefaultTranscriptionModel
	}
	return audioio.TranscriptionConfig{Enabled: true, Model: model}
}

func (s *Service) VoiceGainDB(voice string) float64 { return sharedaudio.VoiceLoudnessGainDB(voice) }

func validateRate(rate int) error {
	if rate <= 0 {
		return fmt.Errorf("%w: rate=%d", audioio.ErrUnsupportedRate, rate)
	}
	if _, err := wavio.Resample(nil, rate, rate); err != nil {
		return fmt.Errorf("%w: %w", audioio.ErrUnsupportedRate, err)
	}
	return nil
}

var _ audioio.Service = (*Service)(nil)
