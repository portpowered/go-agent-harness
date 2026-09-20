package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func (w *wavRecorder) write(ctx context.Context, pcm []byte) error {
	if w == nil {
		return errors.New("self-play WAV recorder is unavailable")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
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
		return errors.New("self-play WAV recorder is closed")
	}
	if w.err != nil {
		return w.err
	}
	if w.dataBytes+int64(len(pcm)) > w.limit {
		w.err = fmt.Errorf("%w: %s exceeds %d PCM bytes", selfplay.ErrArtifactLimit, w.path, w.limit)
		return w.err
	}
	n, err := writeAll(w.file, pcm)
	w.dataBytes += int64(n)
	if err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
	}
	return w.err
}

func (w *wavRecorder) close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	header, headerErr := wavio.PCM16Header(selfPlaySampleRate, uint64(w.dataBytes))
	if headerErr != nil {
		w.err = errors.Join(w.err, headerErr)
	} else if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("seek %s WAV header: %w", w.path, err))
	} else if _, err := writeAll(w.file, header[:]); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("finalize %s WAV header: %w", w.path, err))
	}
	w.err = errors.Join(w.err, w.file.Sync(), w.file.Close())
	return w.err
}

func (w *jsonlRecorder) write(value any, secret string) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal JSONL record: %w", err)
	}
	return w.writeRaw([]byte(redactError(string(data), secret)))
}

func (w *jsonlRecorder) writeRaw(data []byte) error {
	if w == nil {
		return errors.New("self-play JSONL recorder is unavailable")
	}
	if !json.Valid(data) {
		return errors.New("self-play JSONL record is not valid JSON")
	}
	line := append(append([]byte(nil), data...), '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("self-play JSONL recorder is closed")
	}
	if w.err != nil {
		return w.err
	}
	if w.bytes+int64(len(line)) > w.limit {
		w.err = fmt.Errorf("%w: %s exceeds %d bytes", selfplay.ErrArtifactLimit, w.path, w.limit)
		return w.err
	}
	n, err := writeAll(w.file, line)
	w.bytes += int64(n)
	if err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
	}
	return w.err
}

func (w *jsonlRecorder) close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	w.err = errors.Join(w.err, w.file.Sync(), w.file.Close())
	return w.err
}

func writeAll(writer io.Writer, data []byte) (int, error) {
	total := 0
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written < 0 || written > len(data) {
			return total, fmt.Errorf("%w: writer returned invalid count %d", io.ErrShortWrite, written)
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

func redactError(value, secret string) string {
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return redactMarkedTokens(value, []string{"authorization: bearer ", "authorization=bearer ", "authorization: ", "authorization=", "x-api-key: ", "x-api-key=", "api-key: ", "api-key=", "api_key: ", "api_key=", "bearer "})
}

func redactMarkedTokens(value string, markers []string) string {
	for _, marker := range markers {
		value = redactMarker(value, marker)
	}
	return value
}

func redactMarker(value, marker string) string {
	searchFrom := 0
	for searchFrom < len(value) {
		start := strings.Index(strings.ToLower(value[searchFrom:]), marker)
		if start < 0 {
			return value
		}
		valueStart := searchFrom + start + len(marker)
		if strings.HasPrefix(value[valueStart:], "[REDACTED]") {
			searchFrom = valueStart + len("[REDACTED]")
			continue
		}
		value = replaceRedactedToken(value, valueStart, tokenEnd(value, valueStart))
		searchFrom = valueStart + len("[REDACTED]")
	}
	return value
}

func tokenEnd(value string, start int) int {
	for end := start; end < len(value); end++ {
		if tokenBoundary(value[end]) {
			return end
		}
	}
	return len(value)
}

func tokenBoundary(character byte) bool {
	switch character {
	case ' ', '\t', '\r', '\n', ',', ';', ')', ']', '}':
		return true
	default:
		return false
	}
}

func replaceRedactedToken(value string, start, end int) string {
	return value[:start] + "[REDACTED]" + value[end:]
}

func redactJSONValue(value any, secret string) any {
	switch current := value.(type) {
	case string:
		return redactError(current, secret)
	case []any:
		for index := range current {
			current[index] = redactJSONValue(current[index], secret)
		}
		return current
	case map[string]any:
		for key, nested := range current {
			current[key] = redactJSONValue(nested, secret)
		}
		return current
	default:
		return value
	}
}

type diagnosticRecord struct {
	Event      string `json:"event"`
	Role       string `json:"role,omitempty"`
	ResponseID string `json:"response_id,omitempty"`
	Turn       int    `json:"turn,omitempty"`
	Error      string `json:"error,omitempty"`
}

func streamMessageError(message messages.StreamMessage) string {
	if message.Value == nil {
		return "stream error"
	}
	encoded, err := json.Marshal(message.Value)
	if err != nil {
		return "stream error"
	}
	return string(encoded)
}
