package audiobundle

import (
	"encoding/json"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay/internal/support"
)

type RoomReplayPlan = roomreplay.RoomReplayPlan
type RoomReplayArtifact = roomreplay.RoomReplayArtifact
type RoomReplayPCMFormat = roomreplay.RoomReplayPCMFormat
type RoomReplayParticipant = roomreplay.RoomReplayParticipant
type RoomReplayAudioBundle = roomreplay.RoomReplayAudioBundle
type RoomReplayAudioParticipant = roomreplay.RoomReplayAudioParticipant
type RoomReplayAudioStream = roomreplay.RoomReplayAudioStream
type RoomReplayAudioAnnotation = roomreplay.RoomReplayAudioAnnotation
type RoomReplayAudioDelta = roomreplay.RoomReplayAudioDelta
type RoomReplayToleranceProfile = roomreplay.RoomReplayToleranceProfile
type RoomReplayDeltaReconstructionError = roomreplay.RoomReplayDeltaReconstructionError
type roomReplayJSONObject = support.JSONObject

const (
	roomReplayStreamsPerParticipant   = 3
	RoomReplayBundleMismatch          = roomreplay.RoomReplayBundleMismatch
	RoomReplayBundleIncomplete        = roomreplay.RoomReplayBundleIncomplete
	ErrInvalidRoomReplayBundle        = roomreplay.ErrInvalidRoomReplayBundle
	ErrRoomReplayBundleIncomplete     = roomreplay.ErrRoomReplayBundleIncomplete
	ErrRoomReplayDeltaReconstruction  = roomreplay.ErrRoomReplayDeltaReconstruction
	ErrRoomReplayAudioTimeline        = roomreplay.ErrRoomReplayAudioTimeline
	ErrRoomReplayToleranceProfile     = roomreplay.ErrRoomReplayToleranceProfile
	roomReplayArtifactRoleWAV         = roomreplay.ArtifactRoleWAV
	roomReplayArtifactRoleDiagnostics = roomreplay.ArtifactRoleDiagnostics
	roomReplayArtifactRoleDeltas      = roomreplay.ArtifactRoleDeltas
	roomReplayArtifactRoleSentPCM     = roomreplay.ArtifactRoleSentPCM
	roomReplayArtifactRoleReceivedPCM = roomreplay.ArtifactRoleReceivedPCM
	roomReplayArtifactRoleEvents      = roomreplay.ArtifactRoleEvents
	roomReplayArtifactRoleCapture     = roomreplay.ArtifactRoleCapture
	roomReplayAudioRoleWAV            = "wav"
	roomReplayAudioRoleSent           = "sent"
	roomReplayAudioRoleReceived       = "received"
	roomReplayJSONLInitialBufferBytes = 64 * 1024
	roomReplayJSONLMaxTokenBytes      = 4 * 1024 * 1024
)

func Load(plan roomreplay.RoomReplayPlan) (roomreplay.RoomReplayAudioBundle, error) {
	manifestData, err := os.ReadFile(plan.ManifestPath)
	if err != nil {
		return RoomReplayAudioBundle{}, roomReplayAudioIncomplete("run-manifest.json", "", "readable manifest", err.Error(), err)
	}
	manifest, err := support.Object(json.RawMessage(manifestData))
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
	streamParticipants := make(map[string]string, len(plan.Participants)*roomReplayStreamsPerParticipant)
	for _, participant := range plan.Participants {
		resolved, err := loadRoomReplayAudioParticipant(plan, participant, participantObjects[participant.ID])
		if err != nil {
			return RoomReplayAudioBundle{}, err
		}
		result.Participants = append(result.Participants, resolved)
		if err := registerParticipantStreams(streamParticipants, participant.ID, resolved); err != nil {
			return RoomReplayAudioBundle{}, err
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

func registerParticipantStreams(owners map[string]string, participantID string, participant RoomReplayAudioParticipant) error {
	for _, stream := range []RoomReplayAudioStream{participant.WAV, participant.Sent, participant.Received} {
		if owner, exists := owners[stream.StreamID]; exists {
			return roomReplayAudioMismatch("streams."+stream.StreamID, "run-manifest.json", "unique stream identity", owner+" and "+participantID, nil)
		}
		owners[stream.StreamID] = participantID
	}
	return nil
}

func findRoomReplayArtifact(artifacts []RoomReplayArtifact, owner string) (RoomReplayArtifact, bool) {
	for _, artifact := range artifacts {
		if artifact.Owner == owner {
			return artifact, true
		}
	}
	return RoomReplayArtifact{}, false
}

func roomReplayObject(raw json.RawMessage) (roomReplayJSONObject, error) {
	return support.Object(raw)
}

func firstRoomReplayStringField(primary, fallback roomReplayJSONObject, names ...string) (string, bool, error) {
	return support.FirstString(primary, fallback, names...)
}

func optionalRoomReplayStringField(primary, fallback roomReplayJSONObject, names ...string) (string, bool) {
	value, present, err := firstRoomReplayStringField(primary, fallback, names...)
	if err != nil {
		return "", present
	}
	return value, present
}

func decodeRoomReplayString(raw json.RawMessage) (string, bool) { return support.String(raw) }

func errOrDefault(err, fallback error) error { return support.ErrOrDefault(err, fallback) }

func newRoomReplayBundleError(kind roomreplay.RoomReplayBundleErrorKind, field, artifact, expected, actual string, cause error) error {
	return support.BundleError(kind, field, artifact, expected, actual, cause)
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
	return newRoomReplayBundleError(RoomReplayBundleMismatch, field, artifact, expected, actual, support.Join(ErrRoomReplayAudioTimeline, ErrInvalidRoomReplayBundle))
}
