package audio

import (
	"fmt"
	"strings"
	"unicode"
)

// Direction identifies which side of an audio device can be opened.
// DirectionInput and DirectionOutput are the only valid values.
type Direction string

const (
	// DirectionInput identifies a capture device.
	DirectionInput Direction = "input"
	// DirectionOutput identifies a playback device.
	DirectionOutput Direction = "output"

	// Input and Output are concise aliases for the two supported directions.
	Input  = DirectionInput
	Output = DirectionOutput
)

// String returns the wire spelling of a direction.
func (d Direction) String() string {
	if d == "" {
		return "invalid"
	}
	return string(d)
}

// IsValid reports whether d is one of the two supported directions.
func (d Direction) IsValid() bool {
	return d == DirectionInput || d == DirectionOutput
}

// ValidDirection reports whether d is one of the two supported directions.
func ValidDirection(d Direction) bool { return d.IsValid() }

// ValidateDirection returns a typed error for an unsupported direction.
func ValidateDirection(d Direction) error {
	if d.IsValid() {
		return nil
	}
	return &InvalidDirectionError{Direction: d}
}

// DeviceID is an alias for string so callers can keep IDs opaque without
// needing conversions when storing or receiving backend identifiers.
type DeviceID = string

// NewDeviceID creates the documented backend-qualified ID form:
// <lowercase-backend>:<stable-native-id>. Neither component is normalized, so
// a backend cannot accidentally change identity by changing case or trimming
// its native identifier.
func NewDeviceID(backend, nativeID string) (DeviceID, error) {
	if err := validateIDComponent(backend, "backend", false); err != nil {
		return "", &InvalidDeviceIDError{Backend: backend, NativeID: nativeID, Reason: err.Error()}
	}
	if backend != strings.ToLower(backend) {
		return "", &InvalidDeviceIDError{Backend: backend, NativeID: nativeID, Reason: "backend namespace must be lowercase"}
	}
	if err := validateIDComponent(nativeID, "native ID", true); err != nil {
		return "", &InvalidDeviceIDError{Backend: backend, NativeID: nativeID, Reason: err.Error()}
	}
	return backend + ":" + nativeID, nil
}

// ParseDeviceID validates an ID and returns its backend namespace and native
// component. The first colon is the scheme separator; native IDs may contain
// further colons when the backend's stable identifier requires them.
func ParseDeviceID(id DeviceID) (backend, nativeID string, err error) {
	separator := strings.IndexByte(id, ':')
	if separator <= 0 || separator == len(id)-1 {
		return "", "", &InvalidDeviceIDError{ID: id, Reason: "ID must have the form <backend>:<native-id>"}
	}
	backend, nativeID = id[:separator], id[separator+1:]
	if _, err := NewDeviceID(backend, nativeID); err != nil {
		return "", "", err
	}
	return backend, nativeID, nil
}

// Device describes one enumerated audio device. Name and DisplayName carry
// the same display-oriented value; DisplayName is provided for callers that
// prefer an explicit field name, while Name keeps the common device metadata
// spelling. IDs are stable identity; display names are not.
type Device struct {
	ID          DeviceID
	Backend     string
	NativeID    string
	Name        string
	DisplayName string
	Direction   Direction
}

// NewDevice constructs validated metadata for a backend's enumeration
// snapshot. It derives identity from backend and nativeID, never from name.
func NewDevice(backend, nativeID, displayName string, direction Direction) (Device, error) {
	if err := ValidateDirection(direction); err != nil {
		return Device{}, err
	}
	if strings.TrimSpace(displayName) == "" {
		return Device{}, &InvalidDeviceError{Reason: "display name must not be empty"}
	}
	id, err := NewDeviceID(backend, nativeID)
	if err != nil {
		return Device{}, err
	}
	return Device{
		ID:          id,
		Backend:     backend,
		NativeID:    nativeID,
		Name:        displayName,
		DisplayName: displayName,
		Direction:   direction,
	}, nil
}

// Validate checks the metadata invariants every registry must preserve in a
// List snapshot. Backends should construct values with NewDevice.
func (d Device) Validate() error {
	if err := ValidateDirection(d.Direction); err != nil {
		return err
	}
	if strings.TrimSpace(d.Backend) == "" || strings.TrimSpace(d.NativeID) == "" {
		return &InvalidDeviceError{ID: d.ID, Reason: "backend and native ID must be non-empty"}
	}
	wantID, err := NewDeviceID(d.Backend, d.NativeID)
	if err != nil {
		return err
	}
	if d.ID != wantID {
		return &InvalidDeviceError{ID: d.ID, Reason: fmt.Sprintf("ID must equal %q", wantID)}
	}
	if strings.TrimSpace(d.displayName()) == "" {
		return &InvalidDeviceError{ID: d.ID, Reason: "display name must not be empty"}
	}
	if d.Name != "" && d.DisplayName != "" && d.Name != d.DisplayName {
		return &InvalidDeviceError{ID: d.ID, Reason: "Name and DisplayName must match when both are set"}
	}
	return nil
}

// Display returns the display-oriented device name, accepting either of the
// two equivalent public metadata fields.
func (d Device) Display() string { return d.displayName() }

func (d Device) displayName() string {
	if d.DisplayName != "" {
		return d.DisplayName
	}
	return d.Name
}

func validateIDComponent(value, label string, allowColon bool) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not have leading or trailing whitespace", label)
	}
	if strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return fmt.Errorf("%s must not contain whitespace or control characters", label)
	}
	if !allowColon && strings.ContainsRune(value, ':') {
		return fmt.Errorf("%s must not contain ':'", label)
	}
	return nil
}

func cloneDevices(devices []Device) []Device {
	cloned := make([]Device, len(devices))
	copy(cloned, devices)
	return cloned
}
