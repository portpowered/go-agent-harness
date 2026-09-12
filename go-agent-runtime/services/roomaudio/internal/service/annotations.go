package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

func parseRoomReplayAudioAnnotations(manifest roomReplayJSONObject, plan RoomReplayPlan, participants []RoomReplayAudioParticipant, streamParticipants map[string]string) ([]RoomReplayAudioAnnotation, []roomanalysis.PCM16OverlapInterval, []roomanalysis.PCM16BargeInAnnotation, []roomanalysis.PCM16LoudnessInterval, error) {
	rawAnnotations, err := roomReplayAnnotationSources(manifest)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	participantByID := make(map[string]RoomReplayAudioParticipant, len(participants))
	for _, participant := range participants {
		participantByID[participant.ID] = participant
	}
	annotations := make([]RoomReplayAudioAnnotation, 0, len(rawAnnotations))
	overlaps := make([]roomanalysis.PCM16OverlapInterval, 0)
	barges := make([]roomanalysis.PCM16BargeInAnnotation, 0)
	loudness := make([]roomanalysis.PCM16LoudnessInterval, 0)
	seen := make(map[string]int)
	for index, raw := range rawAnnotations {
		annotation, overlap, barge, loudnessInterval, recognized, err := parseRoomReplayAudioAnnotation(raw, index, plan, participantByID, streamParticipants)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if !recognized {
			continue
		}
		if previous, exists := seen[annotation.ID]; exists {
			return nil, nil, nil, nil, roomReplayAudioMismatch("annotations["+annotation.ID+"]", "run-manifest.json", fmt.Sprintf("unique annotation identity (first seen at index %d)", previous), annotation.ID, nil)
		}
		seen[annotation.ID] = index
		annotations = append(annotations, annotation)
		if overlap != nil {
			overlaps = append(overlaps, *overlap)
		}
		if barge != nil {
			barges = append(barges, *barge)
		}
		if loudnessInterval != nil {
			loudness = append(loudness, *loudnessInterval)
		}
	}
	return annotations, overlaps, barges, loudness, nil
}

func roomReplayAnnotationSources(manifest roomReplayJSONObject) ([]json.RawMessage, error) {
	entries, err := roomReplayAnnotationFields(manifest, "")
	if err != nil {
		return nil, err
	}
	if raw, ok := manifest["analysis"]; ok {
		analysis, err := roomReplayObject(raw)
		if err == nil {
			nested, nestedErr := roomReplayAnnotationFields(analysis, "analysis.")
			if nestedErr != nil {
				return nil, nestedErr
			}
			entries = append(entries, nested...)
		}
	}
	return entries, nil
}

func roomReplayAnnotationFields(object roomReplayJSONObject, prefix string) ([]json.RawMessage, error) {
	entries := make([]json.RawMessage, 0)
	for _, key := range []string{"annotations", "audio_annotations"} {
		raw, ok := object[key]
		if !ok {
			continue
		}
		values, err := roomReplayAnnotationEntries(raw, prefix+key)
		if err != nil {
			return nil, err
		}
		entries = append(entries, values...)
	}
	return entries, nil
}

func parseRoomReplayAudioAnnotation(raw json.RawMessage, index int, plan RoomReplayPlan, participants map[string]RoomReplayAudioParticipant, streamParticipants map[string]string) (RoomReplayAudioAnnotation, *roomanalysis.PCM16OverlapInterval, *roomanalysis.PCM16BargeInAnnotation, *roomanalysis.PCM16LoudnessInterval, bool, error) {
	object, annotation, kind, recognized, err := roomReplayAnnotationHeader(raw, index, plan)
	if err != nil || !recognized {
		return annotation, nil, nil, nil, recognized, err
	}
	if strings.Contains(kind, "overlap") || strings.Contains(kind, "simultaneous") {
		overlap, err := parseRoomReplayOverlapAnnotation(object, &annotation, participants, streamParticipants)
		return annotation, overlap, nil, nil, true, err
	}
	if strings.Contains(kind, "barge") || strings.Contains(kind, "interrupt") {
		barge, err := parseRoomReplayBargeAnnotation(object, &annotation, participants, streamParticipants)
		return annotation, nil, barge, nil, true, err
	}
	loudness, err := parseRoomReplayLoudnessAnnotation(object, &annotation, participants, streamParticipants)
	return annotation, nil, nil, loudness, true, err
}

func roomReplayAnnotationHeader(raw json.RawMessage, index int, plan RoomReplayPlan) (roomReplayJSONObject, RoomReplayAudioAnnotation, string, bool, error) {
	object, err := roomReplayObject(raw)
	if err != nil {
		return nil, RoomReplayAudioAnnotation{}, "", false, roomReplayAudioMismatch(fmt.Sprintf("annotations[%d]", index), "run-manifest.json", "annotation object", "invalid", err)
	}
	kind, _, kindErr := firstRoomReplayStringField(object, nil, "kind", "type", "annotation", "event")
	if kindErr != nil {
		return nil, RoomReplayAudioAnnotation{}, "", false, roomReplayAudioMismatch(fmt.Sprintf("annotations[%d].kind", index), "run-manifest.json", "string annotation kind", "invalid", kindErr)
	}
	kind = normalizeRoomReplayAnnotationKind(kind)
	if !isRoomReplayRecognizedAnnotationKind(kind) {
		return object, RoomReplayAudioAnnotation{}, kind, false, nil
	}
	id, _, _ := firstRoomReplayStringField(object, nil, "id", "annotation_id", "name")
	if strings.TrimSpace(id) == "" {
		id = fmt.Sprintf("%s-%d", kind, index)
	}
	start, end, intervalErr := roomReplayAnnotationInterval(object)
	if intervalErr != nil {
		return nil, RoomReplayAudioAnnotation{}, "", false, roomReplayAudioIncomplete(fmt.Sprintf("annotations[%s].interval", id), "run-manifest.json", "start and end inside room duration", "missing or invalid", intervalErr)
	}
	roomDuration := plan.EndedAt.Sub(plan.ClockBase)
	if start < 0 || end > roomDuration || end <= start {
		return nil, RoomReplayAudioAnnotation{}, "", false, roomReplayAudioTimeline("annotations["+id+"]", "run-manifest.json", "interval inside declared room duration", fmt.Sprintf("%s..%s", start, end))
	}
	return object, RoomReplayAudioAnnotation{ID: id, Kind: kind, Start: start, End: end, Raw: append(json.RawMessage(nil), raw...)}, kind, true, nil
}

func isRoomReplayRecognizedAnnotationKind(kind string) bool {
	return kind != "" && (strings.Contains(kind, "overlap") || strings.Contains(kind, "simultaneous") || strings.Contains(kind, "barge") || strings.Contains(kind, "interrupt") || strings.Contains(kind, "loudness") || strings.Contains(kind, "balance"))
}

func parseRoomReplayOverlapAnnotation(object roomReplayJSONObject, annotation *RoomReplayAudioAnnotation, participants map[string]RoomReplayAudioParticipant, streamParticipants map[string]string) (*roomanalysis.PCM16OverlapInterval, error) {
	a, b := roomReplayAnnotationEndpoints(object, "a", "b")
	if a == "" || b == "" {
		values := roomReplayAnnotationParticipantList(object)
		if len(values) >= 2 {
			a, b = values[0], values[1]
		}
	}
	a, b = normalizeRoomReplayParticipantReference(a, streamParticipants), normalizeRoomReplayParticipantReference(b, streamParticipants)
	if err := validateRoomReplayAnnotationParticipants(annotation.ID, []string{a, b}, participants); err != nil {
		return nil, err
	}
	if a == b {
		return nil, roomReplayAudioMismatch("annotations["+annotation.ID+"]", "run-manifest.json", "two distinct participants", a, nil)
	}
	annotation.Participants = []string{a, b}
	annotation.SourceParticipantID, annotation.TargetParticipantID = a, b
	forwardSent, forwardReceived, err := roomReplayAnnotationStreams(object, "a", a, participants, streamParticipants)
	if err != nil {
		return nil, err
	}
	reverseSent, reverseReceived, err := roomReplayAnnotationStreams(object, "b", b, participants, streamParticipants)
	if err != nil {
		return nil, err
	}
	return &roomanalysis.PCM16OverlapInterval{PCM16TimeInterval: roomanalysis.PCM16TimeInterval{ID: annotation.ID, Start: annotation.Start, End: annotation.End}, A: roomanalysis.PCM16OverlapParticipant{ParticipantID: a, SentStreamID: forwardSent, ReceivedStreamID: forwardReceived}, B: roomanalysis.PCM16OverlapParticipant{ParticipantID: b, SentStreamID: reverseSent, ReceivedStreamID: reverseReceived}}, nil
}

func roomReplayAnnotationEndpoints(object roomReplayJSONObject, first, second string) (string, string) {
	firstNames := roomReplayAnnotationEndpointNames(first)
	secondNames := roomReplayAnnotationEndpointNames(second)
	return roomReplayAnnotationEndpoint(object, firstNames...), roomReplayAnnotationEndpoint(object, secondNames...)
}

func roomReplayAnnotationEndpointNames(endpoint string) []string {
	names := []string{endpoint, "participant_" + endpoint, endpoint + "_participant", endpoint + "_participant_id", "speaker_" + endpoint}
	switch endpoint {
	case "a":
		names = append(names, "first_participant_id", "left_participant_id")
	case "b":
		names = append(names, "second_participant_id", "right_participant_id")
	case "left":
		names = append(names, "participant_a", "a")
	case "right":
		names = append(names, "participant_b", "b")
	}
	return names
}

func parseRoomReplayBargeAnnotation(object roomReplayJSONObject, annotation *RoomReplayAudioAnnotation, participants map[string]RoomReplayAudioParticipant, streamParticipants map[string]string) (*roomanalysis.PCM16BargeInAnnotation, error) {
	interrupter := roomReplayAnnotationEndpoint(object, "interrupter", "interrupter_participant", "interrupter_participant_id", "source_participant_id", "source")
	interrupted := roomReplayAnnotationEndpoint(object, "interrupted", "interrupted_participant", "interrupted_participant_id", "target_participant_id", "target")
	interrupter = normalizeRoomReplayParticipantReference(interrupter, streamParticipants)
	interrupted = normalizeRoomReplayParticipantReference(interrupted, streamParticipants)
	if err := validateRoomReplayAnnotationParticipants(annotation.ID, []string{interrupter, interrupted}, participants); err != nil {
		return nil, err
	}
	if interrupter == interrupted {
		return nil, roomReplayAudioMismatch("annotations["+annotation.ID+"]", "run-manifest.json", "distinct interrupter and interrupted participants", interrupter, nil)
	}
	annotation.Participants = []string{interrupter, interrupted}
	annotation.InterrupterParticipantID, annotation.InterruptedParticipantID = interrupter, interrupted
	return &roomanalysis.PCM16BargeInAnnotation{PCM16TimeInterval: roomanalysis.PCM16TimeInterval{ID: annotation.ID, Start: annotation.Start, End: annotation.End}, InterrupterStreamID: participants[interrupter].Sent.StreamID, InterruptedStreamID: participants[interrupted].WAV.StreamID}, nil
}

func parseRoomReplayLoudnessAnnotation(object roomReplayJSONObject, annotation *RoomReplayAudioAnnotation, participants map[string]RoomReplayAudioParticipant, streamParticipants map[string]string) (*roomanalysis.PCM16LoudnessInterval, error) {
	left, right := roomReplayAnnotationEndpoints(object, "left", "right")
	if left == "" || right == "" {
		values := roomReplayAnnotationParticipantList(object)
		if len(values) >= 2 {
			left, right = values[0], values[1]
		}
	}
	left, right = normalizeRoomReplayParticipantReference(left, streamParticipants), normalizeRoomReplayParticipantReference(right, streamParticipants)
	if err := validateRoomReplayAnnotationParticipants(annotation.ID, []string{left, right}, participants); err != nil {
		return nil, err
	}
	if left == right {
		return nil, roomReplayAudioMismatch("annotations["+annotation.ID+"]", "run-manifest.json", "distinct loudness participants", left, nil)
	}
	annotation.Participants = []string{left, right}
	return &roomanalysis.PCM16LoudnessInterval{PCM16TimeInterval: roomanalysis.PCM16TimeInterval{ID: annotation.ID, Start: annotation.Start, End: annotation.End}, LeftStreamID: participants[left].WAV.StreamID, RightStreamID: participants[right].WAV.StreamID}, nil
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

func roomReplayAnnotationInterval(object roomReplayJSONObject) (time.Duration, time.Duration, error) {
	start, startPresent, startErr := roomReplayAnnotationDuration(object, "start_ms", "offset_start_ms", "start_offset_ms", "start")
	if startErr != nil || !startPresent {
		return 0, 0, errOrDefault(startErr, errors.New("start is missing"))
	}
	end, endPresent, endErr := roomReplayAnnotationDuration(object, "end_ms", "offset_end_ms", "end_offset_ms", "end")
	if endErr != nil || !endPresent {
		if duration, durationPresent, durationErr := roomReplayAnnotationDuration(object, "duration_ms", "duration"); durationErr == nil && durationPresent {
			end = start + duration
			endPresent = true
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
			value, _, _ := firstRoomReplayStringField(nested, nil, "participant_id", "participant", "speaker_id", "id", "stream_id")
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
