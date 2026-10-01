package room

import (
	"context"
	"errors"
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

func TestPCM16MixerZeroConfigUsesDefaultButDirectZeroFormatRejects(t *testing.T) {
	if _, err := (PCM16Format{}).FrameSamples(); !errors.Is(err, ErrMixerInvalidFormat) {
		t.Fatalf("direct zero format error = %v, want ErrMixerInvalidFormat", err)
	}
	mixer, err := NewPCM16MixerWithConfig(context.Background(), PCM16MixerConfig{Manual: true})
	if err != nil {
		t.Fatalf("zero config construction = %v", err)
	}
	t.Cleanup(func() {
		if err := mixer.Close(); err != nil {
			t.Errorf("zero config mixer close: %v", err)
		}
	})
	if mixer.Format() != DefaultPCM16Format() {
		t.Fatalf("zero config format = %+v, want %+v", mixer.Format(), DefaultPCM16Format())
	}
	if got, err := mixer.Format().FrameBytes(); err != nil || got != 960 {
		t.Fatalf("default frame bytes = %d, %v; want 960", got, err)
	}
}
