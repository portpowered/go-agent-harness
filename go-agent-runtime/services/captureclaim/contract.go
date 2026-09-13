// Package captureclaim defines durable, exclusive capture-destination
// reservation for embeddable runtimes. Filesystem and process details are
// supplied through explicit composition seams; the implementation is private.
package captureclaim

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"time"
)

// Suffix is appended to a capture destination for its in-progress claim.
const Suffix = ".lock"

const (
	// DefaultHolderObservationAttempts bounds a competing process's metadata
	// observation. The claim itself is atomic even while its JSON is partial.
	DefaultHolderObservationAttempts = 250
	// DefaultHolderObservationInterval keeps the default observation window
	// bounded at 250ms without making a host depend on an unbounded wait.
	DefaultHolderObservationInterval = time.Millisecond
)

type sentinel string

func (s sentinel) Error() string { return string(s) }

const (
	// ErrInvalidDestination identifies an empty capture destination.
	ErrInvalidDestination sentinel = "capture claim destination is required"
	// ErrDestinationOccupied identifies a destination that already contains an
	// artifact. A claim never replaces an existing artifact.
	ErrDestinationOccupied sentinel = "session recording destination is occupied"
	// ErrDestinationClaimed identifies a destination reserved by another
	// process.
	ErrDestinationClaimed sentinel = "session recording destination is already claimed"
	// ErrClaimLost identifies a claim whose sidecar inode is no longer owned by
	// the returned claim.
	ErrClaimLost sentinel = "session recording claim was lost"
)

// ClaimHolder is the non-secret identity stored beside an in-progress
// capture. It deliberately excludes process arguments, prompts, credentials,
// and capture data.
type ClaimHolder struct {
	RequestedPath string `json:"requested_path"`
	PID           int    `json:"pid"`
	Host          string `json:"host"`
	StartedAtUTC  string `json:"started_at_utc"`
}

// Holder is a compatibility alias for ClaimHolder.
type Holder = ClaimHolder

// ClaimError preserves a stable classification, normalized destination,
// optional redacted holder, and the first lower-level cause.
type ClaimError struct {
	Kind   error
	Path   string
	Holder *ClaimHolder
	Err    error
}

func (e *ClaimError) Error() string {
	if e == nil {
		return "session recording destination is unavailable"
	}
	switch {
	case errors.Is(e.Kind, ErrDestinationOccupied):
		return fmt.Sprintf("session recording destination %q is occupied; an existing capture will not be replaced", e.Path)
	case errors.Is(e.Kind, ErrDestinationClaimed):
		holder := "holder identity unavailable"
		if e.Holder != nil {
			holder = fmt.Sprintf("pid=%d host=%q started_at=%q", e.Holder.PID, e.Holder.Host, e.Holder.StartedAtUTC)
		}
		return fmt.Sprintf("session recording destination %q is already claimed by %s", e.Path, holder)
	case errors.Is(e.Kind, ErrClaimLost):
		if e.Err == nil {
			return fmt.Sprintf("session recording destination %q claim was lost", e.Path)
		}
		return fmt.Sprintf("session recording destination %q claim was lost: %v", e.Path, e.Err)
	}
	if e.Err == nil {
		return fmt.Sprintf("session recording destination %q is unavailable", e.Path)
	}
	return fmt.Sprintf("session recording destination %q is unavailable: %v", e.Path, e.Err)
}

func (e *ClaimError) Unwrap() error {
	if e == nil {
		return nil
	}
	return errors.Join(e.Kind, e.Err)
}

// File is the narrow file capability required by the claim service. A host
// can inject a deterministic implementation without exposing os.File across
// the public service boundary.
type File interface {
	io.Writer
	Name() string
	Sync() error
	Close() error
	Stat() (fs.FileInfo, error)
}

// FileSystem is the explicit filesystem seam used by reservation and
// publication. OpenFile flags follow the standard os package constants.
type FileSystem interface {
	MkdirAll(string, fs.FileMode) error
	Lstat(string) (fs.FileInfo, error)
	OpenFile(string, int, fs.FileMode) (File, error)
	ReadFile(string) ([]byte, error)
	CreateTemp(string, string) (File, error)
	Link(string, string) error
	Remove(string) error
	SameFile(fs.FileInfo, fs.FileInfo) bool
}

// Clock supplies wall-clock metadata and bounded observation delays.
type Clock interface {
	Now() time.Time
	Sleep(time.Duration)
}

// Host resolves the redacted host identity stored in a claim.
type Host interface {
	Hostname() (string, error)
}

// Process resolves the current process identity stored in a claim.
type Process interface {
	PID() int
}

// Dependencies are optional explicit composition seams. Zero-valued fields
// use the private OS-backed defaults installed by the Wire package; callers
// that need deterministic failure/order controls provide all relevant seams.
type Dependencies struct {
	FileSystem                FileSystem
	Clock                     Clock
	Host                      Host
	Process                   Process
	HolderObservationAttempts int
	HolderObservationInterval time.Duration
}

// Claim owns one exclusive destination reservation and its publication
// lifecycle. The implementation retains all mutable ownership state.
type Claim interface {
	Path() string
	Publish(func(string) error) error
	Release() error
}

// Service admits capture destinations and returns opaque lifecycle claims.
// Construction is inert; no filesystem or process state is touched until
// Acquire or ObserveHolder is called.
type Service interface {
	Acquire(string) (Claim, error)
	ObserveHolder(string) *ClaimHolder
}
