package audio

import (
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

var ErrStreamFormatChanged = errors.New("audio stream format changed without a reset")
var ErrStreamIdentityChanged = errors.New("audio stream identity changed without a reset")

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
