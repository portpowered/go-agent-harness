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
)

const terminationSettle = time.Duration(audio.FrameSize) * time.Second / time.Duration(audio.SampleRate)

// Service is the private implementation behind audioinput.Service.
type Service struct{ defaultClock clock.Source }

func New(source clock.Source) *Service { return &Service{defaultClock: clock.Ensure(source)} }

func (s *Service) Validate(input audioinput.Input) error {
	if err := ValidateInput(input); err != nil {
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
	pcm, rate, err := ReadPCM(ctx, source, rate)
	if err != nil {
		return nil, 0, withPath(input.Path, err, audioinput.KindRead)
	}
	return pcm, rate, nil
}

type streamState struct {
	framer        *audio.PCM16Framer
	streamClock   clock.Source
	clockSource   clock.Source
	start         time.Time
	frame         []int16
	frameDuration time.Duration
	sourceRate    int
	providerRate  int
	sequence      atomic.Uint64
	received      bool
	endSent       bool
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
		defer func() { runErr = errors.Join(runErr, closeStreamSource(input.Path, source)) }()
	}
	if bound, ok := source.(interface{ BindContext(context.Context) }); ok {
		bound.BindContext(ctx)
	}
	state, err := s.newStreamState(ctx, input, source)
	if err != nil {
		return err
	}
	return s.runStream(ctx, input, loop, source, state)
}

func closeStreamSource(path string, source audio.AudioSource) error {
	if err := source.Close(); err != nil {
		return withPath(path, err, audioinput.KindClose)
	}
	return nil
}

func (s *Service) newStreamState(ctx context.Context, input audioinput.Input, source audio.AudioSource) (*streamState, error) {
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
		return nil, err
	}
	quantum := audio.FrameSize * providerRate / audio.SampleRate
	framer, err := audio.NewPCM16Framer(sourceRate, providerRate, quantum)
	if err != nil {
		return nil, withPath(input.Path, err, audioinput.KindFormat)
	}
	streamClock := input.Clock
	if streamClock == nil && s != nil {
		streamClock = s.defaultClock
	}
	if input.Pace {
		if _, err := clock.RequireTimerSource(streamClock); err != nil {
			return nil, withPath(input.Path, err, audioinput.KindRead)
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, withPath(input.Path, ctxErr, audioinput.KindRead)
	}
	clockSource := clock.Ensure(streamClock)
	return &streamState{
		framer:        framer,
		streamClock:   streamClock,
		clockSource:   clockSource,
		start:         clockSource.Now(),
		frame:         make([]int16, audio.FrameSize),
		frameDuration: time.Duration(audio.FrameSize) * time.Second / time.Duration(sourceRate),
		sourceRate:    sourceRate,
		providerRate:  providerRate,
	}, nil
}

func (s *Service) runStream(ctx context.Context, input audioinput.Input, loop audioinput.SessionLoop, source audio.AudioSource, state *streamState) error {
	for frameIndex := 0; ; frameIndex++ {
		done, err := s.readStreamFrame(ctx, input, loop, source, state, frameIndex)
		if err != nil || done {
			return err
		}
	}
}

func (s *Service) readStreamFrame(ctx context.Context, input audioinput.Input, loop audioinput.SessionLoop, source audio.AudioSource, state *streamState, frameIndex int) (bool, error) {
	if err := waitForStreamFrame(ctx, input, state, frameIndex); err != nil {
		return true, err
	}
	clear(state.frame)
	readErr := source.ReadFrame(ctx, state.frame)
	if errors.Is(readErr, audio.ErrEndOfTurn) {
		return false, s.handleStreamEnd(ctx, input, loop, state)
	}
	if errors.Is(readErr, io.EOF) {
		if !state.received {
			return true, newEmptyError(input.Path)
		}
		if state.endSent {
			return true, nil
		}
		return true, s.finish(ctx, input, loop, state.framer, input.Pace, state.frameDuration, state.sourceRate, state.providerRate, state.clockSource, &state.sequence)
	}
	if readErr != nil {
		return true, withPath(input.Path, readErr, audioinput.KindRead)
	}
	payload := make([]byte, len(state.frame)*2)
	if err := codec.EncodePCM16Into(payload, state.frame); err != nil {
		return true, withPath(input.Path, err, audioinput.KindFormat)
	}
	frames, err := state.framer.Push(payload)
	if err != nil {
		return true, withPath(input.Path, err, audioinput.KindFormat)
	}
	state.received = true
	state.endSent = false
	if err := s.sendFrames(ctx, input, loop, frames, state.sourceRate, state.providerRate, frameIndex, state.clockSource, &state.sequence); err != nil {
		return true, err
	}
	return false, nil
}

func waitForStreamFrame(ctx context.Context, input audioinput.Input, state *streamState, frameIndex int) error {
	if !input.Pace || frameIndex == 0 {
		return nil
	}
	target := state.start.Add(time.Duration(frameIndex) * state.frameDuration)
	if delay := target.Sub(state.clockSource.Now()); delay > 0 {
		if err := clock.Wait(ctx, state.streamClock, delay); err != nil {
			return withPath(input.Path, err, audioinput.KindRead)
		}
	}
	return nil
}

func (s *Service) handleStreamEnd(ctx context.Context, input audioinput.Input, loop audioinput.SessionLoop, state *streamState) error {
	if !state.received {
		return newEmptyError(input.Path)
	}
	if state.endSent {
		return nil
	}
	if err := s.finish(ctx, input, loop, state.framer, input.Pace, state.frameDuration, state.sourceRate, state.providerRate, state.clockSource, &state.sequence); err != nil {
		return err
	}
	state.endSent = true
	return nil
}

func (s *Service) finish(ctx context.Context, input audioinput.Input, loop audioinput.SessionLoop, framer *audio.PCM16Framer, paced bool, frameDuration time.Duration, sourceRate, providerRate int, sourceClock clock.Source, sequence *atomic.Uint64) error {
	frames, err := framer.Flush()
	if err != nil {
		return withPath(input.Path, err, audioinput.KindFormat)
	}
	if err := s.sendFrames(ctx, input, loop, frames, sourceRate, providerRate, -1, sourceClock, sequence); err != nil {
		return err
	}
	if paced && terminationSettle > frameDuration {
		if err := clock.Wait(ctx, sourceClock, terminationSettle-frameDuration); err != nil {
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
			return withPath(input.Path, errors.Join(audioinput.ErrEndOfTurnLost, err), audioinput.KindSend)
		}
		return withPath(input.Path, fmt.Errorf("end-of-turn signaling: %w", err), audioinput.KindSend)
	}
	return nil
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
