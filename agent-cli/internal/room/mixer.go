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
	// ErrMixerInputBufferFull means that a single write is larger than the
	// mixer's entire bounded input queue. Writes that are larger than current
	// free capacity but fit in the queue wait for space instead of returning
	// this error or dropping audio.
	ErrMixerInputBufferFull = errors.New("PCM16 mixer input buffer is full")
	// ErrMixerOutputBackpressure identifies output pressure for callers that
	// classify mixer failures. Ordinary output saturation is propagated to
	// writers through bounded input backpressure rather than returned as an
	// error or used to discard a frame.
	ErrMixerOutputBackpressure = errors.New("PCM16 mixer output queue is full")
	// ErrMixerManualAdvance identifies an attempt to drive a wall-clock mixer
	// through the deterministic frame API when that mixer was not configured
	// for manual advancement.
	ErrMixerManualAdvance = errors.New("PCM16 mixer is not manually advanced")
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
	CadenceFactory    PCM16CadenceFactory
	// Manual disables the wall-clock cadence and exposes Advance for a room
	// owner that must release frames from a recorded logical timeline.
	Manual bool
}

// PCM16Cadence is the timer contract used to advance mixer frames. The
// production implementation is backed by time.Ticker; tests can provide a
// manually advanced implementation without changing mixer lifecycle code.
type PCM16Cadence interface {
	C() <-chan time.Time
	Stop()
}

// PCM16CadenceFactory creates the cadence source for one mixer during mixer
// construction. The factory receives the configured frame duration and must
// return a non-nil cadence. A cadence is stopped when the mixer exits,
// including cancellation.
type PCM16CadenceFactory func(time.Duration) PCM16Cadence

type realPCM16Cadence struct {
	ticker *time.Ticker
}

func (c *realPCM16Cadence) C() <-chan time.Time {
	return c.ticker.C
}

func (c *realPCM16Cadence) Stop() {
	c.ticker.Stop()
}

func realPCM16CadenceFactory(interval time.Duration) PCM16Cadence {
	return &realPCM16Cadence{ticker: time.NewTicker(interval)}
}

// PCM16QueueStats describes queued PCM in both storage units and audio time.
// Duration is calculated from the mixer's sample rate and channel count, so it
// remains meaningful for partial-frame input buffers as well as whole output
// frames.
type PCM16QueueStats struct {
	Bytes            int
	CapacityBytes    int
	Frames           int
	CapacityFrames   int
	Duration         time.Duration
	CapacityDuration time.Duration
}

// PCM16MixerStats is a point-in-time snapshot of mixer occupancy. Inputs is a
// copy keyed by participant ID; mutating it does not affect the mixer.
type PCM16MixerStats struct {
	Inputs map[string]PCM16QueueStats
	Output PCM16QueueStats
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
	if c.CadenceFactory == nil {
		c.CadenceFactory = realPCM16CadenceFactory
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
	cadence      PCM16Cadence
	manual       bool

	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte

	mu        sync.Mutex
	advanceMu sync.Mutex
	inputs    map[string]*pcm16MixerInput
	writeWake chan struct{}
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
	var cadence PCM16Cadence
	if !config.Manual {
		cadence = config.CadenceFactory(config.Format.FrameDuration)
	}
	mixer := &PCM16Mixer{
		format:       config.Format,
		frameBytes:   frameBytes,
		maxInputSize: config.InputQueueFrames * frameBytes,
		cadence:      cadence,
		manual:       config.Manual,
		ctx:          mixerCtx,
		cancel:       cancel,
		out:          make(chan []byte, config.OutputQueueFrames),
		inputs:       make(map[string]*pcm16MixerInput),
		writeWake:    make(chan struct{}),
		done:         make(chan struct{}),
	}
	go mixer.run()
	return mixer, nil
}

// Advance emits exactly one mixer frame synchronously. It is available only
// for mixers created with PCM16MixerConfig.Manual. The frame is mixed using
// the same sorted-input, bounded-queue implementation as the live ticker
// path, then placed on the normal output queue before Advance returns.
func (m *PCM16Mixer) Advance(ctx context.Context) error {
	if m == nil {
		return ErrMixerClosed
	}
	if !m.manual {
		return ErrMixerManualAdvance
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Close waits for this lock before the manual run closes m.out. That keeps
	// a concurrent cancellation from racing a frame send with channel close.
	m.advanceMu.Lock()
	defer m.advanceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.ctx.Err(); err != nil {
		return m.writeTerminationError()
	}
	frame, err := m.mixFrame()
	if err != nil {
		return err
	}
	select {
	case m.out <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ctx.Done():
		return m.writeTerminationError()
	}
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

// Stats returns a point-in-time snapshot of input and output queue occupancy.
// The snapshot is intended for runtime diagnostics and bounded-pressure
// tests; it does not reserve capacity or change mixer scheduling.
func (m *PCM16Mixer) Stats() PCM16MixerStats {
	stats := PCM16MixerStats{Inputs: make(map[string]PCM16QueueStats)}
	if m == nil {
		return stats
	}

	m.mu.Lock()
	for id, input := range m.inputs {
		stats.Inputs[id] = m.queueStats(len(input.data), m.maxInputSize)
	}
	m.mu.Unlock()

	stats.Output = m.queueStats(len(m.out)*m.frameBytes, cap(m.out)*m.frameBytes)
	return stats
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
	m.signalWritersLocked()
	return nil
}

// Write appends an even-byte PCM16 chunk to one active input. It waits for
// bounded capacity when necessary and never partially accepts a chunk, so
// an error cannot create a dropped prefix. The mixer lifecycle is the
// cancellation context for this compatibility method; use WriteContext
// when the caller also owns a cancellation boundary.
func (m *PCM16Mixer) Write(inputID string, pcm []byte) error {
	return m.WriteContext(context.Background(), inputID, pcm)
}

// WriteContext appends a complete even-byte PCM16 chunk to one active input.
// If the chunk fits in the bounded input queue but current capacity is
// unavailable, it waits until the cadence drain frees space, ctx is canceled,
// the input is removed, or the mixer shuts down. A chunk larger than the
// entire queue is rejected immediately because it can never be accepted
// atomically without an unbounded or partial write.
func (m *PCM16Mixer) WriteContext(ctx context.Context, inputID string, pcm []byte) error {
	if m == nil {
		return ErrMixerClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
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

	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return ErrMixerClosed
		}
		if m.err != nil {
			err := m.err
			m.mu.Unlock()
			return err
		}
		if err := m.ctx.Err(); err != nil {
			m.mu.Unlock()
			return err
		}
		input, exists := m.inputs[inputID]
		if !exists {
			m.mu.Unlock()
			return fmt.Errorf("%w: %q", ErrMixerInputMissing, inputID)
		}
		if len(pcm) > m.maxInputSize {
			m.mu.Unlock()
			return fmt.Errorf("%w: input %q write is %d bytes but queue capacity is %d bytes", ErrMixerInputBufferFull, inputID, len(pcm), m.maxInputSize)
		}
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return err
		}
		if len(pcm) <= m.maxInputSize-len(input.data) {
			input.data = append(input.data, pcm...)
			m.mu.Unlock()
			return nil
		}
		wake := m.writeWake
		m.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.ctx.Done():
			return m.writeTerminationError()
		case <-wake:
		}
	}
}

// Write is the io.Writer-compatible spelling for a scoped input.
func (i *PCM16Input) Write(pcm []byte) (int, error) {
	return i.WriteContext(context.Background(), pcm)
}

// WriteContext is the cancellation-aware spelling for a scoped input.
func (i *PCM16Input) WriteContext(ctx context.Context, pcm []byte) (int, error) {
	if i == nil || i.mixer == nil {
		return 0, ErrMixerClosed
	}
	if err := i.mixer.WriteContext(ctx, i.id, pcm); err != nil {
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
		m.signalWritersLocked()
		m.mu.Unlock()
		m.cancel()
		<-m.done
	})
	return m.Err()
}

func (m *PCM16Mixer) run() {
	defer close(m.done)
	if m.manual {
		<-m.ctx.Done()
		m.advanceMu.Lock()
		close(m.out)
		m.advanceMu.Unlock()
		return
	}
	defer close(m.out)
	cadence := m.cadence
	if cadence == nil {
		m.setError(fmt.Errorf("%w: cadence factory returned nil", ErrMixerInvalidFormat))
		return
	}
	defer cadence.Stop()
	ticks := cadence.C()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticks:
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
	if len(ids) > 0 {
		m.signalWritersLocked()
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
		m.signalWritersLocked()
	}
	m.mu.Unlock()
}

// signalWritersLocked wakes every writer that observed the previous input
// occupancy. Replacing the channel lets a writer safely go back to sleep
// without missing a signal that happened between its unlock and select.
func (m *PCM16Mixer) signalWritersLocked() {
	if m.writeWake == nil {
		m.writeWake = make(chan struct{})
		return
	}
	close(m.writeWake)
	m.writeWake = make(chan struct{})
}

func (m *PCM16Mixer) writeTerminationError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.closed {
		return ErrMixerClosed
	}
	if err := m.ctx.Err(); err != nil {
		return err
	}
	return ErrMixerClosed
}

func (m *PCM16Mixer) queueStats(queuedBytes, capacityBytes int) PCM16QueueStats {
	return PCM16QueueStats{
		Bytes:            queuedBytes,
		CapacityBytes:    capacityBytes,
		Frames:           queueFrameCount(queuedBytes, m.frameBytes),
		CapacityFrames:   queueFrameCount(capacityBytes, m.frameBytes),
		Duration:         pcm16Duration(queuedBytes, m.format),
		CapacityDuration: pcm16Duration(capacityBytes, m.format),
	}
}

func queueFrameCount(bytes, frameBytes int) int {
	if bytes <= 0 || frameBytes <= 0 {
		return 0
	}
	return (bytes + frameBytes - 1) / frameBytes
}

func pcm16Duration(bytes int, format PCM16Format) time.Duration {
	if bytes <= 0 || format.SampleRate <= 0 || format.Channels <= 0 {
		return 0
	}
	bytesPerSecond := int64(format.SampleRate) * int64(format.Channels) * 2
	return time.Duration(int64(bytes) * int64(time.Second) / bytesPerSecond)
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
