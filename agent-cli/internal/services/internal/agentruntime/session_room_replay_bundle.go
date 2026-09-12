package agentruntime

import (
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplaybundle"
	roomReplayWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplaybundle/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
)

// Deprecated: roomreplaybundle owns the public room replay contract and
// admission policy. These aliases preserve unchanged room orchestration and
// scheduler source compatibility while adding no independent state.
const (
	RoomReplayBundleSchemaVersion = roomreplaybundle.RoomReplayBundleSchemaVersion
	RoomReplayBundleManifestPath  = roomreplaybundle.RoomReplayBundleManifestPath
)

var (
	ErrInvalidRoomReplayBundle    = roomreplaybundle.ErrInvalidRoomReplayBundle
	ErrRoomReplayBundleIncomplete = roomreplaybundle.ErrRoomReplayBundleIncomplete
	ErrRoomReplaySourceConflict   = roomreplaybundle.ErrRoomReplaySourceConflict
)

type RoomReplayBundleErrorKind = roomreplaybundle.RoomReplayBundleErrorKind
type RoomReplayBundleError = roomreplaybundle.RoomReplayBundleError
type RoomReplayPCMFormat = roomreplaybundle.RoomReplayPCMFormat
type RoomReplayArtifact = roomreplaybundle.RoomReplayArtifact
type RoomReplayParticipant = roomreplaybundle.RoomReplayParticipant
type RoomReplayTimelineEvent = roomreplaybundle.RoomReplayTimelineEvent
type RoomReplayPlan = roomreplaybundle.RoomReplayPlan

const (
	RoomReplayBundleMismatch   = roomreplaybundle.RoomReplayBundleMismatch
	RoomReplayBundleIncomplete = roomreplaybundle.RoomReplayBundleIncomplete
)

var roomReplayBundleService = roomReplayWire.NewService()

// Deprecated: use roomreplaybundle/wire.NewService from a host composition
// root. This adapter is retained for unchanged CLI room callers.
func LoadRoomReplayPlan(bundle string) (RoomReplayPlan, error) {
	return roomReplayBundleService.Load(bundle)
}

// Deprecated: use the injected roomreplaybundle.Service admission contract.
func ValidateRoomReplayBundle(bundle string) error {
	_, err := LoadRoomReplayPlan(bundle)
	return err
}

// Deprecated: output exclusion is owned by the roomreplaybundle service.
func ValidateRoomReplayOutput(plan RoomReplayPlan, destination string) error {
	return roomReplayBundleService.ValidateOutput(plan, destination)
}

// Deprecated: this helper only keeps the audio-bundle compatibility parser's
// error shape source-compatible. Bundle admission constructs these errors in
// the private runtime service.
func newRoomReplayBundleError(kind RoomReplayBundleErrorKind, field, artifact, expected, actual string, cause error) error {
	if kind == "" {
		kind = RoomReplayBundleMismatch
	}
	var replayCause error
	if kind == RoomReplayBundleIncomplete {
		replayCause = gateway.NewReplayIncompleteError(expected, actual, cause)
	} else {
		replayCause = gateway.NewReplayMismatchError(expected, actual, cause)
	}
	return &roomreplaybundle.RoomReplayBundleError{Kind: kind, Field: field, Artifact: artifact, Expected: expected, Actual: actual, Err: replayCause}
}

// Deprecated: room orchestration still owns the decision whether a replay
// source is present; the selected bundle is always admitted by the runtime
// service before it reaches scheduling.
func resolveRoomReplayPlan(opts RoomRunOptions) (RoomReplayPlan, bool, error) {
	if opts.ReplayPlan != nil {
		// A plan supplied by the host has already crossed the public runtime
		// admission boundary. The CLI adapter does not reimplement validation.
		return *opts.ReplayPlan, true, nil
	}
	path := strings.TrimSpace(opts.ReplayPath)
	if path == "" {
		return RoomReplayPlan{}, false, nil
	}
	plan, err := LoadRoomReplayPlan(path)
	return plan, true, err
}
