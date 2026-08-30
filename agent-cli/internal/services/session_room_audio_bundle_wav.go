package services

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"encoding/binary"
	"encoding/json"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
)

func roomReplaySampleDuration(samples, sampleRate int) time.Duration {
	if samples <= 0 || sampleRate <= 0 {
		return 0
	}
	seconds := int64(samples) / int64(sampleRate)
	remainder := int64(samples) % int64(sampleRate)
	return time.Duration(seconds)*time.Second + time.Duration(remainder)*time.Second/time.Duration(sampleRate)
}

func cloneTimedStream(stream audio.PCM16TimedStream) audio.PCM16TimedStream {
	stream.Samples = append([]int16(nil), stream.Samples...)
	stream.ExpectedSpeech = append([]audio.SpeechAnnotation(nil), stream.ExpectedSpeech...)
	stream.ChunkBoundaries = append([]audio.ChunkBoundary(nil), stream.ChunkBoundaries...)
	return stream
}

type roomReplayWAVPayload struct {
	SampleRate int
	Channels   int
	Bits       int
	PCM        []byte
}

func decodeRoomReplayWAV(data []byte, artifact string) (roomReplayWAVPayload, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return roomReplayWAVPayload{}, roomReplayAudioMismatch("artifact.wav", artifact, "RIFF/WAVE container", "invalid header", nil)
	}
	riffSize := uint64(binary.LittleEndian.Uint32(data[4:8]))
	if riffSize < 4 || riffSize+8 > uint64(len(data)) {
		return roomReplayWAVPayload{}, roomReplayAudioIncomplete("artifact.wav", artifact, "complete RIFF payload", fmt.Sprintf("declared %d bytes in %d", riffSize+8, len(data)), io.ErrUnexpectedEOF)
	}
	end := int(riffSize + 8)
	position := 12
	var sampleRate, channels, bits, blockAlign, byteRate, formatCode int
	var pcm []byte
	for position < end {
		if end-position < 8 {
			return roomReplayWAVPayload{}, roomReplayAudioMismatch("artifact.wav.chunk", artifact, "complete chunk header", "truncated", nil)
		}
		chunkID := string(data[position : position+4])
		chunkSize := uint64(binary.LittleEndian.Uint32(data[position+4 : position+8]))
		position += 8
		if chunkSize > uint64(end-position) {
			return roomReplayWAVPayload{}, roomReplayAudioIncomplete("artifact.wav."+chunkID, artifact, "chunk inside RIFF payload", "truncated", io.ErrUnexpectedEOF)
		}
		chunkEnd := position + int(chunkSize)
		switch chunkID {
		case "fmt ":
			if sampleRate != 0 || chunkSize < 16 {
				return roomReplayWAVPayload{}, roomReplayAudioMismatch("artifact.wav.fmt", artifact, "one PCM fmt chunk of at least 16 bytes", fmt.Sprintf("size=%d", chunkSize), nil)
			}
			formatCode = int(binary.LittleEndian.Uint16(data[position : position+2]))
			channels = int(binary.LittleEndian.Uint16(data[position+2 : position+4]))
			sampleRate = int(binary.LittleEndian.Uint32(data[position+4 : position+8]))
			byteRate = int(binary.LittleEndian.Uint32(data[position+8 : position+12]))
			blockAlign = int(binary.LittleEndian.Uint16(data[position+12 : position+14]))
			bits = int(binary.LittleEndian.Uint16(data[position+14 : position+16]))
		case "data":
			if pcm != nil {
				return roomReplayWAVPayload{}, roomReplayAudioMismatch("artifact.wav.data", artifact, "one data chunk", "duplicate", nil)
			}
			pcm = append([]byte(nil), data[position:chunkEnd]...)
		}
		position = chunkEnd
		if chunkSize%2 == 1 {
			if position >= end {
				return roomReplayWAVPayload{}, roomReplayAudioIncomplete("artifact.wav."+chunkID, artifact, "odd chunk padding byte", "missing", io.ErrUnexpectedEOF)
			}
			position++
		}
	}
	if sampleRate == 0 || pcm == nil {
		return roomReplayWAVPayload{}, roomReplayAudioIncomplete("artifact.wav", artifact, "fmt and data chunks", "missing", ErrRoomReplayBundleIncomplete)
	}
	if formatCode != 1 || channels <= 0 || bits != 16 || blockAlign != channels*2 || byteRate != sampleRate*blockAlign || len(pcm)%(channels*2) != 0 {
		return roomReplayWAVPayload{}, roomReplayAudioMismatch("artifact.wav.fmt", artifact, "PCM16 little-endian format", fmt.Sprintf("code=%d rate=%d channels=%d bits=%d block=%d byte_rate=%d", formatCode, sampleRate, channels, bits, blockAlign, byteRate), nil)
	}
	return roomReplayWAVPayload{SampleRate: sampleRate, Channels: channels, Bits: bits, PCM: pcm}, nil
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
