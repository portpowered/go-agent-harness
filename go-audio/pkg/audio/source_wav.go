package audio

import (
	"context"
	"errors"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"io"
	"sync"
)

// WAVSource streams PCM16 frames from a RIFF WAVE file incrementally.
// Header chunks are parsed once at open; data-chunk bytes are read one frame
// at a time by ReadFrame.
type WAVSource struct {
	path       string
	file       io.ReadSeekCloser
	remaining  int64
	sampleRate int
	done       bool
	closed     bool
	mu         sync.Mutex
	closeOnce  sync.Once
	closeErr   error
}

var _ AudioSource = (*WAVSource)(nil)

func (s *WAVSource) SampleRate() int {
	if s == nil {
		return 0
	}
	return s.sampleRate
}

// NewWAVSource validates the container without loading PCM and takes ownership
// of the supplied file on success. The file remains caller-owned on failure.
func NewWAVSource(path string, r io.ReadSeekCloser) (*WAVSource, error) {
	if r == nil {
		return nil, ErrNilStream
	}
	layout, err := wavio.Inspect(r)
	if err != nil {
		return nil, err
	}
	if err := wavio.ValidateSampleRate(layout.SampleRate); err != nil {
		return nil, err
	}
	return &WAVSource{path: path, file: r, remaining: int64(layout.DataBytes), sampleRate: layout.SampleRate, done: layout.DataBytes == 0}, nil
}

// ReadFrame fills buf with the next data-chunk frame, zero-padding a final
// short frame. Once the payload is exhausted it returns io.EOF. Each call
// consumes at most FrameSize*2 payload bytes, never the remaining file.
func (s *WAVSource) ReadFrame(ctx context.Context, buf []int16) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if len(buf) != FrameSize {
		return &FrameSizeError{Operation: "read", Got: len(buf), Want: FrameSize}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return &ClosedError{Operation: "read", Path: s.path}
	}
	if s.done {
		return io.EOF
	}

	want := int64(FrameSize * 2)
	count := want
	if s.remaining < count {
		count = s.remaining
	}
	encoded := make([]byte, count)
	if _, err := io.ReadFull(s.file, encoded); err != nil {
		s.done = true
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return &TruncatedPCMError{Path: s.path, Bytes: int(count % 2)}
		}
		return &StreamError{Operation: "read", Path: s.path, Format: "wav", Err: err}
	}
	s.remaining -= count
	if count%2 != 0 {
		s.done = true
		return &TruncatedPCMError{Path: s.path, Bytes: 1}
	}
	clear(buf)
	if err := codec.DecodePCM16Into(buf, encoded); err != nil {
		return &StreamError{Operation: "read", Path: s.path, Format: "wav", Err: err}
	}
	if s.remaining == 0 {
		s.done = true
	}
	return nil
}

// Close releases the owned file. It is safe to call more than once.
func (s *WAVSource) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.done = true
		err := s.file.Close()
		s.mu.Unlock()
		if err != nil {
			s.closeErr = &StreamError{Operation: "close", Path: s.path, Format: "wav", Err: err}
		}
	})
	return s.closeErr
}
