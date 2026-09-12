package service

import (
	"errors"
	"os"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplaybundle"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
)

// Service owns the private parser, filesystem admission, and integrity
// validation implementation. It has no invocation state and is safe to share
// as a constructor across independent hosts.
type Service struct{}

func New() *Service { return &Service{} }

// LoadRoomReplayPlan is retained only for the moved in-package behavior tests;
// hosts use the public Service contract through roomreplaybundle/wire.
func LoadRoomReplayPlan(bundle string) (RoomReplayPlan, error) {
	return New().Load(bundle)
}

func (s *Service) Load(bundle string) (RoomReplayPlan, error) {
	root, manifestPath, manifestRelative, err := resolveRoomReplayBundle(bundle)
	if err != nil {
		return RoomReplayPlan{}, err
	}
	data, err := readRoomReplayManifest(manifestPath)
	if err != nil {
		kind := RoomReplayBundleIncomplete
		if !errors.Is(err, os.ErrNotExist) {
			kind = RoomReplayBundleMismatch
		}
		return RoomReplayPlan{}, newRoomReplayBundleError(kind, "run-manifest.json", manifestRelative, "readable JSON manifest", err.Error(), err)
	}
	return validateRoomReplayManifest(root, manifestPath, data)
}

func (s *Service) ValidateOutput(plan RoomReplayPlan, destination string) error {
	return roomreplaybundle.ValidateRoomReplayOutput(plan, destination)
}

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
	return &roomreplaybundle.RoomReplayBundleError{
		Kind:     kind,
		Field:    field,
		Artifact: artifact,
		Expected: expected,
		Actual:   actual,
		Err:      replayCause,
	}
}

var _ roomreplaybundle.Service = (*Service)(nil)
