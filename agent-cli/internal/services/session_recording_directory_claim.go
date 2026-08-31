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

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

const sessionRecordingDirectoryClaimSuffix = ".lock"

var (
	// ErrSessionRecordingDirectoryClaimed identifies a record directory that is
	// reserved by another session process.
	ErrSessionRecordingDirectoryClaimed = errors.New("session recording directory is already claimed")
	// ErrSessionRecordingDirectoryClaimLost identifies a recorder that no
	// longer owns its directory claim at finalization time.
	ErrSessionRecordingDirectoryClaimLost = errors.New("session recording directory claim was lost")
	// ErrSessionRecordingDirectorySymlink identifies a symlink supplied as a
	// record directory. Symlinks are rejected even when their target is empty.
	ErrSessionRecordingDirectorySymlink = errors.New("session recording directory must not be a symlink")
	// ErrSessionRecordingDirectoryNotDirectory identifies a non-directory
	// record destination.
	ErrSessionRecordingDirectoryNotDirectory = errors.New("session recording directory must be a directory")
)

// SessionRecordingDirectoryClaimError reports a competing directory owner or
// a claim that could not be retained. The holder contains only non-secret
// diagnostics, matching single-file recording claims.
type SessionRecordingDirectoryClaimError struct {
	Kind   error
	Path   string
	Holder *SessionRecordingClaimHolder
	Err    error
}

func (e *SessionRecordingDirectoryClaimError) Error() string {
	if e == nil {
		return "session recording directory is unavailable"
	}
	if errors.Is(e.Kind, ErrSessionRecordingDirectoryClaimed) {
		holder := "holder identity unavailable"
		if e.Holder != nil {
			holder = fmt.Sprintf("pid=%d host=%q started_at=%q", e.Holder.PID, e.Holder.Host, e.Holder.StartedAtUTC)
		}
		return fmt.Sprintf("session recording directory %q is already claimed by %s", e.Path, holder)
	}
	if errors.Is(e.Kind, ErrSessionRecordingDirectoryClaimLost) {
		return fmt.Sprintf("session recording directory %q claim was lost", e.Path)
	}
	if e.Err == nil {
		return fmt.Sprintf("session recording directory %q is unavailable", e.Path)
	}
	return fmt.Sprintf("session recording directory %q is unavailable: %v", e.Path, e.Err)
}

func (e *SessionRecordingDirectoryClaimError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(e.Kind, e.Err)
}

type sessionRecordingDirectoryClaim struct {
	path     string
	lockPath string
	file     *os.File

	mu       sync.Mutex
	released bool
}

// ensureSessionRecordingDirectoryClaim reserves directory once for a
// composed invocation. The private option pointer lets image/audio wrappers
// retain one owner through runtime planning and finalization.
func ensureSessionRecordingDirectoryClaim(opts *SessionRunOptions, directory string) (*sessionRecordingDirectoryClaim, string, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, "", nil
	}
	path := filepath.Clean(directory)
	if opts != nil && opts.recordingDirectoryClaim != nil {
		claim := opts.recordingDirectoryClaim
		if claim.path != path {
			return nil, "", &SessionRecordingDirectoryClaimError{
				Kind: ErrSessionRecordingDirectoryClaimLost,
				Path: path,
				Err:  fmt.Errorf("claim is held for %q", claim.path),
			}
		}
		if err := claim.owns(); err != nil {
			return nil, "", &SessionRecordingDirectoryClaimError{Kind: ErrSessionRecordingDirectoryClaimLost, Path: path, Err: err}
		}
		return claim, path, nil
	}

	claim, err := acquireSessionRecordingDirectoryClaim(path)
	if err != nil {
		return nil, "", err
	}
	if opts != nil {
		opts.recordingDirectoryClaim = claim
	}
	return claim, path, nil
}

func acquireSessionRecordingDirectoryClaim(path string) (*sessionRecordingDirectoryClaim, error) {
	if strings.TrimSpace(path) == "" {
		return nil, recordingDestinationError(transcript.ErrRecordingDestination, "validate destination", path, errors.New("destination is required"))
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, recordingDestinationError(transcript.ErrRecordingDestination, "prepare destination", path, err)
	}

	lockPath := path + sessionRecordingDirectoryClaimSuffix
	if _, err := os.Lstat(lockPath); err == nil {
		return nil, &SessionRecordingDirectoryClaimError{
			Kind:   ErrSessionRecordingDirectoryClaimed,
			Path:   path,
			Holder: readSessionRecordingClaimHolder(lockPath),
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, &SessionRecordingDirectoryClaimError{Path: path, Err: fmt.Errorf("inspect claim: %w", err)}
	}

	// Validate and probe before creating the sidecar so an occupied or invalid
	// destination reports its own classification instead of a transient claim.
	if _, err := prepareSessionRecordingDestination(path); err != nil {
		return nil, err
	}

	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, &SessionRecordingDirectoryClaimError{
				Kind:   ErrSessionRecordingDirectoryClaimed,
				Path:   path,
				Holder: readSessionRecordingClaimHolder(lockPath),
			}
		}
		return nil, &SessionRecordingDirectoryClaimError{Path: path, Err: fmt.Errorf("claim destination: %w", err)}
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
	claimInfo, claimInfoErr := file.Stat()
	metadata, metadataErr := json.Marshal(holder)
	if metadataErr == nil && claimInfoErr != nil {
		metadataErr = claimInfoErr
	}
	if metadataErr == nil {
		_, metadataErr = file.Write(metadata)
	}
	if metadataErr == nil {
		metadataErr = file.Sync()
	}
	if closeErr := file.Close(); metadataErr == nil {
		metadataErr = closeErr
	}
	if metadataErr != nil {
		removeClaimSidecar(lockPath, claimInfo)
		return nil, &SessionRecordingDirectoryClaimError{Path: path, Err: fmt.Errorf("write claim metadata: %w", metadataErr)}
	}

	claimFile, err := os.OpenFile(lockPath, os.O_RDONLY, 0)
	if err != nil {
		removeClaimSidecar(lockPath, claimInfo)
		return nil, &SessionRecordingDirectoryClaimError{Path: path, Err: fmt.Errorf("retain claim: %w", err)}
	}
	claim := &sessionRecordingDirectoryClaim{path: path, lockPath: lockPath, file: claimFile}

	// Close the check-to-claim race: a directory may appear after the first
	// probe but before the sidecar is fully published. A claim never permits a
	// newly appeared non-empty or invalid destination to reach provider work.
	if _, err := prepareSessionRecordingDestination(path); err != nil {
		_ = claim.release()
		return nil, err
	}
	return claim, nil
}

func (c *sessionRecordingDirectoryClaim) owns() error {
	if c == nil {
		return ErrSessionRecordingDirectoryClaimLost
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ownsLocked()
}

func (c *sessionRecordingDirectoryClaim) ownsLocked() error {
	if c.released || c.file == nil {
		return ErrSessionRecordingDirectoryClaimLost
	}
	claimInfo, err := c.file.Stat()
	if err != nil {
		return fmt.Errorf("%w: inspect owner: %v", ErrSessionRecordingDirectoryClaimLost, err)
	}
	pathInfo, err := os.Lstat(c.lockPath)
	if err != nil {
		return fmt.Errorf("%w: inspect claim: %v", ErrSessionRecordingDirectoryClaimLost, err)
	}
	if !os.SameFile(claimInfo, pathInfo) {
		return ErrSessionRecordingDirectoryClaimLost
	}
	return nil
}

// release removes only this claim's sidecar. It is intentionally idempotent
// because composed wrappers may each retain a cleanup defer.
func (c *sessionRecordingDirectoryClaim) release() error {
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
	if c.file != nil {
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
	}
	return errors.Join(errs...)
}

func removeClaimSidecar(path string, expected os.FileInfo) {
	if expected == nil {
		return
	}
	pathInfo, err := os.Lstat(path)
	if err == nil && os.SameFile(expected, pathInfo) {
		_ = os.Remove(path)
	}
}
