package service

import (
	"encoding/json"

	streamanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
)

type roomReplayParticipantArtifacts struct {
	wav, deltas, sent, received, events, diagnostics RoomReplayArtifact
}

func loadRoomReplayAudioParticipant(plan RoomReplayPlan, participant RoomReplayParticipant, object roomReplayJSONObject) (RoomReplayAudioParticipant, error) {
	artifacts, err := requiredRoomReplayParticipantArtifacts(participant)
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	events, diagnostics, err := loadRoomReplayParticipantSidecars(artifacts, participant.ID)
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	metadata := roomReplayParticipantMetadata(object, events, diagnostics, participant.ID)
	wav, err := loadRoomReplayWAVParticipant(plan, participant.ID, artifacts, metadata["wav"])
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	sent, err := loadRoomReplayParticipantStream(plan, artifacts.sent, metadata["sent"], participant.ID, "sent")
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	received, err := loadRoomReplayParticipantStream(plan, artifacts.received, metadata["received"], participant.ID, "received")
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	return RoomReplayAudioParticipant{ID: participant.ID, WAV: wav, Sent: sent, Received: received, Events: events, Diagnostics: diagnostics}, nil
}

func requiredRoomReplayParticipantArtifacts(participant RoomReplayParticipant) (roomReplayParticipantArtifacts, error) {
	roles := []struct {
		role  string
		field string
	}{
		{roomReplayArtifactRoleWAV, "wav"},
		{roomReplayArtifactRoleDeltas, "deltas"},
		{roomReplayArtifactRoleSentPCM, "sent_pcm"},
		{roomReplayArtifactRoleReceivedPCM, "received_pcm"},
		{roomReplayArtifactRoleEvents, "events"},
		{roomReplayArtifactRoleDiagnostics, "diagnostics"},
	}
	var result roomReplayParticipantArtifacts
	for _, item := range roles {
		artifact, ok := roomReplayParticipantArtifact(participant, item.role)
		if !ok {
			return roomReplayParticipantArtifacts{}, roomReplayAudioIncomplete("participants["+participant.ID+"].artifacts."+item.field, "", "validated "+item.field+" artifact", "missing", ErrRoomReplayBundleIncomplete)
		}
		switch item.role {
		case roomReplayArtifactRoleWAV:
			result.wav = artifact
		case roomReplayArtifactRoleDeltas:
			result.deltas = artifact
		case roomReplayArtifactRoleSentPCM:
			result.sent = artifact
		case roomReplayArtifactRoleReceivedPCM:
			result.received = artifact
		case roomReplayArtifactRoleEvents:
			result.events = artifact
		case roomReplayArtifactRoleDiagnostics:
			result.diagnostics = artifact
		}
	}
	return result, nil
}

func loadRoomReplayParticipantSidecars(artifacts roomReplayParticipantArtifacts, participantID string) ([]json.RawMessage, []json.RawMessage, error) {
	events, err := loadRoomReplayJSONL(artifacts.events, "participants["+participantID+"].events")
	if err != nil {
		return nil, nil, err
	}
	diagnostics, err := loadRoomReplayJSONL(artifacts.diagnostics, "participants["+participantID+"].diagnostics")
	if err != nil {
		return nil, nil, err
	}
	return events, diagnostics, nil
}

func roomReplayParticipantMetadata(object roomReplayJSONObject, events, diagnostics []json.RawMessage, participantID string) map[string]roomReplayAudioStreamMetadata {
	metadata := make(map[string]roomReplayAudioStreamMetadata, 3)
	for _, role := range []string{"wav", "sent", "received"} {
		metadata[role] = parseRoomReplayStreamMetadata(object, role)
	}
	mergeRoomReplaySidecarMetadata(metadata, events)
	mergeRoomReplaySidecarMetadata(metadata, diagnostics)
	defaults := map[string]string{"wav": participantID + ":output", "sent": participantID + ":sent", "received": participantID + ":received"}
	for role, id := range defaults {
		if metadata[role].StreamID == "" {
			metadata[role] = withRoomReplayStreamID(metadata[role], id)
		}
	}
	return metadata
}

func withRoomReplayStreamID(metadata roomReplayAudioStreamMetadata, streamID string) roomReplayAudioStreamMetadata {
	metadata.StreamID = streamID
	return metadata
}

func loadRoomReplayWAVParticipant(plan RoomReplayPlan, participantID string, artifacts roomReplayParticipantArtifacts, metadata roomReplayAudioStreamMetadata) (RoomReplayAudioStream, error) {
	deltas, err := loadRoomReplayAudioDeltas(artifacts.deltas, participantID, metadata.StreamID, plan)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	wav, err := loadRoomReplayWAVStream(plan, artifacts.wav, metadata.StreamID, participantID, "wav")
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	wav.Role, wav.DeltaArtifact, wav.Deltas = "wav", artifacts.deltas, deltas
	appendRoomReplayDeltaBoundaries(&wav, deltas)
	metadata = completeRoomReplayWAVMetadata(metadata, deltas, len(wav.Samples), plan.PCMFormat.SampleRate)
	applyRoomReplayStreamMetadata(&wav, metadata)
	if err := validateRoomReplayDeltaStream(wav, plan, participantID); err != nil {
		return RoomReplayAudioStream{}, err
	}
	if err := reconstructRoomReplayDeltaStream(wav, participantID); err != nil {
		return RoomReplayAudioStream{}, err
	}
	return wav, nil
}

func appendRoomReplayDeltaBoundaries(stream *RoomReplayAudioStream, deltas []RoomReplayAudioDelta) {
	endSample := 0
	for _, delta := range deltas {
		endSample += len(delta.PCM) / 2
		stream.ChunkBoundaries = append(stream.ChunkBoundaries, streamanalysis.ChunkBoundary{ID: delta.ID, SampleIndex: endSample})
	}
}

func completeRoomReplayWAVMetadata(metadata roomReplayAudioStreamMetadata, deltas []RoomReplayAudioDelta, sampleCount, sampleRate int) roomReplayAudioStreamMetadata {
	if !metadata.HasStart {
		for _, delta := range deltas {
			if delta.HasOffset {
				metadata.TimelineStart, metadata.HasStart = delta.Offset, true
				break
			}
		}
	}
	if !metadata.HasEnd {
		for index := len(deltas) - 1; index >= 0; index-- {
			if deltas[index].HasOffset {
				metadata.TimelineEnd = deltas[index].Offset + roomReplaySampleDuration(len(deltas[index].PCM)/2, sampleRate)
				metadata.HasEnd = true
				break
			}
		}
	}
	if !metadata.HasEnd {
		metadata.TimelineEnd = metadata.TimelineStart + roomReplaySampleDuration(sampleCount, sampleRate)
		metadata.HasEnd = true
	}
	return metadata
}

func loadRoomReplayParticipantStream(plan RoomReplayPlan, artifact RoomReplayArtifact, metadata roomReplayAudioStreamMetadata, participantID, role string) (RoomReplayAudioStream, error) {
	stream, err := loadRoomReplayPCMStream(plan, artifact, metadata.StreamID, participantID, role)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	applyRoomReplayStreamMetadata(&stream, metadata)
	if err := validateRoomReplayAudioStreamTimeline(stream, plan, "participants["+participantID+"]."+role); err != nil {
		return RoomReplayAudioStream{}, err
	}
	return stream, nil
}

func roomReplayParticipantArtifact(participant RoomReplayParticipant, role string) (RoomReplayArtifact, bool) {
	for _, artifact := range participant.Artifacts {
		if artifact.Role == role || artifact.Name == role {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}
