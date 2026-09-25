package audiobundle

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
	streamanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"os"
	"strconv"
	"strings"
	"time"
)

type roomReplayAudioStreamMetadata struct {
	StreamID        string
	TimelineStart   time.Duration
	TimelineEnd     time.Duration
	HasStart        bool
	HasEnd          bool
	Duration        time.Duration
	HasDuration     bool
	ChunkBoundaries []streamanalysis.ChunkBoundary
	ExpectedSpeech  []streamanalysis.SpeechAnnotation
}

func roomReplayParticipantArtifact(participant RoomReplayParticipant, role string) (RoomReplayArtifact, bool) {
	for _, artifact := range participant.Artifacts {
		if artifact.Role == role || artifact.Name == role {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}

func loadRoomReplayJSONL(artifact RoomReplayArtifact, field string) ([]json.RawMessage, error) {
	data, err := os.ReadFile(artifact.AbsolutePath)
	if err != nil {
		return nil, roomReplayAudioIncomplete(field, artifact.Path, "readable JSONL", err.Error(), err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, roomReplayJSONLInitialBufferBytes), roomReplayJSONLMaxTokenBytes)
	lines := make([]json.RawMessage, 0)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("%s.line[%d]", field, lineNumber), artifact.Path, "valid JSON object", "invalid JSON", nil)
		}
		object, err := roomReplayObject(line)
		if err != nil {
			return nil, roomReplayAudioMismatch(fmt.Sprintf("%s.line[%d]", field, lineNumber), artifact.Path, "JSON object", "non-object", err)
		}
		_ = object
		lines = append(lines, append(json.RawMessage(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return nil, roomReplayAudioMismatch(field, artifact.Path, "readable JSONL", err.Error(), err)
	}
	if len(lines) == 0 {
		return nil, roomReplayAudioIncomplete(field, artifact.Path, "at least one JSONL record", "empty", ErrRoomReplayBundleIncomplete)
	}
	return lines, nil
}

func loadRoomReplayWAVStream(plan RoomReplayPlan, artifact RoomReplayArtifact, streamID, participantID, role string) (RoomReplayAudioStream, error) {
	data, err := os.ReadFile(artifact.AbsolutePath)
	if err != nil {
		return RoomReplayAudioStream{}, roomReplayAudioIncomplete("artifact."+role, artifact.Path, "readable WAV", err.Error(), err)
	}
	wav, err := decodeRoomReplayWAV(data, artifact.Path)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	if err := validateRoomReplayWAVFormat(wav, plan.PCMFormat, artifact.Path); err != nil {
		return RoomReplayAudioStream{}, err
	}
	if len(wav.PCM) == 0 {
		return RoomReplayAudioStream{}, roomReplayAudioIncomplete("artifact."+role, artifact.Path, "non-empty WAV PCM payload", "empty", ErrRoomReplayBundleIncomplete)
	}
	samples, err := decodeMonoPCM16(wav.PCM, wav.Channels, artifact.Path)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	stream := RoomReplayAudioStream{
		PCM16TimedStream: roomanalysis.PCM16TimedStream{PCM16Input: roomanalysis.PCM16Input{StreamID: streamID, ParticipantID: participantID, SampleRate: wav.SampleRate, Samples: samples}},
		Role:             role,
		PCM:              append([]byte(nil), wav.PCM...),
		SampleCount:      len(samples),
		Artifact:         artifact,
	}
	stream.TimelineStart = 0
	stream.TimelineEnd = roomReplaySampleDuration(len(samples), wav.SampleRate)
	return stream, nil
}

func loadRoomReplayPCMStream(plan RoomReplayPlan, artifact RoomReplayArtifact, streamID, participantID, role string) (RoomReplayAudioStream, error) {
	data, err := os.ReadFile(artifact.AbsolutePath)
	if err != nil {
		return RoomReplayAudioStream{}, roomReplayAudioIncomplete("artifact."+role, artifact.Path, "readable PCM", err.Error(), err)
	}
	if len(data) == 0 {
		return RoomReplayAudioStream{}, roomReplayAudioIncomplete("artifact."+role, artifact.Path, "non-empty PCM payload", "empty", ErrRoomReplayBundleIncomplete)
	}
	pcm := data
	rate := plan.PCMFormat.SampleRate
	channels := plan.PCMFormat.Channels
	if bytes.HasPrefix(data, []byte("RIFF")) {
		wav, err := decodeRoomReplayWAV(data, artifact.Path)
		if err != nil {
			return RoomReplayAudioStream{}, err
		}
		if err := validateRoomReplayWAVFormat(wav, plan.PCMFormat, artifact.Path); err != nil {
			return RoomReplayAudioStream{}, err
		}
		pcm = wav.PCM
		rate = wav.SampleRate
		channels = wav.Channels
	}
	if len(pcm)%(2*channels) != 0 {
		return RoomReplayAudioStream{}, roomReplayAudioMismatch("artifact."+role, artifact.Path, "PCM16 frame-aligned payload", fmt.Sprintf("%d bytes", len(pcm)), nil)
	}
	samples, err := decodeMonoPCM16(pcm, channels, artifact.Path)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	stream := RoomReplayAudioStream{
		PCM16TimedStream: roomanalysis.PCM16TimedStream{PCM16Input: roomanalysis.PCM16Input{StreamID: streamID, ParticipantID: participantID, SampleRate: rate, Samples: samples}},
		Role:             role,
		PCM:              append([]byte(nil), pcm...),
		SampleCount:      len(samples),
		Artifact:         artifact,
	}
	stream.TimelineStart = 0
	stream.TimelineEnd = roomReplaySampleDuration(len(samples), rate)
	return stream, nil
}

func decodeMonoPCM16(data []byte, channels int, artifact string) ([]int16, error) {
	if channels != 1 {
		return nil, roomReplayAudioMismatch("pcm_format.channels", artifact, "1 channel for audio analysis", strconv.Itoa(channels), nil)
	}
	if len(data) == 0 {
		return nil, nil
	}
	if len(data)%2 != 0 {
		return nil, roomReplayAudioMismatch("pcm_format.sample_width_bits", artifact, "even PCM16 byte count", strconv.Itoa(len(data)), nil)
	}
	// A room artifact is an aggregate file, so its bound is owned by the room
	// bundle reader rather than the per-provider payload default.
	samples, err := codec.DecodePCM16WithLimit(data, len(data))
	if err != nil {
		return nil, roomReplayAudioMismatch("pcm_format.sample_width_bits", artifact, "even PCM16 byte count", strconv.Itoa(len(data)), err)
	}
	return samples, nil
}

func applyRoomReplayStreamMetadata(stream *RoomReplayAudioStream, metadata roomReplayAudioStreamMetadata) {
	if stream == nil {
		return
	}
	if metadata.StreamID != "" {
		stream.StreamID = metadata.StreamID
	}
	if metadata.HasStart {
		stream.TimelineStart = metadata.TimelineStart
	}
	if metadata.HasEnd {
		stream.TimelineEnd = metadata.TimelineEnd
	} else if metadata.HasDuration {
		stream.TimelineEnd = stream.TimelineStart + metadata.Duration
	}
	if len(metadata.ChunkBoundaries) > 0 {
		stream.ChunkBoundaries = append(stream.ChunkBoundaries[:0], metadata.ChunkBoundaries...)
	}
	if len(metadata.ExpectedSpeech) > 0 {
		stream.ExpectedSpeech = append(stream.ExpectedSpeech[:0], metadata.ExpectedSpeech...)
	}
}

func validateRoomReplayDeltaStream(stream RoomReplayAudioStream, plan RoomReplayPlan, participantID string) error {
	if len(stream.Deltas) == 0 {
		return &RoomReplayDeltaReconstructionError{ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: "missing", DeltaIndex: 0, ByteOffset: 0, ExpectedLength: len(stream.PCM), ActualLength: 0, ExpectedSampleCount: stream.SampleCount, ActualSampleCount: 0, Cause: ErrRoomReplayDeltaReconstruction}
	}
	previousOffset := time.Duration(-1)
	for _, delta := range stream.Deltas {
		if delta.HasOffset {
			if delta.Offset < 0 || delta.Offset > plan.EndedAt.Sub(plan.ClockBase) {
				return roomReplayAudioTimeline(fmt.Sprintf("participants[%s].deltas.line[%d].offset", participantID, delta.LineNumber), stream.DeltaArtifact.Path, "offset within declared room duration", delta.Offset.String())
			}
			if previousOffset >= 0 && delta.Offset < previousOffset {
				return roomReplayAudioTimeline(fmt.Sprintf("participants[%s].deltas.line[%d].offset", participantID, delta.LineNumber), stream.DeltaArtifact.Path, "monotonic delta timestamps", delta.Offset.String())
			}
			previousOffset = delta.Offset
		}
	}
	return validateRoomReplayAudioStreamTimeline(stream, plan, "participants["+participantID+"].wav")
}

func reconstructRoomReplayDeltaStream(stream RoomReplayAudioStream, participantID string) error {
	position := 0
	actualLength := 0
	for index, delta := range stream.Deltas {
		if position < len(stream.PCM) {
			shared := len(delta.PCM)
			if remaining := len(stream.PCM) - position; shared > remaining {
				shared = remaining
			}
			for offset := 0; offset < shared; offset++ {
				if delta.PCM[offset] != stream.PCM[position+offset] {
					return &RoomReplayDeltaReconstructionError{
						ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: delta.ID, DeltaIndex: index, ByteOffset: position + offset,
						ExpectedByte: int(stream.PCM[position+offset]), ActualByte: int(delta.PCM[offset]),
						ExpectedLength: len(stream.PCM), ActualLength: actualLength + len(delta.PCM),
						ExpectedSampleCount: stream.SampleCount, ActualSampleCount: (actualLength + len(delta.PCM)) / 2, Cause: ErrRoomReplayDeltaReconstruction,
					}
				}
			}
		}
		if position+len(delta.PCM) > len(stream.PCM) {
			return &RoomReplayDeltaReconstructionError{
				ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: delta.ID, DeltaIndex: index, ByteOffset: len(stream.PCM),
				ExpectedByte: -1, ActualByte: int(delta.PCM[len(stream.PCM)-position]), ExpectedLength: len(stream.PCM), ActualLength: actualLength + len(delta.PCM),
				ExpectedSampleCount: stream.SampleCount, ActualSampleCount: (actualLength + len(delta.PCM)) / 2, Cause: ErrRoomReplayDeltaReconstruction,
			}
		}
		position += len(delta.PCM)
		actualLength = position
	}
	if actualLength != len(stream.PCM) {
		deltaID := "missing"
		if len(stream.Deltas) > 0 {
			deltaID = "after-" + stream.Deltas[len(stream.Deltas)-1].ID
		}
		return &RoomReplayDeltaReconstructionError{
			ParticipantID: participantID, StreamID: stream.StreamID, DeltaID: deltaID, DeltaIndex: len(stream.Deltas), ByteOffset: actualLength,
			ExpectedByte: int(stream.PCM[actualLength]), ActualByte: -1, ExpectedLength: len(stream.PCM), ActualLength: actualLength,
			ExpectedSampleCount: stream.SampleCount, ActualSampleCount: actualLength / 2, Cause: ErrRoomReplayDeltaReconstruction,
		}
	}
	return nil
}

func decodeRoomReplayAudioRaw(raw json.RawMessage) ([]byte, bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false, nil
	}
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		if encoded == "" {
			return []byte{}, true, nil
		}
		decoded, err := codec.DecodeLegacyBase64(encoded)
		if err != nil {
			return nil, true, err
		}
		return decoded, true, nil
	}
	var numbers []int
	if json.Unmarshal(raw, &numbers) == nil {
		payload := make([]byte, len(numbers))
		for index, value := range numbers {
			if value < 0 || value > 255 {
				return nil, true, fmt.Errorf("audio byte %d is outside 0..255", value)
			}
			payload[index] = byte(value)
		}
		return payload, true, nil
	}
	if object, err := roomReplayObject(raw); err == nil {
		for _, key := range []string{"base64", "data", "content", "pcm", "delta"} {
			if nested, ok := object[key]; ok {
				return decodeRoomReplayAudioRaw(nested)
			}
		}
	}
	return nil, true, errors.New("audio payload must be base64 string or byte array")
}

func isRoomReplayAudioDeltaKind(kind string) bool {
	normalized := strings.ToLower(strings.NewReplacer(".", "", "_", "", "-", "", " ", "").Replace(strings.TrimSpace(kind)))
	return strings.Contains(normalized, "audio") && (strings.Contains(normalized, "delta") || strings.Contains(normalized, "chunk")) || normalized == "deltaaudio" || normalized == "pcmdelta"
}

func roomReplayFirstIntField(object roomReplayJSONObject, names ...string) (int64, string, bool, error) {
	for _, name := range names {
		value, present, err := roomReplayInt64Field(object, name)
		if present {
			return value, name, true, err
		}
	}
	return 0, "", false, nil
}

func roomReplayInt64Field(object roomReplayJSONObject, name string) (int64, bool, error) {
	raw, ok := object[name]
	if !ok {
		return 0, false, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, true, err
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	return value, true, err
}

func roomReplayAudioOffset(object roomReplayJSONObject) (time.Duration, bool, error) {
	for _, name := range []string{"monotonic_offset_ms", "offset_ms", "timestamp_ms", "offset"} {
		if raw, ok := object[name]; ok {
			value, err := roomReplayDurationValue(raw, true)
			return value, true, err
		}
	}
	return 0, false, nil
}

func parseRoomReplayStreamMetadata(object roomReplayJSONObject, role string) roomReplayAudioStreamMetadata {
	metadata := roomReplayAudioStreamMetadata{}
	if object == nil {
		return metadata
	}
	var candidates []json.RawMessage
	for _, key := range []string{"streams", "audio_streams", "stream_metadata"} {
		if raw, ok := object[key]; ok {
			if nested, err := roomReplayObject(raw); err == nil {
				for _, alias := range roomReplayStreamRoleAliases(role) {
					if value, exists := nested[alias]; exists {
						candidates = append(candidates, value)
					}
				}
			}
		}
	}
	for _, key := range roomReplayStreamRoleAliases(role) {
		if raw, ok := object[key]; ok {
			candidates = append(candidates, raw)
		}
	}
	if raw, ok := object["artifacts"]; ok {
		if nested, err := roomReplayObject(raw); err == nil {
			for _, key := range roomReplayStreamRoleAliases(role) {
				if value, exists := nested[key]; exists {
					candidates = append(candidates, value)
				}
			}
		}
	}
	for _, candidate := range candidates {
		parsed := parseRoomReplayStreamMetadataObject(candidate)
		mergeRoomReplayAudioStreamMetadata(&metadata, parsed)
	}
	return metadata
}

func roomReplayStreamRoleAliases(role string) []string {
	switch role {
	case roomReplayAudioRoleWAV:
		return []string{roomReplayAudioRoleWAV, "output", "audio", "output_stream", "wav_stream"}
	case roomReplayAudioRoleSent:
		return []string{roomReplayAudioRoleSent, "sent_pcm", "sent_stream", "uplink"}
	case roomReplayAudioRoleReceived:
		return []string{roomReplayAudioRoleReceived, "received_pcm", "received_stream", "downlink"}
	default:
		return []string{role}
	}
}

func parseRoomReplayStreamMetadataObject(raw json.RawMessage) roomReplayAudioStreamMetadata {
	metadata := roomReplayAudioStreamMetadata{}
	object, err := roomReplayObject(raw)
	if err != nil {
		return metadata
	}
	metadata.StreamID, _ = optionalRoomReplayStringField(object, nil, "stream_id", "id", "identity")
	if value, present, err := roomReplayFirstDurationField(object, "timeline_start_ms", "start_ms", "start_offset_ms", "timeline_start", "start"); err == nil && present {
		metadata.TimelineStart, metadata.HasStart = value, true
	}
	if value, present, err := roomReplayFirstDurationField(object, "timeline_end_ms", "end_ms", "timeline_end", "end"); err == nil && present {
		metadata.TimelineEnd, metadata.HasEnd = value, true
	}
	if value, present, err := roomReplayFirstDurationField(object, "duration_ms", "duration"); err == nil && present {
		metadata.Duration, metadata.HasDuration = value, true
	}
	if raw, ok := roomReplayFirstRawField(object, "chunk_boundaries", "boundaries", "chunks"); ok {
		metadata.ChunkBoundaries = parseRoomReplayChunkBoundaries(raw)
	}
	if raw, ok := roomReplayFirstRawField(object, "expected_speech", "speech", "speech_regions"); ok {
		metadata.ExpectedSpeech = parseRoomReplaySpeechAnnotations(raw)
	}
	return metadata
}

func mergeRoomReplayAudioStreamMetadata(destination *roomReplayAudioStreamMetadata, source roomReplayAudioStreamMetadata) {
	if destination == nil {
		return
	}
	if destination.StreamID == "" {
		destination.StreamID = source.StreamID
	}
	if !destination.HasStart && source.HasStart {
		destination.TimelineStart, destination.HasStart = source.TimelineStart, true
	}
	if !destination.HasEnd && source.HasEnd {
		destination.TimelineEnd, destination.HasEnd = source.TimelineEnd, true
	}
	if !destination.HasDuration && source.HasDuration {
		destination.Duration, destination.HasDuration = source.Duration, true
	}
	if len(destination.ChunkBoundaries) == 0 {
		destination.ChunkBoundaries = append([]streamanalysis.ChunkBoundary(nil), source.ChunkBoundaries...)
	}
	if len(destination.ExpectedSpeech) == 0 {
		destination.ExpectedSpeech = append([]streamanalysis.SpeechAnnotation(nil), source.ExpectedSpeech...)
	}
}

func mergeRoomReplaySidecarMetadata(metadata map[string]roomReplayAudioStreamMetadata, lines []json.RawMessage) {
	for _, line := range lines {
		object, err := roomReplayObject(line)
		if err != nil {
			continue
		}
		role, _ := optionalRoomReplayStringField(object, nil, "stream_role", "audio_role", "role")
		role = normalizeRoomReplayAudioRole(role)
		if role == "" {
			continue
		}
		parsed := parseRoomReplayStreamMetadataObject(line)
		if parsed.StreamID == "" {
			continue
		}
		current := metadata[role]
		mergeRoomReplayAudioStreamMetadata(&current, parsed)
		metadata[role] = current
	}
}

func normalizeRoomReplayAudioRole(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(normalized)
	switch normalized {
	case "output", roomReplayAudioRoleWAV, "audio", "output_stream", "wav_stream":
		return roomReplayAudioRoleWAV
	case roomReplayAudioRoleSent, "sent_pcm", "sent_stream", "uplink":
		return roomReplayAudioRoleSent
	case roomReplayAudioRoleReceived, "received_pcm", "received_stream", "downlink":
		return roomReplayAudioRoleReceived
	default:
		return ""
	}
}

func roomReplayFirstRawField(object roomReplayJSONObject, names ...string) (json.RawMessage, bool) {
	for _, name := range names {
		if raw, ok := object[name]; ok {
			return raw, true
		}
	}
	return nil, false
}

func roomReplayFirstDurationField(object roomReplayJSONObject, names ...string) (time.Duration, bool, error) {
	for _, name := range names {
		if raw, ok := object[name]; ok {
			value, err := roomReplayDurationValue(raw, strings.HasSuffix(name, "_ms") || name == "offset")
			return value, true, err
		}
	}
	return 0, false, nil
}

func roomReplayDurationValue(raw json.RawMessage, numericIsMilliseconds bool) (time.Duration, error) {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		value = strings.TrimSpace(value)
		if value == "" {
			return 0, errors.New("duration is empty")
		}
		if parsed, err := time.ParseDuration(value); err == nil {
			return parsed, nil
		}
		if numeric, err := strconv.ParseInt(value, 10, 64); err == nil && numericIsMilliseconds {
			return time.Duration(numeric) * time.Millisecond, nil
		}
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return 0, err
	}
	if numericIsMilliseconds {
		value, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(value) * time.Millisecond, nil
	}
	return 0, fmt.Errorf("duration must be a string")
}

func parseRoomReplayChunkBoundaries(raw json.RawMessage) []streamanalysis.ChunkBoundary {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	boundaries := make([]streamanalysis.ChunkBoundary, 0, len(values))
	for index, value := range values {
		object, err := roomReplayObject(value)
		if err != nil {
			continue
		}
		position, _, _, err := roomReplayFirstIntField(object, "sample_index", "end_sample", "offset_samples", "sample")
		if err != nil {
			continue
		}
		id, _ := optionalRoomReplayStringField(object, nil, "id", "chunk_id", "name")
		if strings.TrimSpace(id) == "" {
			id = fmt.Sprintf("chunk-%d", index)
		}
		if position > 0 && position <= int64(^uint(0)>>1) {
			boundaries = append(boundaries, streamanalysis.ChunkBoundary{ID: id, SampleIndex: int(position)})
		}
	}
	return boundaries
}

func parseRoomReplaySpeechAnnotations(raw json.RawMessage) []streamanalysis.SpeechAnnotation {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	annotations := make([]streamanalysis.SpeechAnnotation, 0, len(values))
	for _, value := range values {
		object, err := roomReplayObject(value)
		if err != nil {
			continue
		}
		start, startPresent, startErr := roomReplayFirstDurationField(object, "start_ms", "start_offset_ms", "start")
		end, endPresent, endErr := roomReplayFirstDurationField(object, "end_ms", "end_offset_ms", "end")
		if startErr != nil || endErr != nil || !startPresent || !endPresent || end <= start {
			continue
		}
		label, _ := optionalRoomReplayStringField(object, nil, "label", "id", "name")
		annotations = append(annotations, streamanalysis.SpeechAnnotation{Label: label, Start: start, End: end})
	}
	return annotations
}
