package service

import "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplaybundle"

type RoomReplayBundleErrorKind = roomreplaybundle.RoomReplayBundleErrorKind
type RoomReplayBundleError = roomreplaybundle.RoomReplayBundleError
type RoomReplayPlan = roomreplaybundle.RoomReplayPlan
type RoomReplayPCMFormat = roomreplaybundle.RoomReplayPCMFormat
type RoomReplayArtifact = roomreplaybundle.RoomReplayArtifact
type RoomReplayParticipant = roomreplaybundle.RoomReplayParticipant
type RoomReplayTimelineEvent = roomreplaybundle.RoomReplayTimelineEvent

const (
	RoomReplayBundleSchemaVersion = roomreplaybundle.RoomReplayBundleSchemaVersion
	RoomReplayBundleManifestPath  = roomreplaybundle.RoomReplayBundleManifestPath
	RoomReplayBundleMismatch      = roomreplaybundle.RoomReplayBundleMismatch
	RoomReplayBundleIncomplete    = roomreplaybundle.RoomReplayBundleIncomplete
)

const (
	ErrInvalidRoomReplayBundle    = roomreplaybundle.ErrInvalidRoomReplayBundle
	ErrRoomReplayBundleIncomplete = roomreplaybundle.ErrRoomReplayBundleIncomplete
)
