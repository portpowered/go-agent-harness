package audio

import "github.com/portpowered/go-agent-harness/go-audio/pkg/codec"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const rawFrameBytes = FrameSize * 2

var (
	// ErrClosed indicates that an operation was attempted after Close.
	ErrClosed = errors.New("audio adapter is closed")
	// ErrInvalidFrameSize indicates that a frame buffer is not FrameSize samples.
	ErrInvalidFrameSize = errors.New("audio frame has an invalid size")
	// ErrUnsupportedFormat indicates that a path does not select a supported format.
	ErrUnsupportedFormat = errors.New("unsupported audio format")
	// ErrTruncatedPCM indicates an odd number of raw PCM16 bytes at end of input.
	ErrTruncatedPCM = errors.New("truncated raw PCM16")
	// ErrNilStream indicates that a standard stream dependency was not supplied.
	ErrNilStream = errors.New("audio standard stream is nil")
	// ErrEndOfTurn marks a boundary in a persistent raw PCM stream. FileSource
	// propagates it without marking the source exhausted so the session layer can
	// commit the current audio turn and continue reading the next one.
	ErrEndOfTurn = errors.New("audio input end of turn")
)

// FormatError reports a path whose extension or audio properties are not
// supported by the file adapters.
type FormatError struct {
	Path      string
	Extension string
	Format    string
	Reason    string
	Err       error
}

func (e *FormatError) Error() string {
	detail := e.Reason
	if detail == "" {
		detail = "unsupported format"
	}
	return fmt.Sprintf("audio path %q (%s): %s", e.Path, e.Format, detail)
}

func (e *FormatError) Unwrap() error { return e.Err }

func (e *FormatError) Is(target error) bool { return target == ErrUnsupportedFormat }

// AudioFormatError is an explicit alias for FormatError.
type AudioFormatError = FormatError

// FrameSizeError reports a frame buffer whose length is not FrameSize.
type FrameSizeError struct {
	Operation string
	Got       int
	Want      int
}

func (e *FrameSizeError) Error() string {
	return fmt.Sprintf("audio %s frame has %d samples; want exactly %d", e.Operation, e.Got, e.Want)
}

func (e *FrameSizeError) Is(target error) bool { return target == ErrInvalidFrameSize }

// InvalidFrameSizeError is an explicit alias for FrameSizeError.
type InvalidFrameSizeError = FrameSizeError

// ClosedError reports an operation on a closed adapter.
type ClosedError struct {
	Operation string
	Path      string
}

func (e *ClosedError) Error() string {
	return fmt.Sprintf("audio %s %q: %s", e.Operation, e.Path, ErrClosed)
}

func (e *ClosedError) Is(target error) bool { return target == ErrClosed }

// TruncatedPCMError reports a raw PCM16 stream ending on a half-sample.
type TruncatedPCMError struct {
	Path  string
	Bytes int
}

func (e *TruncatedPCMError) Error() string {
	return fmt.Sprintf("truncated raw PCM16 audio %q: %d trailing byte", e.Path, e.Bytes)
}

func (e *TruncatedPCMError) Is(target error) bool { return target == ErrTruncatedPCM }

// StreamError adds adapter operation, path, and format context while
// preserving the underlying stream or filesystem error for errors.Is/As.
type StreamError struct {
	Operation string
	Path      string
	Format    string
	Err       error
}

func (e *StreamError) Error() string {
	message := fmt.Sprintf("audio %s %q (%s)", e.Operation, e.Path, e.Format)
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *StreamError) Unwrap() error { return e.Err }

type audioFormat uint8

const (
	formatRaw audioFormat = iota
	formatWAV
)

func (f audioFormat) String() string {
	if f == formatWAV {
		return "wav"
	}
	return "raw PCM16"
}

func resolveAudioFormat(path string) (audioFormat, error) {
	if path == "-" {
		return formatRaw, nil
	}

	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case ".wav":
		return formatWAV, nil
	case ".pcm", ".raw":
		return formatRaw, nil
	default:
		return formatRaw, &FormatError{
			Path:      path,
			Extension: extension,
			Format:    "unknown",
			Reason:    `use .wav, .pcm, .raw, or -`,
		}
	}
}

func newStreamError(operation string, path string, format audioFormat, err error) error {
	return &StreamError{Operation: operation, Path: path, Format: format.String(), Err: err}
}

func ContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func ValidateFrame(operation string, frame []int16) error {
	if len(frame) == FrameSize {
		return nil
	}
	return &FrameSizeError{Operation: operation, Got: len(frame), Want: FrameSize}
}

// FileSource reads 16 kHz mono PCM16 from a WAV or raw PCM path.
type FileSource struct {
	mu     sync.Mutex
	path   string
	format audioFormat

	reader io.Reader
	closer io.Closer

	samples  []int16
	position int
	done     bool
	termErr  error
	closed   bool
	closeErr error
}

var _ AudioSource = (*FileSource)(nil)

// NewFileSource opens path as a file-backed AudioSource. A path of "-" reads
// raw PCM16 from stdin. The supplied stdin is caller-owned and is never closed.
func NewFileSource(path string, stdin io.Reader) (*FileSource, error) {
	format, err := resolveAudioFormat(path)
	if err != nil {
		return nil, err
	}

	if path == "-" {
		if stdin == nil {
			return nil, newStreamError("open", path, format, ErrNilStream)
		}
		return &FileSource{path: path, format: format, reader: stdin}, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, newStreamError("open", path, format, err)
	}

	source := &FileSource{path: path, format: format, reader: file, closer: file}
	if format == formatWAV {
		rate, samples, readErr := wavio.Read(file)
		if readErr != nil {
			_ = file.Close()
			return nil, newStreamError("read", path, format, readErr)
		}
		if rate != SampleRate {
			_ = file.Close()
			return nil, &FormatError{
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
		source.samples = samples
	}
	return source, nil
}

// ReadFrame fills buf with the next frame, zero-padding a final short raw or
// WAV frame. Once the input is exhausted it returns io.EOF.
func (s *FileSource) ReadFrame(ctx context.Context, buf []int16) error {
	if err := ContextError(ctx); err != nil {
		return err
	}
	if err := ValidateFrame("read", buf); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return &ClosedError{Operation: "read", Path: s.path}
	}
	if s.termErr != nil {
		return s.termErr
	}
	if s.done {
		return io.EOF
	}
	if s.format == formatWAV {
		return s.readDecodedFrame(buf)
	}
	return s.readRawFrame(buf)
}

func (s *FileSource) readDecodedFrame(buf []int16) error {
	if s.position >= len(s.samples) {
		s.done = true
		return io.EOF
	}

	clear(buf)
	count := len(s.samples) - s.position
	if count > FrameSize {
		count = FrameSize
	}
	copy(buf, s.samples[s.position:s.position+count])
	s.position += count
	if count < FrameSize {
		s.done = true
	}
	return nil
}

func (s *FileSource) readRawFrame(buf []int16) error {
	var encoded [rawFrameBytes]byte
	count, err := io.ReadFull(s.reader, encoded[:])
	if err == nil {
		return codec.DecodePCM16Into(buf, encoded[:])
	}
	if errors.Is(err, ErrEndOfTurn) {
		if count != 0 {
			return newStreamError("read", s.path, s.format, fmt.Errorf("end-of-turn marker after %d PCM bytes", count))
		}
		return ErrEndOfTurn
	}

	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return newStreamError("read", s.path, s.format, err)
	}
	if count == 0 {
		s.done = true
		return io.EOF
	}
	if count%2 != 0 {
		s.termErr = &TruncatedPCMError{Path: s.path, Bytes: count % 2}
		return s.termErr
	}

	clear(buf)
	if err := codec.DecodePCM16Into(buf[:count/2], encoded[:count]); err != nil {
		return newStreamError("read", s.path, s.format, err)
	}
	s.done = true
	return nil
}

// Close releases an owned file. It is safe to call more than once. Standard
// streams are caller-owned and remain open.
func (s *FileSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return s.closeErr
	}
	s.closed = true
	if s.closer == nil {
		return nil
	}
	if err := s.closer.Close(); err != nil {
		s.closeErr = newStreamError("close", s.path, s.format, err)
	}
	return s.closeErr
}
