package service

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"

type RoomReplayBundleErrorKind = roomreplay.RoomReplayBundleErrorKind
type RoomReplayBundleError = roomreplay.RoomReplayBundleError
type RoomReplayPlan = roomreplay.RoomReplayPlan
type RoomReplayPCMFormat = roomreplay.RoomReplayPCMFormat
type RoomReplayArtifact = roomreplay.RoomReplayArtifact
type RoomReplayParticipant = roomreplay.RoomReplayParticipant
type RoomReplayTimelineEvent = roomreplay.RoomReplayTimelineEvent

const (
	RoomReplayBundleSchemaVersion = roomreplay.RoomReplayBundleSchemaVersion
	RoomReplayBundleManifestPath  = roomreplay.RoomReplayBundleManifestPath
	RoomReplayBundleMismatch      = roomreplay.RoomReplayBundleMismatch
	RoomReplayBundleIncomplete    = roomreplay.RoomReplayBundleIncomplete
)

const (
	ErrInvalidRoomReplayBundle    = roomreplay.ErrInvalidRoomReplayBundle
	ErrRoomReplayBundleIncomplete = roomreplay.ErrRoomReplayBundleIncomplete
)
