package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

type roomLifecycleWorkError struct {
	outstanding []string
}

func (e *roomLifecycleWorkError) Error() string {
	if e == nil || len(e.outstanding) == 0 {
		return "room lifecycle work did not complete"
	}
	return "room lifecycle work did not complete: " + strings.Join(e.outstanding, "; ")
}

func newRoomLifecycleWorkError(outstanding ...string) error {
	seen := make(map[string]struct{}, len(outstanding))
	ordered := make([]string, 0, len(outstanding))
	for _, item := range outstanding {
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		ordered = append(ordered, item)
	}
	if len(ordered) == 0 {
		return nil
	}
	sort.Strings(ordered)
	return &roomLifecycleWorkError{outstanding: ordered}
}

func roomLifecycleWorkLabel(participantID, phase string) string {
	if participantID == "" {
		return phase
	}
	return fmt.Sprintf("participant %q phase %s", participantID, phase)
}

func roomParticipantFailure(participantID string, err error, secrets []string) error {
	if err == nil {
		err = errors.New("unknown room participant failure")
	}
	return &roomSafeError{
		prefix:        fmt.Sprintf("room participant %q", participantID),
		participantID: participantID,
		cause:         err,
		secrets:       append([]string(nil), secrets...),
	}
}

// roomParticipantFailureReason returns the credential-free cause carried by a
// participant_failed room event. roomSafeError deliberately keeps the
// participant identity in its outer message for command/result diagnostics;
// the event already carries that identity separately, so publish only its
// sanitized local cause here.
func roomParticipantFailureReason(err error, terminationReason ParticipantTerminationReason, closeReason string, transportEnded bool, secrets []string) string {
	if cause := roomParticipantFailureCause(err, secrets); cause != "" {
		return cause
	}
	if closeReason = strings.TrimSpace(closeReason); closeReason != "" {
		if reason := strings.TrimSpace(sanitizeRoomError(errors.New(closeReason), secrets)); reason != "" {
			return reason
		}
	}
	if transportEnded {
		return "transport disconnected"
	}
	switch terminationReason {
	case ParticipantTerminationDisconnected:
		return "participant disconnected"
	case ParticipantTerminationError:
		return "participant failure"
	default:
		return "participant failure"
	}
}

func roomParticipantFailureCause(err error, secrets []string) string {
	if err == nil {
		return ""
	}
	var safe *roomSafeError
	if errors.As(err, &safe) && safe != nil {
		secrets = append(append([]string(nil), secrets...), safe.secrets...)
		err = safe.cause
	}
	if err == nil {
		return ""
	}
	return strings.TrimSpace(sanitizeRoomError(err, secrets))
}

func roomFailureResult(err error, secrets []string) RoomResult {
	return RoomResult{
		TerminationReason: RoomTerminationFailed,
		Reason:            RoomTerminationFailed,
		Error:             sanitizeRoomError(err, secrets),
		Participants:      make(map[string]RoomParticipantResult),
	}
}

type roomSafeError struct {
	prefix        string
	participantID string
	cause         error
	secrets       []string
}

func (e *roomSafeError) Error() string {
	if e == nil {
		return "room failure"
	}
	if e.cause == nil {
		return e.prefix
	}
	return e.prefix + ": " + sanitizeRoomError(e.cause, e.secrets)
}

func (e *roomSafeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func sanitizeRoomError(err error, secrets []string) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return redactRoomError(value, "")
}

func secretsForPlan(plan *roomParticipantPlan) []string {
	if plan == nil || plan.secret == "" {
		return nil
	}
	return []string{plan.secret}
}

// roomDiagnosticLine is the room evidence JSONL shape shared by per-session
// diagnostics and stream records.
type roomDiagnosticLine struct {
	Event  string            `json:"event"`
	Fields map[string]string `json:"fields,omitempty"`
}

const roomEvidenceFileMode = 0o600

type roomWAVRecorder struct {
	path       string
	sampleRate int
	file       *os.File

	mu        sync.Mutex
	dataBytes uint64
	closed    bool
	err       error
}

func newRoomWAVRecorder(path string, sampleRate int) (*roomWAVRecorder, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("WAV sample rate must be positive, got %d", sampleRate)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, roomEvidenceFileMode)
	if err != nil {
		return nil, err
	}
	recorder := &roomWAVRecorder{path: path, sampleRate: sampleRate, file: file}
	header, err := wavio.PCM16Header(sampleRate, 0)
	if err == nil {
		_, err = writeRoomEvidenceAllCount(file, header[:])
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("write WAV header: %w", err), file.Close(), os.Remove(path))
	}
	return recorder, nil
}

func (w *roomWAVRecorder) write(ctx context.Context, pcm []byte) error {
	if w == nil {
		return errors.New("room WAV recorder is nil")
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
		if w.err != nil {
			return w.err
		}
		return errors.New("room WAV recorder is closed")
	}
	if w.err != nil {
		return w.err
	}
	written, err := writeRoomEvidenceAllCount(w.file, pcm)
	w.dataBytes += uint64(written)
	if err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
	}
	return w.err
}

func (w *roomWAVRecorder) close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true

	if header, headerErr := wavio.PCM16Header(w.sampleRate, w.dataBytes); headerErr != nil {
		w.err = errors.Join(w.err, headerErr)
	} else if _, writeErr := w.file.Seek(0, io.SeekStart); writeErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("seek %s for WAV header: %w", w.path, writeErr))
	} else if _, writeErr := writeRoomEvidenceAllCount(w.file, header[:]); writeErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("finalize %s WAV header: %w", w.path, writeErr))
	}
	if syncErr := w.file.Sync(); syncErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, syncErr))
	}
	if closeErr := w.file.Close(); closeErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, closeErr))
	}
	return w.err
}

type roomEvidenceJSONLWriter struct {
	path string
	file *os.File

	mu     sync.Mutex
	closed bool
	err    error
}

func newRoomEvidenceJSONLWriter(path string) (*roomEvidenceJSONLWriter, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, roomEvidenceFileMode)
	if err != nil {
		return nil, err
	}
	return &roomEvidenceJSONLWriter{path: path, file: file}, nil
}

func (w *roomEvidenceJSONLWriter) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal room JSONL record: %w", err)
	}
	return w.writeRaw(data)
}

func (w *roomEvidenceJSONLWriter) writeRaw(data []byte) error {
	if w == nil {
		return errors.New("room JSONL writer is nil")
	}
	if !json.Valid(data) {
		return errors.New("room JSONL record is not valid JSON")
	}
	line := make([]byte, 0, len(data)+1)
	line = append(line, data...)
	line = append(line, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		if w.err != nil {
			return w.err
		}
		return errors.New("room JSONL writer is closed")
	}
	if w.err != nil {
		return w.err
	}
	if err := writeRoomEvidenceAll(w.file, line); err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
		return w.err
	}
	return nil
}

func (w *roomEvidenceJSONLWriter) close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.err
	}
	w.closed = true
	if syncErr := w.file.Sync(); syncErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, syncErr))
	}
	if closeErr := w.file.Close(); closeErr != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, closeErr))
	}
	return w.err
}

func redactRoomError(value, secret string) string {
	if value == "" {
		return ""
	}
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return redactRoomMarkedTokens(value, []string{
		"authorization: bearer ", "authorization=bearer ", "authorization: ",
		"authorization=", "x-api-key: ", "x-api-key=", "api-key: ",
		"api-key=", "api_key: ", "api_key=", "bearer ",
	})
}

func redactRoomMarkedTokens(value string, markers []string) string {
	for _, marker := range markers {
		value = redactRoomMarker(value, marker)
	}
	return value
}

func redactRoomMarker(value, marker string) string {
	searchFrom := 0
	for searchFrom < len(value) {
		start := strings.Index(strings.ToLower(value[searchFrom:]), marker)
		if start < 0 {
			return value
		}
		markerEnd := searchFrom + start + len(marker)
		if strings.HasPrefix(value[markerEnd:], "[REDACTED]") {
			searchFrom = markerEnd + len("[REDACTED]")
			continue
		}
		value = replaceRoomSecret(value, markerEnd, roomTokenEnd(value, markerEnd))
		searchFrom = markerEnd + len("[REDACTED]")
	}
	return value
}

func roomTokenEnd(value string, start int) int {
	for end := start; end < len(value); end++ {
		if roomTokenBoundary(value[end]) {
			return end
		}
	}
	return len(value)
}

func roomTokenBoundary(character byte) bool {
	switch character {
	case ' ', '\t', '\r', '\n', ',', ';', ')', ']', '}':
		return true
	default:
		return false
	}
}

func replaceRoomSecret(value string, start, end int) string {
	return value[:start] + "[REDACTED]" + value[end:]
}
