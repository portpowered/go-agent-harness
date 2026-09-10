//go:build windows

package devices

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
	"unsafe"

	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

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
		energy, err := audio.PacketEnergy(raw, 1, len(samples), len(raw), codec.SampleFormat{
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
