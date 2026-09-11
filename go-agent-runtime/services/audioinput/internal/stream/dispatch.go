package stream

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func (s *Service) Dispatch(ctx context.Context, loop audioinput.SessionLoop, input audioinput.ScheduledInput, options audioinput.DispatchOptions) error {
	if len(input.PCM) == 0 {
		return withPath(options.Path, audioinput.ErrEmpty, audioinput.KindEmpty)
	}
	pcm := append([]byte(nil), input.PCM...)
	providerRate := resolvedProviderRate(input.SourceSampleRate, options.ProviderSampleRate)
	pcm, err := convertPCM(pcm, input.SourceSampleRate, providerRate)
	if err != nil {
		return withPath(options.Path, err, audioinput.KindFormat)
	}
	if loop == nil {
		return withPath(options.Path, audioinput.ErrUnavailable, audioinput.KindSend)
	}
	if err := loop.SendAudioInput(ctx, pcm); err != nil {
		return withPath(options.Path, err, audioinput.KindSend)
	}
	if options.Observer != nil {
		s.observeDispatch(options, input, providerRate, pcm)
	}
	if !input.EndOfTurn {
		return nil
	}
	return sendDispatchEnd(ctx, loop, options.Path)
}

func (s *Service) observeDispatch(options audioinput.DispatchOptions, input audioinput.ScheduledInput, providerRate int, pcm []byte) {
	sourceClock := clock.Ensure(options.Clock)
	sequence := uint64(1)
	tick := sequence
	if ticker, ok := sourceClock.(interface{ Tick() uint64 }); ok {
		tick = ticker.Tick()
	}
	options.Observer.ObserveAudioInput(audioinput.AudioObservation{
		Sequence: sequence, FrameIndex: -1, Tick: tick, Timestamp: sourceClock.Now(), Path: options.Path,
		CorrelationID: options.CorrelationID, SourceSampleRate: input.SourceSampleRate,
		ProviderSampleRate: providerRate, PCM: append([]byte(nil), pcm...),
	})
}

func sendDispatchEnd(ctx context.Context, loop audioinput.SessionLoop, path string) error {
	if err := loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return withPath(path, errors.Join(audioinput.ErrEndOfTurnLost, err), audioinput.KindSend)
		}
		return withPath(path, fmt.Errorf("end-of-turn signaling: %w", err), audioinput.KindSend)
	}
	return nil
}

func (s *Service) ConvertPCM(pcm []byte, sourceRate, providerRate int) ([]byte, error) {
	return convertPCM(pcm, sourceRate, providerRate)
}

func (s *Service) ConvertScheduled(inputs []audioinput.ScheduledInput, providerRate int) ([]audioinput.ScheduledInput, error) {
	if inputs == nil {
		return nil, nil
	}
	converted := make([]audioinput.ScheduledInput, len(inputs))
	for index, input := range inputs {
		pcm, err := convertPCM(append([]byte(nil), input.PCM...), input.SourceSampleRate, providerRate)
		if err != nil {
			return nil, fmt.Errorf("convert scheduled audio input %d: %w", index+1, err)
		}
		converted[index] = input
		converted[index].PCM = pcm
		converted[index].SourceSampleRate = resolvedProviderRate(input.SourceSampleRate, providerRate)
	}
	return converted, nil
}

func sourceFor(input audioinput.Input) (audio.AudioSource, bool, error) {
	if input.Source != nil {
		return input.Source, false, nil
	}
	if input.Buffer != nil {
		source, err := NewBufferSource(input.Buffer)
		return source, true, err
	}
	if input.Reader != nil {
		source, err := NewReaderSource(input.Reader, input.CloseOnCancel)
		return source, true, err
	}
	return nil, false, audioinput.ErrEmpty
}

func sourceRateFromSource(source audio.AudioSource) int {
	return sourceRate(source, 0)
}

func sourceRate(source audio.AudioSource, fallback int) int {
	if rated, ok := source.(interface{ SampleRate() int }); ok && rated.SampleRate() > 0 {
		return rated.SampleRate()
	}
	return fallback
}

func validateRates(path string, sourceRate, providerRate int) error {
	if sourceRate <= 0 {
		sourceRate = audio.SampleRate
	}
	if providerRate <= 0 {
		providerRate = sourceRate
	}
	if _, err := wavio.Resample(nil, sourceRate, providerRate); err != nil {
		return withPath(path, err, audioinput.KindFormat)
	}
	return nil
}

func convertPCM(pcm []byte, sourceRate, providerRate int) ([]byte, error) {
	if providerRate <= 0 {
		providerRate = resolvedProviderRate(sourceRate, providerRate)
	}
	if sourceRate == 0 {
		if _, err := wavio.Resample(nil, providerRate, providerRate); err != nil {
			return nil, fmt.Errorf("validate injected session input at provider rate %d Hz: %w", providerRate, err)
		}
		return append([]byte(nil), pcm...), nil
	}
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("%w: %d bytes at %d Hz", audioinput.ErrPCM16Truncated, len(pcm), sourceRate)
	}
	if _, err := wavio.Resample(nil, sourceRate, providerRate); err != nil {
		return nil, fmt.Errorf("convert session input from %d Hz to provider rate %d Hz: %w", sourceRate, providerRate, err)
	}
	if sourceRate == providerRate {
		return pcm, nil
	}
	samples, err := codec.DecodePCM16WithLimit(pcm, len(pcm))
	if err != nil {
		return nil, fmt.Errorf("decode session input PCM16: %w", err)
	}
	converted, err := wavio.Resample(samples, sourceRate, providerRate)
	if err != nil {
		return nil, fmt.Errorf("convert session input from %d Hz to provider rate %d Hz: %w", sourceRate, providerRate, err)
	}
	return codec.EncodePCM16(converted), nil
}

func resolvedProviderRate(sourceRate, providerRate int) int {
	if providerRate > 0 {
		return providerRate
	}
	if sourceRate > 0 {
		return sourceRate
	}
	return audio.SampleRate
}
