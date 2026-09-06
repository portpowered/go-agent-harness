package devices_test

import (
	"errors"
	"sort"
	"sync"
	"testing"

	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

type Device = devicegw.Device
type DeviceID = devicegw.DeviceID
type Direction = devicegw.Direction
type DeviceRegistry = devicegw.DeviceRegistry
type OpenedDevice = devicegw.OpenedDevice
type DeviceRegistryObservations = devicegw.DeviceRegistryObservations
type DeviceRegistryConformanceFixture = devicegw.DeviceRegistryConformanceFixture
type DeviceRegistryConformanceFactory = devicegw.DeviceRegistryConformanceFactory
type DeviceNotFoundError = devicegw.DeviceNotFoundError
type AmbiguousDeviceNameError = devicegw.AmbiguousDeviceNameError
type NoDefaultDeviceError = devicegw.NoDefaultDeviceError
type DeviceInUseError = devicegw.DeviceInUseError

const (
	DirectionInput  = devicegw.DirectionInput
	DirectionOutput = devicegw.DirectionOutput
)

var (
	ErrDeviceNotFound           = devicegw.ErrDeviceNotFound
	ErrAmbiguousDeviceName      = devicegw.ErrAmbiguousDeviceName
	ErrNoDefaultDevice          = devicegw.ErrNoDefaultDevice
	ErrDeviceInUse              = devicegw.ErrDeviceInUse
	NewDevice                   = devicegw.NewDevice
	NewDeviceNotFoundError      = devicegw.NewDeviceNotFoundError
	NewAmbiguousDeviceNameError = devicegw.NewAmbiguousDeviceNameError
	NewNoDefaultDeviceError     = devicegw.NewNoDefaultDeviceError
	NewDeviceInUseError         = devicegw.NewDeviceInUseError
	ValidateDirection           = devicegw.ValidateDirection
)

func TestDeviceRegistryConformance(t *testing.T) {
	devicegw.RunDeviceRegistryConformance(t, newFixture)
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
	var notFound *DeviceNotFoundError
	if !errors.As(err, &notFound) || !errors.Is(err, ErrDeviceNotFound) || notFound.ID != fixture.ExclusiveID {
		t.Fatalf("disappeared-device error=%v, want typed not-found for %q", err, fixture.ExclusiveID)
	}
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

func TestDeviceMetadataValidationAndIDs(t *testing.T) {
	if got := (devicegw.Direction("")).String(); got != "invalid" {
		t.Fatalf("empty direction String()=%q, want invalid", got)
	}
	if got := devicegw.DirectionInput.String(); got != "input" {
		t.Fatalf("input String()=%q, want input", got)
	}
	if !devicegw.ValidDirection(devicegw.DirectionInput) || !devicegw.ValidDirection(devicegw.DirectionOutput) {
		t.Fatal("valid directions rejected by ValidDirection")
	}

	validID, err := devicegw.NewDeviceID("backend", "native:stable")
	if err != nil {
		t.Fatal(err)
	}
	if backend, nativeID, err := devicegw.ParseDeviceID(validID); err != nil || backend != "backend" || nativeID != "native:stable" {
		t.Fatalf("ParseDeviceID(%q)=(%q, %q, %v), want backend/native:stable", validID, backend, nativeID, err)
	}

	invalidIDs := []struct {
		name, backend, nativeID string
	}{
		{name: "empty backend", backend: "", nativeID: "native"},
		{name: "trimmed backend", backend: " backend", nativeID: "native"},
		{name: "internal backend whitespace", backend: "back end", nativeID: "native"},
		{name: "backend colon", backend: "back:end", nativeID: "native"},
		{name: "uppercase backend", backend: "Backend", nativeID: "native"},
		{name: "empty native ID", backend: "backend", nativeID: ""},
		{name: "trimmed native ID", backend: "backend", nativeID: " native"},
		{name: "internal native whitespace", backend: "backend", nativeID: "native id"},
	}
	for _, testCase := range invalidIDs {
		t.Run("invalid ID/"+testCase.name, func(t *testing.T) {
			if _, err := devicegw.NewDeviceID(testCase.backend, testCase.nativeID); err == nil || !errors.Is(err, devicegw.ErrInvalidDeviceID) {
				t.Fatalf("NewDeviceID(%q, %q)=%v, want ErrInvalidDeviceID", testCase.backend, testCase.nativeID, err)
			}
		})
	}

	for _, id := range []devicegw.DeviceID{"", "backend", ":native", "Backend:native", "backend:"} {
		t.Run("invalid parse/"+string(id), func(t *testing.T) {
			if _, _, err := devicegw.ParseDeviceID(id); err == nil || !errors.Is(err, devicegw.ErrInvalidDeviceID) {
				t.Fatalf("ParseDeviceID(%q)=%v, want ErrInvalidDeviceID", id, err)
			}
		})
	}

	if _, err := devicegw.NewDevice("backend", "native", "name", devicegw.Direction("invalid")); err == nil || !errors.Is(err, devicegw.ErrInvalidDirection) {
		t.Fatalf("invalid direction NewDevice error=%v, want ErrInvalidDirection", err)
	}
	if _, err := devicegw.NewDevice("backend", "native", "   ", devicegw.DirectionInput); err == nil || !errors.Is(err, devicegw.ErrInvalidDevice) {
		t.Fatalf("empty display NewDevice error=%v, want ErrInvalidDevice", err)
	}
	if _, err := devicegw.NewDevice("Backend", "native", "name", devicegw.DirectionInput); err == nil || !errors.Is(err, devicegw.ErrInvalidDeviceID) {
		t.Fatalf("invalid backend NewDevice error=%v, want ErrInvalidDeviceID", err)
	}

	valid, err := devicegw.NewDevice("backend", "native", "Display", devicegw.DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	if valid.Display() != "Display" || valid.Name != valid.DisplayName {
		t.Fatalf("valid device display fields=%#v, want matching Display", valid)
	}
	nameOnly := valid
	nameOnly.DisplayName = ""
	if err := nameOnly.Validate(); err != nil || nameOnly.Display() != "Display" {
		t.Fatalf("Name-only device Validate/Display=(%v, %q), want valid/Display", err, nameOnly.Display())
	}
	displayOnly := valid
	displayOnly.Name = ""
	if err := displayOnly.Validate(); err != nil || displayOnly.Display() != "Display" {
		t.Fatalf("DisplayName-only device Validate/Display=(%v, %q), want valid/Display", err, displayOnly.Display())
	}

	invalidDevices := []struct {
		name   string
		device devicegw.Device
	}{
		{name: "invalid direction", device: devicegw.Device{ID: valid.ID, Backend: valid.Backend, NativeID: valid.NativeID, Name: valid.Name, DisplayName: valid.DisplayName, Direction: devicegw.Direction("invalid")}},
		{name: "empty backend", device: devicegw.Device{ID: valid.ID, Backend: "", NativeID: valid.NativeID, Name: valid.Name, DisplayName: valid.DisplayName, Direction: valid.Direction}},
		{name: "empty native ID", device: devicegw.Device{ID: valid.ID, Backend: valid.Backend, NativeID: "", Name: valid.Name, DisplayName: valid.DisplayName, Direction: valid.Direction}},
		{name: "mismatched ID", device: devicegw.Device{ID: "backend:other", Backend: valid.Backend, NativeID: valid.NativeID, Name: valid.Name, DisplayName: valid.DisplayName, Direction: valid.Direction}},
		{name: "empty display", device: devicegw.Device{ID: valid.ID, Backend: valid.Backend, NativeID: valid.NativeID, Direction: valid.Direction}},
		{name: "mismatched display fields", device: devicegw.Device{ID: valid.ID, Backend: valid.Backend, NativeID: valid.NativeID, Name: "First", DisplayName: "Second", Direction: valid.Direction}},
	}
	for _, testCase := range invalidDevices {
		t.Run("invalid metadata/"+testCase.name, func(t *testing.T) {
			if err := testCase.device.Validate(); err == nil || !errors.Is(err, devicegw.ErrInvalidDevice) && !errors.Is(err, devicegw.ErrInvalidDirection) {
				t.Fatalf("Validate(%#v)=%v, want typed metadata/direction error", testCase.device, err)
			}
		})
	}
}

func TestDeviceErrorEdges(t *testing.T) {
	var notFound *devicegw.DeviceNotFoundError
	var ambiguous *devicegw.AmbiguousDeviceNameError
	var noDefault *devicegw.NoDefaultDeviceError
	var inUse *devicegw.DeviceInUseError
	var invalidDirection *devicegw.InvalidDirectionError
	var invalidID *devicegw.InvalidDeviceIDError
	var invalidDevice *devicegw.InvalidDeviceError
	for _, testCase := range []struct {
		name string
		err  interface {
			error
			Unwrap() error
		}
		want error
	}{
		{name: "not found", err: notFound, want: devicegw.ErrDeviceNotFound},
		{name: "ambiguous", err: ambiguous, want: devicegw.ErrAmbiguousDeviceName},
		{name: "no default", err: noDefault, want: devicegw.ErrNoDefaultDevice},
		{name: "in use", err: inUse, want: devicegw.ErrDeviceInUse},
		{name: "invalid direction", err: invalidDirection, want: devicegw.ErrInvalidDirection},
		{name: "invalid ID", err: invalidID, want: devicegw.ErrInvalidDeviceID},
		{name: "invalid device", err: invalidDevice, want: devicegw.ErrInvalidDevice},
	} {
		t.Run("nil/"+testCase.name, func(t *testing.T) {
			if got := testCase.err.Error(); got != "<nil>" {
				t.Fatalf("nil Error()=%q, want <nil>", got)
			}
			if testCase.err.Unwrap() != testCase.want {
				t.Fatalf("nil Unwrap()=%v, want %v", testCase.err.Unwrap(), testCase.want)
			}
		})
	}

	nameOnly, err := devicegw.NewDevice("backend", "name-only", "Candidate", devicegw.DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	nameOnly.DisplayName = ""
	other := nameOnly
	other.ID = "backend:other"
	other.NativeID = "other"
	other.Direction = devicegw.DirectionOutput
	other.Name = "Other"
	manual := &devicegw.AmbiguousDeviceNameError{
		Substring:  "candidate",
		Candidates: []devicegw.Device{other, nameOnly},
	}
	if got, want := manual.Error(), `device name "candidate" is ambiguous; candidates: backend:other ("Other", output); backend:name-only ("Candidate", input)`; got != want {
		t.Fatalf("manual ambiguous message=%q, want %q", got, want)
	}
	copyOfCandidates := manual.CandidatesCopy()
	copyOfCandidates[0].Name = "changed"
	if manual.Candidates[0].Name == "changed" {
		t.Fatal("CandidatesCopy returned aliased candidate metadata")
	}
	var nilAmbiguous *devicegw.AmbiguousDeviceNameError
	if nilAmbiguous.CandidatesCopy() != nil {
		t.Fatal("nil CandidatesCopy should be nil")
	}
	inputSameID, err := devicegw.NewDevice("backend", "same", "Input", devicegw.DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	outputSameID, err := devicegw.NewDevice("backend", "same", "Output", devicegw.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	thirdSameID := outputSameID
	thirdSameID.Name = "Another Output"
	thirdSameID.DisplayName = "Another Output"
	ordered := devicegw.NewAmbiguousDeviceNameError("same", []devicegw.Device{thirdSameID, outputSameID, inputSameID})
	if len(ordered.Candidates) != 3 || ordered.Candidates[0].Direction != devicegw.DirectionInput || ordered.Candidates[1].Display() != "Another Output" || ordered.Candidates[2].Display() != "Output" {
		t.Fatalf("same-ID candidates=%#v, want direction/name tie-break ordering", ordered.Candidates)
	}

	for _, testCase := range []struct {
		name string
		err  error
		want string
	}{
		{name: "invalid direction", err: &devicegw.InvalidDirectionError{Direction: devicegw.Direction("sideways")}, want: `"sideways" is not a valid audio direction; want input or output`},
		{name: "invalid ID", err: &devicegw.InvalidDeviceIDError{}, want: "invalid device ID"},
		{name: "invalid ID reason", err: &devicegw.InvalidDeviceIDError{Reason: "bad native ID"}, want: "invalid device ID: bad native ID"},
		{name: "invalid device", err: &devicegw.InvalidDeviceError{}, want: "invalid device metadata"},
		{name: "invalid device reason", err: &devicegw.InvalidDeviceError{Reason: "bad display name"}, want: "invalid device metadata: bad display name"},
	} {
		t.Run("typed/"+testCase.name, func(t *testing.T) {
			if got := testCase.err.Error(); got != testCase.want {
				t.Fatalf("Error()=%q, want %q", got, testCase.want)
			}
		})
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

type conformanceProbeFailure struct{}

type conformanceProbe struct {
	failed       bool
	panicOnFatal bool
}

func (p *conformanceProbe) Helper() {}

func (p *conformanceProbe) Fatal(args ...any) {
	p.failed = true
	if p.panicOnFatal {
		panic(conformanceProbeFailure{})
	}
}

func (p *conformanceProbe) Fatalf(format string, args ...any) {
	p.failed = true
	if p.panicOnFatal {
		panic(conformanceProbeFailure{})
	}
}

func (p *conformanceProbe) Run(_ string, f func(*conformanceProbe)) bool {
	child := &conformanceProbe{panicOnFatal: p.panicOnFatal}
	func() {
		defer func() {
			if recover() != nil {
				child.failed = true
			}
		}()
		f(child)
	}()
	p.failed = p.failed || child.failed
	return !child.failed
}

type conformanceProbeRegistry struct {
	listed       []devicegw.Device
	listErr      error
	defaultErr   error
	openErr      error
	defaultByDir map[devicegw.Direction]devicegw.Device
}

func (r *conformanceProbeRegistry) List() ([]devicegw.Device, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.listed, nil
}

func (r *conformanceProbeRegistry) Default(direction devicegw.Direction) (devicegw.Device, error) {
	if r.defaultErr != nil {
		return devicegw.Device{}, r.defaultErr
	}
	device, ok := r.defaultByDir[direction]
	if !ok {
		return devicegw.Device{}, devicegw.NewNoDefaultDeviceError(direction)
	}
	return device, nil
}

func (r *conformanceProbeRegistry) Open(devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	if r.openErr != nil {
		return nil, r.openErr
	}
	return conformanceProbeHandle{}, nil
}

type conformanceProbeHandle struct{}

func (conformanceProbeHandle) Close() error { return nil }

func probeFixture(registry devicegw.DeviceRegistry) devicegw.DeviceRegistryConformanceFixture {
	return devicegw.DeviceRegistryConformanceFixture{
		Registry:      registry,
		InputDefault:  "input",
		OutputDefault: "output",
		ExclusiveID:   "exclusive",
		RemoveDevice:  func(devicegw.DeviceID) {},
		Observations:  func() devicegw.DeviceRegistryObservations { return devicegw.DeviceRegistryObservations{} },
	}
}

func TestDeviceRegistryConformanceRejectsBrokenFixtures(t *testing.T) {
	valid, err := devicegw.NewDevice("virtual", "valid", "Valid", devicegw.DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name    string
		factory devicegw.DeviceRegistryConformanceFactory
	}{
		{name: "nil factory", factory: nil},
		{name: "missing registry and callbacks", factory: func() devicegw.DeviceRegistryConformanceFixture { return devicegw.DeviceRegistryConformanceFixture{} }},
		{name: "missing fixture IDs", factory: func() devicegw.DeviceRegistryConformanceFixture {
			fixture := probeFixture(&conformanceProbeRegistry{})
			fixture.InputDefault = ""
			return fixture
		}},
		{name: "list error", factory: func() devicegw.DeviceRegistryConformanceFixture {
			return probeFixture(&conformanceProbeRegistry{listErr: errors.New("list failed")})
		}},
		{name: "invalid listed metadata", factory: func() devicegw.DeviceRegistryConformanceFixture {
			return probeFixture(&conformanceProbeRegistry{listed: []devicegw.Device{{}}})
		}},
		{name: "duplicate listed ID", factory: func() devicegw.DeviceRegistryConformanceFixture {
			return probeFixture(&conformanceProbeRegistry{listed: []devicegw.Device{valid, valid}})
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			probe := &conformanceProbe{panicOnFatal: testCase.factory != nil}
			devicegw.RunDeviceRegistryConformance(probe, testCase.factory)
			if !probe.failed {
				t.Fatal("conformance helper accepted a broken fixture")
			}
		})
	}
}
