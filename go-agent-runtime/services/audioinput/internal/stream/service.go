// Package stream contains the private audio-input lifecycle implementation.
// It depends only on the public audioinput contract and reusable audio/loop
// primitives; hosts and providers are not part of this package.
package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioinput"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const terminationSettle = time.Duration(audio.FrameSize) * time.Second / time.Duration(audio.SampleRate)

// Service is the private implementation behind audioinput.Service.
type Service struct{ defaultClock clock.Source }

func New(source clock.Source) *Service { return &Service{defaultClock: clock.Ensure(source)} }

func (s *Service) Validate(input audioinput.Input) error {
	if err := audioinput.ValidateInput(input); err != nil {
		return err
	}
	return validateRates(input.Path, input.SourceSampleRate, input.ProviderSampleRate)
}

func (s *Service) Read(ctx context.Context, input audioinput.Input) ([]byte, int, error) {
	if err := s.Validate(input); err != nil {
		return nil, 0, err
	}
	source, owned, err := sourceFor(input)
	if err != nil {
		return nil, 0, withPath(input.Path, err, audioinput.KindFormat)
	}
	if owned {
		defer func() { _ = source.Close() }()
	}
	rate := input.SourceSampleRate
	if rate <= 0 {
		rate = sourceRate(source, audio.SampleRate)
	}
	pcm, rate, err := audioinput.ReadPCM(ctx, source, rate)
	if err != nil {
		return nil, 0, withPath(input.Path, err, audioinput.KindRead)
	}
	return pcm, rate, nil
}

func (s *Service) Stream(ctx context.Context, input audioinput.Input, loop audioinput.SessionLoop) (runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.Validate(input); err != nil {
		return err
	}
	source, owned, err := sourceFor(input)
	if err != nil {
		return withPath(input.Path, err, audioinput.KindFormat)
	}
	if owned {
		defer func() {
			if closeErr := source.Close(); closeErr != nil {
				runErr = errors.Join(runErr, withPath(input.Path, closeErr, audioinput.KindClose))
			}
		}()
	}
	if bound, ok := source.(interface{ BindContext(context.Context) }); ok {
		bound.BindContext(ctx)
	}
	sourceRate := input.SourceSampleRate
	if sourceRate <= 0 {
		sourceRate = sourceRateFromSource(source)
	}
	if sourceRate <= 0 {
		sourceRate = audio.SampleRate
	}
	providerRate := input.ProviderSampleRate
	if providerRate <= 0 {
		providerRate = sourceRate
	}
	if err := validateRates(input.Path, sourceRate, providerRate); err != nil {
		return err
	}
	quantum := audio.FrameSize * providerRate / audio.SampleRate
	framer, err := audio.NewPCM16Framer(sourceRate, providerRate, quantum)
	if err != nil {
		return withPath(input.Path, err, audioinput.KindFormat)
	}
	streamClock := input.Clock
	if streamClock == nil && s != nil {
		streamClock = s.defaultClock
	}
	if input.Pace {
		if _, err := clock.RequireTimerSource(streamClock); err != nil {
			return withPath(input.Path, err, audioinput.KindRead)
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return withPath(input.Path, ctxErr, audioinput.KindRead)
	}
	clockSource := clock.Ensure(streamClock)
	start := clockSource.Now()
	frameDuration := time.Duration(audio.FrameSize) * time.Second / time.Duration(sourceRate)
	frame := make([]int16, audio.FrameSize)
	sequence := atomic.Uint64{}
	received := false
	endSent := false
	for frameIndex := 0; ; frameIndex++ {
		if input.Pace && frameIndex > 0 {
			target := start.Add(time.Duration(frameIndex) * frameDuration)
			if delay := target.Sub(clockSource.Now()); delay > 0 {
				if err := clock.Wait(ctx, streamClock, delay); err != nil {
					return withPath(input.Path, err, audioinput.KindRead)
				}
			}
		}
		clear(frame)
		readErr := source.ReadFrame(ctx, frame)
		if errors.Is(readErr, audio.ErrEndOfTurn) {
			if !received {
				return audioinput.NewEmptyError(input.Path)
			}
			if !endSent {
				if err := s.finish(ctx, input, loop, framer, input.Pace, frameDuration, sourceRate, providerRate, &sequence); err != nil {
					return err
				}
				endSent = true
			}
			continue
		}
		if errors.Is(readErr, io.EOF) {
			if !received {
				return audioinput.NewEmptyError(input.Path)
			}
			if endSent {
				return nil
			}
			return s.finish(ctx, input, loop, framer, input.Pace, frameDuration, sourceRate, providerRate, &sequence)
		}
		if readErr != nil {
			return withPath(input.Path, readErr, audioinput.KindRead)
		}
		payload := make([]byte, len(frame)*2)
		if err := codec.EncodePCM16Into(payload, frame); err != nil {
			return withPath(input.Path, err, audioinput.KindFormat)
		}
		frames, err := framer.Push(payload)
		if err != nil {
			return withPath(input.Path, err, audioinput.KindFormat)
		}
		received = true
		endSent = false
		if err := s.sendFrames(ctx, input, loop, frames, sourceRate, providerRate, frameIndex, clockSource, &sequence); err != nil {
			return err
		}
	}
}

func (s *Service) finish(ctx context.Context, input audioinput.Input, loop audioinput.SessionLoop, framer *audio.PCM16Framer, paced bool, frameDuration time.Duration, sourceRate, providerRate int, sequence *atomic.Uint64) error {
	frames, err := framer.Flush()
	if err != nil {
		return withPath(input.Path, err, audioinput.KindFormat)
	}
	clockSource := clock.Ensure(input.Clock)
	if input.Clock == nil && s != nil {
		clockSource = clock.Ensure(s.defaultClock)
	}
	if err := s.sendFrames(ctx, input, loop, frames, sourceRate, providerRate, -1, clockSource, sequence); err != nil {
		return err
	}
	if paced && terminationSettle > frameDuration {
		if err := clock.Wait(ctx, clockSource, terminationSettle-frameDuration); err != nil {
			return withPath(input.Path, err, audioinput.KindRead)
		}
	}
	return s.sendEnd(ctx, input, loop)
}

func (s *Service) sendFrames(ctx context.Context, input audioinput.Input, loop audioinput.SessionLoop, frames [][]byte, sourceRate, providerRate, frameIndex int, sourceClock clock.Source, sequence *atomic.Uint64) error {
	for _, frame := range frames {
		send := input.SendAudioInput
		if send == nil && loop != nil {
			send = loop.SendAudioInput
		}
		if send == nil {
			return withPath(input.Path, audioinput.ErrUnavailable, audioinput.KindSend)
		}
		if err := send(ctx, frame); err != nil {
			return withPath(input.Path, err, audioinput.KindSend)
		}
		if input.Observer != nil {
			sequenceNumber := sequence.Add(1)
			tick := sequenceNumber
			if ticker, ok := sourceClock.(interface{ Tick() uint64 }); ok {
				tick = ticker.Tick()
			}
			input.Observer.ObserveAudioInput(audioinput.AudioObservation{
				Sequence: sequenceNumber, FrameIndex: frameIndex, Tick: tick, Timestamp: sourceClock.Now(),
				Path: input.Path, CorrelationID: input.CorrelationID, SourceSampleRate: sourceRate,
				ProviderSampleRate: providerRate, PCM: append([]byte(nil), frame...),
			})
		}
	}
	return nil
}

func (s *Service) sendEnd(ctx context.Context, input audioinput.Input, loop audioinput.SessionLoop) error {
	send := input.SendEndOfTurn
	if send == nil && loop != nil {
		send = func(ctx context.Context) error {
			return loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
		}
	}
	if send == nil {
		return withPath(input.Path, audioinput.ErrUnavailable, audioinput.KindSend)
	}
	if err := send(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return withPath(input.Path, fmt.Errorf("%w (%v)", audioinput.ErrEndOfTurnLost, err), audioinput.KindSend)
		}
		return withPath(input.Path, fmt.Errorf("end-of-turn signaling: %w", err), audioinput.KindSend)
	}
	return nil
}

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
	if !input.EndOfTurn {
		return nil
	}
	if err := loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return withPath(options.Path, errors.Join(audioinput.ErrEndOfTurnLost, err), audioinput.KindSend)
		}
		return withPath(options.Path, fmt.Errorf("end-of-turn signaling: %w", err), audioinput.KindSend)
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
		source, err := audioinput.NewBufferSource(input.Buffer)
		return source, true, err
	}
	if input.Reader != nil {
		source, err := audioinput.NewReaderSource(input.Reader, input.CloseOnCancel)
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
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("%w: %d bytes at %d Hz", audioinput.ErrPCM16Truncated, len(pcm), sourceRate)
	}
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
		return append([]byte(nil), pcm...), nil
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

func withPath(path string, err error, kind audioinput.ErrorKind) error {
	if err == nil {
		return nil
	}
	var typed *audioinput.Error
	if errors.As(err, &typed) {
		if typed.Path != "" || path == "" {
			return err
		}
		clone := *typed
		clone.Path = path
		return &clone
	}
	return &audioinput.Error{Kind: kind, Path: path, Err: err}
}

var _ audioinput.Service = (*Service)(nil)
