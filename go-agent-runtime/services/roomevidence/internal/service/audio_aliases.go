package service

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	roomanalysis "github.com/portpowered/go-agent-harness/go-audio/pkg/analysis/room"
)

type (
	RoomReplayPlan                     = roomevidence.RoomReplayPlan
	RoomReplayPCMFormat                = roomevidence.RoomReplayPCMFormat
	RoomReplayArtifact                 = roomevidence.RoomReplayArtifact
	RoomReplayParticipant              = roomevidence.RoomReplayParticipant
	RoomReplayTimelineEvent            = roomevidence.RoomReplayTimelineEvent
	RoomReplayAudioDelta               = roomevidence.RoomReplayAudioDelta
	RoomReplayDeltaReconstructionError = roomevidence.RoomReplayDeltaReconstructionError
	RoomReplayAudioStream              = roomevidence.RoomReplayAudioStream
	RoomReplayAudioParticipant         = roomevidence.RoomReplayAudioParticipant
	RoomReplayAudioAnnotation          = roomevidence.RoomReplayAudioAnnotation
	RoomReplayAudioBundle              = roomevidence.RoomReplayAudioBundle
	RoomReplayToleranceProfile         = roomevidence.RoomReplayToleranceProfile
	ParticipantKind                    = roomevidence.ParticipantKind
	roomReplayJSONObject               map[string]json.RawMessage
)

const (
	RoomReplayBundleManifestPath = roomevidence.RoomReplayBundleManifestPath
	RoomReplayBundleMismatch     = roomevidence.RoomReplayBundleMismatch
	RoomReplayBundleIncomplete   = roomevidence.RoomReplayBundleIncomplete
	roomReplayAudioRoleSent      = "sent"
	roomReplayAudioRoleReceived  = "received"

	roomReplayArtifactRoleWAV         = roomevidence.RoomReplayAudioRoleWAV
	roomReplayArtifactRoleDiagnostics = roomevidence.RoomReplayAudioRoleDiagnostics
	roomReplayArtifactRoleDeltas      = roomevidence.RoomReplayAudioRoleDeltas
	roomReplayArtifactRoleSentPCM     = roomevidence.RoomReplayAudioRoleSentPCM
	roomReplayArtifactRoleReceivedPCM = roomevidence.RoomReplayAudioRoleReceivedPCM
	roomReplayArtifactRoleEvents      = roomevidence.RoomReplayAudioRoleEvents
)

const (
	ErrInvalidRoomReplayBundle       = roomevidence.ErrInvalidRoomReplayBundle
	ErrRoomReplayBundleIncomplete    = roomevidence.ErrRoomReplayBundleIncomplete
	ErrRoomReplayDeltaReconstruction = roomevidence.ErrRoomReplayDeltaReconstruction
	ErrRoomReplayAudioTimeline       = roomevidence.ErrRoomReplayAudioTimeline
	ErrRoomReplayToleranceProfile    = roomevidence.ErrRoomReplayToleranceProfile
)

func DefaultRoomReplayToleranceProfile() RoomReplayToleranceProfile {
	return RoomReplayToleranceProfile{
		Name:         "suite-default",
		StreamConfig: roomanalysis.DefaultPCM16AnalysisConfig(),
		RoomConfig:   roomanalysis.DefaultPCM16RoomAnalysisConfig(),
	}
}
