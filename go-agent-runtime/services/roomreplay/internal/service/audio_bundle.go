package service

import (
	"encoding/json"
	"os"
)

// LoadAudioBundle parses the optional audio-analysis projection only after the
// ordinary replay service has admitted the source bundle and its artifacts.
func (s *Service) LoadAudioBundle(bundle string) (RoomReplayAudioBundle, error) {
	plan, err := s.Load(bundle)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	manifestData, err := os.ReadFile(plan.ManifestPath)
	if err != nil {
		return RoomReplayAudioBundle{}, roomReplayAudioIncomplete("run-manifest.json", "", "readable manifest", err.Error(), err)
	}
	manifest, err := roomReplayObject(json.RawMessage(manifestData))
	if err != nil {
		return RoomReplayAudioBundle{}, roomReplayAudioMismatch("run-manifest.json", "", "JSON object", "invalid", err)
	}
	profile, err := parseRoomReplayToleranceProfile(manifest)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	participantObjects, err := roomReplayAudioParticipantObjects(manifest)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}

	result := RoomReplayAudioBundle{
		Plan:         plan,
		Format:       plan.PCMFormat,
		Tolerances:   profile,
		Participants: make([]RoomReplayAudioParticipant, 0, len(plan.Participants)),
	}
	streamParticipants := make(map[string]string, len(plan.Participants)*3)
	for _, participant := range plan.Participants {
		participantObject := participantObjects[participant.ID]
		resolved, err := loadRoomReplayAudioParticipant(plan, participant, participantObject)
		if err != nil {
			return RoomReplayAudioBundle{}, err
		}
		result.Participants = append(result.Participants, resolved)
		for _, stream := range []RoomReplayAudioStream{resolved.WAV, resolved.Sent, resolved.Received} {
			if owner, exists := streamParticipants[stream.StreamID]; exists {
				return RoomReplayAudioBundle{}, roomReplayAudioMismatch("streams."+stream.StreamID, "run-manifest.json", "unique stream identity", owner+" and "+participant.ID, nil)
			}
			streamParticipants[stream.StreamID] = participant.ID
		}
	}

	roomMixArtifact, ok := findRoomReplayArtifact(plan.Artifacts, "room:mix")
	if !ok {
		return RoomReplayAudioBundle{}, roomReplayAudioIncomplete("artifacts.room_mix", "", "validated room mix artifact", "missing", ErrRoomReplayBundleIncomplete)
	}
	roomMix, err := loadRoomReplayWAVStream(plan, roomMixArtifact, "room:mix", "room", "room-mix")
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	if err := validateRoomReplayAudioStreamTimeline(roomMix, plan, "room_mix"); err != nil {
		return RoomReplayAudioBundle{}, err
	}
	if owner, exists := streamParticipants[roomMix.StreamID]; exists {
		return RoomReplayAudioBundle{}, roomReplayAudioMismatch("streams."+roomMix.StreamID, "run-manifest.json", "unique stream identity", owner+" and room", nil)
	}
	result.RoomMix = roomMix
	streamParticipants[roomMix.StreamID] = "room"

	annotations, overlaps, barges, loudness, err := parseRoomReplayAudioAnnotations(manifest, plan, result.Participants, streamParticipants)
	if err != nil {
		return RoomReplayAudioBundle{}, err
	}
	result.Annotations = annotations
	result.Overlaps = overlaps
	result.BargeIns = barges
	result.Loudness = loudness
	return result, nil
}
