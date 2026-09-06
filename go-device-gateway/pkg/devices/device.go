package devices

import (
	"errors"
	"fmt"
	"reflect"
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
		return &InvalidDeviceError{ID: d.ID, Reason: fmt.Sprintf("ID must Equal %q", wantID)}
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

// DeviceRegistryObservations are side-effect counters exposed by a
// conformance fixture. OpenCount counts successful acquisitions and
// ReleaseCount counts backend releases, so tests can prove that failed opens
// and repeated closes do not create hidden effects.
type DeviceRegistryObservations struct {
	ListCalls    int
	DefaultCalls int
	OpenCount    int
	ReleaseCount int
}

// DeviceRegistryConformanceFixture describes one isolated backend fixture.
// A factory must return a fresh fixture for every invocation. RemoveDevice
// simulates disappearance after enumeration; Observations exposes only the
// counters needed to prove acquisition and release behavior.
type DeviceRegistryConformanceFixture struct {
	Registry      DeviceRegistry
	InputDefault  DeviceID
	OutputDefault DeviceID
	ExclusiveID   DeviceID
	RemoveDevice  func(DeviceID)
	Observations  func() DeviceRegistryObservations
}

// DeviceRegistryConformanceFactory creates isolated fixture state for each
// conformance subtest. Backend tests can call RunDeviceRegistryConformance
// with this factory rather than copying the behavioral assertions.
type DeviceRegistryConformanceFactory func() DeviceRegistryConformanceFixture

// deviceRegistryConformanceTester is the small testing contract required by
// RunDeviceRegistryConformance. *testing.T satisfies it, while keeping the
// reusable helper importable without making the production package depend on
// the testing package.
type deviceRegistryConformanceTester[T any] interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Run(name string, f func(T)) bool
}

// RunDeviceRegistryConformance runs the reusable runtime contract checks for a
// backend. Every subtest obtains a new fixture, and the exact positive counts
// keep a nil, empty, or no-op registry from passing accidentally.
//
// The helper is intentionally part of the importable audio package: an
// external backend test can pass its *testing.T and a factory implementing the
// fixture contract above.
func RunDeviceRegistryConformance[T deviceRegistryConformanceTester[T]](t T, factory DeviceRegistryConformanceFactory) {
	t.Helper()
	if factory == nil {
		t.Fatal("device registry conformance factory is nil")
	}

	t.Run("list is stable and validated", func(t T) {
		fixture := newConformanceFixture(t, factory)
		first := listDevices(t, fixture.Registry)
		second := listDevices(t, fixture.Registry)
		if len(first) == 0 {
			t.Fatal("List returned no devices")
		}
		firstByID := snapshotByID(t, first)
		secondByID := snapshotByID(t, second)
		if !reflect.DeepEqual(firstByID, secondByID) {
			t.Fatalf("repeated List snapshots differ: first=%#v second=%#v", firstByID, secondByID)
		}
		if got := fixture.Observations(); got.OpenCount != 0 || got.ReleaseCount != 0 {
			t.Fatalf("List acquired resources: observations=%+v", got)
		}
	})

	t.Run("directional defaults are listed and observational", func(t T) {
		fixture := newConformanceFixture(t, factory)
		listed := snapshotByID(t, listDevices(t, fixture.Registry))
		for _, want := range []struct {
			name      string
			direction Direction
			id        DeviceID
		}{
			{name: "input", direction: DirectionInput, id: fixture.InputDefault},
			{name: "output", direction: DirectionOutput, id: fixture.OutputDefault},
		} {
			device, err := fixture.Registry.Default(want.direction)
			if err != nil {
				t.Fatalf("Default(%s): %v", want.direction, err)
			}
			if device.ID != want.id || device.Direction != want.direction {
				t.Fatalf("Default(%s)=%#v, want ID %q and direction %s", want.direction, device, want.id, want.direction)
			}
			if listed[device.ID].Direction != want.direction {
				t.Fatalf("Default(%s) returned device absent or mismatched in List: %#v", want.direction, device)
			}
		}
		if _, err := fixture.Registry.Default(Direction("invalid")); err == nil {
			t.Fatal("Default(invalid) silently resolved a device")
		}
		if got := fixture.Observations(); got.OpenCount != 0 || got.ReleaseCount != 0 {
			t.Fatalf("Default acquired resources: observations=%+v", got)
		}
	})

	t.Run("missing default is typed", func(t T) {
		fixture := newConformanceFixture(t, factory)
		fixture.RemoveDevice(fixture.InputDefault)
		if _, err := fixture.Registry.Default(DirectionInput); err == nil {
			t.Fatal("Default(input) succeeded after its device disappeared")
		} else {
			assertNoDefault(t, err, DirectionInput)
		}
	})

	t.Run("listed ID opens and close is idempotent", func(t T) {
		fixture := newConformanceFixture(t, factory)
		listed := snapshotByID(t, listDevices(t, fixture.Registry))
		if _, ok := listed[fixture.ExclusiveID]; !ok {
			t.Fatalf("fixture ExclusiveID %q is not listed", fixture.ExclusiveID)
		}
		opened, err := fixture.Registry.Open(fixture.ExclusiveID)
		if err != nil || opened == nil {
			t.Fatalf("Open(%q)=(%v, %v), want a handle", fixture.ExclusiveID, opened, err)
		}
		if got := fixture.Observations().OpenCount; got != 1 {
			t.Fatalf("successful opens=%d, want 1", got)
		}
		if err := opened.Close(); err != nil {
			t.Fatalf("first Close: %v", err)
		}
		if err := opened.Close(); err != nil {
			t.Fatalf("second Close: %v", err)
		}
		if got := fixture.Observations().ReleaseCount; got != 1 {
			t.Fatalf("backend releases=%d, want exactly 1 after two closes", got)
		}
	})

	t.Run("unknown ID is typed not found", func(t T) {
		fixture := newConformanceFixture(t, factory)
		unknown := DeviceID("missing:never-listed")
		if _, err := fixture.Registry.Open(unknown); err == nil {
			t.Fatal("Open(unknown) succeeded")
		} else {
			assertNotFound(t, err, unknown)
		}
		if got := fixture.Observations().OpenCount; got != 0 {
			t.Fatalf("successful opens=%d after rejected unknown ID, want 0", got)
		}
	})

	t.Run("disappeared ID is typed not found", func(t T) {
		fixture := newConformanceFixture(t, factory)
		listed := snapshotByID(t, listDevices(t, fixture.Registry))
		if _, ok := listed[fixture.ExclusiveID]; !ok {
			t.Fatalf("fixture ExclusiveID %q is not listed", fixture.ExclusiveID)
		}
		fixture.RemoveDevice(fixture.ExclusiveID)
		if _, err := fixture.Registry.Open(fixture.ExclusiveID); err == nil {
			t.Fatal("Open(disappeared ID) succeeded")
		} else {
			assertNotFound(t, err, fixture.ExclusiveID)
		}
		if got := fixture.Observations().OpenCount; got != 0 {
			t.Fatalf("successful opens=%d after disappeared ID, want 0", got)
		}
	})

	t.Run("exclusive device reports in use and reopens after close", func(t T) {
		fixture := newConformanceFixture(t, factory)
		first, err := fixture.Registry.Open(fixture.ExclusiveID)
		if err != nil {
			t.Fatalf("first Open(%q): %v", fixture.ExclusiveID, err)
		}
		if _, err := fixture.Registry.Open(fixture.ExclusiveID); err == nil {
			t.Fatal("second exclusive Open succeeded")
		} else {
			assertInUse(t, err, fixture.ExclusiveID)
		}
		if got := fixture.Observations().OpenCount; got != 1 {
			t.Fatalf("successful opens after rejected second open=%d, want 1", got)
		}
		if err := first.Close(); err != nil {
			t.Fatalf("first handle Close: %v", err)
		}
		second, err := fixture.Registry.Open(fixture.ExclusiveID)
		if err != nil {
			t.Fatalf("reopen(%q): %v", fixture.ExclusiveID, err)
		}
		if err := second.Close(); err != nil {
			t.Fatalf("reopened handle Close: %v", err)
		}
		if err := second.Close(); err != nil {
			t.Fatalf("reopened handle second Close: %v", err)
		}
		got := fixture.Observations()
		if got.OpenCount != 2 || got.ReleaseCount != 2 {
			t.Fatalf("exclusive observations=%+v, want two successful opens and two releases", got)
		}
	})
}

func newConformanceFixture[T deviceRegistryConformanceTester[T]](t T, factory DeviceRegistryConformanceFactory) DeviceRegistryConformanceFixture {
	t.Helper()
	fixture := factory()
	if fixture.Registry == nil || fixture.RemoveDevice == nil || fixture.Observations == nil {
		t.Fatal("fixture must provide Registry, RemoveDevice, and Observations")
	}
	if fixture.InputDefault == "" || fixture.OutputDefault == "" || fixture.ExclusiveID == "" {
		t.Fatal("fixture must provide non-empty default and exclusive IDs")
	}
	return fixture
}

func listDevices[T deviceRegistryConformanceTester[T]](t T, registry DeviceRegistry) []Device {
	t.Helper()
	devices, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return devices
}

func snapshotByID[T deviceRegistryConformanceTester[T]](t T, devices []Device) map[DeviceID]Device {
	t.Helper()
	byID := make(map[DeviceID]Device, len(devices))
	for _, device := range devices {
		if err := device.Validate(); err != nil {
			t.Fatalf("invalid listed device %#v: %v", device, err)
		}
		if _, exists := byID[device.ID]; exists {
			t.Fatalf("duplicate listed device ID %q", device.ID)
		}
		byID[device.ID] = device
	}
	return byID
}

func assertNotFound[T deviceRegistryConformanceTester[T]](t T, err error, id DeviceID) {
	t.Helper()
	var typed *DeviceNotFoundError
	if !errors.As(err, &typed) || !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("error=%v, want DeviceNotFoundError and ErrDeviceNotFound", err)
	}
	if typed.ID != id || err.Error() != NewDeviceNotFoundError(id).Error() {
		t.Fatalf("not-found error=%v, want ID %q and message %q", err, id, NewDeviceNotFoundError(id))
	}
}

func assertInUse[T deviceRegistryConformanceTester[T]](t T, err error, id DeviceID) {
	t.Helper()
	var typed *DeviceInUseError
	if !errors.As(err, &typed) || !errors.Is(err, ErrDeviceInUse) {
		t.Fatalf("error=%v, want DeviceInUseError and ErrDeviceInUse", err)
	}
	if typed.ID != id || err.Error() != NewDeviceInUseError(id).Error() {
		t.Fatalf("in-use error=%v, want ID %q and message %q", err, id, NewDeviceInUseError(id))
	}
}

func assertNoDefault[T deviceRegistryConformanceTester[T]](t T, err error, direction Direction) {
	t.Helper()
	var typed *NoDefaultDeviceError
	if !errors.As(err, &typed) || !errors.Is(err, ErrNoDefaultDevice) {
		t.Fatalf("error=%v, want NoDefaultDeviceError and ErrNoDefaultDevice", err)
	}
	want := NewNoDefaultDeviceError(direction)
	if typed.Direction != direction || err.Error() != want.Error() {
		t.Fatalf("no-default error=%v, want direction %s and message %q", err, direction, want)
	}
}
