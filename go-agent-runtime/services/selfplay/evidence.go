package selfplay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

const (
	AgentAWAVPath          = "agent-a.wav"
	AgentBWAVPath          = "agent-b.wav"
	AgentADiagnosticsPath  = "agent-a-diagnostics.jsonl"
	AgentBDiagnosticsPath  = "agent-b-diagnostics.jsonl"
	AgentAStreamDeltasPath = "agent-a-stream-deltas.jsonl"
	AgentBStreamDeltasPath = "agent-b-stream-deltas.jsonl"
	ManifestPath           = "run-manifest.json"
	EvidenceSchemaVersion  = 1
	EvidenceSampleRate     = 24000
)

type evidenceError string

func (e evidenceError) Error() string { return string(e) }

const (
	ErrEvidenceQuota  evidenceError = "self-play evidence quota exceeded"
	ErrEvidenceClosed evidenceError = "self-play evidence writer is closed"
)

// EvidenceLimits bounds each side's diagnostic/stream and PCM artifacts; zero selects finite defaults.
type EvidenceLimits struct {
	JSONLBytes int64
	JSONLItems int64
	WAVBytes   int64
}

const (
	DefaultJSONLBytes int64 = 64 << 20
	DefaultJSONLItems int64 = 1 << 20
	DefaultWAVBytes   int64 = 64 << 20
)

func normalizedLimits(limits EvidenceLimits) EvidenceLimits {
	if limits.JSONLBytes <= 0 || limits.JSONLBytes > DefaultJSONLBytes {
		limits.JSONLBytes = DefaultJSONLBytes
	}
	if limits.JSONLItems <= 0 || limits.JSONLItems > DefaultJSONLItems {
		limits.JSONLItems = DefaultJSONLItems
	}
	if limits.WAVBytes <= 0 || limits.WAVBytes > DefaultWAVBytes {
		limits.WAVBytes = DefaultWAVBytes
	}
	return limits
}

// Diagnostic is the host-neutral form of a session diagnostic record.
type Diagnostic struct {
	Event  string
	Fields map[string]string
}

// CloneStringMap returns a detached copy suitable for asynchronous evidence.
func CloneStringMap(fields map[string]string) map[string]string {
	if fields == nil {
		return nil
	}
	copyOf := make(map[string]string, len(fields))
	for key, value := range fields {
		copyOf[key] = value
	}
	return copyOf
}

// WriteAll preserves partial-write accounting and rejects a writer that makes no progress.
func WriteAll(writer io.Writer, data []byte) (int, error) {
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

// WriteJSONLine validates and writes one newline-delimited JSON value.
func WriteJSONLine(writer io.Writer, data []byte) error {
	if !json.Valid(data) {
		return errors.New("self-play JSONL record is not valid JSON")
	}
	line := append(append([]byte(nil), data...), '\n')
	_, err := WriteAll(writer, line)
	return err
}

// RedactError removes explicit credential values and common credential
// markers from operator-facing artifact text.
func RedactError(value, secret string) string {
	if value == "" {
		return ""
	}
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	for _, marker := range selfPlayCredentialMarkers() {
		value = redactSelfPlayMarker(value, marker)
	}
	return value
}

func selfPlayCredentialMarkers() []string {
	return []string{"authorization: bearer ", "authorization=bearer ", "authorization: ", "authorization=", "x-api-key: ", "x-api-key=", "api-key: ", "api-key=", "api_key: ", "api_key=", "bearer "}
}

func redactSelfPlayMarker(value, marker string) string {
	for {
		lower := strings.ToLower(value)
		start := strings.Index(lower, marker)
		if start < 0 {
			return value
		}
		markerEnd := start + len(marker)
		if strings.HasPrefix(value[markerEnd:], "[REDACTED]") {
			return value
		}
		end := credentialTokenEnd(value, markerEnd)
		value = value[:markerEnd] + "[REDACTED]" + value[end:]
	}
}

func credentialTokenEnd(value string, start int) int {
	for end := start; end < len(value); end++ {
		switch value[end] {
		case ' ', '\t', '\r', '\n', ',', ';', ')', ']', '}':
			return end
		}
	}
	return len(value)
}

// JSONLWriter is a bounded, exclusive, synchronized JSONL sink.
type JSONLWriter struct {
	path   string
	file   *os.File
	limits EvidenceLimits
	mu     sync.Mutex
	items  int64
	bytes  int64
	closed bool
	err    error
}

func NewJSONLWriter(path string, limits EvidenceLimits) (*JSONLWriter, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &JSONLWriter{path: path, file: file, limits: normalizedLimits(limits)}, nil
}

func (w *JSONLWriter) Write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal JSONL record: %w", err)
	}
	return w.WriteRaw(data)
}

func (w *JSONLWriter) WriteRaw(data []byte) error {
	if w == nil {
		return ErrEvidenceClosed
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
		w.err = fmt.Errorf("%w: %s", ErrEvidenceQuota, w.path)
		return w.err
	}
	if _, err := WriteAll(w.file, line); err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
		return w.err
	}
	w.items++
	w.bytes += int64(len(line))
	return nil
}

func (w *JSONLWriter) writerErr() error {
	if w.err != nil {
		return w.err
	}
	return ErrEvidenceClosed
}

func (w *JSONLWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.file != nil {
		if err := w.file.Sync(); err != nil {
			w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, err))
		}
		if err := w.file.Close(); err != nil {
			w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, err))
		}
	}
	return w.err
}

// WAVWriter appends bounded PCM16 data and rewrites its RIFF header at close.
type WAVWriter struct {
	path       string
	sampleRate int
	file       *os.File
	limits     EvidenceLimits
	mu         sync.Mutex
	dataBytes  int64
	closed     bool
	err        error
}

func NewWAVWriter(path string, sampleRate int, limits EvidenceLimits) (*WAVWriter, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("WAV sample rate must be positive, got %d", sampleRate)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	header, err := wavio.PCM16Header(sampleRate, 0)
	if err == nil {
		_, err = WriteAll(file, header[:])
	}
	if err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("write WAV header: %w", err)
	}
	return &WAVWriter{path: path, sampleRate: sampleRate, file: file, limits: normalizedLimits(limits)}, nil
}

func (w *WAVWriter) Write(ctx context.Context, pcm []byte) error {
	if w == nil {
		return ErrEvidenceClosed
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
		w.err = fmt.Errorf("%w: %s", ErrEvidenceQuota, w.path)
		return w.err
	}
	written, err := WriteAll(w.file, pcm)
	w.dataBytes += int64(written)
	if err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
	}
	return w.err
}

func (w *WAVWriter) writerErr() error {
	if w.err != nil {
		return w.err
	}
	return ErrEvidenceClosed
}

func (w *WAVWriter) DataBytes() int64 {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dataBytes
}

func (w *WAVWriter) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

func (w *WAVWriter) Close() error {
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
	} else if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("seek %s for WAV header: %w", w.path, err))
	} else if _, err := WriteAll(w.file, header[:]); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("finalize %s WAV header: %w", w.path, err))
	}
	if err := w.file.Sync(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, err))
	}
	if err := w.file.Close(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, err))
	}
	return w.err
}

// WriteAtomicJSON publishes a bounded JSON artifact through a temporary file and rename.
func WriteAtomicJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON artifact: %w", err)
	}
	data = append(data, '\n')
	if int64(len(data)) > 256<<10 {
		return fmt.Errorf("%w: %s", ErrEvidenceQuota, path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".self-play-artifact-*.tmp")
	if err != nil {
		return fmt.Errorf("create JSON artifact temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := WriteAll(tmp, data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write JSON artifact temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync JSON artifact temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close JSON artifact temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace JSON artifact: %w", err)
	}
	remove = false
	return nil
}
