package room

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func TestPCM16FormatDelegatesCanonicalFrameSizing(t *testing.T) {
	tests := []struct {
		name    string
		format  PCM16Format
		samples int
		bytes   int
		wantErr bool
	}{
		{name: "common mono", format: PCM16Format{SampleRate: 24000, Channels: 1, FrameDuration: 20 * time.Millisecond}, samples: 480, bytes: 960},
		{name: "common stereo", format: PCM16Format{SampleRate: 48000, Channels: 2, FrameDuration: 20 * time.Millisecond}, samples: 1920, bytes: 3840},
		{name: "fractional", format: PCM16Format{SampleRate: 44100, Channels: 1, FrameDuration: time.Millisecond}, wantErr: true},
		{name: "zero direct format", format: PCM16Format{}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			samples, sampleErr := test.format.FrameSamples()
			bytes, byteErr := test.format.FrameBytes()
			if test.wantErr {
				if samples != 0 || !errors.Is(sampleErr, ErrMixerInvalidFormat) || bytes != 0 || !errors.Is(byteErr, ErrMixerInvalidFormat) {
					t.Fatalf("legacy sizing = samples %d/%v bytes %d/%v; want ErrMixerInvalidFormat and zero", samples, sampleErr, bytes, byteErr)
				}
				return
			}
			if sampleErr != nil || samples != test.samples || byteErr != nil || bytes != test.bytes {
				t.Fatalf("legacy sizing = samples %d/%v bytes %d/%v; want %d/%d", samples, sampleErr, bytes, byteErr, test.samples, test.bytes)
			}
			canonicalSamples, err := sharedaudio.PCM16FrameSamples(test.format.SampleRate, test.format.Channels, test.format.FrameDuration)
			if err != nil || canonicalSamples != samples {
				t.Fatalf("canonical samples = %d/%v, legacy = %d/%v", canonicalSamples, err, samples, sampleErr)
			}
		})
	}
}

func TestPCM16FormatRejectsDimensionOverflowThroughLegacySentinel(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("the characterization dimensions are not representable on 32-bit platforms")
	}
	tests := []struct {
		name   string
		format PCM16Format
	}{
		{name: "rate duration", format: PCM16Format{SampleRate: int((uint64(1) << 61) + 24000), Channels: 2, FrameDuration: time.Second}},
		{name: "channel product", format: PCM16Format{SampleRate: 4, Channels: int((uint64(1) << 62) + 1), FrameDuration: time.Second}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			samples, sampleErr := test.format.FrameSamples()
			bytes, byteErr := test.format.FrameBytes()
			if test.name == "rate duration" {
				wantSamples := test.format.SampleRate * test.format.Channels
				if sampleErr != nil || samples != wantSamples {
					t.Fatalf("FrameSamples() = %d, %v; want exact sample count %d", samples, sampleErr, wantSamples)
				}
			} else if samples != 0 || !errors.Is(sampleErr, ErrMixerInvalidFormat) {
				t.Fatalf("FrameSamples() = %d, %v; want checked ErrMixerInvalidFormat", samples, sampleErr)
			}
			if bytes != 0 || !errors.Is(byteErr, ErrMixerInvalidFormat) {
				t.Fatalf("FrameBytes() = %d, %v; want zero and ErrMixerInvalidFormat", bytes, byteErr)
			}
		})
	}
}

func TestPCM16MixerRejectsQueueByteOverflowBeforeSetup(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("the large frame boundary is not useful on 32-bit platforms")
	}
	format := PCM16Format{SampleRate: int(^uint(0)>>1) / 2, Channels: 1, FrameDuration: time.Second}
	factoryCalls := 0
	factory := func(time.Duration) PCM16Cadence {
		factoryCalls++
		return newDeterministicPCM16Cadence()
	}
	for _, test := range []struct {
		name              string
		inputQueueFrames  int
		outputQueueFrames int
	}{
		{name: "input product", inputQueueFrames: 2, outputQueueFrames: 1},
		{name: "output product", inputQueueFrames: 1, outputQueueFrames: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			factoryCalls = 0
			mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
				Format:            format,
				InputQueueFrames:  test.inputQueueFrames,
				OutputQueueFrames: test.outputQueueFrames,
				CadenceFactory:    factory,
			})
			if mixer != nil || !errors.Is(err, ErrMixerInvalidFormat) {
				t.Fatalf("NewPCM16MixerWithConfig() = mixer %v, error %v; want nil and ErrMixerInvalidFormat", mixer, err)
			}
			if factoryCalls != 0 {
				t.Fatalf("cadence factory calls = %d, want zero before rejected construction", factoryCalls)
			}
		})
	}
}

func TestPCM16MixerRejectsChannelOverflowBeforeSetup(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("the large channel boundary is not useful on 32-bit platforms")
	}
	factoryCalls := 0
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            PCM16Format{SampleRate: 4, Channels: int((uint64(1) << 62) + 1), FrameDuration: time.Second},
		InputQueueFrames:  1,
		OutputQueueFrames: 1,
		CadenceFactory: func(time.Duration) PCM16Cadence {
			factoryCalls++
			return newDeterministicPCM16Cadence()
		},
	})
	if mixer != nil || !errors.Is(err, ErrMixerInvalidFormat) {
		t.Fatalf("NewPCM16MixerWithConfig() = mixer %v, error %v; want nil and ErrMixerInvalidFormat", mixer, err)
	}
	if factoryCalls != 0 {
		t.Fatalf("cadence factory calls = %d, want zero before rejected construction", factoryCalls)
	}
}

func TestPCM16MixerStatsKeepsLargeRepresentableCapacity(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("the large frame boundary is not useful on 32-bit platforms")
	}
	maximumInt := int(^uint(0) >> 1)
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            PCM16Format{SampleRate: maximumInt / 2, Channels: 1, FrameDuration: time.Second},
		InputQueueFrames:  1,
		OutputQueueFrames: 1,
		Manual:            true,
	})
	if err != nil {
		t.Fatalf("large representable mixer construction = %v", err)
	}
	t.Cleanup(func() { _ = mixer.Close() })
	stats := mixer.Stats()
	if stats.Output.CapacityBytes != maximumInt-1 || stats.Output.CapacityFrames != 1 {
		t.Fatalf("large output capacity stats = %+v; want bytes=%d frames=1", stats.Output, maximumInt-1)
	}
}

func TestPCM16MixerZeroConfigUsesDefaultButDirectZeroFormatRejects(t *testing.T) {
	if _, err := (PCM16Format{}).FrameSamples(); !errors.Is(err, ErrMixerInvalidFormat) {
		t.Fatalf("direct zero format error = %v, want ErrMixerInvalidFormat", err)
	}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{Manual: true})
	if err != nil {
		t.Fatalf("zero config construction = %v", err)
	}
	t.Cleanup(func() { _ = mixer.Close() })
	if mixer.Format() != DefaultPCM16Format() {
		t.Fatalf("zero config format = %+v, want %+v", mixer.Format(), DefaultPCM16Format())
	}
	if got, err := mixer.Format().FrameBytes(); err != nil || got != 960 {
		t.Fatalf("default frame bytes = %d, %v; want 960", got, err)
	}
}
