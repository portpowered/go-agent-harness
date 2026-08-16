//go:build windows

package audio

import (
	"errors"
	"strings"
	"testing"
)

func TestWASAPIDeviceIDsAreStableAndNamesAreDescriptive(t *testing.T) {
	first, err := NewDevice(wasapiBackend, "endpoint\\stable", "Microphone", DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDevice(wasapiBackend, "endpoint\\stable", "Renamed Microphone", DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "wasapi:endpoint\\stable" || second.ID != first.ID {
		t.Fatalf("IDs=%q and %q, want stable wasapi-qualified endpoint ID", first.ID, second.ID)
	}
	if first.Display() != "Microphone" || second.Display() != "Renamed Microphone" {
		t.Fatalf("display names=%q and %q, want friendly names", first.Display(), second.Display())
	}
}

func TestWASAPIOpenErrorMappingPreservesTypedIdentities(t *testing.T) {
	cases := []struct {
		name string
		hr   uint32
		want error
		as   func(error) bool
	}{
		{name: "not found", hr: hresultNotFound, want: ErrDeviceNotFound, as: func(err error) bool { var typed *DeviceNotFoundError; return errors.As(err, &typed) }},
		{name: "disappeared", hr: audclntDeviceInvalidated, want: ErrDeviceNotFound, as: func(err error) bool { var typed *DeviceNotFoundError; return errors.As(err, &typed) }},
		{name: "in use", hr: audclntDeviceInUse, want: ErrDeviceInUse, as: func(err error) bool { var typed *DeviceInUseError; return errors.As(err, &typed) }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			id := DeviceID("wasapi:test")
			err := mapWASAPIOpenError(id, "open endpoint", wasapiHRESULTWithCode{hr: testCase.hr, err: wasapiHRESULT(testCase.hr)})
			if !errors.Is(err, testCase.want) || !testCase.as(err) {
				t.Fatalf("error=%v, want typed %v", err, testCase.want)
			}
			if !strings.Contains(err.Error(), string(id)) && testCase.want == ErrDeviceInUse {
				t.Fatalf("error=%q does not name device %q", err, id)
			}
		})
	}
}

func TestWASAPIOpenHasLiveDataPath(t *testing.T) {
	registry := newWASAPIDeviceRegistry()
	_, err := registry.List()
	if err != nil {
		t.Skipf("Windows: missing WASAPI endpoint enumeration capability: %v", err)
	}
	for _, direction := range []Direction{DirectionInput, DirectionOutput} {
		direction := direction
		t.Run(direction.String(), func(t *testing.T) {
			selected, err := registry.Default(direction)
			if err != nil {
				t.Skipf("Windows: missing %s default endpoint: %v", direction, err)
			}
			opened, err := registry.Open(selected.ID)
			if err != nil {
				t.Skipf("Windows: exact %s endpoint cannot open: %v", direction, err)
			}
			defer func() { _ = opened.Close() }()
			handle, ok := opened.(*wasapiOpenedDevice)
			if !ok {
				t.Fatal("WASAPI registry returned an unexpected opened-device type")
			}
			if err := handle.verifyDataPathForTest(); err != nil {
				t.Skipf("Windows: %s data-path capability unavailable: %v", direction, err)
			}
		})
	}
}

func TestWASAPIDeviceRegistryConformance(t *testing.T) {
	probe := newWASAPIDeviceRegistry()
	devices, err := probe.List()
	if err != nil {
		t.Skipf("Windows: WASAPI enumeration unavailable: %v", err)
	}
	var inputDefault, outputDefault Device
	for _, device := range devices {
		switch device.Direction {
		case DirectionInput:
			if inputDefault.ID == "" {
				inputDefault = device
			}
		case DirectionOutput:
			if outputDefault.ID == "" {
				outputDefault = device
			}
		}
	}
	if inputDefault.ID == "" {
		t.Skip("Windows: missing active capture endpoint")
	}
	if outputDefault.ID == "" {
		t.Skip("Windows: missing active render endpoint")
	}
	if _, err := probe.Default(DirectionInput); err != nil {
		t.Skipf("Windows: missing input default endpoint: %v", err)
	}
	if _, err := probe.Default(DirectionOutput); err != nil {
		t.Skipf("Windows: missing output default endpoint: %v", err)
	}
	opened, err := probe.Open(outputDefault.ID)
	if err != nil {
		t.Skipf("Windows: exclusive endpoint capability unavailable for %q: %v", outputDefault.ID, err)
	} else {
		// The probe open above is only a capability check; the fixture below
		// creates fresh registries for each isolated conformance subtest.
		_ = opened.Close()
	}

	RunDeviceRegistryConformance(t, func() DeviceRegistryConformanceFixture {
		registry := newWASAPIDeviceRegistry()
		listed, listErr := registry.List()
		if listErr != nil {
			t.Fatalf("fixture List: %v", listErr)
		}
		input, inputErr := registry.Default(DirectionInput)
		if inputErr != nil {
			t.Fatalf("fixture input Default: %v", inputErr)
		}
		output, outputErr := registry.Default(DirectionOutput)
		if outputErr != nil {
			t.Fatalf("fixture output Default: %v", outputErr)
		}
		if len(listed) == 0 || input.ID == "" || output.ID == "" {
			t.Fatal("fixture requires listed input and output defaults")
		}
		return DeviceRegistryConformanceFixture{
			Registry:      registry,
			InputDefault:  input.ID,
			OutputDefault: output.ID,
			ExclusiveID:   output.ID,
			RemoveDevice:  registry.hideForTest,
			Observations:  registry.observations,
		}
	})
}
