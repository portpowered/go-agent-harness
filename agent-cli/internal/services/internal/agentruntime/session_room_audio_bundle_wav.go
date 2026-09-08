package agentruntime

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"strconv"
	"strings"
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
