package audiobundle

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"encoding/json"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

// roomReplayAnnotationSets accumulates the analysis inputs derived from recognized annotations.
type roomReplayAnnotationSets struct {
	annotations []RoomReplayAudioAnnotation
	overlaps    []roomanalysis.PCM16OverlapInterval
	barges      []roomanalysis.PCM16BargeInAnnotation
	loudness    []roomanalysis.PCM16LoudnessInterval
}

func (s *roomReplayAnnotationSets) add(annotation RoomReplayAudioAnnotation, overlap *roomanalysis.PCM16OverlapInterval, barge *roomanalysis.PCM16BargeInAnnotation, loudness *roomanalysis.PCM16LoudnessInterval) {
	s.annotations = append(s.annotations, annotation)
	if overlap != nil {
		s.overlaps = append(s.overlaps, *overlap)
	}
	if barge != nil {
		s.barges = append(s.barges, *barge)
	}
	if loudness != nil {
		s.loudness = append(s.loudness, *loudness)
	}
}

func parseRoomReplayAudioAnnotations(manifest roomReplayJSONObject, plan RoomReplayPlan, participants []RoomReplayAudioParticipant, streamParticipants map[string]string) ([]RoomReplayAudioAnnotation, []roomanalysis.PCM16OverlapInterval, []roomanalysis.PCM16BargeInAnnotation, []roomanalysis.PCM16LoudnessInterval, error) {
	participantByID := make(map[string]RoomReplayAudioParticipant, len(participants))
	for _, participant := range participants {
		participantByID[participant.ID] = participant
	}
	rawAnnotations, err := collectRoomReplayAnnotationEntries(manifest)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	sets := roomReplayAnnotationSets{
		annotations: make([]RoomReplayAudioAnnotation, 0, len(rawAnnotations)),
		overlaps:    make([]roomanalysis.PCM16OverlapInterval, 0),
		barges:      make([]roomanalysis.PCM16BargeInAnnotation, 0),
		loudness:    make([]roomanalysis.PCM16LoudnessInterval, 0),
	}
	seenAnnotationIDs := make(map[string]int)
	for index, raw := range rawAnnotations {
		annotation, overlap, barge, loudnessInterval, recognized, err := parseRoomReplayAudioAnnotation(raw, index, plan, participantByID, streamParticipants)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if !recognized {
			continue
		}
		if previousIndex, exists := seenAnnotationIDs[annotation.ID]; exists {
			return nil, nil, nil, nil, roomReplayAudioMismatch("annotations["+annotation.ID+"]", "run-manifest.json", fmt.Sprintf("unique annotation identity (first seen at index %d)", previousIndex), annotation.ID, nil)
		}
		seenAnnotationIDs[annotation.ID] = index
		sets.add(annotation, overlap, barge, loudnessInterval)
	}
	return sets.annotations, sets.overlaps, sets.barges, sets.loudness, nil
}

// collectRoomReplayAnnotationEntries gathers top-level annotation entries
// followed by those nested under an object-shaped "analysis" section.
func collectRoomReplayAnnotationEntries(manifest roomReplayJSONObject) ([]json.RawMessage, error) {
	rawAnnotations, err := appendRoomReplayAnnotationEntries(make([]json.RawMessage, 0), manifest, "")
	if err != nil {
		return nil, err
	}
	if analysisRaw, ok := manifest["analysis"]; ok {
		if analysis, objectErr := roomReplayObject(analysisRaw); objectErr == nil {
			return appendRoomReplayAnnotationEntries(rawAnnotations, analysis, "analysis.")
		}
	}
	return rawAnnotations, nil
}

func appendRoomReplayAnnotationEntries(entries []json.RawMessage, object roomReplayJSONObject, fieldPrefix string) ([]json.RawMessage, error) {
	for _, key := range []string{"annotations", "audio_annotations"} {
		raw, ok := object[key]
		if !ok {
			continue
		}
		values, err := roomReplayAnnotationEntries(raw, fieldPrefix+key)
		if err != nil {
			return nil, err
		}
		entries = append(entries, values...)
	}
	return entries, nil
}

func roomReplayAnnotationEntries(raw json.RawMessage, field string) ([]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, roomReplayAudioMismatch(field, "run-manifest.json", "annotation array", "invalid", err)
		}
		return entries, nil
	}
	object, err := roomReplayObject(raw)
	if err != nil {
		return nil, roomReplayAudioMismatch(field, "run-manifest.json", "annotation object or array", "invalid", err)
	}
	entries := make([]json.RawMessage, 0)
	for _, key := range []string{"overlaps", "simultaneous_speech", "barge_ins", "barge_in", "interruptions", "loudness", "loudness_intervals", "balance"} {
		if nested, ok := object[key]; ok {
			values, nestedErr := roomReplayAnnotationEntries(nested, field+"."+key)
			if nestedErr != nil {
				return nil, nestedErr
			}
			entries = append(entries, values...)
		}
	}
	if len(entries) > 0 {
		return entries, nil
	}
	return []json.RawMessage{raw}, nil
}

// roomReplayAnnotationTarget carries a recognized annotation's validated
// identity and interval together with the participant lookups used to resolve
// its kind-specific endpoints.
type roomReplayAnnotationTarget struct {
	object             roomReplayJSONObject
	annotation         RoomReplayAudioAnnotation
	participants       map[string]RoomReplayAudioParticipant
	streamParticipants map[string]string
}

func parseRoomReplayAudioAnnotation(raw json.RawMessage, index int, plan RoomReplayPlan, participants map[string]RoomReplayAudioParticipant, streamParticipants map[string]string) (RoomReplayAudioAnnotation, *roomanalysis.PCM16OverlapInterval, *roomanalysis.PCM16BargeInAnnotation, *roomanalysis.PCM16LoudnessInterval, bool, error) {
	target, recognized, err := parseRoomReplayAnnotationTarget(raw, index, plan)
	if err != nil || !recognized {
		return RoomReplayAudioAnnotation{}, nil, nil, nil, false, err
	}
	target.participants, target.streamParticipants = participants, streamParticipants
	kind := target.annotation.Kind
	switch {
	case strings.Contains(kind, "overlap") || strings.Contains(kind, "simultaneous"):
		annotation, overlap, err := target.overlap()
		if err != nil {
			return RoomReplayAudioAnnotation{}, nil, nil, nil, false, err
		}
		return annotation, &overlap, nil, nil, true, nil
	case strings.Contains(kind, "barge") || strings.Contains(kind, "interrupt"):
		annotation, barge, err := target.bargeIn()
		if err != nil {
			return RoomReplayAudioAnnotation{}, nil, nil, nil, false, err
		}
		return annotation, nil, &barge, nil, true, nil
	default:
		annotation, loudness, err := target.loudness()
		if err != nil {
			return RoomReplayAudioAnnotation{}, nil, nil, nil, false, err
		}
		return annotation, nil, nil, &loudness, true, nil
	}
}

func parseRoomReplayAnnotationTarget(raw json.RawMessage, index int, plan RoomReplayPlan) (roomReplayAnnotationTarget, bool, error) {
	object, err := roomReplayObject(raw)
	if err != nil {
		return roomReplayAnnotationTarget{}, false, roomReplayAudioMismatch(fmt.Sprintf("annotations[%d]", index), "run-manifest.json", "annotation object", "invalid", err)
	}
	kind, _, kindErr := firstRoomReplayStringField(object, nil, "kind", "type", "annotation", "event")
	if kindErr != nil {
		return roomReplayAnnotationTarget{}, false, roomReplayAudioMismatch(fmt.Sprintf("annotations[%d].kind", index), "run-manifest.json", "string annotation kind", "invalid", kindErr)
	}
	kind = normalizeRoomReplayAnnotationKind(kind)
	if !isRoomReplayAnnotationKindRecognized(kind) {
		return roomReplayAnnotationTarget{}, false, nil
	}
	id, _ := optionalRoomReplayStringField(object, nil, "id", "annotation_id", "name")
	if strings.TrimSpace(id) == "" {
		id = fmt.Sprintf("%s-%d", kind, index)
	}
	start, end, intervalErr := roomReplayAnnotationInterval(object)
	if intervalErr != nil {
		return roomReplayAnnotationTarget{}, false, roomReplayAudioIncomplete(fmt.Sprintf("annotations[%s].interval", id), "run-manifest.json", "start and end inside room duration", "missing or invalid", intervalErr)
	}
	roomDuration := plan.EndedAt.Sub(plan.ClockBase)
	if start < 0 || end > roomDuration || end <= start {
		return roomReplayAnnotationTarget{}, false, roomReplayAudioTimeline("annotations["+id+"]", "run-manifest.json", "interval inside declared room duration", fmt.Sprintf("%s..%s", start, end))
	}
	annotation := RoomReplayAudioAnnotation{ID: id, Kind: kind, Start: start, End: end, Raw: append(json.RawMessage(nil), raw...)}
	return roomReplayAnnotationTarget{object: object, annotation: annotation}, true, nil
}

func isRoomReplayAnnotationKindRecognized(kind string) bool {
	if kind == "" {
		return false
	}
	for _, marker := range []string{"overlap", "simultaneous", "barge", "interrupt", "loudness", "balance"} {
		if strings.Contains(kind, marker) {
			return true
		}
	}
	return false
}

func roomReplayAnnotationInterval(object roomReplayJSONObject) (time.Duration, time.Duration, error) {
	start, startPresent, startErr := roomReplayAnnotationDuration(object, "start_ms", "offset_start_ms", "start_offset_ms", "start")
	if startErr != nil || !startPresent {
		return 0, 0, errOrDefault(startErr, errors.New("start is missing"))
	}
	end, endPresent, endErr := roomReplayAnnotationDuration(object, "end_ms", "offset_end_ms", "end_offset_ms", "end")
	if endErr != nil || !endPresent {
		if duration, durationPresent, durationErr := roomReplayAnnotationDuration(object, "duration_ms", "duration"); durationErr == nil && durationPresent {
			end = start + duration
		} else {
			return 0, 0, errOrDefault(endErr, errors.New("end is missing"))
		}
	}
	return start, end, nil
}

func roomReplayAnnotationDuration(object roomReplayJSONObject, names ...string) (time.Duration, bool, error) {
	for _, name := range names {
		if raw, ok := object[name]; ok {
			value, err := roomReplayDurationValue(raw, true)
			return value, true, err
		}
	}
	return 0, false, nil
}

func normalizeRoomReplayAnnotationKind(value string) string {
	return strings.ToLower(strings.NewReplacer("-", "_", ".", "_").Replace(strings.TrimSpace(value)))
}

func roomReplayAnnotationEndpoint(object roomReplayJSONObject, names ...string) string {
	for _, name := range names {
		raw, ok := object[name]
		if !ok {
			continue
		}
		if value, ok := decodeRoomReplayString(raw); ok {
			return strings.TrimSpace(value)
		}
		if nested, err := roomReplayObject(raw); err == nil {
			value, _ := optionalRoomReplayStringField(nested, nil, "participant_id", "participant", "speaker_id", "id", "stream_id")
			if value != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func roomReplayAnnotationParticipantList(object roomReplayJSONObject) []string {
	raw, ok := object["participants"]
	if !ok {
		return nil
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := decodeRoomReplayString(value); ok {
			result = append(result, strings.TrimSpace(text))
			continue
		}
		if object, err := roomReplayObject(value); err == nil {
			if text := roomReplayAnnotationEndpoint(object, "participant_id", "participant", "speaker_id", "id", "stream_id"); text != "" {
				result = append(result, text)
			}
		}
	}
	return result
}

func normalizeRoomReplayParticipantReference(value string, streamParticipants map[string]string) string {
	value = strings.TrimSpace(value)
	if participant, ok := streamParticipants[value]; ok {
		return participant
	}
	return value
}

func validateRoomReplayAnnotationParticipants(id string, IDs []string, participants map[string]RoomReplayAudioParticipant) error {
	for _, participantID := range IDs {
		if participantID == "" {
			return roomReplayAudioIncomplete("annotations["+id+"] .participants", "run-manifest.json", "declared participant identities", "missing", ErrRoomReplayBundleIncomplete)
		}
		if _, ok := participants[participantID]; !ok {
			return roomReplayAudioMismatch("annotations["+id+"] .participants", "run-manifest.json", "declared participant identity", participantID, nil)
		}
	}
	return nil
}

func roomReplayAnnotationStreams(object roomReplayJSONObject, endpoint, participantID string, participants map[string]RoomReplayAudioParticipant, streamParticipants map[string]string) (string, string, error) {
	participant := participants[participantID]
	sent := participant.Sent.StreamID
	received := participant.Received.StreamID
	for _, name := range []string{endpoint + "_sent_stream_id", endpoint + "_sent", endpoint + "_source_stream_id"} {
		if value := roomReplayAnnotationEndpoint(object, name); value != "" {
			sent = value
		}
	}
	for _, name := range []string{endpoint + "_received_stream_id", endpoint + "_received", endpoint + "_target_stream_id"} {
		if value := roomReplayAnnotationEndpoint(object, name); value != "" {
			received = value
		}
	}
	if sent == participantID {
		sent = participant.Sent.StreamID
	}
	if received == participantID {
		received = participant.Received.StreamID
	}
	if owner, ok := streamParticipants[sent]; !ok || owner != participantID {
		return "", "", roomReplayAudioMismatch("annotations["+endpoint+"] .sent_stream_id", "run-manifest.json", "sent stream owned by participant", sent, nil)
	}
	if owner, ok := streamParticipants[received]; !ok || owner != participantID {
		return "", "", roomReplayAudioMismatch("annotations["+endpoint+"] .received_stream_id", "run-manifest.json", "received stream owned by participant", received, nil)
	}
	return sent, received, nil
}

func validateRoomReplayAudioStreamTimeline(stream RoomReplayAudioStream, plan RoomReplayPlan, field string) error {
	roomDuration := plan.EndedAt.Sub(plan.ClockBase)
	if stream.StreamID == "" || stream.ParticipantID == "" {
		return roomReplayAudioMismatch(field+".identity", stream.Artifact.Path, "non-empty stream and participant identity", stream.StreamID+"/"+stream.ParticipantID, nil)
	}
	if stream.TimelineStart < 0 || stream.TimelineEnd <= stream.TimelineStart {
		return roomReplayAudioTimeline(field+".timeline", stream.Artifact.Path, "positive interval within room", fmt.Sprintf("%s..%s", stream.TimelineStart, stream.TimelineEnd))
	}
	if stream.TimelineEnd > roomDuration {
		return roomReplayAudioTimeline(field+".timeline", stream.Artifact.Path, "timeline end within declared room duration", stream.TimelineEnd.String())
	}
	sampleDuration := roomReplaySampleDuration(len(stream.Samples), stream.SampleRate)
	if stream.TimelineStart+sampleDuration > roomDuration {
		return roomReplayAudioTimeline(field+".samples", stream.Artifact.Path, "sample payload within declared room duration", (stream.TimelineStart + sampleDuration).String())
	}
	if stream.TimelineStart+sampleDuration > stream.TimelineEnd {
		return roomReplayAudioTimeline(field+".samples", stream.Artifact.Path, "sample payload within stream timeline", (stream.TimelineStart + sampleDuration).String())
	}
	previous := 0
	for index, boundary := range stream.ChunkBoundaries {
		if boundary.SampleIndex <= previous || boundary.SampleIndex > len(stream.Samples) {
			return roomReplayAudioTimeline(fmt.Sprintf("%s.chunk_boundaries[%d]", field, index), stream.Artifact.Path, fmt.Sprintf("sample index in 1..%d and increasing", len(stream.Samples)), strconv.Itoa(boundary.SampleIndex))
		}
		previous = boundary.SampleIndex
	}
	for index, annotation := range stream.ExpectedSpeech {
		if annotation.Start < stream.TimelineStart || annotation.End > stream.TimelineEnd || annotation.End <= annotation.Start {
			return roomReplayAudioTimeline(fmt.Sprintf("%s.expected_speech[%d]", field, index), stream.Artifact.Path, "annotation within stream timeline", fmt.Sprintf("%s..%s", annotation.Start, annotation.End))
		}
	}
	return nil
}
