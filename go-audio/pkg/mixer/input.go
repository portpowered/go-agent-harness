package mixer

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type input struct {
	producer audio.FrameProducer
	consumer audio.FrameConsumer
	control  audio.BufferControl
}

// Input is one source scoped to a participant. One producer should own an
// Input; concurrent writers are safe, but preserving one source order remains
// the caller's responsibility.
type Input struct {
	mixer   *Mixer
	id      string
	input   *input
	once    sync.Once
	write   sync.Mutex
	pending []int16
	meta    audio.PCMFrame
	hasMeta bool
}

func (m *Mixer) AddInput(id string) (*Input, error) {
	if m == nil {
		return nil, ErrClosed
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w: input ID is empty", ErrInvalidFormat)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if _, exists := m.inputs[id]; exists {
		return nil, fmt.Errorf("%w: %q", ErrInputExists, id)
	}
	producer, consumer, control, err := audio.NewFrameBuffer(m.inputCap, m.frameSize)
	if err != nil {
		return nil, err
	}
	source := &input{producer: producer, consumer: consumer, control: control}
	m.inputs[id] = source
	return &Input{mixer: m, id: id, input: source}, nil
}

func (m *Mixer) RemoveInput(id string) error {
	if m == nil {
		return ErrClosed
	}
	id = strings.TrimSpace(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	source, ok := m.inputs[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrInputMissing, id)
	}
	delete(m.inputs, id)
	source.producer.Close()
	return nil
}

// WriteFrame accepts provider packet boundaries and reframes them into the
// negotiated cadence. A partial packet is retained only until enough samples
// arrive or the packet explicitly marks EndOfResponse; it is never padded as
// if a provider packet were a complete cadence frame.
func (i *Input) WriteFrame(ctx context.Context, frame audio.PCMFrame) error {
	if i == nil || i.mixer == nil || i.input == nil {
		return ErrClosed
	}
	if ctx == nil {
		return ErrContextUnavailable
	}
	if err := i.validateFrameFormat(frame); err != nil {
		return err
	}
	i.write.Lock()
	defer i.write.Unlock()
	if err := i.advanceEpoch(frame.Epoch); err != nil {
		return err
	}
	return i.submitFrameSamples(ctx, frame)
}

func (i *Input) validateFrameFormat(frame audio.PCMFrame) error {
	if frame.Format.SampleRate != 0 && frame.Format.SampleRate != i.mixer.format.SampleRate {
		return fmt.Errorf("%w: got %d Hz, want %d Hz", ErrFrameSizeMismatch, frame.Format.SampleRate, i.mixer.format.SampleRate)
	}
	if frame.Format.Channels != 0 && frame.Format.Channels != i.mixer.format.Channels {
		return fmt.Errorf("%w: got %d channels, want %d", ErrFrameSizeMismatch, frame.Format.Channels, i.mixer.format.Channels)
	}
	return nil
}

func (i *Input) advanceEpoch(epoch uint64) error {
	stats := i.input.control.Snapshot()
	if epoch < stats.Epoch {
		return audio.ErrStaleEpoch
	}
	if epoch == stats.Epoch {
		return nil
	}
	i.input.control.Invalidate(epoch)
	// Pending samples belong to the old source epoch and cannot be
	// allowed to cross an interruption boundary.
	i.pending = nil
	i.meta = audio.PCMFrame{}
	i.hasMeta = false
	return nil
}

func (i *Input) submitFrameSamples(ctx context.Context, frame audio.PCMFrame) error {
	for offset := 0; offset < len(frame.Samples); {
		if len(i.pending) == 0 {
			i.rememberFrame(frame)
		}
		want := i.mixer.frameSize - len(i.pending)
		if remaining := len(frame.Samples) - offset; remaining < want {
			want = remaining
		}
		i.pending = append(i.pending, frame.Samples[offset:offset+want]...)
		offset += want
		if len(i.pending) != i.mixer.frameSize {
			continue
		}
		full := i.meta
		full.Samples = i.pending
		i.pending = nil
		i.hasMeta = false
		full.EndOfResponse = frame.EndOfResponse && offset == len(frame.Samples)
		if err := i.input.producer.Submit(ctx, full); err != nil {
			return err
		}
	}
	return i.submitResponseTail(ctx, frame)
}

func (i *Input) rememberFrame(frame audio.PCMFrame) {
	i.meta = frame
	i.meta.Samples = nil
	i.hasMeta = true
}

func (i *Input) submitResponseTail(ctx context.Context, frame audio.PCMFrame) error {
	if !frame.EndOfResponse {
		return nil
	}
	if len(i.pending) == 0 && len(frame.Samples) > 0 {
		// The final packet ended exactly on a cadence boundary. The full
		// frame above already carries the response marker.
		return nil
	}
	tail := i.meta
	tail.Samples = i.pending
	tail.EndOfResponse = true
	i.pending = nil
	i.hasMeta = false
	return i.input.producer.Submit(ctx, tail)
}

func (i *Input) Close() error {
	if i == nil || i.input == nil {
		return nil
	}
	i.once.Do(func() { i.input.producer.Close() })
	return nil
}

func (i *Input) ID() string {
	if i == nil {
		return ""
	}
	return i.id
}
