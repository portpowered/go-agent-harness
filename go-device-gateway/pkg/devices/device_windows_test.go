//go:build windows

package devices

import audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
	"unsafe"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

// TestWindowsPortablePlaybackBurstPreservesFIFO runs the same bounded pacing
// contract on Windows even though the current WASAPI registry only exposes
// endpoint discovery/probing and has no native PCM writer yet.
func TestWindowsPortablePlaybackBurstPreservesFIFO(t *testing.T) {
	_, output, input := adversarialVirtualPair(t, 24000)
	testPacedPlaybackBackend(t, output, func(raw []byte) {
		samples := make([]int16, audio.FrameSize)
		if err := input.ReadFrame(context.Background(), samples); err != nil {
			t.Fatalf("read Windows portable playback frame: %v", err)
		}
		if err := codec.EncodePCM16Into(raw, samples); err != nil {
			t.Fatal(err)
		}
	})
}

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

func TestWASAPICapturePacketEnergyMeasuresFramesAndHonorsSilence(t *testing.T) {
	format := wasapiAudioFormat{
		formatTag:          waveFormatPCM,
		channels:           1,
		blockAlign:         2,
		bitsPerSample:      16,
		validBitsPerSample: 16,
		subFormat:          wasapiSubtypePCM,
	}
	raw := make([]byte, 6)
	positive := int16(1000)
	negative := int16(-1000)
	small := int16(250)
	binary.LittleEndian.PutUint16(raw[0:], uint16(positive))
	binary.LittleEndian.PutUint16(raw[2:], uint16(negative))
	binary.LittleEndian.PutUint16(raw[4:], uint16(small))
	energy, err := wasapiCapturePacketEnergy(unsafe.Pointer(&raw[0]), 3, 0, format)
	if err != nil {
		t.Fatal(err)
	}
	if energy <= 0 {
		t.Fatalf("capture energy=%g, want positive measured energy", energy)
	}
	silentEnergy, err := wasapiCapturePacketEnergy(unsafe.Pointer(&raw[0]), 3, audclntBufferFlagsSilent, format)
	if err != nil {
		t.Fatal(err)
	}
	if silentEnergy != 0 {
		t.Fatalf("silent capture energy=%g, want zero", silentEnergy)
	}
}

// TestWindowsPortablePlaybackBurstPreservesFIFOCanonicalCaptureEnergy keeps
// the existing Windows CI regex while proving that a portable callback burst
// can measure its captured bytes through the same platform-neutral helper.
// It intentionally uses the virtual callback seam; it is not native WASAPI or
// acoustic evidence.
func TestWindowsPortablePlaybackBurstPreservesFIFOCanonicalCaptureEnergy(t *testing.T) {
	_, output, input := adversarialVirtualPair(t, 24000)
	testPacedPlaybackBackend(t, output, func(raw []byte) {
		samples := make([]int16, audio.FrameSize)
		if err := input.ReadFrame(context.Background(), samples); err != nil {
			t.Fatalf("read Windows portable playback frame: %v", err)
		}
		if err := codec.EncodePCM16Into(raw, samples); err != nil {
			t.Fatal(err)
		}
		energy, err := codec.PacketEnergy(raw, 1, len(samples), len(raw), codec.SampleFormat{
			Encoding:           codec.SampleEncodingPCM,
			BitsPerSample:      16,
			ValidBitsPerSample: 16,
		})
		if err != nil {
			t.Fatalf("measure portable captured frame: %v", err)
		}
		if energy <= 0 {
			t.Fatalf("portable captured frame energy = %g, want positive", energy)
		}
	})
}

func TestWASAPICapturePacketEnergyDelegatesCanonicalBoundedSemantics(t *testing.T) {
	raw := []byte{
		0x00, 0x40, 0x00, 0xc0, 0xaa, 0xbb,
		0x00, 0x00, 0x00, 0x80, 0xcc, 0xdd,
	}
	format := wasapiAudioFormat{
		formatTag:          waveFormatExtensible,
		channels:           2,
		blockAlign:         6,
		bitsPerSample:      16,
		validBitsPerSample: 12,
		subFormat:          wasapiSubtypePCM,
	}
	energy, err := wasapiCapturePacketEnergy(unsafe.Pointer(&raw[0]), 2, 0, format)
	if err != nil {
		t.Fatal(err)
	}
	if energy != 1.5 {
		t.Fatalf("padded capture energy = %.17g, want 1.5", energy)
	}

	if energy, err := wasapiCapturePacketEnergy(nil, 0, 0, format); err != nil || energy != 0 {
		t.Fatalf("zero-frame nil capture = %g, %v, want zero without dereference", energy, err)
	}
	if energy, err := wasapiCapturePacketEnergy(nil, 2, audclntBufferFlagsSilent, format); err != nil || energy != 0 {
		t.Fatalf("silent nil capture = %g, %v, want zero without dereference", energy, err)
	}
	if _, err := wasapiCapturePacketEnergy(nil, 2, 0, format); err == nil {
		t.Fatal("non-silent nil capture returned nil error")
	}

	invalid := format
	invalid.validBitsPerSample = 17
	if _, err := wasapiCapturePacketEnergy(unsafe.Pointer(&raw[0]), 2, 0, invalid); !errors.Is(err, codec.ErrInvalidSampleFormat) {
		t.Fatalf("invalid valid-bit metadata error = %v, want ErrInvalidSampleFormat", err)
	}

	floatRaw := make([]byte, 4)
	binary.LittleEndian.PutUint32(floatRaw, math.Float32bits(float32(math.NaN())))
	floatFormat := wasapiAudioFormat{
		channels:           1,
		blockAlign:         4,
		bitsPerSample:      32,
		validBitsPerSample: 32,
		subFormat:          wasapiSubtypeIEEEFloat,
	}
	if _, err := wasapiCapturePacketEnergy(unsafe.Pointer(&floatRaw[0]), 1, 0, floatFormat); !errors.Is(err, codec.ErrNonFiniteSample) {
		t.Fatalf("non-finite capture error = %v, want ErrNonFiniteSample", err)
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
				t.Fatalf("Windows: %s data-path assertion failed after endpoint open: %v", direction, err)
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
