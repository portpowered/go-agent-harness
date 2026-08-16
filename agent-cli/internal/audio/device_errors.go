package audio

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	// ErrDeviceNotFound identifies an exact stable ID that is unavailable.
	ErrDeviceNotFound = errors.New("device not found")
	// ErrAmbiguousDeviceName identifies a name query with more than one match.
	ErrAmbiguousDeviceName = errors.New("ambiguous device name")
	// ErrNoDefaultDevice identifies a direction without a configured default.
	ErrNoDefaultDevice = errors.New("no default device")
	// ErrDeviceInUse identifies an exclusive device already opened elsewhere.
	ErrDeviceInUse = errors.New("device in use")

	// ErrInvalidDirection identifies a value outside input and output.
	ErrInvalidDirection = errors.New("invalid device direction")
	// ErrInvalidDeviceID identifies a malformed backend-qualified ID.
	ErrInvalidDeviceID = errors.New("invalid device ID")
	// ErrInvalidDevice identifies malformed enumerated device metadata.
	ErrInvalidDevice = errors.New("invalid device metadata")
)

// DeviceNotFoundError identifies the exact stable ID that could not be opened.
type DeviceNotFoundError struct {
	ID DeviceID
}

func (e *DeviceNotFoundError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("device %q not found; run agent devices list", e.ID)
}

func (e *DeviceNotFoundError) Unwrap() error { return ErrDeviceNotFound }

// NewDeviceNotFoundError creates a typed not-found error for id.
func NewDeviceNotFoundError(id DeviceID) *DeviceNotFoundError {
	return &DeviceNotFoundError{ID: id}
}

// AmbiguousDeviceNameError preserves the attempted substring and every
// matching device so callers never need to parse the human-readable message.
type AmbiguousDeviceNameError struct {
	Query      string
	Substring  string
	Candidates []Device
}

func (e *AmbiguousDeviceNameError) Error() string {
	if e == nil {
		return "<nil>"
	}
	parts := make([]string, len(e.Candidates))
	for i, candidate := range e.Candidates {
		parts[i] = fmt.Sprintf("%s (%q, %s)", candidate.ID, candidate.Display(), candidate.Direction)
	}
	query := e.Query
	if query == "" {
		query = e.Substring
	}
	return fmt.Sprintf("device name %q is ambiguous; candidates: %s", query, strings.Join(parts, "; "))
}

func (e *AmbiguousDeviceNameError) Unwrap() error { return ErrAmbiguousDeviceName }

// NewAmbiguousDeviceNameError creates an ambiguity error with candidates in a
// deterministic ID order and copies the caller's slice.
func NewAmbiguousDeviceNameError(query string, candidates []Device) *AmbiguousDeviceNameError {
	ordered := cloneDevices(candidates)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].ID != ordered[j].ID {
			return ordered[i].ID < ordered[j].ID
		}
		if ordered[i].Direction != ordered[j].Direction {
			return ordered[i].Direction < ordered[j].Direction
		}
		return ordered[i].Display() < ordered[j].Display()
	})
	return &AmbiguousDeviceNameError{Query: query, Substring: query, Candidates: ordered}
}

// CandidatesCopy returns a caller-owned copy of the structured candidates.
func (e *AmbiguousDeviceNameError) CandidatesCopy() []Device {
	if e == nil {
		return nil
	}
	return cloneDevices(e.Candidates)
}

// NoDefaultDeviceError identifies the direction with no configured default.
type NoDefaultDeviceError struct {
	Direction Direction
}

func (e *NoDefaultDeviceError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("no default %s device; run agent devices list", e.Direction)
}

func (e *NoDefaultDeviceError) Unwrap() error { return ErrNoDefaultDevice }

// NewNoDefaultDeviceError creates a typed no-default error for direction.
func NewNoDefaultDeviceError(direction Direction) *NoDefaultDeviceError {
	return &NoDefaultDeviceError{Direction: direction}
}

// DeviceInUseError identifies an exclusively opened stable ID.
type DeviceInUseError struct {
	ID DeviceID
}

func (e *DeviceInUseError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("device %q is in use", e.ID)
}

func (e *DeviceInUseError) Unwrap() error { return ErrDeviceInUse }

// NewDeviceInUseError creates a typed in-use error for id.
func NewDeviceInUseError(id DeviceID) *DeviceInUseError { return &DeviceInUseError{ID: id} }

// InvalidDirectionError identifies a direction outside input and output.
type InvalidDirectionError struct {
	Direction Direction
}

func (e *InvalidDirectionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%q is not a valid audio direction; want input or output", e.Direction)
}

func (e *InvalidDirectionError) Unwrap() error { return ErrInvalidDirection }

// InvalidDeviceIDError identifies which part of a backend-qualified ID was
// malformed. ID is set for failures found while parsing a complete ID.
type InvalidDeviceIDError struct {
	ID       DeviceID
	Backend  string
	NativeID string
	Reason   string
}

func (e *InvalidDeviceIDError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Reason == "" {
		return "invalid device ID"
	}
	return "invalid device ID: " + e.Reason
}

func (e *InvalidDeviceIDError) Unwrap() error { return ErrInvalidDeviceID }

// InvalidDeviceError identifies malformed device metadata returned by a
// registry snapshot.
type InvalidDeviceError struct {
	ID     DeviceID
	Reason string
}

func (e *InvalidDeviceError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Reason == "" {
		return "invalid device metadata"
	}
	return "invalid device metadata: " + e.Reason
}

func (e *InvalidDeviceError) Unwrap() error { return ErrInvalidDevice }

// DeviceRegistry enumerates snapshots, resolves directional defaults, and
// opens an exact stable ID. List and Default must not acquire a device; Open
// is the acquisition boundary.
type DeviceRegistry interface {
	List() ([]Device, error)
	Default(Direction) (Device, error)
	Open(DeviceID) (OpenedDevice, error)
}

// OpenedDevice owns the side effect created by DeviceRegistry.Open.
// Implementations must make Close idempotent.
type OpenedDevice interface {
	Close() error
}

// DeviceHandle is a descriptive alias for OpenedDevice.
type DeviceHandle = OpenedDevice
