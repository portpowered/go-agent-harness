package service

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomaudio"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

type (
	RoomReplayPlan                     = roomaudio.RoomReplayPlan
	RoomReplayPCMFormat                = roomaudio.RoomReplayPCMFormat
	RoomReplayArtifact                 = roomaudio.RoomReplayArtifact
	RoomReplayParticipant              = roomaudio.RoomReplayParticipant
	RoomReplayTimelineEvent            = roomaudio.RoomReplayTimelineEvent
	RoomReplayAudioDelta               = roomaudio.RoomReplayAudioDelta
	RoomReplayDeltaReconstructionError = roomaudio.RoomReplayDeltaReconstructionError
	RoomReplayAudioStream              = roomaudio.RoomReplayAudioStream
	RoomReplayAudioParticipant         = roomaudio.RoomReplayAudioParticipant
	RoomReplayAudioAnnotation          = roomaudio.RoomReplayAudioAnnotation
	RoomReplayAudioBundle              = roomaudio.RoomReplayAudioBundle
	RoomReplayToleranceProfile         = roomaudio.RoomReplayToleranceProfile
	ParticipantKind                    = roomaudio.ParticipantKind
	roomReplayJSONObject               map[string]json.RawMessage
)

const (
	RoomReplayBundleManifestPath = roomaudio.RoomReplayBundleManifestPath
	RoomReplayBundleMismatch     = roomaudio.RoomReplayBundleMismatch
	RoomReplayBundleIncomplete   = roomaudio.RoomReplayBundleIncomplete
	roomReplayAudioRoleSent      = "sent"
	roomReplayAudioRoleReceived  = "received"

	roomReplayArtifactRoleWAV         = roomaudio.RoomReplayAudioRoleWAV
	roomReplayArtifactRoleDiagnostics = roomaudio.RoomReplayAudioRoleDiagnostics
	roomReplayArtifactRoleDeltas      = roomaudio.RoomReplayAudioRoleDeltas
	roomReplayArtifactRoleSentPCM     = roomaudio.RoomReplayAudioRoleSentPCM
	roomReplayArtifactRoleReceivedPCM = roomaudio.RoomReplayAudioRoleReceivedPCM
	roomReplayArtifactRoleEvents      = roomaudio.RoomReplayAudioRoleEvents
)

const (
	ErrInvalidRoomReplayBundle       = roomaudio.ErrInvalidRoomReplayBundle
	ErrRoomReplayBundleIncomplete    = roomaudio.ErrRoomReplayBundleIncomplete
	ErrRoomReplayDeltaReconstruction = roomaudio.ErrRoomReplayDeltaReconstruction
	ErrRoomReplayAudioTimeline       = roomaudio.ErrRoomReplayAudioTimeline
	ErrRoomReplayToleranceProfile    = roomaudio.ErrRoomReplayToleranceProfile
)

func DefaultRoomReplayToleranceProfile() RoomReplayToleranceProfile {
	return RoomReplayToleranceProfile{
		Name:         "suite-default",
		StreamConfig: roomanalysis.DefaultPCM16AnalysisConfig(),
		RoomConfig:   roomanalysis.DefaultPCM16RoomAnalysisConfig(),
	}
}
