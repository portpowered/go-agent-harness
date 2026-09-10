package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"io"
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

const evidenceBudgetMessage = "recording evidence budget exceeded"

type evidenceBudgetMarker struct{}

func (evidenceBudgetMarker) Error() string { return evidenceBudgetMessage }

type evidenceBudgetError struct {
	resource string
	bytes    int64
	items    int64
	maxBytes int64
	maxItems int64
}

func (e *evidenceBudgetError) Error() string {
	if e == nil {
		return evidenceBudgetMessage
	}
	return fmt.Sprintf("%s: %s bytes=%d/%d items=%d/%d", evidenceBudgetMessage, e.resource, e.bytes, e.maxBytes, e.items, e.maxItems)
}

func (e *evidenceBudgetError) Is(target error) bool {
	_, marker := target.(evidenceBudgetMarker)
	return marker || target == io.ErrShortBuffer
}

type evidenceResourceBudget struct {
	limits recording.ResourceLimits

	transcriptBytes int64
	transcriptItems int64
	audioBytes      int64
	audioItems      int64
	sidecarBytes    int64
	sidecarItems    int64
	metadataBytes   int64
	metadataItems   int64
	terminalBytes   int64
	terminalItems   int64
}

func newEvidenceResourceBudget(input recording.ResourceLimits) (evidenceResourceBudget, error) {
	limits, err := normalizeResourceLimits(input)
	if err != nil {
		return evidenceResourceBudget{}, err
	}
	return evidenceResourceBudget{limits: limits}, nil
}

func normalizeResourceLimits(input recording.ResourceLimits) (recording.ResourceLimits, error) {
	limits := recording.ResourceLimits{
		TranscriptBytes: recording.DefaultTranscriptBytes,
		TranscriptItems: recording.DefaultTranscriptItems,
		AudioBytes:      recording.DefaultAudioBytes,
		AudioItems:      recording.DefaultAudioItems,
		SidecarBytes:    recording.DefaultSidecarBytes,
		SidecarItems:    recording.DefaultSidecarItems,
		MetadataBytes:   recording.DefaultMetadataBytes,
		MetadataItems:   recording.DefaultMetadataItems,
		TerminalBytes:   recording.DefaultTerminalBytes,
		TerminalItems:   recording.DefaultTerminalItems,
		ProviderBytes:   recording.DefaultProviderBytes,
		ProviderItems:   recording.DefaultProviderItems,
	}
	if err := applyLimit(&limits.TranscriptBytes, input.TranscriptBytes, recording.DefaultTranscriptBytes); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("transcript byte limit: %w", err)
	}
	if err := applyLimit(&limits.TranscriptItems, input.TranscriptItems, recording.DefaultTranscriptItems); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("transcript item limit: %w", err)
	}
	if err := applyLimit(&limits.AudioBytes, input.AudioBytes, recording.DefaultAudioBytes); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("audio byte limit: %w", err)
	}
	if err := applyLimit(&limits.AudioItems, input.AudioItems, recording.DefaultAudioItems); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("audio item limit: %w", err)
	}
	if err := applyLimit(&limits.SidecarBytes, input.SidecarBytes, recording.DefaultSidecarBytes); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("sidecar byte limit: %w", err)
	}
	if err := applyLimit(&limits.SidecarItems, input.SidecarItems, recording.DefaultSidecarItems); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("sidecar item limit: %w", err)
	}
	if err := applyLimit(&limits.MetadataBytes, input.MetadataBytes, recording.DefaultMetadataBytes); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("metadata byte limit: %w", err)
	}
	if err := applyLimit(&limits.MetadataItems, input.MetadataItems, recording.DefaultMetadataItems); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("metadata item limit: %w", err)
	}
	if err := applyLimit(&limits.TerminalBytes, input.TerminalBytes, recording.DefaultTerminalBytes); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("terminal byte limit: %w", err)
	}
	if err := applyLimit(&limits.TerminalItems, input.TerminalItems, recording.DefaultTerminalItems); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("terminal item limit: %w", err)
	}
	if err := applyLimit(&limits.ProviderBytes, input.ProviderBytes, recording.DefaultProviderBytes); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("provider byte limit: %w", err)
	}
	if err := applyLimit(&limits.ProviderItems, input.ProviderItems, recording.DefaultProviderItems); err != nil {
		return recording.ResourceLimits{}, fmt.Errorf("provider item limit: %w", err)
	}
	return limits, nil
}

func applyLimit(destination *int64, requested, maximum int64) error {
	if requested < 0 {
		return errors.New("must be zero or positive")
	}
	if requested > 0 && requested < *destination {
		*destination = requested
	}
	if *destination > maximum {
		*destination = maximum
	}
	return nil
}

func (b *evidenceResourceBudget) reserveTranscript(bytes, items int64, terminal bool) error {
	if terminal {
		return reserveResource("terminal transcript", bytes, items, &b.terminalBytes, &b.terminalItems, b.limits.TerminalBytes, b.limits.TerminalItems)
	}
	return reserveResource("transcript", bytes, items, &b.transcriptBytes, &b.transcriptItems, b.limits.TranscriptBytes, b.limits.TranscriptItems)
}

func (b *evidenceResourceBudget) reserveAudioWithTranscript(audioBytes, transcriptBytes, audioItems int64) error {
	if err := checkResource("audio", audioBytes, audioItems, b.audioBytes, b.audioItems, b.limits.AudioBytes, b.limits.AudioItems); err != nil {
		return err
	}
	if err := checkResource("transcript", transcriptBytes, 1, b.transcriptBytes, b.transcriptItems, b.limits.TranscriptBytes, b.limits.TranscriptItems); err != nil {
		return err
	}
	b.audioBytes += audioBytes
	b.audioItems += audioItems
	b.transcriptBytes += transcriptBytes
	b.transcriptItems++
	return nil
}

func (b *evidenceResourceBudget) reserveSidecar(bytes, items int64) error {
	return reserveResource("sidecar", bytes, items, &b.sidecarBytes, &b.sidecarItems, b.limits.SidecarBytes, b.limits.SidecarItems)
}

func (b *evidenceResourceBudget) reserveMetadata(bytes, items int64) error {
	return reserveResource("metadata", bytes, items, &b.metadataBytes, &b.metadataItems, b.limits.MetadataBytes, b.limits.MetadataItems)
}

func reserveResource(resource string, bytes, items int64, usedBytes, usedItems *int64, maxBytes, maxItems int64) error {
	if err := checkResource(resource, bytes, items, *usedBytes, *usedItems, maxBytes, maxItems); err != nil {
		return err
	}
	*usedBytes += bytes
	*usedItems += items
	return nil
}

func checkResource(resource string, bytes, items, usedBytes, usedItems, maxBytes, maxItems int64) error {
	if bytes < 0 || items < 0 || bytes > maxBytes-usedBytes || items > maxItems-usedItems {
		return &evidenceBudgetError{resource: resource, bytes: bytes + usedBytes, items: items + usedItems, maxBytes: maxBytes, maxItems: maxItems}
	}
	return nil
}

func isEvidenceBudgetError(err error) bool {
	return errors.Is(err, evidenceBudgetMarker{})
}
