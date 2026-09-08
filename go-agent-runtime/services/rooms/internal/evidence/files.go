package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	evidenceFileMode      = 0o600
	evidenceDirectoryMode = 0o700
)

type jsonlWriter struct {
	path   string
	file   *os.File
	mu     sync.Mutex
	closed bool
	err    error
}

func newJSONLWriter(path string) (*jsonlWriter, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, evidenceFileMode)
	if err != nil {
		return nil, err
	}
	return &jsonlWriter{path: path, file: file}, nil
}

func (w *jsonlWriter) write(value any) error {
	if w == nil {
		return errors.New("evidence JSONL writer is nil")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal JSONL record: %w", err)
	}
	data = append(data, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.errOr(errors.New("evidence JSONL writer is closed"))
	}
	if w.err != nil {
		return w.err
	}
	if err := writeAll(w.file, data); err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
	}
	return w.err
}

func (w *jsonlWriter) close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	if err := w.file.Sync(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, err))
	}
	if err := w.file.Close(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, err))
	}
	return w.err
}

func (w *jsonlWriter) errOr(fallback error) error {
	if w.err != nil {
		return w.err
	}
	return fallback
}

type pcmWriter struct {
	path   string
	file   *os.File
	mu     sync.Mutex
	bytes  uint64
	closed bool
	err    error
}

func newPCMWriter(path string) (*pcmWriter, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, evidenceFileMode)
	if err != nil {
		return nil, err
	}
	return &pcmWriter{path: path, file: file}, nil
}

func (w *pcmWriter) write(samples []int16) error {
	if w == nil || len(samples) == 0 {
		return nil
	}
	encoded := codec.EncodePCM16(samples)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.errOr(errors.New("evidence PCM writer is closed"))
	}
	if w.err != nil {
		return w.err
	}
	if err := writeAll(w.file, encoded); err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
		return w.err
	}
	w.bytes += uint64(len(encoded))
	return nil
}

func (w *pcmWriter) close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	if err := w.file.Sync(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, err))
	}
	if err := w.file.Close(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, err))
	}
	return w.err
}

func (w *pcmWriter) errOr(fallback error) error {
	if w.err != nil {
		return w.err
	}
	return fallback
}

type wavWriter struct {
	path   string
	rate   int
	file   *os.File
	mu     sync.Mutex
	bytes  uint64
	closed bool
	err    error
}

func newWAVWriter(path string, rate int) (*wavWriter, error) {
	if rate <= 0 {
		return nil, fmt.Errorf("WAV sample rate must be positive, got %d", rate)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, evidenceFileMode)
	if err != nil {
		return nil, err
	}
	header, err := wavio.PCM16Header(rate, 0)
	if err == nil {
		err = writeAll(file, header[:])
	}
	if err != nil {
		closeErr := file.Close()
		removeErr := os.Remove(path)
		return nil, errors.Join(fmt.Errorf("write WAV header: %w", err), closeErr, removeErr)
	}
	return &wavWriter{path: path, rate: rate, file: file}, nil
}

func (w *wavWriter) write(samples []int16) error {
	if w == nil || len(samples) == 0 {
		return nil
	}
	encoded := codec.EncodePCM16(samples)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.errOr(errors.New("evidence WAV writer is closed"))
	}
	if w.err != nil {
		return w.err
	}
	if err := writeAll(w.file, encoded); err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
		return w.err
	}
	w.bytes += uint64(len(encoded))
	return nil
}

func (w *wavWriter) close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	header, err := wavio.PCM16Header(w.rate, w.bytes)
	if err != nil {
		w.err = errors.Join(w.err, err)
	} else if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("seek %s for WAV header: %w", w.path, err))
	} else if err := writeAll(w.file, header[:]); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("rewrite %s WAV header: %w", w.path, err))
	}
	if err := w.file.Sync(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, err))
	}
	if err := w.file.Close(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, err))
	}
	return w.err
}

func (w *wavWriter) errOr(fallback error) error {
	if w.err != nil {
		return w.err
	}
	return fallback
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written < 0 || written > len(data) {
			return fmt.Errorf("%w: writer returned invalid byte count %d", io.ErrShortWrite, written)
		}
		if written == 0 && err == nil {
			return io.ErrShortWrite
		}
		data = data[written:]
		if err != nil {
			return err
		}
	}
	return nil
}

func cleanupDir(path string) error { return os.RemoveAll(filepath.Clean(path)) }
