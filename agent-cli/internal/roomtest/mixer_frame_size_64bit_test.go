//go:build amd64 || arm64 || loong64 || mips64 || mips64le || ppc64 || ppc64le || riscv64 || s390x || wasm

package roomtest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPCM16FormatRejectsDimensionOverflowThroughLegacySentinel(t *testing.T) {
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

func TestPCM16MixerRejectsTinyFrameLargeQueueOverflowBeforeSetup(t *testing.T) {
	maximumInt := int(^uint(0) >> 1)
	format := PCM16Format{SampleRate: 1, Channels: 1, FrameDuration: time.Second}
	largeQueueFrames := maximumInt/2 + 1
	for _, test := range []struct {
		name              string
		inputQueueFrames  int
		outputQueueFrames int
	}{
		{name: "input product", inputQueueFrames: largeQueueFrames, outputQueueFrames: 1},
		{name: "output product", inputQueueFrames: 1, outputQueueFrames: largeQueueFrames},
	} {
		t.Run(test.name, func(t *testing.T) {
			factoryCalls := 0
			mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
				Format:            format,
				InputQueueFrames:  test.inputQueueFrames,
				OutputQueueFrames: test.outputQueueFrames,
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
		})
	}
}

func TestPCM16MixerRejectsChannelOverflowBeforeSetup(t *testing.T) {
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
	t.Cleanup(func() {
		if err := mixer.Close(); err != nil {
			t.Errorf("large representable mixer close: %v", err)
		}
	})
	stats := mixer.Stats()
	if stats.Output.CapacityBytes != maximumInt-1 || stats.Output.CapacityFrames != 1 {
		t.Fatalf("large output capacity stats = %+v; want bytes=%d frames=1", stats.Output, maximumInt-1)
	}
}

func TestPCM16MixerStatsHandlesWideRateChannelProduct(t *testing.T) {
	format := PCM16Format{
		SampleRate:    int(uint64(1) << 62),
		Channels:      2,
		FrameDuration: 1_953_125 * time.Nanosecond,
	}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{
		Format:            format,
		InputQueueFrames:  1,
		OutputQueueFrames: 1,
		Manual:            true,
	})
	if err != nil {
		t.Fatalf("wide rate/channel mixer construction = %v", err)
	}
	t.Cleanup(func() {
		if err := mixer.Close(); err != nil {
			t.Errorf("wide rate/channel mixer close: %v", err)
		}
	})
	frameBytes, err := format.FrameBytes()
	if err != nil {
		t.Fatalf("wide rate/channel frame bytes = %v", err)
	}
	stats := mixer.Stats()
	if stats.Output.CapacityBytes != frameBytes || stats.Output.CapacityDuration != format.FrameDuration {
		t.Fatalf("wide rate/channel stats = %+v; want capacity bytes=%d duration=%s", stats.Output, frameBytes, format.FrameDuration)
	}
}
