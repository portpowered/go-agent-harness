//go:build darwin && cgo && !nomicrophone

package audio

import (
	"context"
	"testing"
	"time"
)

type coreAudioTestCapabilities struct {
	input  Device
	output Device
}

func TestCoreAudioDeviceRegistryConformance(t *testing.T) {
	capabilities := requireCoreAudioCapabilities(t)
	RunDeviceRegistryConformance(t, func() DeviceRegistryConformanceFixture {
		return newCoreAudioFixture(capabilities)
	})
}

func TestCoreAudioOpenProducesSignals(t *testing.T) {
	capabilities := requireCoreAudioCapabilities(t)
	registry := NewCoreAudioDeviceRegistry()
	for _, capability := range []struct {
		name   string
		device Device
		output bool
	}{
		{name: "input", device: capabilities.input},
		{name: "output", device: capabilities.output, output: true},
	} {
		opened, err := registry.Open(capability.device.ID)
		if err != nil {
			t.Fatalf("Open(%s %q): %v", capability.name, capability.device.ID, err)
		}
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
		} else {
			source, ok := opened.(AudioSource)
			if !ok {
				t.Fatalf("%s handle does not implement AudioSource", capability.name)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			readErr := source.ReadFrame(ctx, make([]int16, FrameSize))
			cancel()
			if readErr != nil {
				t.Fatalf("%s ReadFrame: %v", capability.name, readErr)
			}
		}
		waitForCoreAudioSignal(t, handle, capability.output)
		if err := opened.Close(); err != nil {
			t.Fatalf("%s Close: %v", capability.name, err)
		}
		if err := opened.Close(); err != nil {
			t.Fatalf("%s second Close: %v", capability.name, err)
		}
	}
}

func requireCoreAudioCapabilities(t *testing.T) coreAudioTestCapabilities {
	t.Helper()
	registry := NewCoreAudioDeviceRegistry()
	listed, err := registry.List()
	if err != nil {
		t.Skipf("darwin: CoreAudio enumeration unavailable: %v", err)
	}
	var capabilities coreAudioTestCapabilities
	for _, device := range listed {
		t.Logf("darwin CoreAudio endpoint: id=%q name=%q direction=%s", device.ID, device.Display(), device.Direction)
	}
	capabilities.input, err = registry.Default(DirectionInput)
	if err != nil {
		t.Skipf("darwin: missing usable default input device or microphone permission: %v", err)
	}
	capabilities.output, err = registry.Default(DirectionOutput)
	if err != nil {
		t.Skipf("darwin: missing usable default output device: %v", err)
	}
	t.Logf("darwin CoreAudio defaults: input=%q output=%q", capabilities.input.ID, capabilities.output.ID)
	for _, capability := range []struct {
		name   string
		device Device
	}{
		{name: "input", device: capabilities.input},
		{name: "output", device: capabilities.output},
	} {
		opened, openErr := registry.Open(capability.device.ID)
		if openErr != nil {
			t.Skipf("darwin: %s endpoint lacks open capability or is in use: %v", capability.name, openErr)
		}
		if closeErr := opened.Close(); closeErr != nil {
			t.Fatalf("darwin: close %s capability probe: %v", capability.name, closeErr)
		}
	}
	return capabilities
}

func waitForCoreAudioSignal(t *testing.T, handle *coreAudioHandle, output bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if output && handle.callbacks.Load() > 0 && handle.nonZero.Load() > 0 {
			return
		}
		if !output && handle.samples.Load() >= uint64(FrameSize) && handle.energy.Load() > handle.samples.Load()*2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("CoreAudio %s produced no observable signal: callbacks=%d samples=%d energy=%d nonzero=%d", map[bool]string{true: "output", false: "input"}[output], handle.callbacks.Load(), handle.samples.Load(), handle.energy.Load(), handle.nonZero.Load())
}

type coreAudioFixtureRegistry struct {
	DeviceRegistry
	exclusiveID  DeviceID
	removed      DeviceID
	inUse        bool
	openCount    int
	releaseCount int
}

func newCoreAudioFixture(capabilities coreAudioTestCapabilities) DeviceRegistryConformanceFixture {
	registry := &coreAudioFixtureRegistry{
		DeviceRegistry: NewCoreAudioDeviceRegistry(),
		exclusiveID:    capabilities.output.ID,
	}
	return DeviceRegistryConformanceFixture{
		Registry: registry, InputDefault: capabilities.input.ID, OutputDefault: capabilities.output.ID,
		ExclusiveID: capabilities.output.ID, RemoveDevice: registry.remove, Observations: registry.observations,
	}
}

func (r *coreAudioFixtureRegistry) Default(direction Direction) (Device, error) {
	device, err := r.DeviceRegistry.Default(direction)
	if err != nil {
		return Device{}, err
	}
	if device.ID == r.removed {
		return Device{}, NewNoDefaultDeviceError(direction)
	}
	return device, nil
}

func (r *coreAudioFixtureRegistry) Open(id DeviceID) (OpenedDevice, error) {
	if id == r.removed {
		return nil, NewDeviceNotFoundError(id)
	}
	if id == r.exclusiveID && r.inUse {
		return nil, NewDeviceInUseError(id)
	}
	opened, err := r.DeviceRegistry.Open(id)
	if err != nil {
		return nil, err
	}
	r.openCount++
	if id == r.exclusiveID {
		r.inUse = true
	}
	return &coreAudioFixtureHandle{parent: r, id: id, opened: opened}, nil
}

func (r *coreAudioFixtureRegistry) remove(id DeviceID) {
	r.removed = id
}

func (r *coreAudioFixtureRegistry) observations() DeviceRegistryObservations {
	return DeviceRegistryObservations{OpenCount: r.openCount, ReleaseCount: r.releaseCount}
}

type coreAudioFixtureHandle struct {
	parent *coreAudioFixtureRegistry
	id     DeviceID
	opened OpenedDevice
	closed bool
	err    error
}

func (h *coreAudioFixtureHandle) Close() error {
	if h.closed {
		return h.err
	}
	h.closed = true
	h.err = h.opened.Close()
	h.parent.releaseCount++
	if h.id == h.parent.exclusiveID {
		h.parent.inUse = false
	}
	return h.err
}
