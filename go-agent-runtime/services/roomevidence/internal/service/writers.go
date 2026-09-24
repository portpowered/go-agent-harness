package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"io"
	"os"
	"path/filepath"
	"sync"
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
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal JSONL record: %w", err)
	}
	return w.writeRaw(data)
}

func (w *jsonlWriter) writeRaw(data []byte) error {
	if w == nil {
		return errors.New("room evidence JSONL writer is nil")
	}
	if len(data) > maxJSONLRecordBytes {
		return fmt.Errorf("room evidence JSONL record exceeds %d-byte bound", maxJSONLRecordBytes)
	}
	if !json.Valid(data) {
		return errors.New("room evidence JSONL record is not valid JSON")
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
		return errors.New("room evidence JSONL writer is closed")
	}
	if w.err != nil {
		return w.err
	}
	if err := writeAll(w.file, line); err != nil {
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
	if w.file == nil {
		if w.err == nil {
			w.err = errors.New("room evidence JSONL writer has no file")
		}
		return w.err
	}
	if err := w.file.Sync(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, err))
	}
	if err := w.file.Close(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, err))
	}
	return w.err
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

func (w *pcmWriter) write(pcm []byte) error {
	if w == nil {
		return errors.New("room evidence PCM writer is nil")
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
		return errors.New("room evidence PCM writer is closed")
	}
	if w.err != nil {
		return w.err
	}
	written, err := writeAllCount(w.file, pcm)
	w.bytes += uint64(written)
	if err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
	}
	return w.err
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
	if w.file == nil {
		if w.err == nil {
			w.err = errors.New("room evidence PCM writer has no file")
		}
		return w.err
	}
	if err := w.file.Sync(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("sync %s: %w", w.path, err))
	}
	if err := w.file.Close(); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("close %s: %w", w.path, err))
	}
	return w.err
}

type wavWriter struct {
	path       string
	sampleRate int
	file       *os.File
	mu         sync.Mutex
	dataBytes  uint64
	closed     bool
	err        error
}

func newWAVWriter(path string, sampleRate int) (*wavWriter, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("WAV sample rate must be positive, got %d", sampleRate)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, evidenceFileMode)
	if err != nil {
		return nil, err
	}
	header, err := wavio.PCM16Header(sampleRate, 0)
	if err == nil {
		_, err = writeAllCount(file, header[:])
	}
	if err != nil {
		return nil, fmt.Errorf("write WAV header: %w", errors.Join(err, file.Close(), os.Remove(path)))
	}
	return &wavWriter{path: path, sampleRate: sampleRate, file: file}, nil
}

func (w *wavWriter) write(pcm []byte) error {
	if w == nil {
		return errors.New("room evidence WAV writer is nil")
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
		return errors.New("room evidence WAV writer is closed")
	}
	if w.err != nil {
		return w.err
	}
	written, err := writeAllCount(w.file, pcm)
	w.dataBytes += uint64(written)
	if err != nil {
		w.err = fmt.Errorf("write %s: %w", w.path, err)
	}
	return w.err
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
	if w.file == nil {
		if w.err == nil {
			w.err = errors.New("room evidence WAV writer has no file")
		}
		return w.err
	}
	header, err := wavio.PCM16Header(w.sampleRate, w.dataBytes)
	if err != nil {
		w.err = errors.Join(w.err, err)
	} else if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		w.err = errors.Join(w.err, fmt.Errorf("seek %s for WAV header: %w", w.path, err))
	} else if _, err := writeAllCount(w.file, header[:]); err != nil {
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

func writeAll(writer io.Writer, data []byte) error {
	_, err := writeAllCount(writer, data)
	return err
}

func writeAllCount(writer io.Writer, data []byte) (int, error) {
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

type healthSnapshot struct {
	recordErr       error
	artifactErrs    map[string]error
	participantErrs map[string]error
	secrets         []string
	participants    map[string]roomevidence.ArtifactPaths
}

func (r *recorder) Health() roomevidence.Health {
	if r == nil {
		return roomevidence.Health{}
	}
	return buildHealth(r.snapshotHealth())
}

func (r *recorder) snapshotHealth() healthSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := healthSnapshot{
		recordErr:       r.recordErr,
		artifactErrs:    make(map[string]error, len(r.artifactRecordErr)),
		participantErrs: make(map[string]error, len(r.participantRecordErr)),
		secrets:         cloneStrings(r.secrets),
		participants:    make(map[string]roomevidence.ArtifactPaths, len(r.participants)),
	}
	for path, err := range r.artifactRecordErr {
		snapshot.artifactErrs[path] = err
	}
	for id, err := range r.participantRecordErr {
		snapshot.participantErrs[id] = err
	}
	for id, participant := range r.participants {
		if participant != nil {
			snapshot.participants[id] = participant.artifacts
		}
	}
	return snapshot
}

func buildHealth(snapshot healthSnapshot) roomevidence.Health {
	return roomevidence.Health{
		Status:               recordingStatus(snapshot.recordErr, snapshot.secrets),
		DegradedArtifacts:    sanitizedErrors(snapshot.artifactErrs, snapshot.secrets),
		ParticipantStatuses:  participantStatuses(snapshot.participantErrs, snapshot.secrets),
		ParticipantArtifacts: participantArtifactsHealth(snapshot.participants, snapshot.artifactErrs, snapshot.secrets),
	}
}

func recordingStatus(err error, secrets []string) *transcript.RecordingStatus {
	if err == nil {
		return nil
	}
	return &transcript.RecordingStatus{State: transcript.RecordingStatusPartial, Reason: sanitizedError(err, secrets)}
}

func sanitizedErrors(values map[string]error, secrets []string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for path, err := range values {
		result[path] = sanitizedError(err, secrets)
	}
	return result
}

func participantStatuses(values map[string]error, secrets []string) map[string]*transcript.RecordingStatus {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]*transcript.RecordingStatus, len(values))
	for id, err := range values {
		result[id] = recordingStatus(err, secrets)
	}
	return result
}

func participantArtifactsHealth(participants map[string]roomevidence.ArtifactPaths, artifactErrs map[string]error, secrets []string) map[string]map[string]string {
	if len(participants) == 0 || len(artifactErrs) == 0 {
		return nil
	}
	result := make(map[string]map[string]string)
	for id, paths := range participants {
		for _, path := range participantArtifactList(paths) {
			if err, exists := artifactErrs[filepath.ToSlash(path)]; exists {
				if result[id] == nil {
					result[id] = make(map[string]string)
				}
				result[id][filepath.ToSlash(path)] = sanitizedError(err, secrets)
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func participantArtifactList(paths roomevidence.ArtifactPaths) []string {
	return []string{paths.WAV, paths.Diagnostics, paths.Deltas, paths.SentPCM, paths.ReceivedPCM, paths.Events, paths.Capture}
}

func (r *recorder) applyRecordingHealth(result *rooms.RoomResult) {
	if r == nil || result == nil {
		return
	}
	health := r.Health()
	result.RecordingStatus = cloneStatus(health.Status)
	result.DegradedArtifacts = cloneMap(health.DegradedArtifacts)
	for id, participant := range result.Participants {
		participant.RecordingStatus = cloneStatus(health.ParticipantStatuses[id])
		result.Participants[id] = participant
	}
}
