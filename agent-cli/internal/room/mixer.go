package room

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrMixerClosed means that the mixer no longer accepts input.
	ErrMixerClosed = errors.New("PCM16 mixer is closed")
	// ErrMixerInputExists means that an input ID is already registered.
	ErrMixerInputExists = errors.New("PCM16 mixer input already exists")
	// ErrMixerInputMissing means that an input ID is not registered.
	ErrMixerInputMissing = errors.New("PCM16 mixer input is not registered")
	// ErrMixerInputBufferFull means that accepting a write would require
	// dropping samples. Callers should treat it as an unrecoverable mixer
	// failure instead of silently losing audio.
	ErrMixerInputBufferFull = errors.New("PCM16 mixer input buffer is full")
	// ErrMixerOutputBackpressure means that the bounded output queue could not
	// keep up. No frame is discarded before this error is reported.
	ErrMixerOutputBackpressure = errors.New("PCM16 mixer output queue is full")
	// ErrMixerInvalidFormat means that a mixer format cannot produce aligned
	// PCM16 frames.
	ErrMixerInvalidFormat = errors.New("invalid PCM16 mixer format")
	// ErrMixerInvalidInputID means that an input ID cannot safely identify a
	// participant in room-owned state or diagnostics.
	ErrMixerInvalidInputID = errors.New("PCM16 mixer input ID is invalid")
)

const (
	// DefaultPCM16SampleRate is the sample rate used by the realtime room
	// runtime. It matches the existing OpenAI realtime session configuration.
	DefaultPCM16SampleRate = 24000
	// DefaultPCM16Channels is the room's mono PCM contract.
	DefaultPCM16Channels = 1
	// DefaultPCM16FrameDuration is the shared room cadence.
	DefaultPCM16FrameDuration = 20 * time.Millisecond
	// DefaultPCM16InputQueueFrames bounds per-peer buffered audio to a finite
	// amount while leaving enough room for normal realtime jitter.
	DefaultPCM16InputQueueFrames = 250
	// DefaultPCM16OutputQueueFrames keeps a small amount of output ready for a
	// session loop without allowing an abandoned reader to grow memory forever.
	DefaultPCM16OutputQueueFrames = 8
)

// PCM16Format defines the only audio format accepted by a PCM16Mixer. Samples
// are signed little-endian 16-bit PCM and channels are interleaved.
type PCM16Format struct {
	SampleRate    int
	Channels      int
	FrameDuration time.Duration
}

// DefaultPCM16Format returns the room runtime's explicit audio contract.
func DefaultPCM16Format() PCM16Format {
	return PCM16Format{
		SampleRate:    DefaultPCM16SampleRate,
		Channels:      DefaultPCM16Channels,
		FrameDuration: DefaultPCM16FrameDuration,
	}
}

// FrameSamples returns the number of interleaved samples in one cadence
// frame. The duration must resolve to an integral number of samples.
func (f PCM16Format) FrameSamples() (int, error) {
	if f.SampleRate <= 0 || f.Channels <= 0 || f.FrameDuration <= 0 {
		return 0, fmt.Errorf("%w: sample rate, channels, and frame duration must be positive", ErrMixerInvalidFormat)
	}
	samplesPerChannel := (int64(f.SampleRate) * int64(f.FrameDuration)) / int64(time.Second)
	if samplesPerChannel <= 0 || (int64(f.SampleRate)*int64(f.FrameDuration))%int64(time.Second) != 0 {
		return 0, fmt.Errorf("%w: frame duration %s is not sample aligned at %d Hz", ErrMixerInvalidFormat, f.FrameDuration, f.SampleRate)
	}
	samples := samplesPerChannel * int64(f.Channels)
	if samples > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("%w: frame is too large", ErrMixerInvalidFormat)
	}
	return int(samples), nil
}

// FrameBytes returns the byte length of one interleaved PCM16 frame.
func (f PCM16Format) FrameBytes() (int, error) {
	samples, err := f.FrameSamples()
	if err != nil {
		return 0, err
	}
	if samples > int(^uint(0)>>1)/2 {
		return 0, fmt.Errorf("%w: frame is too large", ErrMixerInvalidFormat)
	}
	return samples * 2, nil
}

// PCM16MixerConfig controls bounded buffering around a PCM16Mixer.
type PCM16MixerConfig struct {
	Format            PCM16Format
	InputQueueFrames  int
	OutputQueueFrames int
}

func (c PCM16MixerConfig) normalized() (PCM16MixerConfig, int, error) {
	if c.Format == (PCM16Format{}) {
		c.Format = DefaultPCM16Format()
	}
	frameBytes, err := c.Format.FrameBytes()
	if err != nil {
		return PCM16MixerConfig{}, 0, err
	}
	if c.InputQueueFrames == 0 {
		c.InputQueueFrames = DefaultPCM16InputQueueFrames
	}
	if c.OutputQueueFrames == 0 {
		c.OutputQueueFrames = DefaultPCM16OutputQueueFrames
	}
	if c.InputQueueFrames < 0 || c.OutputQueueFrames < 0 {
		return PCM16MixerConfig{}, 0, fmt.Errorf("%w: queue frame limits must not be negative", ErrMixerInvalidFormat)
	}
	if c.InputQueueFrames == 0 || c.OutputQueueFrames == 0 {
		return PCM16MixerConfig{}, 0, fmt.Errorf("%w: queue frame limits must be positive", ErrMixerInvalidFormat)
	}
	return c, frameBytes, nil
}

type pcm16MixerInput struct {
	data []byte
}

// PCM16Mixer is a cadence-driven N-input PCM16 mixer. At every cadence it
// emits exactly one frame. Each active input contributes the samples currently
// due for that frame; missing samples are silence. Writes are buffered per
// input so uneven provider delta boundaries cannot reorder or duplicate bytes.
type PCM16Mixer struct {
	format       PCM16Format
	frameBytes   int
	maxInputSize int

	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte

	mu        sync.Mutex
	inputs    map[string]*pcm16MixerInput
	closed    bool
	err       error
	closeOnce sync.Once
	done      chan struct{}
}

// Mixer is a descriptive alias for PCM16Mixer.
type Mixer = PCM16Mixer

// NewPCM16Mixer creates a mixer with the default room format and bounded
// queues. A nil context is treated as context.Background.
func NewPCM16Mixer(ctx context.Context, format ...PCM16Format) (*PCM16Mixer, error) {
	config := PCM16MixerConfig{}
	if len(format) > 1 {
		return nil, fmt.Errorf("%w: at most one format is supported", ErrMixerInvalidFormat)
	}
	if len(format) == 1 {
		config.Format = format[0]
	}
	return NewPCM16MixerWithConfig(ctx, config)
}

// NewMixer is an alias for NewPCM16MixerWithConfig.
func NewMixer(ctx context.Context, config PCM16MixerConfig) (*PCM16Mixer, error) {
	return NewPCM16MixerWithConfig(ctx, config)
}

// NewPCM16MixerWithConfig creates a cadence-controlled mixer.
func NewPCM16MixerWithConfig(ctx context.Context, config PCM16MixerConfig) (*PCM16Mixer, error) {
	config, frameBytes, err := config.normalized()
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	mixerCtx, cancel := context.WithCancel(ctx)
	mixer := &PCM16Mixer{
		format:       config.Format,
		frameBytes:   frameBytes,
		maxInputSize: config.InputQueueFrames * frameBytes,
		ctx:          mixerCtx,
		cancel:       cancel,
		out:          make(chan []byte, config.OutputQueueFrames),
		inputs:       make(map[string]*pcm16MixerInput),
		done:         make(chan struct{}),
	}
	go mixer.run()
	return mixer, nil
}

// Format returns the immutable mixer format.
func (m *PCM16Mixer) Format() PCM16Format {
	if m == nil {
		return PCM16Format{}
	}
	return m.format
}

// FrameBytes returns the exact size of every emitted frame.
func (m *PCM16Mixer) FrameBytes() int {
	if m == nil {
		return 0
	}
	return m.frameBytes
}

// AddInput registers one participant source. The change takes effect on the
// next cadence frame; a new input starts with silence and no stale samples.
func (m *PCM16Mixer) AddInput(inputID string) error {
	if m == nil {
		return ErrMixerClosed
	}
	inputID, err := normalizeMixerInputID(inputID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrMixerClosed
	}
	if _, exists := m.inputs[inputID]; exists {
		return fmt.Errorf("%w: %q", ErrMixerInputExists, inputID)
	}
	m.inputs[inputID] = &pcm16MixerInput{}
	return nil
}

// AddInputWriter registers an input and returns its scoped writer.
func (m *PCM16Mixer) AddInputWriter(inputID string) (*PCM16Input, error) {
	if err := m.AddInput(inputID); err != nil {
		return nil, err
	}
	return &PCM16Input{mixer: m, id: strings.TrimSpace(inputID)}, nil
}

// Input returns a scoped writer for an already registered input.
func (m *PCM16Mixer) Input(inputID string) (*PCM16Input, error) {
	if m == nil {
		return nil, ErrMixerClosed
	}
	inputID, err := normalizeMixerInputID(inputID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrMixerClosed
	}
	if _, exists := m.inputs[inputID]; !exists {
		return nil, fmt.Errorf("%w: %q", ErrMixerInputMissing, inputID)
	}
	return &PCM16Input{mixer: m, id: inputID}, nil
}

// RemoveInput removes one input and discards only its queued samples. Later
// frames no longer contain that participant; other inputs remain untouched.
func (m *PCM16Mixer) RemoveInput(inputID string) error {
	if m == nil {
		return ErrMixerClosed
	}
	inputID, err := normalizeMixerInputID(inputID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrMixerClosed
	}
	if _, exists := m.inputs[inputID]; !exists {
		return fmt.Errorf("%w: %q", ErrMixerInputMissing, inputID)
	}
	delete(m.inputs, inputID)
	return nil
}

// Write appends an even-byte PCM16 chunk to one active input. It never
// partially accepts a chunk, so an error cannot create a dropped prefix.
func (m *PCM16Mixer) Write(inputID string, pcm []byte) error {
	if m == nil {
		return ErrMixerClosed
	}
	if len(pcm) == 0 {
		return nil
	}
	if len(pcm)%2 != 0 {
		return fmt.Errorf("%w: PCM16 input has odd byte length %d", ErrMixerInvalidFormat, len(pcm))
	}
	inputID, err := normalizeMixerInputID(inputID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrMixerClosed
	}
	input, exists := m.inputs[inputID]
	if !exists {
		return fmt.Errorf("%w: %q", ErrMixerInputMissing, inputID)
	}
	if len(pcm) > m.maxInputSize-len(input.data) {
		return fmt.Errorf("%w: input %q cannot buffer %d bytes", ErrMixerInputBufferFull, inputID, len(pcm))
	}
	input.data = append(input.data, pcm...)
	return nil
}

// Write is the io.Writer-compatible spelling for a scoped input.
func (i *PCM16Input) Write(pcm []byte) (int, error) {
	if i == nil || i.mixer == nil {
		return 0, ErrMixerClosed
	}
	if err := i.mixer.Write(i.id, pcm); err != nil {
		return 0, err
	}
	return len(pcm), nil
}

// InputID returns the participant ID associated with a scoped input.
func (i *PCM16Input) InputID() string {
	if i == nil {
		return ""
	}
	return i.id
}

// Inputs returns a sorted snapshot of active input IDs.
func (m *PCM16Mixer) Inputs() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	ids := make([]string, 0, len(m.inputs))
	for id := range m.inputs {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	sort.Strings(ids)
	return ids
}

// ReadFrame waits for the next cadence frame or cancellation. A normal Close
// returns io.EOF; an internal mixer failure returns that failure.
func (m *PCM16Mixer) ReadFrame(ctx context.Context) ([]byte, error) {
	if m == nil {
		return nil, ErrMixerClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case frame, ok := <-m.out:
		if !ok {
			if err := m.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		return frame, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Frames exposes the forward-only output stream for callers that prefer a
// channel loop. The channel closes when the mixer is closed or cancelled.
func (m *PCM16Mixer) Frames() <-chan []byte {
	if m == nil {
		return nil
	}
	return m.out
}

// Err returns the first internal mixer failure, if any.
func (m *PCM16Mixer) Err() error {
	if m == nil {
		return ErrMixerClosed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// Close stops cadence production and unblocks writers/readers. It is safe to
// call repeatedly.
func (m *PCM16Mixer) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		m.cancel()
		<-m.done
	})
	return m.Err()
}

func (m *PCM16Mixer) run() {
	defer close(m.done)
	defer close(m.out)
	ticker := time.NewTicker(m.format.FrameDuration)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			frame, err := m.mixFrame()
			if err != nil {
				// Close marks the mixer before cancelling its context. If the
				// ticker wins that small race, the closed sentinel is an
				// intentional shutdown rather than an internal mixer failure.
				if errors.Is(err, ErrMixerClosed) && m.ctx.Err() != nil {
					return
				}
				m.setError(err)
				m.cancel()
				return
			}
			select {
			case m.out <- frame:
			case <-m.ctx.Done():
				return
			}
		}
	}
}

func (m *PCM16Mixer) mixFrame() ([]byte, error) {
	frame := make([]byte, m.frameBytes)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrMixerClosed
	}
	ids := make([]string, 0, len(m.inputs))
	for id := range m.inputs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	accumulated := make([]int32, m.frameBytes/2)
	for _, id := range ids {
		input := m.inputs[id]
		take := len(input.data)
		if take > m.frameBytes {
			take = m.frameBytes
		}
		for offset := 0; offset < take; offset += 2 {
			sample := int16(binary.LittleEndian.Uint16(input.data[offset : offset+2]))
			accumulated[offset/2] += int32(sample)
		}
		if take > 0 {
			copy(input.data, input.data[take:])
			input.data = input.data[:len(input.data)-take]
		}
	}
	for index, sample := range accumulated {
		if sample > 32767 {
			sample = 32767
		} else if sample < -32768 {
			sample = -32768
		}
		binary.LittleEndian.PutUint16(frame[index*2:index*2+2], uint16(int16(sample)))
	}
	return frame, nil
}

func (m *PCM16Mixer) setError(err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	if m.err == nil {
		m.err = err
	}
	m.mu.Unlock()
}

func normalizeMixerInputID(inputID string) (string, error) {
	normalized := strings.TrimSpace(inputID)
	if normalized == "" || strings.ContainsAny(normalized, "\x00\r\n\t") {
		return "", fmt.Errorf("%w: %q", ErrMixerInvalidInputID, inputID)
	}
	return normalized, nil
}

// PCM16Input is a participant-scoped writer returned by AddInputWriter or
// Input. It does not own membership and must not be closed independently.
type PCM16Input struct {
	mixer *PCM16Mixer
	id    string
}

var _ io.Writer = (*PCM16Input)(nil)
