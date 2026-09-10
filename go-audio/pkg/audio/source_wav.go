package audio

import (
	"context"
	"errors"
	"fmt"
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
var _ SampleSource = (*WAVSource)(nil)

func (s *WAVSource) SampleRate() int {
	if s == nil {
		return 0
	}
	return s.sampleRate
}

func fileWAVSampleRateError(path string, format audioFormat, rate int) *FormatError {
	return &FormatError{
		Path:      path,
		Extension: ".wav",
		Format:    format.String(),
		Reason:    fmt.Sprintf("sample rate is %d Hz; want exactly %d Hz", rate, SampleRate),
		Err: &wavio.UnsupportedError{
			Property:  "sample rate",
			Observed:  rate,
			Supported: "16000 Hz",
		},
	}
}

// NewWAVSource validates the container without loading PCM and takes ownership
// of the supplied file on success. The file remains caller-owned on failure.
func NewWAVSource(path string, r io.ReadSeekCloser) (*WAVSource, error) {
	return newWAVSource(path, r, true)
}

// newFileWAVSource validates a file-backed WAV container while leaving the
// FileSource adapter's stricter 16 kHz compatibility check to its caller.
func newFileWAVSource(path string, r io.ReadSeekCloser) (*WAVSource, error) {
	return newWAVSource(path, r, false)
}

func newWAVSource(path string, r io.ReadSeekCloser, validateRate bool) (*WAVSource, error) {
	if r == nil {
		return nil, ErrNilStream
	}
	layout, err := wavio.Inspect(r)
	if err != nil {
		return nil, err
	}
	if validateRate {
		if err := wavio.ValidateSampleRate(layout.SampleRate); err != nil {
			return nil, err
		}
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

	n, err := s.ReadSamples(ctx, buf)
	if err != nil {
		return err
	}
	clear(buf[n:])
	return nil
}

// ReadSamples returns up to len(buf) decoded payload samples without padding
// a short final chunk. It is the count-aware companion to ReadFrame and keeps
// the source's original sample count available to finite media adapters.
func (s *WAVSource) ReadSamples(ctx context.Context, buf []int16) (int, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	if len(buf) == 0 {
		return 0, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, &ClosedError{Operation: "read", Path: s.path}
	}
	if s.done {
		return 0, io.EOF
	}

	count := int64(len(buf) * 2)
	if s.remaining < count {
		count = s.remaining
	}
	encoded := make([]byte, count)
	read, err := io.ReadFull(s.file, encoded)
	if err != nil {
		s.done = true
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return 0, &TruncatedPCMError{Path: s.path, Bytes: read % 2}
		}
		return 0, &StreamError{Operation: "read", Path: s.path, Format: "wav", Err: err}
	}
	s.remaining -= int64(read)
	if read%2 != 0 {
		s.done = true
		return 0, &TruncatedPCMError{Path: s.path, Bytes: 1}
	}
	n := read / 2
	if err := codec.DecodePCM16Into(buf[:n], encoded[:read]); err != nil {
		return 0, &StreamError{Operation: "read", Path: s.path, Format: "wav", Err: err}
	}
	if s.remaining == 0 {
		s.done = true
	}
	return n, nil
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
