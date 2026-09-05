package audio

import (
	"context"
	"io"
)

const (
	// SampleRate is the standard sample rate for speech processing.
	SampleRate = 16000
	// FrameSize is the number of samples per processing frame (30ms at 16kHz).
	FrameSize = 480
	// Channels is the number of audio channels (mono).
	Channels = 1
)

// AudioSource provides raw PCM int16 audio frames.
type AudioSource interface {
	// ReadFrame fills buf with exactly FrameSize int16 samples.
	// Returns io.EOF when no more audio is available.
	ReadFrame(ctx context.Context, buf []int16) error
	// Close releases any underlying resources.
	Close() error
}

// SliceSource reads PCM samples from an in-memory slice, FrameSize at a time.
// Useful for testing and offline replay without hardware.
type SliceSource struct {
	data []int16
	pos  int
}

// NewSliceSource creates a SliceSource backed by the given samples.
func NewSliceSource(samples []int16) *SliceSource {
	return &SliceSource{data: samples}
}

// ReadFrame fills buf with the next FrameSize samples.
// When fewer than FrameSize samples remain the tail is zero-padded;
// when the slice is fully consumed io.EOF is returned.
func (s *SliceSource) ReadFrame(_ context.Context, buf []int16) error {
	if s.pos >= len(s.data) {
		return io.EOF
	}
	end := s.pos + FrameSize
	if end > len(s.data) {
		// Partial frame: copy what we have and pad the rest with silence.
		copy(buf, s.data[s.pos:])
		for i := len(s.data) - s.pos; i < FrameSize; i++ {
			buf[i] = 0
		}
		s.pos = len(s.data)
	} else {
		copy(buf, s.data[s.pos:end])
		s.pos = end
	}
	return nil
}

// Close is a no-op for SliceSource.
func (s *SliceSource) Close() error { return nil }
