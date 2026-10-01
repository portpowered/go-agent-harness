//go:build windows

package devices

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

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
			err := mapWASAPIOpenError(id, "open endpoint", wasapiCodedError{hr: testCase.hr, err: wasapiHRESULTError(testCase.hr)})
			if !errors.Is(err, testCase.want) || !testCase.as(err) {
				t.Fatalf("error=%v, want typed %v", err, testCase.want)
			}
			if !strings.Contains(err.Error(), string(id)) && errors.Is(testCase.want, ErrDeviceInUse) {
				t.Fatalf("error=%q does not name device %q", err, id)
			}
		})
	}
}

// TestWindowsPortablePlaybackBurstPreservesFIFOCanonicalCaptureEnergy keeps
// the existing Windows CI regex while proving that a portable callback burst
// can measure its captured bytes through the same platform-neutral helper.
// It intentionally uses the virtual callback seam; it is not native WASAPI or
// acoustic evidence.
func TestWindowsPortablePlaybackBurstPreservesFIFOCanonicalCaptureEnergy(t *testing.T) {
	_, output, input := adversarialVirtualPair(t)
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

// TestWindowsPortablePlaybackBurstPreservesFIFOCanonicalCaptureAdapter is
// the selected Windows CI entry point for the direct native adapter seam. It
// uses real byte storage and the same callback-shaped name as the portable
// playback coverage, while keeping hardware/acoustic evidence out of unit CI.
func TestWindowsPortablePlaybackBurstPreservesFIFOCanonicalCaptureAdapter(t *testing.T) {
	runWASAPICapturePacketEnergyC42Coverage(t)
}

func runWASAPICapturePacketEnergyC42Coverage(t *testing.T) {
	t.Helper()

	raw := []byte{
		0x01, 0x40, 0x00, 0xc0, 0x00, 0x20, 0xaa, 0xbb,
		0x00, 0x00, 0x00, 0x80, 0x00, 0xe0, 0xcc, 0xdd,
		0x13, 0x37,
	}
	format := wasapiAudioFormat{
		formatTag:          waveFormatExtensible,
		channels:           3,
		blockAlign:         8,
		bitsPerSample:      16,
		validBitsPerSample: 12,
		subFormat:          wasapiSubtypePCM,
	}
	wantEnergy := 1744863233.0 / 1073741824.0
	before := append([]byte(nil), raw...)
	energy, err := wasapiCapturePacketEnergy(unsafe.Pointer(&raw[0]), 2, 0, format)
	if err != nil {
		t.Fatal(err)
	}
	if energy != wantEnergy {
		t.Fatalf("padded 3-channel capture energy = %.17g, want %.17g", energy, wantEnergy)
	}
	if !bytes.Equal(raw, before) {
		t.Fatal("adapter changed caller-owned PCM bytes")
	}

	malformed := format
	malformed.channels = 0
	malformed.validBitsPerSample = 0
	malformed.subFormat = syscall.GUID{Data1: 0xfeedface}
	if energy, err := wasapiCapturePacketEnergy(nil, 0, 0, malformed); err != nil || energy != 0 {
		t.Fatalf("zero-frame malformed capture = %g, %v, want exact zero", energy, err)
	}
	if energy, err := wasapiCapturePacketEnergy(nil, 2, audclntBufferFlagsSilent, malformed); err != nil || energy != 0 {
		t.Fatalf("silent malformed capture = %g, %v, want exact zero", energy, err)
	}
	if _, err := wasapiCapturePacketEnergy(nil, 2, 0, format); err == nil {
		t.Fatal("non-silent nil capture returned nil error")
	}

	unsupported := format
	unsupported.subFormat = syscall.GUID{Data1: 0xfeedface}
	unsupportedBefore := append([]byte(nil), raw...)
	if _, err := wasapiCapturePacketEnergy(unsafe.Pointer(&raw[0]), 2, 0, unsupported); err == nil || !strings.Contains(err.Error(), "unsupported WASAPI audio subformat") {
		t.Fatalf("unsupported subformat error = %v", err)
	}
	if !bytes.Equal(raw, unsupportedBefore) {
		t.Fatal("unsupported-subformat adapter path changed caller bytes or trailing sentinels")
	}

	badLayout := format
	badLayout.channels = 3
	badLayout.blockAlign = 4
	layoutRaw := []byte{0x11, 0x22, 0x33, 0x44, 0xfe, 0xed, 0xca, 0xfe}
	layoutBefore := append([]byte(nil), layoutRaw...)
	if _, err := wasapiCapturePacketEnergy(unsafe.Pointer(&layoutRaw[0]), 1, 0, badLayout); !errors.Is(err, codec.ErrInvalidPacketDimensions) {
		t.Fatalf("malformed adapter layout error = %v, want ErrInvalidPacketDimensions", err)
	}
	if !bytes.Equal(layoutRaw, layoutBefore) {
		t.Fatal("malformed-layout adapter path changed caller bytes or trailing sentinels")
	}

	floatFormat := wasapiAudioFormat{
		formatTag:          waveFormatExtensible,
		channels:           1,
		blockAlign:         8,
		bitsPerSample:      64,
		validBitsPerSample: 64,
		subFormat:          wasapiSubtypeIEEEFloat,
	}
	nonFiniteRaw := wasapiFloat64Packet(0.5, math.Inf(-1))
	nonFiniteBefore := append([]byte(nil), nonFiniteRaw...)
	if _, err := wasapiCapturePacketEnergy(unsafe.Pointer(&nonFiniteRaw[0]), 2, 0, floatFormat); !errors.Is(err, codec.ErrNonFiniteSample) {
		t.Fatalf("later non-finite adapter error = %v, want ErrNonFiniteSample", err)
	}
	if !bytes.Equal(nonFiniteRaw, nonFiniteBefore) {
		t.Fatal("non-finite adapter path changed caller-owned bytes")
	}

	high := math.Ldexp(1, 511)
	overflowRaw := wasapiFloat64Packet(high, high, high, high)
	overflowBefore := append([]byte(nil), overflowRaw...)
	overflowFormat := floatFormat
	overflowFormat.channels = 4
	overflowFormat.blockAlign = 32
	if _, err := wasapiCapturePacketEnergy(unsafe.Pointer(&overflowRaw[0]), 1, 0, overflowFormat); !errors.Is(err, codec.ErrPacketEnergyOverflow) {
		t.Fatalf("four-channel adapter sum overflow = %v, want ErrPacketEnergyOverflow", err)
	}
	if !bytes.Equal(overflowRaw, overflowBefore) {
		t.Fatal("overflow adapter path changed caller-owned bytes")
	}
}

func wasapiFloat64Packet(values ...float64) []byte {
	raw := make([]byte, len(values)*8)
	for index, value := range values {
		binary.LittleEndian.PutUint64(raw[index*8:], math.Float64bits(value))
	}
	return raw
}
