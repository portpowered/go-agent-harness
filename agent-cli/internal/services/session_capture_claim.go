package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const sessionRecordingClaimSuffix = ".lock"

var (
	// ErrSessionRecordingDestinationOccupied identifies a record path that
	// already contains an artifact. Record mode never replaces an artifact.
	ErrSessionRecordingDestinationOccupied = errors.New("session recording destination is occupied")
	// ErrSessionRecordingDestinationClaimed identifies a record path reserved
	// by another session process.
	ErrSessionRecordingDestinationClaimed = errors.New("session recording destination is already claimed")
	// ErrSessionRecordingClaimLost identifies a recorder that no longer owns
	// its claim at publication time.
	ErrSessionRecordingClaimLost = errors.New("session recording claim was lost")
)

// SessionRecordingClaimHolder contains the non-secret identity written next
// to an in-progress capture. It deliberately excludes process arguments,
// prompts, credentials, and capture data.
type SessionRecordingClaimHolder struct {
	RequestedPath string `json:"requested_path"`
	PID           int    `json:"pid"`
	Host          string `json:"host"`
	StartedAtUTC  string `json:"started_at_utc"`
}

// SessionRecordingClaimError reports why a capture destination could not be
// reserved. The holder is optional because another process may be observed
// while it is still writing its small metadata record.
type SessionRecordingClaimError struct {
	Kind   error
	Path   string
	Holder *SessionRecordingClaimHolder
	Err    error
}

func (e *SessionRecordingClaimError) Error() string {
	if e == nil {
		return "session recording destination is unavailable"
	}
	if errors.Is(e.Kind, ErrSessionRecordingDestinationOccupied) {
		return fmt.Sprintf("session recording destination %q is occupied; an existing capture will not be replaced", e.Path)
	}
	if errors.Is(e.Kind, ErrSessionRecordingDestinationClaimed) {
		holder := "holder identity unavailable"
		if e.Holder != nil {
			holder = fmt.Sprintf("pid=%d host=%q started_at=%q", e.Holder.PID, e.Holder.Host, e.Holder.StartedAtUTC)
		}
		return fmt.Sprintf("session recording destination %q is already claimed by %s", e.Path, holder)
	}
	if e.Err == nil {
		return fmt.Sprintf("session recording destination %q is unavailable", e.Path)
	}
	return fmt.Sprintf("session recording destination %q is unavailable: %v", e.Path, e.Err)
}

func (e *SessionRecordingClaimError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(e.Kind, e.Err)
}

type sessionRecordingClaim struct {
	path     string
	lockPath string
	file     *os.File

	mu       sync.Mutex
	released bool
}

// ensureSessionRecordingClaim reserves opts.RecordPath once. The pointer in
// SessionRunOptions lets nested image/audio/duration wrappers share one
// process claim without creating competing sidecars of their own.
func ensureSessionRecordingClaim(opts *SessionRunOptions) (*sessionRecordingClaim, error) {
	if opts == nil || strings.TrimSpace(opts.RecordPath) == "" {
		return nil, nil
	}
	if opts.recordingClaim != nil {
		opts.RecordPath = opts.recordingClaim.path
		return opts.recordingClaim, nil
	}

	path := filepath.Clean(opts.RecordPath)
	claim, err := acquireSessionRecordingClaim(path)
	if err != nil {
		return nil, err
	}
	opts.RecordPath = path
	opts.recordingClaim = claim
	return claim, nil
}

func acquireSessionRecordingClaim(path string) (*sessionRecordingClaim, error) {
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, &SessionRecordingClaimError{Path: path, Err: fmt.Errorf("prepare parent directory: %w", err)}
	}

	lockPath := path + sessionRecordingClaimSuffix
	if _, err := os.Lstat(lockPath); err == nil {
		return nil, &SessionRecordingClaimError{
			Kind:   ErrSessionRecordingDestinationClaimed,
			Path:   path,
			Holder: readSessionRecordingClaimHolder(lockPath),
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, &SessionRecordingClaimError{Path: path, Err: fmt.Errorf("inspect claim: %w", err)}
	}

	if _, err := os.Lstat(path); err == nil {
		return nil, &SessionRecordingClaimError{Kind: ErrSessionRecordingDestinationOccupied, Path: path}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, &SessionRecordingClaimError{Path: path, Err: fmt.Errorf("inspect destination: %w", err)}
	}

	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, &SessionRecordingClaimError{
				Kind:   ErrSessionRecordingDestinationClaimed,
				Path:   path,
				Holder: readSessionRecordingClaimHolder(lockPath),
			}
		}
		return nil, &SessionRecordingClaimError{Path: path, Err: fmt.Errorf("claim destination: %w", err)}
	}

	host, hostErr := os.Hostname()
	if hostErr != nil {
		host = "unknown"
	}
	holder := SessionRecordingClaimHolder{
		RequestedPath: path,
		PID:           os.Getpid(),
		Host:          host,
		StartedAtUTC:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	metadata, marshalErr := json.Marshal(holder)
	if marshalErr == nil {
		_, marshalErr = file.Write(metadata)
	}
	if marshalErr == nil {
		marshalErr = file.Sync()
	}
	if closeErr := file.Close(); marshalErr == nil {
		marshalErr = closeErr
	}
	if marshalErr != nil {
		_ = os.Remove(lockPath)
		return nil, &SessionRecordingClaimError{Path: path, Err: fmt.Errorf("write claim metadata: %w", marshalErr)}
	}
	// The destination can appear after the initial absence check but before the
	// claim metadata is complete. Re-check while this process owns the sidecar
	// so an already-published capture is rejected before provider work starts.
	if _, err := os.Lstat(path); err == nil {
		if removeErr := os.Remove(lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, &SessionRecordingClaimError{
				Kind: ErrSessionRecordingDestinationOccupied,
				Path: path,
				Err:  fmt.Errorf("release claim after destination appeared: %w", removeErr),
			}
		}
		return nil, &SessionRecordingClaimError{Kind: ErrSessionRecordingDestinationOccupied, Path: path}
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(lockPath)
		return nil, &SessionRecordingClaimError{Path: path, Err: fmt.Errorf("recheck destination: %w", err)}
	}

	// Reopen the sidecar so the claim retains an inode identity. Publication
	// and release compare that identity before touching the sidecar, preventing
	// a stale owner from removing or publishing through a replacement claim.
	claimFile, err := os.OpenFile(lockPath, os.O_RDONLY, 0)
	if err != nil {
		_ = os.Remove(lockPath)
		return nil, &SessionRecordingClaimError{Path: path, Err: fmt.Errorf("retain claim: %w", err)}
	}
	return &sessionRecordingClaim{path: path, lockPath: lockPath, file: claimFile}, nil
}

func readSessionRecordingClaimHolder(path string) *SessionRecordingClaimHolder {
	// The sidecar is the atomic reservation, while its small JSON body is
	// written immediately afterward. Give an active writer a bounded window to
	// finish that metadata write so a competing command reports useful holder
	// identity instead of racing a transient partial file.
	for attempt := 0; attempt < 250; attempt++ {
		data, err := os.ReadFile(path)
		if err == nil {
			var holder SessionRecordingClaimHolder
			if json.Unmarshal(data, &holder) == nil && holder.PID > 0 && holder.RequestedPath != "" {
				return &holder
			}
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

// publish runs the existing recorder flush against a private same-directory
// temporary file, syncs and closes that file, then atomically links it into
// the requested destination. os.Link fails when the destination appeared in
// the meantime, so publication cannot overwrite a capture from another
// writer.
func (c *sessionRecordingClaim) publish(flush func(string) error) error {
	if c == nil {
		return errors.New("session recording claim is nil")
	}
	if flush == nil {
		return errors.New("session recording capture flush is nil")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ownsLocked(); err != nil {
		return err
	}

	temp, err := os.CreateTemp(filepath.Dir(c.path), "."+filepath.Base(c.path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create private capture artifact: %w", err)
	}
	tempPath := temp.Name()
	cleanupTemp := func() { _ = os.Remove(tempPath) }
	defer cleanupTemp()
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close private capture artifact: %w", err)
	}
	if err := flush(tempPath); err != nil {
		return err
	}

	// FlushToFile implementations own their write descriptor, so reopen the
	// completed file and sync it before exposing the destination.
	durable, err := os.OpenFile(tempPath, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open private capture artifact: %w", err)
	}
	syncErr := durable.Sync()
	closeErr := durable.Close()
	if syncErr != nil {
		return fmt.Errorf("sync private capture artifact: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close private capture artifact: %w", closeErr)
	}
	if err := c.ownsLocked(); err != nil {
		return err
	}
	if err := os.Link(tempPath, c.path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return &SessionRecordingClaimError{Kind: ErrSessionRecordingDestinationOccupied, Path: c.path}
		}
		return fmt.Errorf("publish session capture: %w", err)
	}
	return nil
}

func (c *sessionRecordingClaim) ownsLocked() error {
	if c.released || c.file == nil {
		return ErrSessionRecordingClaimLost
	}
	claimInfo, err := c.file.Stat()
	if err != nil {
		return fmt.Errorf("%w: inspect owner: %v", ErrSessionRecordingClaimLost, err)
	}
	pathInfo, err := os.Lstat(c.lockPath)
	if err != nil {
		return fmt.Errorf("%w: inspect claim: %v", ErrSessionRecordingClaimLost, err)
	}
	if !os.SameFile(claimInfo, pathInfo) {
		return ErrSessionRecordingClaimLost
	}
	return nil
}

// release removes only this claim's sidecar. A repeated release is a no-op,
// which lets outer pre-plan guards and the runtime finalizer safely overlap.
func (c *sessionRecordingClaim) release() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.released {
		return nil
	}
	c.released = true

	var errs []error
	if c.file == nil {
		return nil
	}
	if claimInfo, err := c.file.Stat(); err == nil {
		if pathInfo, statErr := os.Lstat(c.lockPath); statErr == nil && os.SameFile(claimInfo, pathInfo) {
			if removeErr := os.Remove(c.lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				errs = append(errs, removeErr)
			}
		}
	}
	if err := c.file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
