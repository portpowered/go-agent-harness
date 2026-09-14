package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type input struct {
	source         sharedaudio.AudioSource
	processor      *sharedaudio.Processor
	sourceRate     int
	pace           bool
	continuous     bool
	padFinalFrame  bool
	scheduler      platformclock.Scheduler
	onTurnBoundary func(context.Context) error
	pending        *sharedaudio.PCMFrame
	epoch          uint64
	hasSamples     bool
	turnHasSamples bool
	lastBoundary   bool
	closeOnce      sync.Once
	closeErr       error
}

func newInput(ctx context.Context, request audioio.InputRequest) (audioio.Input, error) {
	if request.Source == nil {
		return nil, errors.New("audio input source is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sourceRate := request.SourceRate
	if sourceRate <= 0 {
		sourceRate = sharedaudio.SampleRate
	}
	providerRate := request.ProviderRate
	if providerRate <= 0 {
		providerRate = audioio.DefaultSampleRate
	}
	quantum := providerRate * sharedaudio.FrameSize / sharedaudio.SampleRate
	if quantum <= 0 {
		quantum = sharedaudio.FrameSize
	}
	processor, err := sharedaudio.NewProcessor(sharedaudio.PCM16DeviceFormat(sourceRate), sharedaudio.PCM16DeviceFormat(providerRate), quantum)
	if err != nil {
		return nil, fmt.Errorf("create audio input processor: %w", err)
	}
	if request.Pace && request.Scheduler == nil {
		return nil, errors.New("paced audio input requires a scheduler")
	}
	return &input{source: request.Source, processor: processor, sourceRate: sourceRate, pace: request.Pace, continuous: request.Continuous, padFinalFrame: request.PadFinalFrame, scheduler: request.Scheduler, onTurnBoundary: request.OnTurnBoundary}, nil
}

func (i *input) Pump(ctx context.Context, outbound sharedaudio.OutboundMedia) error {
	if err := i.validatePump(ctx, outbound); err != nil {
		return err
	}
	frame := make([]int16, sharedaudio.FrameSize)
	start := i.pumpStart()
	sent, consumed := 0, 0
	for {
		if err := i.waitForNextFrame(ctx, start, consumed); err != nil {
			return err
		}
		written, count, eof, err := i.processFrame(ctx, outbound, frame, i.sampleSource())
		if err != nil {
			return err
		}
		sent += written
		consumed += count
		if eof {
			return i.finishEOF(ctx, outbound, sent)
		}
	}
}

func (i *input) validatePump(ctx context.Context, outbound sharedaudio.OutboundMedia) error {
	if i == nil || i.source == nil || i.processor == nil {
		return errors.New("audio input is unavailable")
	}
	if outbound == nil {
		return errors.New("audio input outbound media is nil")
	}
	if ctx == nil {
		return errors.New("audio input context is required")
	}
	return nil
}

func (i *input) pumpStart() time.Time {
	if i.pace {
		return i.scheduler.Now()
	}
	return time.Time{}
}

func (i *input) waitForNextFrame(ctx context.Context, start time.Time, consumed int) error {
	if i.pace && consumed > 0 {
		return waitForSamples(ctx, i.scheduler, start, consumed, i.sourceRate)
	}
	return nil
}

func (i *input) sampleSource() sharedaudio.SampleSource {
	source, _ := i.source.(sharedaudio.SampleSource)
	return source
}

func (i *input) finishEOF(ctx context.Context, outbound sharedaudio.OutboundMedia, sent int) error {
	if sent == 0 && !i.hasSamples {
		return audioio.ErrEmptyInput
	}
	if i.continuous {
		return i.finishContinuousEOF(ctx)
	}
	return i.finishTurn(ctx, outbound, false)
}

// finishContinuousEOF closes the source-owned turn without asking the
// resampler for a finite tail. Explicit ErrEndOfTurn remains the only
// continuous boundary that flushes the resampler.
func (i *input) finishContinuousEOF(ctx context.Context) error {
	if !i.turnHasSamples && i.lastBoundary {
		return nil
	}
	if err := i.notifyBoundary(ctx); err != nil {
		return err
	}
	i.turnHasSamples = false
	i.lastBoundary = true
	return nil
}

func (i *input) processFrame(ctx context.Context, outbound sharedaudio.OutboundMedia, frame []int16, source sharedaudio.SampleSource) (int, int, bool, error) {
	count, readErr := i.readFrame(ctx, frame, source)
	if errors.Is(readErr, sharedaudio.ErrEndOfTurn) {
		if !i.turnHasSamples {
			return 0, 0, false, audioio.ErrEmptyInput
		}
		if err := i.finishTurn(ctx, outbound, true); err != nil {
			return 0, 0, false, err
		}
		return 0, 0, false, nil
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return 0, 0, false, fmt.Errorf("read audio input: %w", readErr)
	}
	if count == 0 {
		if errors.Is(readErr, io.EOF) {
			return 0, 0, true, nil
		}
		return 0, 0, false, io.ErrNoProgress
	}
	frames, err := i.processSamples(frame, count)
	if err != nil {
		return 0, 0, false, err
	}
	i.hasSamples = true
	i.turnHasSamples = true
	i.lastBoundary = false
	written, err := i.writeOpenFrames(ctx, outbound, frames)
	if err != nil {
		return written, count, false, err
	}
	return written, count, errors.Is(readErr, io.EOF), nil
}

func (i *input) processSamples(frame []int16, count int) ([]sharedaudio.PCMFrame, error) {
	process := i.processor.Process
	if i.continuous {
		process = i.processor.ProcessAvailable
	}
	frames, err := process(sharedaudio.PCMFrame{Epoch: i.epoch, Samples: frame[:count]})
	if err != nil {
		return nil, fmt.Errorf("process audio input: %w", err)
	}
	if i.continuous && samplesAreSilent(frame[:count]) {
		clearProcessedFrames(frames)
	}
	return frames, nil
}

func clearProcessedFrames(frames []sharedaudio.PCMFrame) {
	for index := range frames {
		clear(frames[index].Samples)
	}
}

func samplesAreSilent(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return false
		}
	}
	return true
}

func (i *input) readFrame(ctx context.Context, frame []int16, source sharedaudio.SampleSource) (int, error) {
	clear(frame)
	if !i.padFinalFrame && source != nil {
		count, err := source.ReadSamples(ctx, frame)
		if count < 0 || count > len(frame) {
			return 0, fmt.Errorf("audio input source returned invalid sample count %d", count)
		}
		return count, err
	}
	if err := i.source.ReadFrame(ctx, frame); err != nil {
		return 0, err
	}
	return len(frame), nil
}

func (i *input) writeOpenFrames(ctx context.Context, outbound sharedaudio.OutboundMedia, frames []sharedaudio.PCMFrame) (int, error) {
	written := 0
	for _, frame := range frames {
		if len(frame.Samples) == 0 {
			continue
		}
		if i.continuous {
			if err := writeFrame(ctx, outbound, frame); err != nil {
				return written, err
			}
			written += len(frame.Samples)
			continue
		}
		if i.pending != nil {
			if err := writeFrame(ctx, outbound, *i.pending); err != nil {
				return written, err
			}
			written += len(i.pending.Samples)
		}
		copyOf := frame
		copyOf.Samples = append([]int16(nil), frame.Samples...)
		i.pending = &copyOf
	}
	return written, nil
}

func (i *input) finishTurn(ctx context.Context, outbound sharedaudio.OutboundMedia, reset bool) error {
	frames, err := i.processor.Process(sharedaudio.PCMFrame{Epoch: i.epoch, EndOfResponse: true})
	if err != nil {
		return fmt.Errorf("flush audio input: %w", err)
	}
	if err := i.writeFlushedTurn(ctx, outbound, frames); err != nil {
		return err
	}
	if reset {
		if _, err := i.processor.Reset(); err != nil {
			return fmt.Errorf("reset audio input processor: %w", err)
		}
		i.epoch++
		if err := i.notifyBoundary(ctx); err != nil {
			return err
		}
		i.turnHasSamples = false
		i.lastBoundary = true
		return nil
	}
	if !i.turnHasSamples && i.lastBoundary {
		return nil
	}
	if err := i.notifyBoundary(ctx); err != nil {
		return err
	}
	i.turnHasSamples = false
	i.lastBoundary = true
	return nil
}

func (i *input) writeFlushedTurn(ctx context.Context, outbound sharedaudio.OutboundMedia, frames []sharedaudio.PCMFrame) error {
	if i.pending != nil && hasSamples(frames) {
		if err := writeFrame(ctx, outbound, *i.pending); err != nil {
			return fmt.Errorf("send audio input tail: %w", err)
		}
		i.pending = nil
	}
	for _, frame := range frames {
		if len(frame.Samples) == 0 {
			continue
		}
		if err := writeFrame(ctx, outbound, frame); err != nil {
			return fmt.Errorf("send audio input tail: %w", err)
		}
	}
	if i.pending == nil {
		return nil
	}
	i.pending.EndOfResponse = true
	if err := writeFrame(ctx, outbound, *i.pending); err != nil {
		return fmt.Errorf("send audio input boundary: %w", err)
	}
	i.pending = nil
	return nil
}

func hasSamples(frames []sharedaudio.PCMFrame) bool {
	for _, frame := range frames {
		if len(frame.Samples) > 0 {
			return true
		}
	}
	return false
}

func (i *input) notifyBoundary(ctx context.Context) error {
	if i.onTurnBoundary == nil {
		return nil
	}
	if err := i.onTurnBoundary(ctx); err != nil {
		return fmt.Errorf("audio input end-of-turn: %w", err)
	}
	return nil
}

func (i *input) Close() error {
	if i == nil {
		return nil
	}
	i.closeOnce.Do(func() {
		if i.source != nil {
			i.closeErr = i.source.Close()
		}
	})
	return i.closeErr
}

var _ audioio.Input = (*input)(nil)
