package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	streamanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
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

func parseRoomReplayStreamMetadata(object roomReplayJSONObject, role string) roomReplayAudioStreamMetadata {
	metadata := roomReplayAudioStreamMetadata{}
	if object == nil {
		return metadata
	}
	candidates := roomReplayNestedStreamMetadataCandidates(object, role)
	candidates = append(candidates, roomReplayDirectStreamMetadataCandidates(object, role)...)
	candidates = append(candidates, roomReplayArtifactStreamMetadataCandidates(object, role)...)
	for _, candidate := range candidates {
		parsed := parseRoomReplayStreamMetadataObject(candidate)
		mergeRoomReplayAudioStreamMetadata(&metadata, parsed)
	}
	return metadata
}

func roomReplayNestedStreamMetadataCandidates(object roomReplayJSONObject, role string) []json.RawMessage {
	candidates := make([]json.RawMessage, 0)
	for _, key := range []string{"streams", "audio_streams", "stream_metadata"} {
		raw, ok := object[key]
		if !ok {
			continue
		}
		nested, err := roomReplayObject(raw)
		if err != nil {
			continue
		}
		candidates = append(candidates, roomReplayRoleMetadataCandidates(nested, role)...)
	}
	return candidates
}

func roomReplayDirectStreamMetadataCandidates(object roomReplayJSONObject, role string) []json.RawMessage {
	return roomReplayRoleMetadataCandidates(object, role)
}

func roomReplayArtifactStreamMetadataCandidates(object roomReplayJSONObject, role string) []json.RawMessage {
	raw, ok := object["artifacts"]
	if !ok {
		return nil
	}
	nested, err := roomReplayObject(raw)
	if err != nil {
		return nil
	}
	return roomReplayRoleMetadataCandidates(nested, role)
}

func roomReplayRoleMetadataCandidates(object roomReplayJSONObject, role string) []json.RawMessage {
	candidates := make([]json.RawMessage, 0)
	for _, key := range roomReplayStreamRoleAliases(role) {
		if raw, ok := object[key]; ok {
			candidates = append(candidates, raw)
		}
	}
	return candidates
}

func roomReplayStreamRoleAliases(role string) []string {
	switch role {
	case "wav":
		return []string{"wav", "output", "audio", "output_stream", "wav_stream"}
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
	if value, _, err := firstRoomReplayStringField(object, nil, "stream_id", "id", "identity"); err == nil {
		metadata.StreamID = value
	}
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
		role, _, roleErr := firstRoomReplayStringField(object, nil, "stream_role", "audio_role", "role")
		if roleErr != nil {
			continue
		}
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
	case "output", "wav", "audio", "output_stream", "wav_stream":
		return "wav"
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
			return roomReplayMillisecondsDuration(numeric)
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
		return roomReplayMillisecondsDuration(value)
	}
	return 0, fmt.Errorf("duration must be a string")
}

func roomReplayMillisecondsDuration(value int64) (time.Duration, error) {
	maxMilliseconds := int64((1<<63 - 1) / int64(time.Millisecond))
	minMilliseconds := -maxMilliseconds
	if value < minMilliseconds || value > maxMilliseconds {
		return 0, errors.New("duration overflows time.Duration")
	}
	return time.Duration(value) * time.Millisecond, nil
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
		id, _, idErr := firstRoomReplayStringField(object, nil, "id", "chunk_id", "name")
		if idErr != nil {
			continue
		}
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
		label, _, labelErr := firstRoomReplayStringField(object, nil, "label", "id", "name")
		if labelErr != nil {
			continue
		}
		annotations = append(annotations, streamanalysis.SpeechAnnotation{Label: label, Start: start, End: end})
	}
	return annotations
}
