//go:build darwin && cgo && !nomicrophone

package audio

import (
	"context"
	"testing"
	"time"

	"github.com/gen2brain/malgo"
)

func TestCoreAudioDeviceRegistryConformance(t *testing.T) {
	t.Log("platform-independent CoreAudio conformance: 7 groups, 0 capability skips")
	RunDeviceRegistryConformance(t, coreAudioPortableFixture)
}
func coreAudioPortableFixture() DeviceRegistryConformanceFixture {
	state := &coreAudioPortableState{}
	input := coreAudioTestEndpoint("persistent-input", "Test microphone", DirectionInput, true)
	output := coreAudioTestEndpoint("persistent-output", "Test speaker", DirectionOutput, true)
	state.endpoints = []coreAudioEndpoint{input, output}
	return DeviceRegistryConformanceFixture{
		Registry: &CoreAudioDeviceRegistry{enumerate: state.list, open: state.open}, InputDefault: input.device.ID,
		OutputDefault: output.device.ID, ExclusiveID: output.device.ID, RemoveDevice: state.remove,
		Observations: state.observations,
	}
}
func coreAudioTestEndpoint(uid, name string, direction Direction, defaulted bool) coreAudioEndpoint {
	device, _ := NewDevice(coreAudioBackend, coreAudioNativeID(uid, direction), name, direction)
	return coreAudioEndpoint{device: device, defaultDevice: defaulted}
}

type coreAudioPortableState struct {
	endpoints []coreAudioEndpoint
	inUse     bool
	opens     int
	releases  int
}

func (s *coreAudioPortableState) list() ([]coreAudioEndpoint, error) {
	return s.endpoints, nil
}
func (s *coreAudioPortableState) open(_ coreAudioEndpoint) (OpenedDevice, error) {
	if s.inUse {
		return nil, malgo.ErrBusy
	}
	s.inUse = true
	s.opens++
	return &coreAudioHandle{release: func() { s.inUse = false; s.releases++ }}, nil
}
func (s *coreAudioPortableState) remove(id DeviceID) {
	for index, endpoint := range s.endpoints {
		if endpoint.device.ID == id {
			s.endpoints = append(s.endpoints[:index], s.endpoints[index+1:]...)
			return
		}
	}
}
func (s *coreAudioPortableState) observations() DeviceRegistryObservations {
	return DeviceRegistryObservations{OpenCount: s.opens, ReleaseCount: s.releases}
}
func TestCoreAudioDeviceRegistryHardware(t *testing.T) {
	registry, input, output := requireCoreAudioCapabilities(t)
	for _, capability := range []struct {
		name   string
		device Device
		output bool
	}{
		{name: "input", device: input}, {name: "output", device: output, output: true},
	} {
		opened, err := registry.Open(capability.device.ID)
		if err != nil {
			t.Skipf("darwin: %s endpoint lacks open capability or is in use: %v", capability.name, err)
		}
		defer opened.Close()
		handle, ok := opened.(*coreAudioHandle)
		if !ok {
			t.Fatalf("%s handle type=%T, want *coreAudioHandle", capability.name, opened)
		}
		if capability.output {
			sink, ok := opened.(AudioSink)
			if !ok {
				t.Fatalf("%s handle does not implement AudioSink", capability.name)
			}
			frame := make([]int16, FrameSize)
			frame[0] = 1024
			if err := sink.WriteFrame(context.Background(), frame); err != nil {
				t.Fatalf("%s WriteFrame: %v", capability.name, err)
			}
			waitForCoreAudioRender(t, handle)
		} else {
			source, ok := opened.(AudioSource)
			if !ok {
				t.Fatalf("%s handle does not implement AudioSource", capability.name)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			frame := make([]int16, FrameSize)
			err = source.ReadFrame(ctx, frame)
			cancel()
			if err != nil {
				t.Fatalf("%s ReadFrame: %v", capability.name, err)
			}
			var energy int64
			for _, sample := range frame {
				if sample < 0 {
					energy -= int64(sample)
				} else {
					energy += int64(sample)
				}
			}
			if energy <= int64(len(frame))*2 {
				t.Fatalf("darwin: input signal energy=%d for %d samples, want >%d", energy, len(frame), len(frame)*2)
			}
		}
		if err := opened.Close(); err != nil {
			t.Fatalf("darwin: first close %s: %v", capability.name, err)
		}
		if err := opened.Close(); err != nil {
			t.Fatalf("darwin: second close %s: %v", capability.name, err)
		}
	}
}
func requireCoreAudioCapabilities(t *testing.T) (*CoreAudioDeviceRegistry, Device, Device) {
	t.Helper()
	registry := NewCoreAudioDeviceRegistry()
	listed, err := registry.List()
	if err != nil {
		t.Skipf("darwin: CoreAudio enumeration capability unavailable: %v", err)
	}
	second, err := registry.List()
	if err != nil || len(listed) != len(second) {
		t.Fatalf("darwin: repeated enumeration failed: first=%d second=%d err=%v", len(listed), len(second), err)
	}
	byID := make(map[DeviceID]Device, len(listed))
	for _, device := range listed {
		t.Logf("darwin CoreAudio endpoint: id=%q name=%q direction=%s", device.ID, device.Display(), device.Direction)
		byID[device.ID] = device
	}
	for index := range listed {
		if listed[index].ID != second[index].ID {
			t.Fatalf("darwin: repeated enumeration changed ID at %d: %q -> %q", index, listed[index].ID, second[index].ID)
		}
	}
	input, err := registry.Default(DirectionInput)
	if err != nil {
		t.Skipf("darwin: missing usable default input device or microphone permission: %v", err)
	}
	output, err := registry.Default(DirectionOutput)
	if err != nil {
		t.Skipf("darwin: missing usable default output device: %v", err)
	}
	if byID[input.ID].Direction != DirectionInput || byID[output.ID].Direction != DirectionOutput {
		t.Fatalf("darwin: defaults are not listed directionally: input=%#v output=%#v", input, output)
	}
	t.Logf("darwin CoreAudio defaults: input=%q output=%q", input.ID, output.ID)
	return registry, input, output
}
func waitForCoreAudioRender(t *testing.T, handle *coreAudioHandle) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if handle.nonZero.Load() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("darwin: render consumed no non-zero PCM: nonzero=%d", handle.nonZero.Load())
}
