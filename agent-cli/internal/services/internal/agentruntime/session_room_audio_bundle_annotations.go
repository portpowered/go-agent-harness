package agentruntime

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"encoding/json"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

func parseRoomReplayAudioAnnotations(manifest roomReplayJSONObject, plan RoomReplayPlan, participants []RoomReplayAudioParticipant, streamParticipants map[string]string) ([]RoomReplayAudioAnnotation, []roomanalysis.PCM16OverlapInterval, []roomanalysis.PCM16BargeInAnnotation, []roomanalysis.PCM16LoudnessInterval, error) {
	participantByID := make(map[string]RoomReplayAudioParticipant, len(participants))
	for _, participant := range participants {
		participantByID[participant.ID] = participant
	}
	rawAnnotations := make([]json.RawMessage, 0)
	for _, key := range []string{"annotations", "audio_annotations"} {
		if raw, ok := manifest[key]; ok {
			entries, err := roomReplayAnnotationEntries(raw, key)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			rawAnnotations = append(rawAnnotations, entries...)
		}
	}
	if analysisRaw, ok := manifest["analysis"]; ok {
		if analysis, err := roomReplayObject(analysisRaw); err == nil {
			for _, key := range []string{"annotations", "audio_annotations"} {
				if raw, exists := analysis[key]; exists {
					entries, entryErr := roomReplayAnnotationEntries(raw, "analysis."+key)
					if entryErr != nil {
						return nil, nil, nil, nil, entryErr
					}
					rawAnnotations = append(rawAnnotations, entries...)
				}
			}
		}
	}
	annotations := make([]RoomReplayAudioAnnotation, 0, len(rawAnnotations))
	overlaps := make([]roomanalysis.PCM16OverlapInterval, 0)
	barges := make([]roomanalysis.PCM16BargeInAnnotation, 0)
	loudness := make([]roomanalysis.PCM16LoudnessInterval, 0)
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

func parseRoomReplayAudioAnnotation(raw json.RawMessage, index int, plan RoomReplayPlan, participants map[string]RoomReplayAudioParticipant, streamParticipants map[string]string) (RoomReplayAudioAnnotation, *roomanalysis.PCM16OverlapInterval, *roomanalysis.PCM16BargeInAnnotation, *roomanalysis.PCM16LoudnessInterval, bool, error) {
	object, err := roomReplayObject(raw)
	if err != nil {
		return RoomReplayAudioAnnotation{}, nil, nil, nil, false, roomReplayAudioMismatch(fmt.Sprintf("annotations[%d]", index), "run-manifest.json", "annotation object", "invalid", err)
	}
	kind, _, kindErr := firstRoomReplayStringField(object, nil, "kind", "type", "annotation", "event")
	if kindErr != nil {
		return RoomReplayAudioAnnotation{}, nil, nil, nil, false, roomReplayAudioMismatch(fmt.Sprintf("annotations[%d].kind", index), "run-manifest.json", "string annotation kind", "invalid", kindErr)
	}
	kind = normalizeRoomReplayAnnotationKind(kind)
	if kind == "" || (!strings.Contains(kind, "overlap") && !strings.Contains(kind, "simultaneous") && !strings.Contains(kind, "barge") && !strings.Contains(kind, "interrupt") && !strings.Contains(kind, "loudness") && !strings.Contains(kind, "balance")) {
		return RoomReplayAudioAnnotation{}, nil, nil, nil, false, nil
	}
	id, _, _ := firstRoomReplayStringField(object, nil, "id", "annotation_id", "name")
	if strings.TrimSpace(id) == "" {
		id = fmt.Sprintf("%s-%d", kind, index)
	}
	start, end, intervalErr := roomReplayAnnotationInterval(object)
	if intervalErr != nil {
		return RoomReplayAudioAnnotation{}, nil, nil, nil, false, roomReplayAudioIncomplete(fmt.Sprintf("annotations[%s].interval", id), "run-manifest.json", "start and end inside room duration", "missing or invalid", intervalErr)
	}
	roomDuration := plan.EndedAt.Sub(plan.ClockBase)
	if start < 0 || end > roomDuration || end <= start {
		return RoomReplayAudioAnnotation{}, nil, nil, nil, false, roomReplayAudioTimeline("annotations["+id+"]", "run-manifest.json", "interval inside declared room duration", fmt.Sprintf("%s..%s", start, end))
	}
	annotation := RoomReplayAudioAnnotation{ID: id, Kind: kind, Start: start, End: end, Raw: append(json.RawMessage(nil), raw...)}
	if strings.Contains(kind, "overlap") || strings.Contains(kind, "simultaneous") {
		a, b := roomReplayAnnotationEndpoint(object, "a", "participant_a", "participant_a_id", "speaker_a", "a_participant_id", "first_participant_id", "left_participant_id"), roomReplayAnnotationEndpoint(object, "b", "participant_b", "participant_b_id", "speaker_b", "b_participant_id", "second_participant_id", "right_participant_id")
		if a == "" || b == "" {
			values := roomReplayAnnotationParticipantList(object)
			if len(values) >= 2 {
				a, b = values[0], values[1]
			}
		}
		a = normalizeRoomReplayParticipantReference(a, streamParticipants)
		b = normalizeRoomReplayParticipantReference(b, streamParticipants)
		if err := validateRoomReplayAnnotationParticipants(id, []string{a, b}, participants); err != nil {
			return RoomReplayAudioAnnotation{}, nil, nil, nil, false, err
		}
		if a == b {
			return RoomReplayAudioAnnotation{}, nil, nil, nil, false, roomReplayAudioMismatch("annotations["+id+"]", "run-manifest.json", "two distinct participants", a, nil)
		}
		annotation.Participants = []string{a, b}
		annotation.SourceParticipantID, annotation.TargetParticipantID = a, b
		forwardSent, forwardReceived, err := roomReplayAnnotationStreams(object, "a", a, participants, streamParticipants)
		if err != nil {
			return RoomReplayAudioAnnotation{}, nil, nil, nil, false, err
		}
		reverseSent, reverseReceived, err := roomReplayAnnotationStreams(object, "b", b, participants, streamParticipants)
		if err != nil {
			return RoomReplayAudioAnnotation{}, nil, nil, nil, false, err
		}
		overlap := roomanalysis.PCM16OverlapInterval{PCM16TimeInterval: roomanalysis.PCM16TimeInterval{ID: id, Start: start, End: end}, A: roomanalysis.PCM16OverlapParticipant{ParticipantID: a, SentStreamID: forwardSent, ReceivedStreamID: forwardReceived}, B: roomanalysis.PCM16OverlapParticipant{ParticipantID: b, SentStreamID: reverseSent, ReceivedStreamID: reverseReceived}}
		return annotation, &overlap, nil, nil, true, nil
	}
	if strings.Contains(kind, "barge") || strings.Contains(kind, "interrupt") {
		interrupter := normalizeRoomReplayParticipantReference(roomReplayAnnotationEndpoint(object, "interrupter", "interrupter_participant", "interrupter_participant_id", "source_participant_id", "source"), streamParticipants)
		interrupted := normalizeRoomReplayParticipantReference(roomReplayAnnotationEndpoint(object, "interrupted", "interrupted_participant", "interrupted_participant_id", "target_participant_id", "target"), streamParticipants)
		if err := validateRoomReplayAnnotationParticipants(id, []string{interrupter, interrupted}, participants); err != nil {
			return RoomReplayAudioAnnotation{}, nil, nil, nil, false, err
		}
		if interrupter == interrupted {
			return RoomReplayAudioAnnotation{}, nil, nil, nil, false, roomReplayAudioMismatch("annotations["+id+"]", "run-manifest.json", "distinct interrupter and interrupted participants", interrupter, nil)
		}
		annotation.Participants = []string{interrupter, interrupted}
		annotation.InterrupterParticipantID, annotation.InterruptedParticipantID = interrupter, interrupted
		barge := roomanalysis.PCM16BargeInAnnotation{PCM16TimeInterval: roomanalysis.PCM16TimeInterval{ID: id, Start: start, End: end}, InterrupterStreamID: participants[interrupter].Sent.StreamID, InterruptedStreamID: participants[interrupted].WAV.StreamID}
		return annotation, nil, &barge, nil, true, nil
	}
	left := normalizeRoomReplayParticipantReference(roomReplayAnnotationEndpoint(object, "left", "left_participant", "left_participant_id", "participant_a", "a"), streamParticipants)
	right := normalizeRoomReplayParticipantReference(roomReplayAnnotationEndpoint(object, "right", "right_participant", "right_participant_id", "participant_b", "b"), streamParticipants)
	if left == "" || right == "" {
		values := roomReplayAnnotationParticipantList(object)
		if len(values) >= 2 {
			left, right = normalizeRoomReplayParticipantReference(values[0], streamParticipants), normalizeRoomReplayParticipantReference(values[1], streamParticipants)
		}
	}
	if err := validateRoomReplayAnnotationParticipants(id, []string{left, right}, participants); err != nil {
		return RoomReplayAudioAnnotation{}, nil, nil, nil, false, err
	}
	if left == right {
		return RoomReplayAudioAnnotation{}, nil, nil, nil, false, roomReplayAudioMismatch("annotations["+id+"]", "run-manifest.json", "distinct loudness participants", left, nil)
	}
	annotation.Participants = []string{left, right}
	loudness := roomanalysis.PCM16LoudnessInterval{PCM16TimeInterval: roomanalysis.PCM16TimeInterval{ID: id, Start: start, End: end}, LeftStreamID: participants[left].WAV.StreamID, RightStreamID: participants[right].WAV.StreamID}
	return annotation, nil, nil, &loudness, true, nil
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
