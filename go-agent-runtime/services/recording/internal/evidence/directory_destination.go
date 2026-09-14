package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
)

const (
	evidenceFileMode      = 0o600
	evidenceDirectoryMode = 0o755
)

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

const claimFileMode = 0o600

type destinationClaim struct {
	destination string
	kind        recording.ClaimKind
	lockPath    string
	lock        *os.File

	mu       sync.Mutex
	released bool
}

const claimParentDirectoryMode = 0o755

func Claim(options recording.ClaimOptions) (recording.DestinationClaim, error) {
	if options.Kind != recording.ClaimKindCapture && options.Kind != recording.ClaimKindDirectory {
		return nil, &recording.ClaimError{Destination: options.Destination, Err: fmt.Errorf("unsupported claim kind %q", options.Kind)}
	}
	destination := filepath.Clean(options.Destination)
	if destination == "." || options.Destination == "" {
		return nil, &recording.ClaimError{Destination: options.Destination, Err: errors.New("destination is required")}
	}
	if err := os.MkdirAll(filepath.Dir(destination), claimParentDirectoryMode); err != nil {
		return nil, &recording.ClaimError{Destination: destination, Err: fmt.Errorf("prepare parent directory: %w", err)}
	}
	if err := validateClaimDestination(destination, options.Kind); err != nil {
		return nil, &recording.ClaimError{Destination: destination, Err: err}
	}
	lockPath := destination + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, claimFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, &recording.ClaimError{Destination: destination, Err: recording.ErrLiveEvidenceClaimed, Holder: readClaimHolder(lockPath)}
		}
		return nil, &recording.ClaimError{Destination: destination, Err: err}
	}
	metadata, marshalErr := json.Marshal(recording.ClaimHolder{PID: os.Getpid(), StartedAt: time.Now().UTC()})
	if marshalErr == nil {
		_, marshalErr = lock.Write(metadata)
	}
	if marshalErr == nil {
		marshalErr = lock.Sync()
	}
	if marshalErr != nil {
		return nil, &recording.ClaimError{Destination: destination, Err: errors.Join(marshalErr, lock.Close(), os.Remove(lockPath))}
	}
	return &destinationClaim{destination: destination, kind: options.Kind, lockPath: lockPath, lock: lock}, nil
}

func validateClaimDestination(destination string, kind recording.ClaimKind) error {
	if kind == recording.ClaimKindDirectory {
		return inspectEvidenceDestination(destination)
	}
	if info, err := os.Lstat(destination); err == nil {
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("capture destination must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func readClaimHolder(path string) *recording.ClaimHolder {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var holder recording.ClaimHolder
	if json.Unmarshal(data, &holder) != nil || holder.PID <= 0 || holder.StartedAt.IsZero() {
		return nil
	}
	return &holder
}

func (c *destinationClaim) Destination() string {
	if c == nil {
		return ""
	}
	return c.destination
}

func (c *destinationClaim) ownsLocked() error {
	if c == nil || c.lock == nil {
		return recording.ErrClaimLost
	}
	lockInfo, err := c.lock.Stat()
	if err != nil {
		return fmt.Errorf("%w: inspect claim: %w", recording.ErrClaimLost, err)
	}
	pathInfo, err := os.Lstat(c.lockPath)
	if err != nil || !os.SameFile(lockInfo, pathInfo) {
		return recording.ErrClaimLost
	}
	return nil
}

func (c *destinationClaim) Publish(flush func(string) error) (publishErr error) {
	if c == nil || flush == nil {
		return recording.ErrClaimLost
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.released {
		return recording.ErrClaimLost
	}
	if err := c.ownsLocked(); err != nil {
		return &recording.ClaimError{Destination: c.destination, Err: err}
	}
	if c.kind == recording.ClaimKindDirectory {
		return flush(c.destination)
	}
	temporary, err := os.CreateTemp(filepath.Dir(c.destination), "."+filepath.Base(c.destination)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			publishErr = errors.Join(publishErr, removeErr)
		}
	}()
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := flush(temporaryPath); err != nil {
		return err
	}
	if err := c.ownsLocked(); err != nil {
		return &recording.ClaimError{Destination: c.destination, Err: err}
	}
	if err := os.Link(temporaryPath, c.destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return &recording.ClaimError{Destination: c.destination, Err: recording.ErrLiveEvidenceClaimed}
		}
		return err
	}
	return nil
}

func (c *destinationClaim) Release() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.released {
		return nil
	}
	c.released = true
	ownershipErr := c.ownsLocked()
	closeErr := c.lock.Close()
	if ownershipErr != nil {
		return &recording.ClaimError{Destination: c.destination, Err: errors.Join(ownershipErr, closeErr)}
	}
	removeErr := os.Remove(c.lockPath)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}
