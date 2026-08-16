package audio

import (
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
)

// DeviceRegistryObservations are side-effect counters exposed by a conformance
// fixture. OpenCount counts successful acquisitions and ReleaseCount counts
// backend releases, so tests can prove that failed opens and repeated closes
// do not create hidden effects.
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

// RunDeviceRegistryConformance runs the reusable runtime contract checks for a
// backend. Every subtest obtains a new fixture, and the exact positive counts
// keep a nil, empty, or no-op registry from passing accidentally.
func RunDeviceRegistryConformance(t *testing.T, factory DeviceRegistryConformanceFactory) {
	t.Helper()
	if factory == nil {
		t.Fatal("device registry conformance factory is nil")
	}

	t.Run("list is stable and validated", func(t *testing.T) {
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

	t.Run("directional defaults are listed and observational", func(t *testing.T) {
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

	t.Run("missing default is typed", func(t *testing.T) {
		fixture := newConformanceFixture(t, factory)
		fixture.RemoveDevice(fixture.InputDefault)
		if _, err := fixture.Registry.Default(DirectionInput); err == nil {
			t.Fatal("Default(input) succeeded after its device disappeared")
		} else {
			assertNoDefault(t, err, DirectionInput)
		}
	})

	t.Run("listed ID opens and close is idempotent", func(t *testing.T) {
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

	t.Run("unknown ID is typed not found", func(t *testing.T) {
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

	t.Run("disappeared ID is typed not found", func(t *testing.T) {
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

	t.Run("exclusive device reports in use and reopens after close", func(t *testing.T) {
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

func newConformanceFixture(t *testing.T, factory DeviceRegistryConformanceFactory) DeviceRegistryConformanceFixture {
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

func listDevices(t *testing.T, registry DeviceRegistry) []Device {
	t.Helper()
	devices, err := registry.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return devices
}

func snapshotByID(t *testing.T, devices []Device) map[DeviceID]Device {
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

func assertNotFound(t *testing.T, err error, id DeviceID) {
	t.Helper()
	var typed *DeviceNotFoundError
	if !errors.As(err, &typed) || !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("error=%v, want DeviceNotFoundError and ErrDeviceNotFound", err)
	}
	if typed.ID != id || err.Error() != NewDeviceNotFoundError(id).Error() {
		t.Fatalf("not-found error=%v, want ID %q and message %q", err, id, NewDeviceNotFoundError(id))
	}
}

func assertInUse(t *testing.T, err error, id DeviceID) {
	t.Helper()
	var typed *DeviceInUseError
	if !errors.As(err, &typed) || !errors.Is(err, ErrDeviceInUse) {
		t.Fatalf("error=%v, want DeviceInUseError and ErrDeviceInUse", err)
	}
	if typed.ID != id || err.Error() != NewDeviceInUseError(id).Error() {
		t.Fatalf("in-use error=%v, want ID %q and message %q", err, id, NewDeviceInUseError(id))
	}
}

func assertNoDefault(t *testing.T, err error, direction Direction) {
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

func TestDeviceRegistryConformance(t *testing.T) {
	RunDeviceRegistryConformance(t, newFixture)
}

func TestDeviceRegistryErrorContracts(t *testing.T) {
	input, err := NewDevice("virtual", "input", "Input", DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	output, err := NewDevice("virtual", "output", "Output", DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		err    error
		want   string
		target error
		as     func(error) bool
	}{
		{
			name: "not found", err: NewDeviceNotFoundError("virtual:gone"),
			want: `device "virtual:gone" not found; run agent devices list`, target: ErrDeviceNotFound,
			as: func(err error) bool { var typed *DeviceNotFoundError; return errors.As(err, &typed) },
		},
		{
			name: "ambiguous", err: NewAmbiguousDeviceNameError("in", []Device{output, input}),
			want: `device name "in" is ambiguous; candidates: virtual:input ("Input", input); virtual:output ("Output", output)`, target: ErrAmbiguousDeviceName,
			as: func(err error) bool { var typed *AmbiguousDeviceNameError; return errors.As(err, &typed) },
		},
		{
			name: "no default", err: NewNoDefaultDeviceError(DirectionOutput),
			want: "no default output device; run agent devices list", target: ErrNoDefaultDevice,
			as: func(err error) bool { var typed *NoDefaultDeviceError; return errors.As(err, &typed) },
		},
		{
			name: "in use", err: NewDeviceInUseError("virtual:input"),
			want: `device "virtual:input" is in use`, target: ErrDeviceInUse,
			as: func(err error) bool { var typed *DeviceInUseError; return errors.As(err, &typed) },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.err.Error(); got != testCase.want {
				t.Fatalf("message=%q, want %q", got, testCase.want)
			}
			if !errors.Is(testCase.err, testCase.target) || !testCase.as(testCase.err) {
				t.Fatalf("error=%v does not expose target %v and its typed identity", testCase.err, testCase.target)
			}
		})
	}

	var ambiguous *AmbiguousDeviceNameError
	err = NewAmbiguousDeviceNameError("in", []Device{output, input})
	if !errors.As(err, &ambiguous) || !errors.Is(err, ErrAmbiguousDeviceName) {
		t.Fatalf("ambiguous error=%v, want typed identity", err)
	}
	if len(ambiguous.Candidates) != 2 || ambiguous.Candidates[0].ID != input.ID || ambiguous.Candidates[1].ID != output.ID {
		t.Fatalf("ambiguous candidates=%#v, want deterministic ID order", ambiguous.Candidates)
	}

	fixture := newFixture()
	listed, err := fixture.Registry.List()
	if err != nil {
		t.Fatal(err)
	}
	fixture.RemoveDevice(fixture.ExclusiveID)
	_, err = fixture.Registry.Open(fixture.ExclusiveID)
	if err == nil {
		t.Fatal("disappeared device opened")
	}
	assertNotFound(t, err, fixture.ExclusiveID)
	if len(listed) == 0 {
		t.Fatal("fixture must provide a listed device for disappearance case")
	}

	firstName, err := NewDevice("virtual", "stable", "First Name", DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	secondName, err := NewDevice("virtual", "stable", "Renamed Device", DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	if firstName.ID != secondName.ID {
		t.Fatalf("display name changed stable ID: first=%q second=%q", firstName.ID, secondName.ID)
	}
}

type fixtureRegistry struct {
	mu           sync.Mutex
	devices      map[DeviceID]Device
	defaults     map[Direction]DeviceID
	inUse        map[DeviceID]bool
	listCalls    int
	defaultCalls int
	openCount    int
	releases     int
}

type fixtureHandle struct {
	registry *fixtureRegistry
	id       DeviceID
	closed   bool
}

func (r *fixtureRegistry) List() ([]Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls++
	devices := make([]Device, 0, len(r.devices))
	for _, device := range r.devices {
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	return devices, nil
}

func (r *fixtureRegistry) Default(direction Direction) (Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultCalls++
	if err := ValidateDirection(direction); err != nil {
		return Device{}, err
	}
	id, ok := r.defaults[direction]
	if !ok {
		return Device{}, NewNoDefaultDeviceError(direction)
	}
	device, ok := r.devices[id]
	if !ok {
		return Device{}, NewNoDefaultDeviceError(direction)
	}
	return device, nil
}

func (r *fixtureRegistry) Open(id DeviceID) (OpenedDevice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.devices[id]; !ok {
		return nil, NewDeviceNotFoundError(id)
	}
	if r.inUse[id] {
		return nil, NewDeviceInUseError(id)
	}
	r.inUse[id] = true
	r.openCount++
	return &fixtureHandle{registry: r, id: id}, nil
}

func (h *fixtureHandle) Close() error {
	h.registry.mu.Lock()
	defer h.registry.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	h.registry.inUse[h.id] = false
	h.registry.releases++
	return nil
}

func (r *fixtureRegistry) remove(id DeviceID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.devices, id)
	for direction, defaultID := range r.defaults {
		if defaultID == id {
			delete(r.defaults, direction)
		}
	}
}

func (r *fixtureRegistry) observations() DeviceRegistryObservations {
	r.mu.Lock()
	defer r.mu.Unlock()
	return DeviceRegistryObservations{
		ListCalls:    r.listCalls,
		DefaultCalls: r.defaultCalls,
		OpenCount:    r.openCount,
		ReleaseCount: r.releases,
	}
}

func newFixture() DeviceRegistryConformanceFixture {
	input := mustFixtureDevice("input-default", "Input Default", DirectionInput)
	output := mustFixtureDevice("output-default", "Output Default", DirectionOutput)
	exclusive := mustFixtureDevice("exclusive", "Exclusive Output", DirectionOutput)
	registry := &fixtureRegistry{
		devices: map[DeviceID]Device{
			input.ID:     input,
			output.ID:    output,
			exclusive.ID: exclusive,
		},
		defaults: map[Direction]DeviceID{
			DirectionInput:  input.ID,
			DirectionOutput: output.ID,
		},
		inUse: make(map[DeviceID]bool),
	}
	return DeviceRegistryConformanceFixture{
		Registry:      registry,
		InputDefault:  input.ID,
		OutputDefault: output.ID,
		ExclusiveID:   exclusive.ID,
		RemoveDevice:  registry.remove,
		Observations:  registry.observations,
	}
}

func mustFixtureDevice(nativeID, displayName string, direction Direction) Device {
	device, err := NewDevice("virtual", nativeID, displayName, direction)
	if err != nil {
		panic(err)
	}
	return device
}
