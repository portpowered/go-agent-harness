package devices_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	devicegw "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/devices"
)

func TestProbeDeviceAvailabilityReadyUsesOneSideEffectFreeEnumeration(t *testing.T) {
	input, err := devicegw.NewDevice(devicegw.VirtualBackendName, "input", "Microphone", devicegw.DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	output, err := devicegw.NewDevice(devicegw.VirtualBackendName, "output", "Speaker", devicegw.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	registry := &probeRegistry{devices: []devicegw.Device{output, input}}

	got, err := devicegw.ProbeDeviceAvailability(registry)
	if err != nil {
		t.Fatalf("ProbeDeviceAvailability() error = %v", err)
	}
	if got.Status != devicegw.DeviceProbeStatusReady || got.ReasonCode != "" || got.Reason != "" {
		t.Fatalf("availability = %#v, want ready without a skip reason", got)
	}
	if got.InputDeviceCount != 1 || got.OutputDeviceCount != 1 || len(got.InputDevices) != 1 || len(got.OutputDevices) != 1 {
		t.Fatalf("availability counts/devices = %#v, want one input and one output", got)
	}
	if got.InputDevices[0].ID != input.ID || got.OutputDevices[0].ID != output.ID {
		t.Fatalf("directional devices = %#v / %#v, want %q / %q", got.InputDevices, got.OutputDevices, input.ID, output.ID)
	}
	if registry.listCalls != 1 || registry.defaultCalls != 0 || registry.openCalls != 0 {
		t.Fatalf("registry observations = %+v, want one List and no Default/Open calls", registry)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal availability: %v", err)
	}
	if !strings.Contains(string(encoded), `"status":"ready"`) || !strings.Contains(string(encoded), `"input_device_count":1`) {
		t.Fatalf("ready result JSON = %s, want status and counts", encoded)
	}
}

func TestProbeDeviceAvailabilityMissingDirectionIsStructuredSkip(t *testing.T) {
	input, err := devicegw.NewDevice(devicegw.VirtualBackendName, "input", "Microphone", devicegw.DirectionInput)
	if err != nil {
		t.Fatal(err)
	}
	output, err := devicegw.NewDevice(devicegw.VirtualBackendName, "output", "Speaker", devicegw.DirectionOutput)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		devices    []devicegw.Device
		code       devicegw.DeviceProbeSkipCode
		reasonPart string
	}{
		{name: "no input", devices: []devicegw.Device{output}, code: devicegw.DeviceProbeSkipNoInputDevice, reasonPart: "no audio input device"},
		{name: "no output", devices: []devicegw.Device{input}, code: devicegw.DeviceProbeSkipNoOutputDevice, reasonPart: "no audio output device"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			registry := &probeRegistry{devices: testCase.devices}
			got, err := devicegw.ProbeDeviceAvailability(registry)
			if err != nil {
				t.Fatalf("ProbeDeviceAvailability() error = %v", err)
			}
			if got.Status != devicegw.DeviceProbeStatusSkip || got.ReasonCode != testCase.code || got.Reason != testCase.reasonPart {
				t.Fatalf("availability = %#v, want skip code %q and reason %q", got, testCase.code, testCase.reasonPart)
			}
			if registry.listCalls != 1 || registry.defaultCalls != 0 || registry.openCalls != 0 {
				t.Fatalf("registry observations = %+v, want one List and no Default/Open calls", registry)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal skip: %v", err)
			}
			var document map[string]any
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatalf("decode skip JSON %s: %v", encoded, err)
			}
			if document["status"] != "skip" || document["reason_code"] != string(testCase.code) || document["reason"] != testCase.reasonPart {
				t.Fatalf("skip JSON = %v, want machine-readable code and human reason", document)
			}
		})
	}
}

func TestProbeDeviceAvailabilityBothDirectionsMissingUsesCombinedReason(t *testing.T) {
	got, err := devicegw.ProbeDeviceAvailability(&probeRegistry{})
	if err != nil {
		t.Fatalf("ProbeDeviceAvailability() error = %v", err)
	}
	if got.Status != devicegw.DeviceProbeStatusSkip || got.ReasonCode != devicegw.DeviceProbeSkipNoDevices {
		t.Fatalf("availability = %#v, want combined skip", got)
	}
}

func TestProbeDeviceAvailabilityPropagatesEnumerationFailure(t *testing.T) {
	enumerationErr := errors.New("audio backend unavailable")
	got, err := devicegw.ProbeDeviceAvailability(&probeRegistry{listErr: enumerationErr})
	if err == nil || !errors.Is(err, enumerationErr) || got.Status != "" {
		t.Fatalf("availability/error = %#v / %v, want empty result wrapping enumeration error", got, err)
	}
	if !strings.Contains(err.Error(), "enumerate audio devices") {
		t.Fatalf("error = %v, want enumeration context", err)
	}
}

func TestProbeDeviceAvailabilityRejectsMalformedSnapshot(t *testing.T) {
	got, err := devicegw.ProbeDeviceAvailability(&probeRegistry{devices: []devicegw.Device{{Direction: devicegw.DirectionInput}}})
	if err == nil || !errors.Is(err, devicegw.ErrInvalidDevice) || got.Status != "" {
		t.Fatalf("availability/error = %#v / %v, want invalid metadata error", got, err)
	}
}

type probeRegistry struct {
	devices      []devicegw.Device
	listErr      error
	listCalls    int
	defaultCalls int
	openCalls    int
}

func (r *probeRegistry) List() ([]devicegw.Device, error) {
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]devicegw.Device(nil), r.devices...), nil
}

func (r *probeRegistry) Default(devicegw.Direction) (devicegw.Device, error) {
	r.defaultCalls++
	return devicegw.Device{}, fmt.Errorf("probe availability must not resolve defaults")
}

func (r *probeRegistry) Open(devicegw.DeviceID) (devicegw.OpenedDevice, error) {
	r.openCalls++
	return nil, fmt.Errorf("probe availability must not open devices")
}
