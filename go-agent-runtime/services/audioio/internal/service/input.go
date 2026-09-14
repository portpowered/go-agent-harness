package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	audiostream "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type input struct {
	source                sharedaudio.AudioSource
	processor             *sharedaudio.Processor
	sourceRate            int
	quantum               int
	pace                  bool
	continuous            bool
	padFinalFrame         bool
	emitBoundaryOnSilence bool
	scheduler             platformclock.Scheduler
	onTurnBoundary        func(context.Context) error
	pending               *sharedaudio.PCMFrame
	epoch                 uint64
	hasSamples            bool
	turnHasSamples        bool
	lastBoundary          bool
	closeOnce             sync.Once
	closeErr              error
}

func newInput(ctx context.Context, request audioio.InputRequest) (audioio.Input, error) {
	if request.Source == nil {
		return nil, errors.New("audio input source is nil")
	}
	if ctx == nil {
		return nil, errors.New("audio input context is required")
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
	return &input{source: request.Source, processor: processor, sourceRate: sourceRate, quantum: quantum, pace: request.Pace, continuous: request.Continuous, padFinalFrame: request.PadFinalFrame, emitBoundaryOnSilence: request.EmitBoundaryOnSilence, scheduler: request.Scheduler, onTurnBoundary: request.OnTurnBoundary}, nil
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
	source, ok := i.source.(sharedaudio.SampleSource)
	if !ok {
		return nil
	}
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
		return i.processEndOfTurn(ctx, outbound)
	}
	if err := validateReadError(readErr); err != nil {
		return 0, 0, false, err
	}
	if count == 0 {
		return 0, 0, errors.Is(readErr, io.EOF), noProgressError(readErr)
	}
	frames, err := i.processSamples(frame, count)
	if err != nil {
		return 0, 0, false, err
	}
	if samplesAreSilent(frame[:count]) {
		clearProcessedFrames(frames)
	}
	i.hasSamples = true
	// The audio service forwards bounded packets for capture continuity, but
	// low-level silence/noise must not become a provider turn boundary. Track
	// content from the source quantum rather than from packet presence; a
	// non-silent quantum can legitimately produce no packet until a later
	// quantum or boundary flush.
	i.recordTurnSamples(frame[:count])
	i.lastBoundary = false
	written, err := i.writeOpenFrames(ctx, outbound, frames)
	if err != nil {
		return written, count, false, err
	}
	return written, count, errors.Is(readErr, io.EOF), nil
}

func (i *input) processEndOfTurn(ctx context.Context, outbound sharedaudio.OutboundMedia) (int, int, bool, error) {
	if !i.turnHasSamples {
		if _, err := i.processor.Reset(); err != nil {
			return 0, 0, false, fmt.Errorf("reset silent audio input processor: %w", err)
		}
		i.epoch++
		i.lastBoundary = true
		return 0, 0, false, nil
	}
	if err := i.finishTurn(ctx, outbound, true); err != nil {
		return 0, 0, false, err
	}
	return 0, 0, false, nil
}

func validateReadError(err error) error {
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read audio input: %w", err)
	}
	return nil
}

func noProgressError(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return io.ErrNoProgress
}

func (i *input) recordTurnSamples(samples []int16) {
	if !samplesAreSilent(samples) || (i.continuous && hasNonZeroSamples(samples)) {
		i.turnHasSamples = true
	}
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
	return frames, nil
}

func samplesAreSilent(samples []int16) bool {
	const silenceFloorDBFS = -50.0
	threshold := audiostream.PCM16AmplitudeForDBFS(silenceFloorDBFS)
	for _, sample := range samples {
		value := float64(sample)
		if value < 0 {
			value = -value
		}
		if value > threshold {
			return false
		}
	}
	return true
}

func hasNonZeroSamples(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return true
		}
	}
	return false
}

func clearProcessedFrames(frames []sharedaudio.PCMFrame) {
	for index := range frames {
		clear(frames[index].Samples)
	}
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
	if i.continuous {
		return i.writeContinuousFrames(ctx, outbound, frames)
	}
	return i.writeFiniteFrames(ctx, outbound, frames)
}

func (i *input) writeContinuousFrames(ctx context.Context, outbound sharedaudio.OutboundMedia, frames []sharedaudio.PCMFrame) (int, error) {
	written := 0
	for _, frame := range frames {
		if len(frame.Samples) == 0 {
			continue
		}
		if len(frame.Samples) < i.quantum {
			padded := make([]int16, i.quantum)
			copy(padded, frame.Samples)
			frame.Samples = padded
		}
		if err := writeFrame(ctx, outbound, frame); err != nil {
			return written, err
		}
		written += len(frame.Samples)
	}
	return written, nil
}

func (i *input) writeFiniteFrames(ctx context.Context, outbound sharedaudio.OutboundMedia, frames []sharedaudio.PCMFrame) (int, error) {
	written := 0
	for _, frame := range frames {
		if len(frame.Samples) == 0 {
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
		if !i.turnHasSamples && !i.emitBoundaryOnSilence {
			return fmt.Errorf("flush silent audio input: %w", err)
		}
		return fmt.Errorf("flush audio input: %w", err)
	}
	if err := i.writeFlushedTurn(ctx, outbound, frames); err != nil {
		return err
	}
	if !i.shouldNotifyBoundary(reset) {
		return nil
	}
	if reset {
		return i.resetAfterTurn(ctx)
	}
	if err := i.notifyBoundary(ctx); err != nil {
		return err
	}
	i.turnHasSamples = false
	i.lastBoundary = true
	return nil
}

func (i *input) shouldNotifyBoundary(reset bool) bool {
	return reset || i.turnHasSamples || i.emitBoundaryOnSilence
}

func (i *input) resetAfterTurn(ctx context.Context) error {
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
		if i.continuous && len(frame.Samples) < i.quantum {
			// ProcessAvailable already emitted the live fractional packet. A
			// final resampler tail would be a second provider append for the
			// same turn; it is covered by the bounded padding above.
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
