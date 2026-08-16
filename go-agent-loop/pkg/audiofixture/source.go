package audiofixture

import (
	"context"
	"io"
)

const (
	SampleRate = 16000
	Channels   = 1
	FrameSize  = 480
)

// AudioSource is the small source contract used by speech processing code.
type AudioSource interface {
	ReadFrame(context.Context, []int16) error
	Close() error
}

// Source is verified, in-memory PCM16 audio. Samples is a defensive snapshot
// for exact-content assertions; frame reads use a separate private snapshot.
type Source struct {
	ID         string
	Path       string
	SampleRate int
	Channels   int
	Samples    []int16

	samples  []int16
	position int
	closed   bool
}

// Audio and Fixture are descriptive aliases for callers that prefer those
// names while retaining one implementation and one framing contract.
type Audio = Source
type Fixture = Source

func newSource(id, path string, samples []int16) *Source {
	privateSamples := append([]int16(nil), samples...)
	return &Source{
		ID:         id,
		Path:       path,
		SampleRate: SampleRate,
		Channels:   Channels,
		Samples:    append([]int16(nil), privateSamples...),
		samples:    privateSamples,
	}
}

// ReadFrame fills buf with one complete frame. A short final frame is padded
// with zeroes and is returned exactly once; the following call returns EOF.
func (s *Source) ReadFrame(ctx context.Context, buf []int16) error {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if s == nil || s.closed {
		return &ClosedError{Operation: "read"}
	}
	if len(buf) != FrameSize {
		return &FrameSizeError{Operation: "read", Got: len(buf), Want: FrameSize}
	}
	if s.position >= len(s.samples) {
		return io.EOF
	}

	clear(buf)
	end := s.position + FrameSize
	if end > len(s.samples) {
		end = len(s.samples)
	}
	copy(buf, s.samples[s.position:end])
	s.position = end
	return nil
}

// Close marks the source closed. It owns no external resources, so repeated
// calls are successful and do not alter the verified samples.
func (s *Source) Close() error {
	if s != nil {
		s.closed = true
	}
	return nil
}

// SampleCount reports the number of decoded, normalized samples.
func (s *Source) SampleCount() int {
	if s == nil {
		return 0
	}
	return len(s.samples)
}

// SamplesCopy returns a copy of the normalized samples.
func (s *Source) SamplesCopy() []int16 {
	if s == nil {
		return nil
	}
	return append([]int16(nil), s.samples...)
}

var _ AudioSource = (*Source)(nil)
