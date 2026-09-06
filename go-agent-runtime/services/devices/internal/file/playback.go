package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type filePlayback struct {
	sink      sharedaudio.AudioSink
	processor *sharedaudio.Processor
	ended     bool

	closeOnce sync.Once
	closeErr  error
	pumpStart chan struct{}
	pumpDone  chan struct{}
	pumpMu    sync.Mutex
	pumpUsed  bool
	pumpErr   error
	pumpOnce  sync.Once
	doneOnce  sync.Once
}

func newPlayback(output devices.FileOutput, providerRate int) (*filePlayback, error) {
	sinkRate := output.SampleRate
	if sinkRate <= 0 {
		sinkRate = sharedaudio.SampleRate
	}
	processor, err := sharedaudio.NewProcessor(
		sharedaudio.PCM16DeviceFormat(providerRate),
		sharedaudio.PCM16DeviceFormat(sinkRate), sharedaudio.FrameSize,
	)
	if err != nil {
		return nil, fmt.Errorf("create finite output processor: %w", err)
	}
	return &filePlayback{
		sink:      output.Sink,
		processor: processor,
		pumpStart: make(chan struct{}),
		pumpDone:  make(chan struct{}),
	}, nil
}

func (p *filePlayback) Pump(ctx context.Context, inbound sharedaudio.InboundMedia) (runErr error) {
	if p == nil {
		return fmt.Errorf("%w: finite playback is unavailable", devices.ErrUnavailable)
	}
	if ctx == nil {
		return fmt.Errorf("%w: playback context is required", devices.ErrInvalidRequest)
	}
	p.pumpMu.Lock()
	if p.pumpUsed {
		p.pumpMu.Unlock()
		return errors.New("finite playback pump has already been started")
	}
	p.pumpUsed = true
	p.pumpMu.Unlock()
	p.pumpOnce.Do(func() { close(p.pumpStart) })
	defer func() {
		p.pumpMu.Lock()
		p.pumpErr = runErr
		p.pumpMu.Unlock()
		p.doneOnce.Do(func() { close(p.pumpDone) })
	}()
	if p.sink == nil || p.processor == nil {
		return fmt.Errorf("%w: finite playback is unavailable", devices.ErrUnavailable)
	}
	if inbound == nil {
		return fmt.Errorf("%w: provider inbound media is nil", devices.ErrInvalidRequest)
	}
	for {
		frame, err := inbound.ReadFrame(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, sharedaudio.ErrSessionMediaClosed) {
				return p.flush(ctx)
			}
			return fmt.Errorf("read finite audio output: %w", err)
		}
		if err := p.consumeFrame(ctx, frame); err != nil {
			return err
		}
	}
}

// WaitForPump lets the live invocation drain provider audio after the
// provider's terminal event. The playback worker owns the sink until this
// returns, so a normal provider close cannot truncate queued final samples.
func (p *filePlayback) WaitForPump(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("%w: playback wait context is required", devices.ErrInvalidRequest)
	}
	started := false
	select {
	case <-p.pumpStart:
		started = true
	case <-p.pumpDone:
		// Pump closes pumpStart before it can return, so both channels may
		// already be ready when a very short or failed invocation is joined.
		// Prefer the start marker in that case instead of reporting a false
		// "stopped before starting" race.
		select {
		case <-p.pumpStart:
			started = true
		default:
			p.pumpMu.Lock()
			err := p.pumpErr
			p.pumpMu.Unlock()
			if err != nil {
				return err
			}
			return errors.New("finite playback pump stopped before starting")
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	if !started {
		return errors.New("finite playback pump stopped before starting")
	}
	select {
	case <-p.pumpDone:
		p.pumpMu.Lock()
		err := p.pumpErr
		p.pumpMu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *filePlayback) consumeFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	if err := p.resetAfterResponse(); err != nil {
		return err
	}
	frames, err := p.processor.Process(frame)
	if err != nil {
		return fmt.Errorf("process finite audio output: %w", err)
	}
	if err := p.writeFrames(ctx, frames); err != nil {
		return err
	}
	p.ended = frame.EndOfResponse
	return nil
}

func (p *filePlayback) resetAfterResponse() error {
	if !p.ended {
		return nil
	}
	if _, err := p.processor.Reset(); err != nil {
		return fmt.Errorf("reset finite audio output response: %w", err)
	}
	p.ended = false
	return nil
}

func (p *filePlayback) flush(ctx context.Context) error {
	if p == nil || p.ended {
		return nil
	}
	frames, err := p.processor.Process(sharedaudio.PCMFrame{EndOfResponse: true})
	if err != nil {
		return fmt.Errorf("flush finite audio output: %w", err)
	}
	if err := p.writeFrames(ctx, frames); err != nil {
		return err
	}
	p.ended = true
	return nil
}

func (p *filePlayback) writeFrames(ctx context.Context, frames []sharedaudio.PCMFrame) error {
	for _, frame := range frames {
		if len(frame.Samples) == 0 {
			continue
		}
		if sink, ok := p.sink.(sharedaudio.SampleSink); ok {
			if err := sink.WriteSamples(ctx, frame.Samples); err != nil {
				return fmt.Errorf("write finite audio output: %w", err)
			}
			continue
		}
		if len(frame.Samples) != sharedaudio.FrameSize {
			return fmt.Errorf("write finite audio output: %w: sink does not support count-aware samples (%d)", sharedaudio.ErrInvalidFrameSize, len(frame.Samples))
		}
		if err := p.sink.WriteFrame(ctx, frame.Samples); err != nil {
			return fmt.Errorf("write finite audio output: %w", err)
		}
	}
	return nil
}

func (p *filePlayback) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		if p.sink != nil {
			p.closeErr = p.sink.Close()
		}
	})
	return p.closeErr
}

var _ devices.Playback = (*filePlayback)(nil)
