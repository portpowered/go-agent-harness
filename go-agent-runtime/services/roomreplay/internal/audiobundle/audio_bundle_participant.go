package audiobundle

import (
	"encoding/json"

	streamanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/stream"
)

// roomReplayParticipantAudioArtifacts holds the validated artifacts every
// participant must declare before its audio streams can be loaded.
type roomReplayParticipantAudioArtifacts struct {
	wav, deltas, sent, received, events, diagnostics RoomReplayArtifact
}

func loadRoomReplayAudioParticipant(plan RoomReplayPlan, participant RoomReplayParticipant, object roomReplayJSONObject) (RoomReplayAudioParticipant, error) {
	artifacts, err := requireRoomReplayParticipantAudioArtifacts(participant)
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	events, err := loadRoomReplayJSONL(artifacts.events, "participants["+participant.ID+"].events")
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	diagnostics, err := loadRoomReplayJSONL(artifacts.diagnostics, "participants["+participant.ID+"].diagnostics")
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	wavMetadata, sentMetadata, receivedMetadata := resolveRoomReplayParticipantStreamMetadata(object, participant.ID, events, diagnostics)
	wav, err := loadRoomReplayParticipantWAV(plan, participant.ID, artifacts, wavMetadata)
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	sent, err := loadRoomReplayParticipantPCM(plan, participant.ID, artifacts.sent, sentMetadata, roomReplayAudioRoleSent)
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	received, err := loadRoomReplayParticipantPCM(plan, participant.ID, artifacts.received, receivedMetadata, roomReplayAudioRoleReceived)
	if err != nil {
		return RoomReplayAudioParticipant{}, err
	}
	return RoomReplayAudioParticipant{ID: participant.ID, WAV: wav, Sent: sent, Received: received, Events: events, Diagnostics: diagnostics}, nil
}

// requireRoomReplayParticipantAudioArtifacts resolves the participant's
// artifacts in a fixed order so the first missing role is reported.
func requireRoomReplayParticipantAudioArtifacts(participant RoomReplayParticipant) (roomReplayParticipantAudioArtifacts, error) {
	var artifacts roomReplayParticipantAudioArtifacts
	requirements := []struct {
		role, label string
		target      *RoomReplayArtifact
	}{
		{roomReplayArtifactRoleWAV, "WAV", &artifacts.wav},
		{roomReplayArtifactRoleDeltas, "delta", &artifacts.deltas},
		{roomReplayArtifactRoleSentPCM, roomReplayAudioRoleSent, &artifacts.sent},
		{roomReplayArtifactRoleReceivedPCM, roomReplayAudioRoleReceived, &artifacts.received},
		{roomReplayArtifactRoleEvents, roomReplayArtifactRoleEvents, &artifacts.events},
		{roomReplayArtifactRoleDiagnostics, roomReplayArtifactRoleDiagnostics, &artifacts.diagnostics},
	}
	for _, requirement := range requirements {
		artifact, ok := roomReplayParticipantArtifact(participant, requirement.role)
		if !ok {
			return roomReplayParticipantAudioArtifacts{}, roomReplayAudioIncomplete("participants["+participant.ID+"].artifacts."+requirement.role, "", "validated "+requirement.label+" artifact", "missing", ErrRoomReplayBundleIncomplete)
		}
		*requirement.target = artifact
	}
	return artifacts, nil
}

// resolveRoomReplayParticipantStreamMetadata merges manifest and sidecar
// metadata for the participant's three streams and assigns default stream IDs.
func resolveRoomReplayParticipantStreamMetadata(object roomReplayJSONObject, participantID string, events, diagnostics []json.RawMessage) (wav, sent, received roomReplayAudioStreamMetadata) {
	metadata := make(map[string]roomReplayAudioStreamMetadata)
	for _, role := range []string{roomReplayAudioRoleWAV, roomReplayAudioRoleSent, roomReplayAudioRoleReceived} {
		metadata[role] = parseRoomReplayStreamMetadata(object, role)
	}
	mergeRoomReplaySidecarMetadata(metadata, events)
	mergeRoomReplaySidecarMetadata(metadata, diagnostics)
	wav = withDefaultRoomReplayStreamID(metadata[roomReplayAudioRoleWAV], participantID+":output")
	sent = withDefaultRoomReplayStreamID(metadata[roomReplayAudioRoleSent], participantID+":sent")
	received = withDefaultRoomReplayStreamID(metadata[roomReplayAudioRoleReceived], participantID+":received")
	return wav, sent, received
}

func withDefaultRoomReplayStreamID(metadata roomReplayAudioStreamMetadata, streamID string) roomReplayAudioStreamMetadata {
	if metadata.StreamID == "" {
		metadata.StreamID = streamID
	}
	return metadata
}

func loadRoomReplayParticipantWAV(plan RoomReplayPlan, participantID string, artifacts roomReplayParticipantAudioArtifacts, metadata roomReplayAudioStreamMetadata) (RoomReplayAudioStream, error) {
	deltas, err := loadRoomReplayAudioDeltas(artifacts.deltas, participantID, metadata.StreamID, plan)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	wav, err := loadRoomReplayWAVStream(plan, artifacts.wav, metadata.StreamID, participantID, roomReplayAudioRoleWAV)
	if err != nil {
		return RoomReplayAudioStream{}, err
	}
	wav.Role = roomReplayAudioRoleWAV
	wav.DeltaArtifact = artifacts.deltas
	wav.Deltas = deltas
	wav.ChunkBoundaries = appendRoomReplayDeltaChunkBoundaries(wav.ChunkBoundaries, deltas)
	inferRoomReplayWAVTimeline(&metadata, deltas, len(wav.Samples), plan)
	applyRoomReplayStreamMetadata(&wav, metadata)
	if len(wav.ChunkBoundaries) == 0 {
		wav.ChunkBoundaries = appendRoomReplayDeltaChunkBoundaries(wav.ChunkBoundaries, deltas)
	}
	if err := validateRoomReplayDeltaStream(wav, plan, participantID); err != nil {
		return RoomReplayAudioStream{}, err
	}
	if err := reconstructRoomReplayDeltaStream(wav, participantID); err != nil {
		return RoomReplayAudioStream{}, err
	}
	return wav, nil
}

// appendRoomReplayDeltaChunkBoundaries appends one boundary per delta at the
// cumulative PCM16 sample index where that delta ends.
func appendRoomReplayDeltaChunkBoundaries(boundaries []streamanalysis.ChunkBoundary, deltas []RoomReplayAudioDelta) []streamanalysis.ChunkBoundary {
	endSample := 0
	for _, delta := range deltas {
		endSample += len(delta.PCM) / 2
		boundaries = append(boundaries, streamanalysis.ChunkBoundary{ID: delta.ID, SampleIndex: endSample})
	}
	return boundaries
}

// inferRoomReplayWAVTimeline fills an undeclared WAV timeline from the first
// and last timestamped deltas, falling back to the decoded sample duration.
func inferRoomReplayWAVTimeline(metadata *roomReplayAudioStreamMetadata, deltas []RoomReplayAudioDelta, sampleCount int, plan RoomReplayPlan) {
	if !metadata.HasStart {
		for _, delta := range deltas {
			if delta.HasOffset {
				metadata.TimelineStart = delta.Offset
				metadata.HasStart = true
				break
			}
		}
	}
	if !metadata.HasEnd {
		for index := len(deltas) - 1; index >= 0; index-- {
			if deltas[index].HasOffset {
				metadata.TimelineEnd = deltas[index].Offset + roomReplaySampleDuration(len(deltas[index].PCM)/2, plan.PCMFormat.SampleRate)
				metadata.HasEnd = true
				break
			}
		}
	}
	if !metadata.HasEnd {
		metadata.TimelineEnd = metadata.TimelineStart + roomReplaySampleDuration(sampleCount, plan.PCMFormat.SampleRate)
		metadata.HasEnd = true
	}
}

func loadRoomReplayParticipantPCM(plan RoomReplayPlan, participantID string, artifact RoomReplayArtifact, metadata roomReplayAudioStreamMetadata, role string) (RoomReplayAudioStream, error) {
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
