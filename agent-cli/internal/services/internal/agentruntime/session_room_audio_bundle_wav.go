package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"encoding/json"

	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
	streamanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
)

func roomReplaySampleDuration(samples, sampleRate int) time.Duration {
	if samples <= 0 || sampleRate <= 0 {
		return 0
	}
	seconds := int64(samples) / int64(sampleRate)
	remainder := int64(samples) % int64(sampleRate)
	return time.Duration(seconds)*time.Second + time.Duration(remainder)*time.Second/time.Duration(sampleRate)
}

func cloneTimedStream(stream roomanalysis.PCM16TimedStream) roomanalysis.PCM16TimedStream {
	stream.Samples = append([]int16(nil), stream.Samples...)
	stream.ExpectedSpeech = append([]streamanalysis.SpeechAnnotation(nil), stream.ExpectedSpeech...)
	stream.ChunkBoundaries = append([]streamanalysis.ChunkBoundary(nil), stream.ChunkBoundaries...)
	return stream
}

type roomReplayWAVPayload struct {
	SampleRate int
	Channels   int
	Bits       int
	PCM        []byte
}

func decodeRoomReplayWAV(data []byte, artifact string) (roomReplayWAVPayload, error) {
	layout, err := wavio.Inspect(bytes.NewReader(data))
	if err != nil {
		if errors.Is(err, wavio.ErrTruncated) {
			return roomReplayWAVPayload{}, roomReplayAudioIncomplete("artifact.wav", artifact, "complete PCM16 WAV", err.Error(), err)
		}
		return roomReplayWAVPayload{}, roomReplayAudioMismatch("artifact.wav", artifact, "PCM16 WAV", err.Error(), err)
	}
	return roomReplayWAVPayload{SampleRate: layout.SampleRate, Channels: 1, Bits: 16, PCM: append([]byte(nil), data[layout.DataOffset:layout.DataOffset+int64(layout.DataBytes)]...)}, nil
}

func validateRoomReplayWAVFormat(wav roomReplayWAVPayload, declared RoomReplayPCMFormat, artifact string) error {
	width := declared.SampleWidthBits
	if width == 0 {
		width = declared.SampleWidthBit
	}
	if declared.Channels != 1 {
		return roomReplayAudioMismatch("pcm_format.channels", artifact, "1 channel for PCM16 audio analysis", strconv.Itoa(declared.Channels), nil)
	}
	if wav.SampleRate != declared.SampleRate || wav.Channels != declared.Channels || wav.Bits != width || !strings.EqualFold(declared.ByteOrder, "little") {
		return roomReplayAudioMismatch("pcm_format", artifact, fmt.Sprintf("rate=%d channels=%d bits=%d little-endian", declared.SampleRate, declared.Channels, width), fmt.Sprintf("rate=%d channels=%d bits=%d", wav.SampleRate, wav.Channels, wav.Bits), nil)
	}
	return nil
}

func roomReplayAudioParticipantObjects(manifest roomReplayJSONObject) (map[string]roomReplayJSONObject, error) {
	result := make(map[string]roomReplayJSONObject)
	raw, ok := manifest["participants"]
	if !ok {
		return result, roomReplayAudioIncomplete("participants", "run-manifest.json", "participant objects", "missing", ErrRoomReplayBundleIncomplete)
	}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		var values []roomReplayJSONObject
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, roomReplayAudioMismatch("participants", "run-manifest.json", "participant array", "invalid", err)
		}
		for index, object := range values {
			id, _, err := firstRoomReplayStringField(object, nil, "id", "participant_id")
			if err != nil || strings.TrimSpace(id) == "" {
				return nil, roomReplayAudioIncomplete(fmt.Sprintf("participants[%d].id", index), "run-manifest.json", "participant identity", "missing", ErrRoomReplayBundleIncomplete)
			}
			if _, exists := result[id]; exists {
				return nil, roomReplayAudioMismatch("participants.id", "run-manifest.json", "unique participant identity", id, nil)
			}
			result[id] = object
		}
		return result, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, roomReplayAudioMismatch("participants", "run-manifest.json", "participant map", "invalid", err)
	}
	for key, value := range values {
		object, err := roomReplayObject(value)
		if err != nil {
			return nil, roomReplayAudioMismatch("participants["+key+"]", "run-manifest.json", "participant object", "invalid", err)
		}
		id, present, idErr := firstRoomReplayStringField(object, nil, "id", "participant_id")
		if idErr != nil {
			return nil, roomReplayAudioMismatch("participants["+key+"].id", "run-manifest.json", "string participant identity", "invalid", idErr)
		}
		if !present || strings.TrimSpace(id) == "" {
			id = key
		}
		if id != key {
			return nil, roomReplayAudioMismatch("participants["+key+"].id", "run-manifest.json", key, id, nil)
		}
		if _, exists := result[id]; exists {
			return nil, roomReplayAudioMismatch("participants.id", "run-manifest.json", "unique participant identity", id, nil)
		}
		result[id] = object
	}
	return result, nil
}

func roomReplayAudioMismatch(field, artifact, expected, actual string, cause error) error {
	if cause == nil {
		cause = ErrInvalidRoomReplayBundle
	}
	return newRoomReplayBundleError(RoomReplayBundleMismatch, field, artifact, expected, actual, cause)
}

func roomReplayAudioIncomplete(field, artifact, expected, actual string, cause error) error {
	if cause == nil {
		cause = ErrRoomReplayBundleIncomplete
	}
	return newRoomReplayBundleError(RoomReplayBundleIncomplete, field, artifact, expected, actual, cause)
}

func roomReplayAudioTimeline(field, artifact, expected, actual string) error {
	return newRoomReplayBundleError(RoomReplayBundleMismatch, field, artifact, expected, actual, errors.Join(ErrRoomReplayAudioTimeline, ErrInvalidRoomReplayBundle))
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

func writeRoomEvidenceAll(writer io.Writer, data []byte) error {
	_, err := writeRoomEvidenceAllCount(writer, data)
	return err
}

func writeRoomEvidenceAllCount(writer io.Writer, data []byte) (int, error) {
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
