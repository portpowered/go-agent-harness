package audiobundle

import (
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

func (t roomReplayAnnotationTarget) interval() roomanalysis.PCM16TimeInterval {
	return roomanalysis.PCM16TimeInterval{ID: t.annotation.ID, Start: t.annotation.Start, End: t.annotation.End}
}

func (t roomReplayAnnotationTarget) overlap() (RoomReplayAudioAnnotation, roomanalysis.PCM16OverlapInterval, error) {
	id := t.annotation.ID
	a, b := roomReplayAnnotationEndpoint(t.object, "a", "participant_a", "participant_a_id", "speaker_a", "a_participant_id", "first_participant_id", "left_participant_id"), roomReplayAnnotationEndpoint(t.object, "b", "participant_b", "participant_b_id", "speaker_b", "b_participant_id", "second_participant_id", "right_participant_id")
	if a == "" || b == "" {
		values := roomReplayAnnotationParticipantList(t.object)
		if len(values) >= 2 {
			a, b = values[0], values[1]
		}
	}
	a = normalizeRoomReplayParticipantReference(a, t.streamParticipants)
	b = normalizeRoomReplayParticipantReference(b, t.streamParticipants)
	if err := validateRoomReplayAnnotationParticipants(id, []string{a, b}, t.participants); err != nil {
		return RoomReplayAudioAnnotation{}, roomanalysis.PCM16OverlapInterval{}, err
	}
	if a == b {
		return RoomReplayAudioAnnotation{}, roomanalysis.PCM16OverlapInterval{}, roomReplayAudioMismatch("annotations["+id+"]", "run-manifest.json", "two distinct participants", a, nil)
	}
	annotation := t.annotation
	annotation.Participants = []string{a, b}
	annotation.SourceParticipantID, annotation.TargetParticipantID = a, b
	forwardSent, forwardReceived, err := roomReplayAnnotationStreams(t.object, "a", a, t.participants, t.streamParticipants)
	if err != nil {
		return RoomReplayAudioAnnotation{}, roomanalysis.PCM16OverlapInterval{}, err
	}
	reverseSent, reverseReceived, err := roomReplayAnnotationStreams(t.object, "b", b, t.participants, t.streamParticipants)
	if err != nil {
		return RoomReplayAudioAnnotation{}, roomanalysis.PCM16OverlapInterval{}, err
	}
	overlap := roomanalysis.PCM16OverlapInterval{PCM16TimeInterval: t.interval(), A: roomanalysis.PCM16OverlapParticipant{ParticipantID: a, SentStreamID: forwardSent, ReceivedStreamID: forwardReceived}, B: roomanalysis.PCM16OverlapParticipant{ParticipantID: b, SentStreamID: reverseSent, ReceivedStreamID: reverseReceived}}
	return annotation, overlap, nil
}

func (t roomReplayAnnotationTarget) bargeIn() (RoomReplayAudioAnnotation, roomanalysis.PCM16BargeInAnnotation, error) {
	id := t.annotation.ID
	interrupter := normalizeRoomReplayParticipantReference(roomReplayAnnotationEndpoint(t.object, "interrupter", "interrupter_participant", "interrupter_participant_id", "source_participant_id", "source"), t.streamParticipants)
	interrupted := normalizeRoomReplayParticipantReference(roomReplayAnnotationEndpoint(t.object, "interrupted", "interrupted_participant", "interrupted_participant_id", "target_participant_id", "target"), t.streamParticipants)
	if err := validateRoomReplayAnnotationParticipants(id, []string{interrupter, interrupted}, t.participants); err != nil {
		return RoomReplayAudioAnnotation{}, roomanalysis.PCM16BargeInAnnotation{}, err
	}
	if interrupter == interrupted {
		return RoomReplayAudioAnnotation{}, roomanalysis.PCM16BargeInAnnotation{}, roomReplayAudioMismatch("annotations["+id+"]", "run-manifest.json", "distinct interrupter and interrupted participants", interrupter, nil)
	}
	annotation := t.annotation
	annotation.Participants = []string{interrupter, interrupted}
	annotation.InterrupterParticipantID, annotation.InterruptedParticipantID = interrupter, interrupted
	barge := roomanalysis.PCM16BargeInAnnotation{PCM16TimeInterval: t.interval(), InterrupterStreamID: t.participants[interrupter].Sent.StreamID, InterruptedStreamID: t.participants[interrupted].WAV.StreamID}
	return annotation, barge, nil
}

func (t roomReplayAnnotationTarget) loudness() (RoomReplayAudioAnnotation, roomanalysis.PCM16LoudnessInterval, error) {
	id := t.annotation.ID
	left := normalizeRoomReplayParticipantReference(roomReplayAnnotationEndpoint(t.object, "left", "left_participant", "left_participant_id", "participant_a", "a"), t.streamParticipants)
	right := normalizeRoomReplayParticipantReference(roomReplayAnnotationEndpoint(t.object, "right", "right_participant", "right_participant_id", "participant_b", "b"), t.streamParticipants)
	if left == "" || right == "" {
		values := roomReplayAnnotationParticipantList(t.object)
		if len(values) >= 2 {
			left, right = normalizeRoomReplayParticipantReference(values[0], t.streamParticipants), normalizeRoomReplayParticipantReference(values[1], t.streamParticipants)
		}
	}
	if err := validateRoomReplayAnnotationParticipants(id, []string{left, right}, t.participants); err != nil {
		return RoomReplayAudioAnnotation{}, roomanalysis.PCM16LoudnessInterval{}, err
	}
	if left == right {
		return RoomReplayAudioAnnotation{}, roomanalysis.PCM16LoudnessInterval{}, roomReplayAudioMismatch("annotations["+id+"]", "run-manifest.json", "distinct loudness participants", left, nil)
	}
	annotation := t.annotation
	annotation.Participants = []string{left, right}
	loudness := roomanalysis.PCM16LoudnessInterval{PCM16TimeInterval: t.interval(), LeftStreamID: t.participants[left].WAV.StreamID, RightStreamID: t.participants[right].WAV.StreamID}
	return annotation, loudness, nil
}
