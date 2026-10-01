//go:build live && windows && !nomicrophone

// These tests need real WASAPI capture and render endpoints, so they build
// only with the live tag on a Windows host with audio hardware; a missing
// endpoint there is a failure, not a skip.

package devices

import (
	"fmt"
	"slices"
	"testing"
	"time"
	"unsafe"
)

func TestWASAPIOpenHasLiveDataPath(t *testing.T) {
	registry := newWASAPIDeviceRegistry()
	_, err := registry.List()
	if err != nil {
		t.Fatalf("Windows: missing WASAPI endpoint enumeration capability: %v", err)
	}
	for _, direction := range []Direction{DirectionInput, DirectionOutput} {

		t.Run(direction.String(), func(t *testing.T) {
			selected, err := registry.Default(direction)
			if err != nil {
				t.Fatalf("Windows: missing %s default endpoint: %v", direction, err)
			}
			opened, err := registry.Open(selected.ID)
			if err != nil {
				t.Fatalf("Windows: exact %s endpoint cannot open: %v", direction, err)
			}
			defer closeForTest(t, "opened", opened)
			handle, ok := opened.(*wasapiOpenedDevice)
			if !ok {
				t.Fatal("WASAPI registry returned an unexpected opened-device type")
			}
			if err := handle.verifyDataPathForTest(); err != nil {
				t.Fatalf("Windows: %s data-path assertion failed after endpoint open: %v", direction, err)
			}
		})
	}
}

// requireWASAPIEndpoints fails unless the host lists an active capture and
// render endpoint, and returns the first render endpoint.
func requireWASAPIEndpoints(t *testing.T, probe *wasapiDeviceRegistry) Device {
	t.Helper()
	devices, err := probe.List()
	if err != nil {
		t.Fatalf("Windows: WASAPI enumeration unavailable: %v", err)
	}
	inputIndex := slices.IndexFunc(devices, func(device Device) bool { return device.Direction == DirectionInput })
	outputIndex := slices.IndexFunc(devices, func(device Device) bool { return device.Direction == DirectionOutput })
	if inputIndex < 0 {
		t.Fatal("Windows: missing active capture endpoint")
	}
	if outputIndex < 0 {
		t.Fatal("Windows: missing active render endpoint")
	}
	return devices[outputIndex]
}

func TestWASAPIDeviceRegistryConformance(t *testing.T) {
	probe := newWASAPIDeviceRegistry()
	outputDefault := requireWASAPIEndpoints(t, probe)
	if _, err := probe.Default(DirectionInput); err != nil {
		t.Fatalf("Windows: missing input default endpoint: %v", err)
	}
	if _, err := probe.Default(DirectionOutput); err != nil {
		t.Fatalf("Windows: missing output default endpoint: %v", err)
	}
	opened, err := probe.Open(outputDefault.ID)
	if err != nil {
		t.Fatalf("Windows: exclusive endpoint capability unavailable for %q: %v", outputDefault.ID, err)
	}
	// The probe open above is only a capability check; the fixture below
	// creates fresh registries for each isolated conformance subtest.
	closeForTest(t, "probe", opened)

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

func (r *wasapiDeviceRegistry) observations() DeviceRegistryObservations {
	r.mu.Lock()
	defer r.mu.Unlock()
	return DeviceRegistryObservations{
		ListCalls:    r.listCalls,
		DefaultCalls: r.defaultCalls,
		OpenCount:    r.openCount,
		ReleaseCount: r.releaseCount,
	}
}

// hideForTest lets the shared conformance fixture model a device disappearing
// after enumeration without changing the production registry contract.
func (r *wasapiDeviceRegistry) hideForTest(id DeviceID) {
	r.mu.Lock()
	r.hidden[id] = true
	r.mu.Unlock()
}

// verifyDataPathForTest observes the live client rather than only checking
// that COM activation returned a handle. Capture must expose and consume a
// packet with measurable energy, while render is fed an explicit silent packet
// and must show that the audio engine consumes the submitted frames.
func (d *wasapiOpenedDevice) verifyDataPathForTest() error {
	cleanup, err := initializeCOM()
	if err != nil {
		return err
	}
	defer cleanup()
	if d.direction == DirectionInput {
		return d.verifyCaptureDataPath()
	}
	return d.verifyRenderDataPath()
}

// verifyCaptureDataPath polls until the capture client exposes a non-empty
// packet. Energy is measured for every packet so a malformed buffer fails,
// while a valid but currently silent microphone remains a usable capability.
func (d *wasapiOpenedDevice) verifyCaptureDataPath() error {
	if d.formatErr != nil {
		return fmt.Errorf("inspect WASAPI capture format: %w", d.formatErr)
	}
	for range 40 {
		var packets uint32
		if _, err := d.service.call(audioCaptureClientVTableGetNextPacketSize, uintptr(unsafe.Pointer(&packets))); err != nil {
			return fmt.Errorf("read WASAPI capture packet size: %w", err)
		}
		if packets == 0 {
			waitWASAPIPoll()
			continue
		}
		frames, err := d.consumeCapturePacket()
		if err != nil || frames > 0 {
			// A non-empty packet is the positive data-path signal.
			return err
		}
	}
	return fmt.Errorf("WASAPI capture produced no positive signal: frames=0 max-energy=0")
}

// consumeCapturePacket acquires, measures, and releases one capture packet and
// returns its frame count.
func (d *wasapiOpenedDevice) consumeCapturePacket() (uint32, error) {
	var data unsafe.Pointer
	var frames, flags uint32
	if _, err := d.service.call(audioCaptureClientVTableGetBuffer, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)), 0, 0); err != nil {
		return 0, fmt.Errorf("acquire WASAPI capture buffer: %w", err)
	}
	if frames == 0 {
		return 0, nil
	}
	_, energyErr := wasapiCapturePacketEnergy(data, frames, flags, d.format)
	if _, releaseErr := d.service.call(audioCaptureClientVTableReleaseBuffer, uintptr(frames)); releaseErr != nil {
		return 0, fmt.Errorf("release WASAPI capture buffer: %w", releaseErr)
	}
	return frames, energyErr
}

// verifyRenderDataPath feeds an explicit silent packet and requires the audio
// engine to consume the submitted frames.
func (d *wasapiOpenedDevice) verifyRenderDataPath() error {
	var bufferSize uint32
	if _, err := d.client.call(audioClientVTableGetBufferSize, uintptr(unsafe.Pointer(&bufferSize))); err != nil {
		return fmt.Errorf("read WASAPI render buffer size: %w", err)
	}
	if bufferSize == 0 {
		return fmt.Errorf("WASAPI render buffer size is zero")
	}
	var lastBefore, lastAfter, lastSubmitted uint32
	for range 40 {
		padding, err := d.renderPadding("read WASAPI render padding")
		if err != nil {
			return err
		}
		if padding >= bufferSize {
			waitWASAPIPoll()
			continue
		}
		frames := bufferSize - padding
		submittedPadding, err := d.submitSilentRenderPacket(frames)
		if err != nil {
			return err
		}
		lastBefore, lastAfter, lastSubmitted = padding, submittedPadding, frames
		if submittedPadding <= padding {
			// The engine may have consumed the packet between ReleaseBuffer
			// and this observation. Retry until a queued packet is observable.
			waitWASAPIPoll()
			continue
		}
		return d.awaitRenderConsumption(padding, submittedPadding, frames)
	}
	return fmt.Errorf("WASAPI render submission was not observable: before=%d after=%d submitted=%d", lastBefore, lastAfter, lastSubmitted)
}

func (d *wasapiOpenedDevice) renderPadding(operation string) (uint32, error) {
	var padding uint32
	if _, err := d.client.call(audioClientVTableGetCurrentPadding, uintptr(unsafe.Pointer(&padding))); err != nil {
		return 0, fmt.Errorf("%s: %w", operation, err)
	}
	return padding, nil
}

// submitSilentRenderPacket submits frames of silence and returns the padding
// observed immediately after submission.
func (d *wasapiOpenedDevice) submitSilentRenderPacket(frames uint32) (uint32, error) {
	var buffer unsafe.Pointer
	if _, err := d.service.call(audioRenderClientVTableGetBuffer, uintptr(frames), uintptr(unsafe.Pointer(&buffer))); err != nil {
		return 0, fmt.Errorf("acquire WASAPI render buffer: %w", err)
	}
	if _, err := d.service.call(audioRenderClientVTableReleaseBuffer, uintptr(frames), audclntBufferFlagsSilent); err != nil {
		return 0, fmt.Errorf("release WASAPI render buffer: %w", err)
	}
	return d.renderPadding("read WASAPI render padding after submission")
}

func (d *wasapiOpenedDevice) awaitRenderConsumption(padding, submittedPadding, frames uint32) error {
	for range 40 {
		waitWASAPIPoll()
		consumedPadding, err := d.renderPadding("read WASAPI render padding during consumption")
		if err != nil {
			return err
		}
		if consumedPadding < submittedPadding {
			return nil
		}
	}
	return fmt.Errorf("WASAPI render engine did not consume submitted frames: before=%d after=%d submitted=%d", padding, submittedPadding, frames)
}

// wasapiPollInterval paces WASAPI packet and padding polls.
const wasapiPollInterval = 25 * time.Millisecond

// waitWASAPIPoll paces one data-path poll. The client is initialized in
// shared polling mode (no AUDCLNT_STREAMFLAGS_EVENTCALLBACK), so WASAPI
// exposes packet and padding readiness only through
// GetNextPacketSize/GetCurrentPadding; there is no event to wait on.
func waitWASAPIPoll() {
	time.Sleep(wasapiPollInterval) //nolint:forbidigo // polling-mode WASAPI client has no readiness event to wait on; see waitWASAPIPoll
}
