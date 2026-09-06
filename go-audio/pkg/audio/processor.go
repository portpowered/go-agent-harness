package audio

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

var (
	ErrStreamFormatChanged   = errors.New("audio stream format changed without a reset")
	ErrStreamIdentityChanged = errors.New("audio stream identity changed without a reset")
	// ErrFrameAccumulatorMetadataChanged identifies a finite media quantum
	// whose negotiated format or source lineage differs from the first quantum
	// admitted to an accumulator.
	ErrFrameAccumulatorMetadataChanged = errors.New("audio frame accumulator metadata changed")
	// ErrFrameAccumulatorFrameTooLarge identifies one input quantum larger than
	// the configured provider port budget. The accumulator may combine many
	// smaller quanta, but never admits an individual oversized allocation.
	ErrFrameAccumulatorFrameTooLarge = errors.New("audio frame accumulator input frame is too large")
	// ErrFrameAccumulatorClosed identifies an attempt to append after the
	// accumulator has emitted its final response boundary.
	ErrFrameAccumulatorClosed = errors.New("audio frame accumulator is closed")
)

// Processor is the single stream-owned resampling and framing operation.
// It contains no devices, goroutines, clocks or provider protocol state.
// Callers serialize Process/Reset on the owning media worker. A response tail
// is flushed exactly once; an explicit Reset discards it for interruption.
type Processor struct {
	input, output DeviceFormat
	quantum       int
	resampler     wavio.PCM16Resampler
	pending       []int16
	position      uint64
	sequence      uint64
	ended         bool
	identified    bool
	streamID      string
	epoch         uint64
	response      PlaybackResponse
}

func NewProcessor(input, output DeviceFormat, quantum int) (*Processor, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	if err := output.Validate(); err != nil {
		return nil, err
	}
	if quantum <= 0 {
		return nil, fmt.Errorf("audio processing quantum must be positive")
	}
	r, err := wavio.NewPCM16Resampler(input.SampleRate, output.SampleRate)
	if err != nil {
		return nil, err
	}
	return &Processor{input: input, output: output, quantum: quantum, resampler: r}, nil
}

// Process preserves filter phase across arbitrary packet splits. Input with
// an omitted format uses the explicitly negotiated constructor format. Output
// contains exact tails, never invented padding; an empty final marker remains
// observable even when the preceding output ended on a quantum boundary.
func (p *Processor) Process(frame PCMFrame) ([]PCMFrame, error) {
	if frame.Format != (DeviceFormat{}) && frame.Format != p.input {
		return nil, ErrStreamFormatChanged
	}
	if p.ended {
		return nil, wavio.ErrResamplerEnded
	}
	if p.identified && (frame.StreamID != p.streamID || frame.Epoch != p.epoch || frame.PlaybackResponse != p.response) {
		return nil, ErrStreamIdentityChanged
	}
	samples, err := p.resampler.Process(frame.Samples, frame.EndOfResponse)
	if err != nil {
		return nil, err
	}
	p.identified = true
	p.streamID, p.epoch, p.response = frame.StreamID, frame.Epoch, frame.PlaybackResponse
	p.pending = append(p.pending, samples...)
	p.ended = frame.EndOfResponse
	frames := make([]PCMFrame, 0, len(p.pending)/p.quantum+1)
	for len(p.pending) >= p.quantum {
		frames = append(frames, p.next(frame, p.quantum, false))
	}
	if frame.EndOfResponse {
		if len(p.pending) > 0 || len(frames) == 0 {
			frames = append(frames, p.next(frame, len(p.pending), true))
		} else {
			frames[len(frames)-1].EndOfResponse = true
		}
	}
	return frames, nil
}

// ProcessAvailable emits the currently available samples without waiting for a
// full output quantum. Capture uses this to avoid adding a packet of latency
// for the resampler's lookahead. Filter history remains live until the final
// marker; emitted packets still carry contiguous sample offsets.
func (p *Processor) ProcessAvailable(frame PCMFrame) ([]PCMFrame, error) {
	frames, err := p.Process(frame)
	if err != nil {
		return nil, err
	}
	if len(p.pending) > 0 {
		frames = append(frames, p.next(frame, len(p.pending), false))
	}
	return frames, nil
}

func (p *Processor) next(parent PCMFrame, n int, end bool) PCMFrame {
	f := parent
	f.Samples = append([]int16(nil), p.pending[:n]...)
	f.Format = p.output
	f.StartSample = p.position
	f.Sequence = p.sequence
	f.EndOfResponse = end
	p.position += uint64(n / p.output.Channels)
	p.sequence++
	p.pending = p.pending[n:]
	if len(p.pending) == 0 {
		p.pending = nil
	}
	return f
}

// Reset starts a new response/epoch and returns discarded output samples.
// The caller records that loss before resetting its packet lineage.
func (p *Processor) Reset() (int, error) {
	discarded := len(p.pending)
	if err := p.resampler.Reset(p.input.SampleRate, p.output.SampleRate); err != nil {
		return 0, err
	}
	p.pending = nil
	p.position, p.sequence = 0, 0
	p.ended = false
	p.identified = false
	p.streamID, p.epoch, p.response = "", 0, PlaybackResponse{}
	return discarded, nil
}

// FrameAccumulator joins finite PCM quanta into provider-port-sized frames.
// It owns only one bounded pending frame; a long finite turn is emitted as
// soon as the next quantum arrives or when Flush is called. The target remains
// caller-owned and is never closed by the accumulator.
//
// Calls are serialized by the owner, just like Processor calls. WriteFrame
// copies samples before retaining them, and every emitted frame is copied
// before it is handed to the target. Format, stream, epoch, and playback
// response metadata must remain stable for the entire finite turn. Sequence
// and StartSample are generated contiguously from the first frame's lineage so
// splitting at a provider boundary never duplicates or drops its cursor.
type FrameAccumulator struct {
	target         OutboundMedia
	frameBudget    int
	pending        []int16
	metadata       PCMFrame
	hasMetadata    bool
	sequence       uint64
	startSample    uint64
	channels       int
	nextInputSeq   uint64
	nextInputStart uint64
	emptyTail      bool
	ended          bool
	flushed        bool
	writeErr       error
}

// NewFrameAccumulator creates a bounded finite-turn adapter. frameBudget is
// measured in interleaved PCM samples and must be positive.
func NewFrameAccumulator(target OutboundMedia, frameBudget int) (*FrameAccumulator, error) {
	if target == nil {
		return nil, errors.New("audio frame accumulator target is required")
	}
	if frameBudget <= 0 {
		return nil, fmt.Errorf("audio frame accumulator budget must be positive: %d", frameBudget)
	}
	return &FrameAccumulator{
		target: target, frameBudget: frameBudget,
		pending: make([]int16, 0, frameBudget), channels: Channels,
	}, nil
}

// WriteFrame admits one provider quantum. A single quantum must fit within
// the provider budget; a sequence of quanta may be arbitrarily long because
// complete pending frames are forwarded before the next quantum is copied.
// EndOfResponse is intentionally deferred to Flush so only the final emitted
// chunk carries the response boundary.
func (a *FrameAccumulator) WriteFrame(ctx context.Context, frame PCMFrame) error {
	if err := a.validateWrite(ctx, frame); err != nil {
		return err
	}
	if err := a.admitMetadata(frame); err != nil {
		return err
	}
	if len(frame.Samples) == 0 {
		a.emptyTail = frame.EndOfResponse
		a.ended = frame.EndOfResponse
		return nil
	}
	if err := a.appendSamples(ctx, frame.Samples); err != nil {
		return err
	}
	a.ended = frame.EndOfResponse
	return nil
}

func (a *FrameAccumulator) validateWrite(ctx context.Context, frame PCMFrame) error {
	if a == nil || a.flushed || a.ended {
		return ErrFrameAccumulatorClosed
	}
	if a.writeErr != nil {
		return a.writeErr
	}
	if ctx == nil {
		return errors.New("audio frame accumulator context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(frame.Samples) > a.frameBudget {
		return fmt.Errorf("%w: got %d samples, budget %d", ErrFrameAccumulatorFrameTooLarge, len(frame.Samples), a.frameBudget)
	}
	return nil
}

func (a *FrameAccumulator) appendSamples(ctx context.Context, samples []int16) error {
	for len(samples) > 0 {
		if len(a.pending) == a.frameBudget {
			if err := a.writePending(ctx, false); err != nil {
				return err
			}
		}
		available := a.frameBudget - len(a.pending)
		count := len(samples)
		if count > available {
			count = available
		}
		a.pending = append(a.pending, samples[:count]...)
		samples = samples[count:]
	}
	return nil
}

// Flush forwards the final pending samples without padding. The final data
// frame, or an explicit empty tail marker, is the only frame marked
// EndOfResponse. A successful flush is idempotent; callers cannot append a
// second finite turn to the same accumulator.
func (a *FrameAccumulator) Flush(ctx context.Context) error {
	if a == nil {
		return ErrFrameAccumulatorClosed
	}
	if a.writeErr != nil {
		return a.writeErr
	}
	if a.flushed {
		return nil
	}
	if ctx == nil {
		return errors.New("audio frame accumulator context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(a.pending) > 0 {
		if err := a.writePending(ctx, true); err != nil {
			return err
		}
	} else if a.emptyTail {
		if err := a.writePending(ctx, true); err != nil {
			return err
		}
	}
	a.flushed = true
	return nil
}

// Close satisfies OutboundMedia without taking ownership of the target. The
// finite-turn owner must flush before closing its session endpoint.
func (*FrameAccumulator) Close() error { return nil }

func (a *FrameAccumulator) admitMetadata(frame PCMFrame) error {
	if !a.hasMetadata {
		channels, err := validateAccumulatorFormat(frame.Format)
		if err != nil {
			return err
		}
		if a.frameBudget%channels != 0 {
			return fmt.Errorf("%w: frame budget %d is not aligned to %d channels", ErrInvalidDeviceFormat, a.frameBudget, channels)
		}
		if len(frame.Samples)%channels != 0 {
			return fmt.Errorf("%w: frame has %d interleaved samples for %d channels", ErrInvalidDeviceFormat, len(frame.Samples), channels)
		}
		a.metadata = frame
		a.metadata.Samples = nil
		a.metadata.EndOfResponse = false
		a.sequence = frame.Sequence
		a.startSample = frame.StartSample
		a.channels = channels
		a.nextInputSeq = frame.Sequence + 1
		a.nextInputStart = frame.StartSample + uint64(len(frame.Samples)/channels)
		a.hasMetadata = true
		return nil
	}
	if frame.Format != a.metadata.Format || frame.StreamID != a.metadata.StreamID ||
		frame.Epoch != a.metadata.Epoch || frame.PlaybackResponse != a.metadata.PlaybackResponse {
		return fmt.Errorf("%w: first=%+v next=%+v", ErrFrameAccumulatorMetadataChanged, frameLineageOf(a.metadata), frameLineageOf(frame))
	}
	if len(frame.Samples)%a.channels != 0 {
		return fmt.Errorf("%w: frame has %d interleaved samples for %d channels", ErrInvalidDeviceFormat, len(frame.Samples), a.channels)
	}
	if frame.Sequence != a.nextInputSeq || frame.StartSample != a.nextInputStart {
		return fmt.Errorf("%w: expected input sequence=%d start=%d, got sequence=%d start=%d", ErrFrameAccumulatorMetadataChanged, a.nextInputSeq, a.nextInputStart, frame.Sequence, frame.StartSample)
	}
	a.nextInputSeq++
	a.nextInputStart += uint64(len(frame.Samples) / a.channels)
	return nil
}

func validateAccumulatorFormat(format DeviceFormat) (int, error) {
	if format.SampleRate <= 0 || format.Channels <= 0 || format.BitDepth != DeviceBitDepthPCM16 || format.Encoding != DeviceEncodingPCM16 {
		return 0, fmt.Errorf("%w: frame accumulator requires explicit PCM16 format, got %v", ErrInvalidDeviceFormat, format)
	}
	return format.Channels, nil
}

func (a *FrameAccumulator) writePending(ctx context.Context, endOfResponse bool) error {
	frame := a.metadata
	frame.Samples = append([]int16(nil), a.pending...)
	frame.Sequence = a.sequence
	frame.StartSample = a.startSample
	frame.EndOfResponse = endOfResponse
	if err := a.target.WriteFrame(ctx, frame); err != nil {
		a.writeErr = err
		return err
	}
	a.sequence++
	a.startSample += uint64(len(a.pending) / a.channels)
	a.pending = a.pending[:0]
	a.emptyTail = false
	return nil
}

type frameLineage struct {
	Format           DeviceFormat
	StreamID         string
	Epoch            uint64
	PlaybackResponse PlaybackResponse
}

func frameLineageOf(frame PCMFrame) frameLineage {
	return frameLineage{
		Format: frame.Format, StreamID: frame.StreamID, Epoch: frame.Epoch, PlaybackResponse: frame.PlaybackResponse,
	}
}

var _ OutboundMedia = (*FrameAccumulator)(nil)
