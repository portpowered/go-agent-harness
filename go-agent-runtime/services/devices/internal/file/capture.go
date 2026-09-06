package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

var errEmptyInput = errors.New("finite audio input contains no frames")

type fileCapture struct {
	source         sharedaudio.AudioSource
	processor      *sharedaudio.Processor
	sourceRate     int
	pace           bool
	continuous     bool
	scheduler      platformclock.Scheduler
	onTurnBoundary func(context.Context) error
	pending        *sharedaudio.PCMFrame
	epoch          uint64
	hasSamples     bool
	turnHasSamples bool
	lastBoundary   bool

	closeOnce sync.Once
	closeErr  error
}

func newCapture(input devices.FileInput, providerRate int) (*fileCapture, error) {
	if input.SampleRate < 0 {
		return nil, fmt.Errorf("%w: finite input sample rate must not be negative", devices.ErrInvalidRequest)
	}
	if providerRate < 0 {
		return nil, fmt.Errorf("%w: provider sample rate must not be negative", devices.ErrInvalidRequest)
	}
	sourceRate := input.SampleRate
	if sourceRate <= 0 {
		sourceRate = sharedaudio.SampleRate
	}
	quantum := providerRate * sharedaudio.FrameSize / sharedaudio.SampleRate
	if quantum <= 0 {
		quantum = sharedaudio.FrameSize
	}
	if input.Pace && input.Scheduler == nil {
		return nil, fmt.Errorf("%w: finite input pacing requires a scheduler", devices.ErrInvalidRequest)
	}
	processor, err := sharedaudio.NewProcessor(
		sharedaudio.PCM16DeviceFormat(sourceRate),
		sharedaudio.PCM16DeviceFormat(providerRate), quantum,
	)
	if err != nil {
		return nil, fmt.Errorf("create finite input processor: %w", err)
	}
	return &fileCapture{
		source: input.Source, processor: processor, sourceRate: sourceRate, pace: input.Pace, continuous: input.Continuous,
		scheduler: input.Scheduler, onTurnBoundary: input.OnTurnBoundary,
	}, nil
}

func (c *fileCapture) Pump(ctx context.Context, outbound sharedaudio.OutboundMedia) error {
	if c == nil || c.source == nil || c.processor == nil {
		return fmt.Errorf("%w: finite capture is unavailable", devices.ErrUnavailable)
	}
	if outbound == nil {
		return fmt.Errorf("%w: provider outbound media is nil", devices.ErrInvalidRequest)
	}
	if ctx == nil {
		return fmt.Errorf("%w: capture context is required", devices.ErrInvalidRequest)
	}
	frame := make([]int16, sharedaudio.FrameSize)
	var start time.Time
	if c.pace {
		start = c.scheduler.Now()
	}
	sent, err := c.pumpFrames(ctx, outbound, frame, start)
	if err != nil {
		return err
	}
	if sent == 0 && !c.hasSamples {
		return errEmptyInput
	}
	return nil
}

func (c *fileCapture) pumpFrames(ctx context.Context, outbound sharedaudio.OutboundMedia, frame []int16, start time.Time) (int, error) {
	sent, consumed := 0, 0
	var countSource sharedaudio.SampleSource
	if source, ok := c.source.(sharedaudio.SampleSource); ok {
		countSource = source
	}
	for {
		if c.pace && consumed > 0 {
			if err := waitForSamples(ctx, c.scheduler, start, consumed, c.sourceRate); err != nil {
				return sent, err
			}
		}
		written, count, eof, err := c.processFrame(ctx, outbound, frame, countSource)
		if err != nil {
			return sent, err
		}
		sent += written
		consumed += count
		if eof {
			if err := c.finishTurn(ctx, outbound, false); err != nil {
				return sent, err
			}
			return sent, nil
		}
	}
}

func (c *fileCapture) processFrame(ctx context.Context, outbound sharedaudio.OutboundMedia, frame []int16, source sharedaudio.SampleSource) (int, int, bool, error) {
	count, readErr := c.readFrame(ctx, frame, source)
	if errors.Is(readErr, sharedaudio.ErrEndOfTurn) {
		if !c.hasSamples {
			return 0, 0, false, errEmptyInput
		}
		if err := c.finishTurn(ctx, outbound, true); err != nil {
			return 0, 0, false, err
		}
		return 0, 0, false, nil
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return 0, 0, false, fmt.Errorf("read finite audio input: %w", readErr)
	}
	if count == 0 {
		if errors.Is(readErr, io.EOF) {
			return 0, 0, true, nil
		}
		return 0, 0, false, io.ErrNoProgress
	}
	process := c.processor.Process
	if c.continuous {
		process = c.processor.ProcessAvailable
	}
	frames, err := process(sharedaudio.PCMFrame{Samples: frame[:count], Epoch: c.epoch})
	if err != nil {
		return 0, 0, false, fmt.Errorf("process finite audio input: %w", err)
	}
	if c.continuous && samplesAreSilent(frame[:count]) {
		// A live source must preserve a digital-silence frame as silence even
		// when an upsampler's one-sample interpolation lookahead still carries
		// the preceding speech frame. The provider's VAD boundary belongs to
		// the current source quantum; leaking that transition sample turns a
		// real silence into a second utterance.
		for index := range frames {
			clear(frames[index].Samples)
		}
	}
	c.hasSamples = true
	c.turnHasSamples = true
	c.lastBoundary = false
	written, err := c.writeOpenFrames(ctx, outbound, frames)
	if err != nil {
		return written, count, false, err
	}
	return written, count, errors.Is(readErr, io.EOF), nil
}

func samplesAreSilent(samples []int16) bool {
	for _, sample := range samples {
		if sample != 0 {
			return false
		}
	}
	return true
}

func (c *fileCapture) readFrame(ctx context.Context, frame []int16, source sharedaudio.SampleSource) (int, error) {
	clear(frame)
	if source != nil {
		count, err := source.ReadSamples(ctx, frame)
		if count < 0 || count > len(frame) {
			return 0, fmt.Errorf("read finite audio input: source returned invalid sample count %d", count)
		}
		return count, err
	}
	if err := c.source.ReadFrame(ctx, frame); err != nil {
		return 0, err
	}
	return len(frame), nil
}

// writeOpenFrames keeps one output frame back so an explicit source boundary
// can mark the final frame EndOfResponse without inventing an empty media
// packet. The provider endpoint intentionally rejects zero-sample frames.
func (c *fileCapture) writeOpenFrames(ctx context.Context, outbound sharedaudio.OutboundMedia, frames []sharedaudio.PCMFrame) (int, error) {
	written := 0
	for _, frame := range frames {
		if len(frame.Samples) == 0 {
			continue
		}
		if c.continuous {
			if err := writeCaptureFrame(ctx, outbound, frame); err != nil {
				return written, err
			}
			written += len(frame.Samples)
			continue
		}
		if c.pending != nil {
			if err := writeCaptureFrame(ctx, outbound, *c.pending); err != nil {
				return written, err
			}
			written += len(c.pending.Samples)
		}
		copyOf := frame
		copyOf.Samples = append([]int16(nil), frame.Samples...)
		c.pending = &copyOf
	}
	return written, nil
}

// finishTurn closes the current processor response, writes the held final
// frame with its boundary bit, and optionally resets the processor for the
// next explicit source turn. A marker callback is invoked only after media
// admission succeeds, preserving the same ordering as the legacy loop's
// audio-then-commit path.
func (c *fileCapture) finishTurn(ctx context.Context, outbound sharedaudio.OutboundMedia, reset bool) error {
	frames, err := c.processor.Process(sharedaudio.PCMFrame{Epoch: c.epoch, EndOfResponse: true})
	if err != nil {
		return fmt.Errorf("flush finite audio input: %w", err)
	}
	if err := c.writeFlushedTurn(ctx, outbound, frames); err != nil {
		return err
	}
	if reset {
		return c.resetTurn(ctx)
	}
	if !c.turnHasSamples && c.lastBoundary {
		return nil
	}
	if err := c.notifyBoundary(ctx); err != nil {
		return err
	}
	c.turnHasSamples = false
	c.lastBoundary = true
	return nil
}

func (c *fileCapture) writeFlushedTurn(ctx context.Context, outbound sharedaudio.OutboundMedia, frames []sharedaudio.PCMFrame) error {
	if c.pending != nil && hasSamples(frames) {
		if err := writeCaptureFrame(ctx, outbound, *c.pending); err != nil {
			return fmt.Errorf("send finite audio input tail: %w", err)
		}
		c.pending = nil
	}
	for _, frame := range frames {
		if len(frame.Samples) == 0 {
			continue
		}
		if err := writeCaptureFrame(ctx, outbound, frame); err != nil {
			return fmt.Errorf("send finite audio input tail: %w", err)
		}
	}
	if c.pending == nil {
		return nil
	}
	c.pending.EndOfResponse = true
	if err := writeCaptureFrame(ctx, outbound, *c.pending); err != nil {
		return fmt.Errorf("send finite audio input boundary: %w", err)
	}
	c.pending = nil
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

func (c *fileCapture) resetTurn(ctx context.Context) error {
	if err := c.processorReset(); err != nil {
		return fmt.Errorf("reset finite audio input processor: %w", err)
	}
	if err := c.notifyBoundary(ctx); err != nil {
		return err
	}
	c.turnHasSamples = false
	c.lastBoundary = true
	return nil
}

func (c *fileCapture) notifyBoundary(ctx context.Context) error {
	if c.onTurnBoundary == nil {
		return nil
	}
	if err := c.onTurnBoundary(ctx); err != nil {
		return fmt.Errorf("send finite audio input end-of-turn: %w", err)
	}
	return nil
}

func (c *fileCapture) processorReset() error {
	if _, err := c.processor.Reset(); err != nil {
		return err
	}
	c.epoch++
	return nil
}

func writeCaptureFrame(ctx context.Context, outbound sharedaudio.OutboundMedia, frame sharedaudio.PCMFrame) error {
	if len(frame.Samples) == 0 {
		return nil
	}
	if err := outbound.WriteFrame(ctx, frame); err != nil {
		return fmt.Errorf("send finite audio input: %w", err)
	}
	return nil
}

func waitForSamples(ctx context.Context, scheduler platformclock.Scheduler, start time.Time, samples, sourceRate int) error {
	if scheduler == nil {
		return fmt.Errorf("%w: finite input pacing requires a scheduler", devices.ErrInvalidRequest)
	}
	sampleDuration := time.Duration(samples) * time.Second / time.Duration(sourceRate)
	wait := start.Add(sampleDuration).Sub(scheduler.Now())
	if wait <= 0 {
		return nil
	}
	timer := scheduler.NewTimer(wait)
	if timer == nil {
		return fmt.Errorf("%w: finite input pacing scheduler returned a nil timer", devices.ErrInvalidRequest)
	}
	defer timer.Stop()
	select {
	case <-timer.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *fileCapture) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		if c.source != nil {
			c.closeErr = c.source.Close()
		}
	})
	return c.closeErr
}

var _ devices.Capture = (*fileCapture)(nil)
