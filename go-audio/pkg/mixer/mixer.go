// Package mixer owns cadence aligned PCM mixing for room style media graphs.
// It consumes and produces the canonical audio frame contract; device pacing,
// resampling, and provider transport remain outside this package.
package mixer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

var (
	ErrClosed             = errors.New("PCM mixer is closed")
	ErrInputExists        = errors.New("PCM mixer input already exists")
	ErrInputMissing       = errors.New("PCM mixer input is not registered")
	ErrInvalidFormat      = errors.New("invalid PCM mixer format")
	ErrFrameSizeMismatch  = errors.New("PCM mixer frame size does not match negotiated cadence")
	ErrClockUnavailable   = errors.New("PCM mixer clock is unavailable")
	ErrContextUnavailable = errors.New("PCM mixer context is unavailable")
)

const (
	pcm16MaxSample = 32767
	pcm16MinSample = -32768
)

// Mixer emits one frame for every cadence interval. Each input is bounded and
// independently epoch aware. A missing source contributes silence; a short
// final response frame is retained at its actual length and marks the output
// boundary instead of being padded as if it were a provider packet.
type Mixer struct {
	ctx       context.Context
	cancel    context.CancelFunc
	scheduler clock.TimerSource
	format    Format
	streamID  string
	frameSize int
	inputCap  int
	sequence  uint64
	start     uint64

	mu     sync.Mutex
	inputs map[string]*input
	closed bool
	err    error
	output chan MixedFrame
	done   chan struct{}
	close  sync.Once
}

// New creates a running bounded mixer. scheduler is mandatory so replay and
// live room runs share one timing domain.
func New(ctx context.Context, scheduler clock.TimerSource, config Config) (*Mixer, error) {
	if scheduler == nil {
		return nil, ErrClockUnavailable
	}
	config, samples, err := config.normalize()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, ErrContextUnavailable
	}
	runCtx, cancel := context.WithCancel(ctx)
	m := &Mixer{
		ctx: runCtx, cancel: cancel, scheduler: scheduler, format: config.Format,
		streamID:  config.StreamID,
		frameSize: samples, inputCap: config.InputQueueFrames,
		inputs: make(map[string]*input), output: make(chan MixedFrame, config.OutputQueueFrames), done: make(chan struct{}),
	}
	go m.run()
	return m, nil
}

func (m *Mixer) Format() Format {
	if m == nil {
		return Format{}
	}
	return m.format
}

func (m *Mixer) run() {
	defer close(m.done)
	defer close(m.output)
	next := m.scheduler.Now().Add(m.format.FrameDuration)
	for {
		delay := next.Sub(m.scheduler.Now())
		if delay < 0 {
			delay = 0
		}
		timer := m.scheduler.NewTimer(delay)
		if timer == nil {
			m.fail(ErrClockUnavailable)
			return
		}
		select {
		case <-m.ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
			timer.Stop()
		}
		next = next.Add(m.format.FrameDuration)
		for !next.After(m.scheduler.Now()) {
			next = next.Add(m.format.FrameDuration)
		}
		frame, sources, err := m.mix()
		// Close may win after the cadence timer fires but before mix
		// acquires the state lock. That is normal shutdown, not a fault.
		if errors.Is(err, ErrClosed) {
			return
		}
		if err != nil {
			m.fail(err)
			return
		}
		select {
		case m.output <- MixedFrame{Frame: frame, Sources: sources}:
		case <-m.ctx.Done():
			return
		}
	}
}

func (m *Mixer) mix() (audio.PCMFrame, []string, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return audio.PCMFrame{}, nil, ErrClosed
	}
	ids := make([]string, 0, len(m.inputs))
	for id := range m.inputs {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	sort.Strings(ids)
	// Rebuild source order after sorting IDs. The map lookup stays outside the
	// input lock; each FrameBuffer has its own synchronization.
	frames := make([]audio.PCMFrame, len(ids))
	for index, id := range ids {
		m.mu.Lock()
		source := m.inputs[id]
		m.mu.Unlock()
		if source == nil {
			continue
		}
		frame, ok, err := source.consumer.TryReceive()
		if err != nil && !errors.Is(err, io.EOF) {
			return audio.PCMFrame{}, nil, fmt.Errorf("read mixer input %q: %w", id, err)
		}
		if ok {
			frames[index] = frame
		}
	}
	sources := make([]string, 0, len(ids))
	for index, frame := range frames {
		if len(frame.Samples) > 0 || frame.EndOfResponse {
			sources = append(sources, ids[index])
		}
	}
	result, err := combine(m.format, m.frameSize, frames)
	if err != nil {
		return audio.PCMFrame{}, nil, err
	}
	// The mixed stream has its own timeline identity. Input epochs are only
	// source-local interruption generations and must never become the output
	// device/provider epoch. The room graph assigns that output epoch at its
	// boundary; mixer metadata remains useful for diagnostics here.
	m.mu.Lock()
	result.StreamID = m.streamID
	result.Sequence = m.sequence
	result.StartSample = m.start
	m.sequence++
	m.start += uint64(len(result.Samples))
	m.mu.Unlock()
	result.Epoch = 0
	return result, sources, nil
}

func combine(format Format, frameSize int, frames []audio.PCMFrame) (audio.PCMFrame, error) {
	shape := inspectMixedFrameShape(frames)
	length, err := mixedFrameLength(shape, frameSize)
	if err != nil {
		return audio.PCMFrame{}, err
	}
	metadata := audio.PCMFrame{Samples: clipMixedSamples(sumMixedSamples(frames, length))}
	metadata.Format = audio.PCM16DeviceFormat(format.SampleRate)
	// Source epochs intentionally do not cross the mix boundary. A target
	// playback queue has a graph-owned epoch domain, while evidence owners can
	// retain source epochs from their input observations.
	metadata.Epoch = 0
	// A boundary belongs to one source stream. Keep it only when the mixed
	// cadence has one contributing source; OR-ing independent peer boundaries
	// would make a target provider end an unrelated response.
	metadata.EndOfResponse = shape.boundaries == 1 && shape.contributors == 1
	metadata.StreamID = ""
	metadata.Sequence = 0
	metadata.StartSample = 0
	metadata.PlaybackResponse = audio.PlaybackResponse{}
	return metadata, nil
}

type mixedFrameShape struct {
	length       int
	nonEmpty     int
	contributors int
	boundaries   int
}

func inspectMixedFrameShape(frames []audio.PCMFrame) mixedFrameShape {
	var shape mixedFrameShape
	for _, frame := range frames {
		if len(frame.Samples) == 0 && !frame.EndOfResponse {
			continue
		}
		shape.contributors++
		if len(frame.Samples) > shape.length {
			shape.length = len(frame.Samples)
		}
		if len(frame.Samples) > 0 {
			shape.nonEmpty++
		}
		if frame.EndOfResponse {
			shape.boundaries++
		}
	}
	return shape
}

func mixedFrameLength(shape mixedFrameShape, frameSize int) (int, error) {
	if shape.length > frameSize {
		return 0, fmt.Errorf("%w: got %d samples, want at most %d", ErrFrameSizeMismatch, shape.length, frameSize)
	}
	if shape.length == 0 && shape.nonEmpty == 0 && shape.boundaries == 0 {
		return frameSize, nil
	}
	return shape.length, nil
}

func sumMixedSamples(frames []audio.PCMFrame, length int) []int32 {
	accumulated := make([]int32, length)
	for _, frame := range frames {
		for index, sample := range frame.Samples {
			if index < len(accumulated) {
				accumulated[index] += int32(sample)
			}
		}
	}
	return accumulated
}

func clipMixedSamples(accumulated []int32) []int16 {
	result := make([]int16, len(accumulated))
	for index, sample := range accumulated {
		if sample > pcm16MaxSample {
			sample = pcm16MaxSample
		} else if sample < pcm16MinSample {
			sample = pcm16MinSample
		}
		result[index] = int16(sample)
	}
	return result
}

func (m *Mixer) fail(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	if m.err == nil {
		m.err = err
	}
	m.mu.Unlock()
	m.cancel()
}

func (m *Mixer) Err() error {
	if m == nil {
		return ErrClosed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

func (m *Mixer) Close() error {
	if m == nil {
		return nil
	}
	m.close.Do(func() {
		m.mu.Lock()
		m.closed = true
		for _, source := range m.inputs {
			source.producer.Close()
		}
		m.mu.Unlock()
		m.cancel()
		<-m.done
	})
	return m.Err()
}
