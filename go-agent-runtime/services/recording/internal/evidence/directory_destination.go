package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	evidenceFileMode      = 0o600
	evidenceDirectoryMode = 0o755
)

func claimEvidenceDestination(raw string, observed time.Time) (string, *os.File, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil, &transcript.RecordingError{Kind: transcript.ErrRecordingDestination, Operation: "validate destination", Cause: errors.New("destination is required")}
	}
	destination := filepath.Clean(raw)
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, evidenceDirectoryMode); err != nil {
		return "", nil, evidenceDestinationError(destination, "prepare destination", err)
	}
	if err := inspectEvidenceDestination(destination); err != nil {
		return "", nil, err
	}
	lockPath := destination + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, evidenceFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", nil, fmt.Errorf("%w: %s", recording.ErrLiveEvidenceClaimed, destination)
		}
		return "", nil, evidenceDestinationError(destination, "claim destination", err)
	}
	metadata, marshalErr := json.Marshal(struct {
		SessionID   string `json:"session_id,omitempty"`
		Participant string `json:"participant_id,omitempty"`
		StartedAt   string `json:"started_at,omitempty"`
	}{StartedAt: observed.UTC().Format(time.RFC3339Nano)})
	if marshalErr == nil {
		_, marshalErr = lock.Write(metadata)
	}
	if marshalErr == nil {
		marshalErr = lock.Sync()
	}
	if marshalErr != nil {
		return "", nil, errors.Join(evidenceDestinationError(destination, "write destination claim", marshalErr), releaseEvidenceClaim(lock, lockPath))
	}
	return destination, lock, nil
}

func inspectEvidenceDestination(destination string) error {
	info, err := os.Lstat(destination)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return evidenceDestinationError(destination, "validate destination", errors.New("recording destination must not be a symlink"))
	case err == nil && !info.IsDir():
		return evidenceDestinationError(destination, "validate destination", errors.New("recording destination must be a directory"))
	case err == nil:
		entries, readErr := os.ReadDir(destination)
		if readErr != nil {
			return evidenceDestinationError(destination, "inspect destination", readErr)
		}
		if len(entries) != 0 {
			return &transcript.RecordingError{Kind: transcript.ErrRecordingDestinationNotEmpty, Operation: "validate destination", Path: destination, Cause: errors.New("destination is not empty")}
		}
	case !errors.Is(err, os.ErrNotExist):
		return evidenceDestinationError(destination, "inspect destination", err)
	}
	return nil
}

func evidenceDestinationError(path, operation string, cause error) error {
	return &transcript.RecordingError{Kind: transcript.ErrRecordingDestination, Operation: operation, Path: path, Cause: cause}
}

func releaseEvidenceClaim(lock *os.File, path string) error {
	if lock == nil {
		return nil
	}
	ownershipErr := evidenceClaimOwns(lock, path)
	closeErr := lock.Close()
	if ownershipErr != nil {
		return errors.Join(ownershipErr, closeErr)
	}
	removeErr := os.Remove(path)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}
