package audio

import "github.com/portpowered/go-agent-harness/go-audio/pkg/codec"

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

// FileSink writes 16 kHz mono PCM16 to a WAV or raw PCM path.
type FileSink struct {
	mu     sync.Mutex
	path   string
	format audioFormat

	writer io.Writer
	closer io.Closer

	wav      *wavio.StreamWriter
	closed   bool
	closeErr error
}

var _ AudioSink = (*FileSink)(nil)

// NewFileSink opens path as a file-backed AudioSink. A path of "-" writes raw
// PCM16 to stdout. The supplied stdout is caller-owned and is never closed.
func NewFileSink(path string, stdout io.Writer) (*FileSink, error) {
	format, err := resolveAudioFormat(path)
	if err != nil {
		return nil, err
	}

	if path == "-" {
		if stdout == nil {
			return nil, newStreamError("open", path, format, ErrNilStream)
		}
		return &FileSink{path: path, format: format, writer: stdout}, nil
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, newStreamError("open", path, format, err)
	}
	sink := &FileSink{path: path, format: format, writer: file, closer: file}
	if format == formatWAV {
		sink.wav, err = wavio.NewStreamWriter(file, SampleRate)
		if err != nil {
			return nil, errors.Join(newStreamError("open", path, format, err), file.Close())
		}
	}
	return sink, nil
}

// WriteFrame validates and persists one complete PCM16 frame with bounded memory.
// WAV headers are checkpointed so each successful write leaves a readable prefix.
func (s *FileSink) WriteFrame(ctx context.Context, frame []int16) error {
	if err := ContextError(ctx); err != nil {
		return err
	}
	if err := ValidateFrame("write", frame); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return &ClosedError{Operation: "write", Path: s.path}
	}
	if s.format == formatWAV {
		if err := s.wav.WriteSamples(frame); err != nil {
			return newStreamError("write", s.path, s.format, err)
		}
		if err := s.wav.Checkpoint(); err != nil {
			return newStreamError("write", s.path, s.format, err)
		}
		return nil
	}

	encoded := make([]byte, rawFrameBytes)
	if err := codec.EncodePCM16Into(encoded, frame); err != nil {
		return newStreamError("write", s.path, s.format, err)
	}
	if err := writeAll(s.writer, encoded); err != nil {
		return newStreamError("write", s.path, s.format, err)
	}
	return nil
}

func writeAll(writer io.Writer, encoded []byte) error {
	for len(encoded) > 0 {
		written, err := writer.Write(encoded)
		if written < 0 || written > len(encoded) {
			return fmt.Errorf("%w: writer returned invalid byte count %d", io.ErrShortWrite, written)
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		encoded = encoded[written:]
	}
	return nil
}

// Close finalizes a WAV payload, closes an owned file, and is idempotent.
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return s.closeErr
	}
	s.closed = true

	var closeErr error
	if s.format == formatWAV {
		if s.wav == nil || s.wav.BytesWritten() == 0 {
			closeErr = newStreamError("write", s.path, s.format, wavio.ErrEmptySamples)
		}
		if s.wav != nil {
			if err := s.wav.Close(); err != nil {
				closeErr = errors.Join(closeErr, newStreamError("write", s.path, s.format, err))
			}
		}
	}
	if s.closer != nil {
		if err := s.closer.Close(); err != nil {
			closeErr = errors.Join(closeErr, newStreamError("close", s.path, s.format, err))
		}
	}
	s.closeErr = closeErr
	return closeErr
}
