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

// FileSink writes mono PCM16 to a WAV or raw PCM path. Raw PCM has no
// container rate; WAV output keeps the rate selected at construction.
type FileSink struct {
	mu     sync.Mutex
	path   string
	format audioFormat
	rate   int

	writer io.Writer
	closer io.Closer

	wav      *wavio.StreamWriter
	closed   bool
	closeErr error
}

var _ AudioSink = (*FileSink)(nil)
var _ SampleSink = (*FileSink)(nil)

// NewFileSink opens path as a file-backed AudioSink using the compatibility
// 16 kHz rate. A path of "-" writes raw PCM16 to stdout. The supplied stdout
// is caller-owned and is never closed.
func NewFileSink(path string, stdout io.Writer) (*FileSink, error) {
	return NewFileSinkAtSampleRate(path, stdout, SampleRate)
}

// NewFileSinkAtSampleRate opens path as a file-backed AudioSink at sampleRate.
// WAV headers use this rate, so a provider negotiated at 24 kHz is not
// mislabeled as the legacy 16 kHz stream. Raw PCM output remains unchanged
// because its format has no in-band sample-rate field. A zero rate selects the
// compatibility 16 kHz rate; negative rates are rejected.
func NewFileSinkAtSampleRate(path string, stdout io.Writer, sampleRate int) (*FileSink, error) {
	if sampleRate == 0 {
		sampleRate = SampleRate
	}
	if sampleRate < 0 {
		return nil, newStreamError("open", path, formatRaw, fmt.Errorf("%w: negative sample rate %d", ErrInvalidDeviceFormat, sampleRate))
	}
	format, err := resolveAudioFormat(path)
	if err != nil {
		return nil, err
	}

	if path == "-" {
		if stdout == nil {
			return nil, newStreamError("open", path, format, ErrNilStream)
		}
		return &FileSink{path: path, format: format, rate: sampleRate, writer: stdout}, nil
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, newStreamError("open", path, format, err)
	}
	sink := &FileSink{path: path, format: format, rate: sampleRate, writer: file, closer: file}
	if format == formatWAV {
		sink.wav, err = wavio.NewStreamWriter(file, sampleRate)
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
	return s.writeSamples(ctx, frame)
}

// WriteSamples writes an arbitrary PCM16 sample count without imposing the
// legacy FrameSize boundary. Stream owners use this method for provider audio
// whose final resampled response can end between device quanta; no silence is
// invented at the artifact boundary. Calls are serialized with WriteFrame
// and retain the same context, close, and stream-error semantics.
func (s *FileSink) WriteSamples(ctx context.Context, samples []int16) error {
	if err := ContextError(ctx); err != nil {
		return err
	}
	return s.writeSamples(ctx, samples)
}

func (s *FileSink) writeSamples(_ context.Context, samples []int16) error {

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return &ClosedError{Operation: "write", Path: s.path}
	}
	if s.format == formatWAV {
		if err := s.wav.WriteSamples(samples); err != nil {
			return newStreamError("write", s.path, s.format, err)
		}
		if err := s.wav.Checkpoint(); err != nil {
			return newStreamError("write", s.path, s.format, err)
		}
		return nil
	}

	encoded := make([]byte, len(samples)*2)
	if err := codec.EncodePCM16Into(encoded, samples); err != nil {
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
