package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
)

func validateRoomReplayArtifacts(root string, refs []roomReplayArtifactRef, metadata map[string]roomReplayArtifactRef) ([]RoomReplayArtifact, map[string]RoomReplayArtifact, error) {
	validated := make([]RoomReplayArtifact, 0, len(refs))
	byPath := make(map[string]RoomReplayArtifact, len(refs))
	for _, ref := range refs {
		artifact, normalized, err := validateRoomReplayArtifact(root, ref, metadata)
		if err != nil {
			return nil, nil, err
		}
		if existing, exists := byPath[normalized]; exists {
			return nil, nil, newRoomReplayBundleError(RoomReplayBundleMismatch, "artifact ownership", normalized, "one logical owner", existing.Owner+" and "+ref.Owner, ErrInvalidRoomReplayBundle)
		}
		validated = append(validated, artifact)
		byPath[normalized] = artifact
	}
	return validated, byPath, nil
}

func validateRoomReplayArtifact(root string, ref roomReplayArtifactRef, metadata map[string]roomReplayArtifactRef) (RoomReplayArtifact, string, error) {
	absolute, normalized, err := safeRoomReplayPath(root, ref.Path)
	if err != nil {
		return RoomReplayArtifact{}, "", err
	}
	if normalized == roomReplayPathKey(RoomReplayBundleManifestPath) {
		return RoomReplayArtifact{}, "", newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, "artifact distinct from manifest", "run-manifest.json", ErrInvalidRoomReplayBundle)
	}
	declared := ref
	if value, ok := metadata[normalized]; ok {
		declared = value
	}
	if err := validateDeclaredRoomReplayArtifact(ref, normalized, declared); err != nil {
		return RoomReplayArtifact{}, "", err
	}
	info, err := statRoomReplayArtifact(ref, normalized, absolute, *declared.Size)
	if err != nil {
		return RoomReplayArtifact{}, "", err
	}
	actualDigest, err := roomReplayFileDigest(absolute)
	if err != nil {
		return RoomReplayArtifact{}, "", newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, "readable artifact", err.Error(), err)
	}
	if actualDigest != declared.SHA256 {
		return RoomReplayArtifact{}, "", newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, declared.SHA256, actualDigest, ErrInvalidRoomReplayBundle)
	}
	return RoomReplayArtifact{Name: ref.Name, Role: ref.Role, Owner: ref.Owner, Path: normalized, AbsolutePath: absolute, Size: info.Size(), SHA256: actualDigest}, normalized, nil
}

func validateDeclaredRoomReplayArtifact(ref roomReplayArtifactRef, normalized string, declared roomReplayArtifactRef) error {
	if declared.Size == nil || declared.SHA256 == "" {
		return newRoomReplayBundleError(RoomReplayBundleIncomplete, ref.Field, normalized, "declared size and sha256", "missing", ErrRoomReplayBundleIncomplete)
	}
	if len(declared.SHA256) != sha256.Size*2 {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, "64-character sha256 hex digest", declared.SHA256, ErrInvalidRoomReplayBundle)
	}
	if _, err := hex.DecodeString(declared.SHA256); err != nil {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, "sha256 hex digest", declared.SHA256, err)
	}
	if *declared.Size < 0 || *declared.Size > roomReplayMaxArtifactBytes {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, fmt.Sprintf("size from 0 to %d", roomReplayMaxArtifactBytes), fmt.Sprintf("size %d", *declared.Size), ErrInvalidRoomReplayBundle)
	}
	return nil
}

func statRoomReplayArtifact(ref roomReplayArtifactRef, normalized, absolute string, declaredSize int64) (os.FileInfo, error) {
	info, err := os.Lstat(absolute)
	if err != nil {
		kind := RoomReplayBundleMismatch
		if errors.Is(err, os.ErrNotExist) {
			kind = RoomReplayBundleIncomplete
		}
		return nil, newRoomReplayBundleError(kind, ref.Field, normalized, "declared artifact file", err.Error(), err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, "regular file", info.Mode().String(), ErrInvalidRoomReplayBundle)
	}
	if info.Size() > roomReplayMaxArtifactBytes {
		return nil, newRoomReplayBundleError(RoomReplayBundleMismatch, ref.Field, normalized, fmt.Sprintf("size at most %d", roomReplayMaxArtifactBytes), fmt.Sprintf("size %d", info.Size()), ErrInvalidRoomReplayBundle)
	}
	if info.Size() == declaredSize {
		return info, nil
	}
	kind := RoomReplayBundleMismatch
	cause := error(ErrInvalidRoomReplayBundle)
	if info.Size() < declaredSize {
		kind, cause = RoomReplayBundleIncomplete, ErrRoomReplayBundleIncomplete
	}
	return nil, newRoomReplayBundleError(kind, ref.Field, normalized, fmt.Sprintf("size %d", declaredSize), fmt.Sprintf("size %d", info.Size()), cause)
}

func validateRoomReplayCaptures(replayService replay.CaptureInspector, plan *RoomReplayPlan) error {
	if replayService == nil {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "participants.capture", "", "replay service", "unavailable", ErrInvalidRoomReplayBundle)
	}
	for index := range plan.Participants {
		participant := &plan.Participants[index]
		if participant.Kind == "human" {
			continue
		}
		if err := validateRoomReplayCapture(replayService, *participant); err != nil {
			return err
		}
	}
	return nil
}

func validateRoomReplayCapture(replayService replay.CaptureInspector, participant RoomReplayParticipant) error {
	if participant.Capture.AbsolutePath == "" {
		return newRoomReplayBundleError(RoomReplayBundleIncomplete, "participants["+participant.ID+"].capture", "", "provider capture", "missing", ErrRoomReplayBundleIncomplete)
	}
	if participant.Capture.Size > roomReplayMaxCaptureBytes {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+participant.ID+"].capture", participant.Capture.Path, fmt.Sprintf("capture size at most %d", roomReplayMaxCaptureBytes), fmt.Sprintf("size %d", participant.Capture.Size), ErrInvalidRoomReplayBundle)
	}
	inspection, err := replayService.InspectCapture(context.Background(), participant.Capture.AbsolutePath)
	if err != nil {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+participant.ID+"].capture", participant.Capture.Path, "valid session capture", err.Error(), err)
	}
	if inspection.Facts.EventCount == 0 {
		return newRoomReplayBundleError(RoomReplayBundleIncomplete, "participants["+participant.ID+"].capture.records", participant.Capture.Path, "at least one provider event", "empty", ErrRoomReplayBundleIncomplete)
	}
	if inspection.Provider != "" && participant.Provider != "" && !strings.EqualFold(inspection.Provider, participant.Provider) {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+participant.ID+"].provider", participant.Capture.Path, participant.Provider, inspection.Provider, ErrInvalidRoomReplayBundle)
	}
	if inspection.Model != "" && participant.Model != "" && inspection.Model != participant.Model {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+participant.ID+"].model", participant.Capture.Path, participant.Model, inspection.Model, ErrInvalidRoomReplayBundle)
	}
	if !inspection.Facts.RealtimeWebSocketReplayable {
		return newRoomReplayBundleError(RoomReplayBundleMismatch, "participants["+participant.ID+"].capture", participant.Capture.Path, "provider websocket payloads", string(inspection.Kind), ErrInvalidRoomReplayBundle)
	}
	return nil
}
