package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	raw := strings.TrimSpace(destination)
	if raw == "" {
		return errors.New("room replay output directory is required")
	}
	root, err := filepath.Abs(filepath.Clean(plan.BundlePath))
	if err != nil {
		return fmt.Errorf("resolve room replay bundle path: %w", err)
	}
	output, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		return fmt.Errorf("resolve room replay output path: %w", err)
	}
	relative, err := filepath.Rel(root, output)
	if err != nil {
		return fmt.Errorf("compare room replay source and output paths: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("room replay output directory %q must be outside source bundle %q", destination, plan.BundlePath)
	}
	return nil
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
