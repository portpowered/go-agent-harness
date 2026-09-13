package service

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roommedia"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type Dependencies struct {
	Clock roommedia.Clock
}

type Service struct {
	clock roommedia.Clock
}

func New(dependencies Dependencies) *Service {
	clockSource := dependencies.Clock
	if clockSource == nil {
		clockSource = clock.Real{}
	}
	return &Service{clock: clockSource}
}

func normalizedContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (s *Service) ConvertProviderInput(pcm []byte, format roommedia.PCM16Format, providerRate int) ([]byte, error) {
	return s.convertProviderInput(pcm, format, providerRate)
}

func (s *Service) convertProviderInput(pcm []byte, format roommedia.PCM16Format, providerRate int) ([]byte, error) {
	if format.Channels != 1 {
		return nil, fmt.Errorf("%w: room input requires mono mixer audio, got %d channels", roommedia.ErrInvalidFormat, format.Channels)
	}
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("%w: got %d bytes", roommedia.ErrPCM16Truncated, len(pcm))
	}
	if providerRate == 0 {
		return append([]byte(nil), pcm...), nil
	}
	if format.SampleRate <= 0 {
		return nil, fmt.Errorf("%w: source sample rate %d", roommedia.ErrInvalidFormat, format.SampleRate)
	}
	samples, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return nil, fmt.Errorf("decode room participant PCM16: %w", err)
	}
	converted, err := audio.ResamplePCM16(samples, format.SampleRate, providerRate)
	if err != nil {
		return nil, fmt.Errorf("convert room input from %d Hz to provider rate %d Hz: %w", format.SampleRate, providerRate, err)
	}
	return codec.EncodePCM16(converted), nil
}

func (s *Service) EncodePCM16(samples []int16) []byte {
	return codec.EncodePCM16(samples)
}
