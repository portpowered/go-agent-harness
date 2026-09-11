package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

type evidenceFactory struct{}

const (
	evidenceFileMode      os.FileMode = 0o600
	evidenceDirectoryMode os.FileMode = 0o700
)

// NewEvidenceFactory exposes only the runtime's evidence port to the generated
// composition layer. File and redaction implementations stay in this package.
func NewEvidenceFactory() selfplay.EvidenceFactory { return evidenceFactory{} }

func (evidenceFactory) NewJSONLWriter(path string, limits selfplay.EvidenceLimits) (selfplay.JSONLWriter, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, evidenceFileMode)
	if err != nil {
		return nil, err
	}
	return &jsonlWriter{
		path:   path,
		writer: file,
		limits: normalizeEvidenceLimits(limits),
		sync:   file.Sync,
		close:  file.Close,
	}, nil
}

func (evidenceFactory) WrapJSONLWriter(path string, writer io.Writer, limits selfplay.EvidenceLimits) (selfplay.JSONLWriter, error) {
	if writer == nil {
		return nil, errors.New("self-play evidence writer is required")
	}
	return &jsonlWriter{
		path:   path,
		writer: writer,
		limits: normalizeEvidenceLimits(limits),
		sync: func() error {
			if syncer, ok := writer.(interface{ Sync() error }); ok {
				return syncer.Sync()
			}
			return nil
		},
		close: func() error {
			if closer, ok := writer.(io.Closer); ok {
				return closer.Close()
			}
			return nil
		},
	}, nil
}

func (evidenceFactory) NewWAVWriter(path string, sampleRate int, limits selfplay.EvidenceLimits) (selfplay.WAVWriter, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("WAV sample rate must be positive, got %d", sampleRate)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, evidenceFileMode)
	if err != nil {
		return nil, err
	}
	header, err := wavio.PCM16Header(sampleRate, 0)
	if err == nil {
		_, err = writeEvidenceAll(file, header[:])
	}
	if err != nil {
		closeErr := file.Close()
		removeErr := os.Remove(path)
		return nil, fmt.Errorf("write WAV header: %w", errors.Join(err, closeErr, removeErr))
	}
	return &wavWriter{
		path:       path,
		sampleRate: sampleRate,
		writer:     file,
		limits:     normalizeEvidenceLimits(limits),
		sync:       file.Sync,
		close:      file.Close,
	}, nil
}

func (evidenceFactory) WriteAll(writer io.Writer, data []byte) (int, error) {
	return writeEvidenceAll(writer, data)
}

func (evidenceFactory) CloneStringMap(fields map[string]string) map[string]string {
	if fields == nil {
		return nil
	}
	copyOf := make(map[string]string, len(fields))
	for key, value := range fields {
		copyOf[key] = value
	}
	return copyOf
}

func (evidenceFactory) RedactError(value, secret string) string {
	return redactEvidenceError(value, secret)
}

func (evidenceFactory) WriteAtomicJSON(path string, value any) error {
	return writeAtomicEvidenceJSON(path, value)
}

func normalizeEvidenceLimits(limits selfplay.EvidenceLimits) selfplay.EvidenceLimits {
	if limits.JSONLBytes <= 0 || limits.JSONLBytes > selfplay.DefaultJSONLBytes {
		limits.JSONLBytes = selfplay.DefaultJSONLBytes
	}
	if limits.JSONLItems <= 0 || limits.JSONLItems > selfplay.DefaultJSONLItems {
		limits.JSONLItems = selfplay.DefaultJSONLItems
	}
	if limits.WAVBytes <= 0 || limits.WAVBytes > selfplay.DefaultWAVBytes {
		limits.WAVBytes = selfplay.DefaultWAVBytes
	}
	return limits
}

func writeEvidenceAll(writer io.Writer, data []byte) (int, error) {
	if writer == nil {
		return 0, errors.New("self-play evidence writer is required")
	}
	total := 0
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written < 0 || written > len(data) {
			return total, fmt.Errorf("%w: writer returned invalid byte count %d", io.ErrShortWrite, written)
		}
		total += written
		if err != nil {
			return total, err
		}
		if written == 0 {
			return total, io.ErrShortWrite
		}
		data = data[written:]
	}
	return total, nil
}

type jsonlWriter struct {
	path   string
	writer io.Writer
	limits selfplay.EvidenceLimits
	sync   func() error
	close  func() error
	mu     sync.Mutex
	items  int64
	bytes  int64
	closed bool
	err    error
}

func (w *jsonlWriter) Write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal JSONL record: %w", err)
	}
	return w.WriteRaw(data)
}

func (w *jsonlWriter) WriteRaw(data []byte) error {
	if w == nil {
		return selfplay.ErrEvidenceClosed
	}
	if !json.Valid(data) {
		return errors.New("self-play JSONL record is not valid JSON")
	}
	line := append(append([]byte(nil), data...), '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.writerErr()
	}
	if w.err != nil {
		return w.err
	}
	if w.items >= w.limits.JSONLItems || w.bytes+int64(len(line)) > w.limits.JSONLBytes {
		w.err = fmt.Errorf("%w: %s", selfplay.ErrEvidenceQuota, w.path)
		return w.err
	}
	if _, err := writeEvidenceAll(w.writer, line); err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
		return w.err
	}
	w.items++
	w.bytes += int64(len(line))
	return nil
}

func (w *jsonlWriter) writerErr() error {
	if w.err != nil {
		return w.err
	}
	return selfplay.ErrEvidenceClosed
}

func (w *jsonlWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.sync != nil {
		if err := w.sync(); err != nil {
			w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, err))
		}
	}
	if w.close != nil {
		if err := w.close(); err != nil {
			w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, err))
		}
	}
	return w.err
}

type wavWriter struct {
	path       string
	sampleRate int
	writer     io.WriteSeeker
	limits     selfplay.EvidenceLimits
	sync       func() error
	close      func() error
	mu         sync.Mutex
	dataBytes  int64
	closed     bool
	err        error
}

func (w *wavWriter) Write(ctx context.Context, pcm []byte) error {
	if w == nil {
		return selfplay.ErrEvidenceClosed
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if len(pcm) == 0 {
		return nil
	}
	if len(pcm)%2 != 0 {
		return fmt.Errorf("PCM16 audio delta has odd byte length %d", len(pcm))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.writerErr()
	}
	if w.err != nil {
		return w.err
	}
	if w.dataBytes+int64(len(pcm)) > w.limits.WAVBytes {
		w.err = fmt.Errorf("%w: %s", selfplay.ErrEvidenceQuota, w.path)
		return w.err
	}
	written, err := writeEvidenceAll(w.writer, pcm)
	w.dataBytes += int64(written)
	if err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
	}
	return w.err
}

func (w *wavWriter) writerErr() error {
	if w.err != nil {
		return w.err
	}
	return selfplay.ErrEvidenceClosed
}

func (w *wavWriter) DataBytes() int64 {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dataBytes
}

func (w *wavWriter) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

func (w *wavWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	if header, err := wavio.PCM16Header(w.sampleRate, uint64(w.dataBytes)); err != nil {
		w.err = errors.Join(w.err, err)
	} else if _, err := w.writer.Seek(0, io.SeekStart); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("seek %s for WAV header: %w", w.path, err))
	} else if _, err := writeEvidenceAll(w.writer, header[:]); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("finalize %s WAV header: %w", w.path, err))
	}
	if w.sync != nil {
		if err := w.sync(); err != nil {
			w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, err))
		}
	}
	if w.close != nil {
		if err := w.close(); err != nil {
			w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, err))
		}
	}
	return w.err
}
