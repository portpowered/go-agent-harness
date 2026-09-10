package audio

import (
	"bytes"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestPCM16FrameSizingKnownAnswers(t *testing.T) {
	tests := []struct {
		name     string
		rate     int
		channels int
		duration time.Duration
		samples  int
		bytes    int
	}{
		{name: "8k mono", rate: 8000, channels: 1, duration: 20 * time.Millisecond, samples: 160, bytes: 320},
		{name: "8k stereo", rate: 8000, channels: 2, duration: 20 * time.Millisecond, samples: 320, bytes: 640},
		{name: "16k mono", rate: 16000, channels: 1, duration: 20 * time.Millisecond, samples: 320, bytes: 640},
		{name: "16k stereo", rate: 16000, channels: 2, duration: 20 * time.Millisecond, samples: 640, bytes: 1280},
		{name: "24k mono", rate: 24000, channels: 1, duration: 20 * time.Millisecond, samples: 480, bytes: 960},
		{name: "24k stereo", rate: 24000, channels: 2, duration: 20 * time.Millisecond, samples: 960, bytes: 1920},
		{name: "44.1k mono", rate: 44100, channels: 1, duration: 20 * time.Millisecond, samples: 882, bytes: 1764},
		{name: "44.1k stereo", rate: 44100, channels: 2, duration: 20 * time.Millisecond, samples: 1764, bytes: 3528},
		{name: "48k mono", rate: 48000, channels: 1, duration: 20 * time.Millisecond, samples: 960, bytes: 1920},
		{name: "48k stereo", rate: 48000, channels: 2, duration: 20 * time.Millisecond, samples: 1920, bytes: 3840},
		{name: "one Hz one second", rate: 1, channels: 1, duration: time.Second, samples: 1, bytes: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			samples, err := PCM16FrameSamples(test.rate, test.channels, test.duration)
			if err != nil || samples != test.samples {
				t.Fatalf("PCM16FrameSamples() = %d, %v; want %d", samples, err, test.samples)
			}
			bytes, err := PCM16FrameBytes(test.rate, test.channels, test.duration)
			if err != nil || bytes != test.bytes {
				t.Fatalf("PCM16FrameBytes() = %d, %v; want %d", bytes, err, test.bytes)
			}
		})
	}
}

func TestPCM16FrameSizingRejectsInvalidDimensionsAndFractionalSamples(t *testing.T) {
	tests := []struct {
		name     string
		rate     int
		channels int
		duration time.Duration
	}{
		{name: "zero rate", rate: 0, channels: 1, duration: time.Second},
		{name: "negative rate", rate: -1, channels: 1, duration: time.Second},
		{name: "zero channels", rate: 8000, channels: 0, duration: 20 * time.Millisecond},
		{name: "negative channels", rate: 8000, channels: -1, duration: 20 * time.Millisecond},
		{name: "zero duration", rate: 8000, channels: 1, duration: 0},
		{name: "negative duration", rate: 8000, channels: 1, duration: -time.Nanosecond},
		{name: "fractional 44.1k millisecond", rate: 44100, channels: 1, duration: time.Millisecond},
		{name: "subsample duration", rate: 1, channels: 1, duration: time.Nanosecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, err := PCM16FrameSamples(test.rate, test.channels, test.duration); got != 0 || !errors.Is(err, ErrInvalidPCM16FrameSize) {
				t.Fatalf("PCM16FrameSamples() = %d, %v; want zero and ErrInvalidPCM16FrameSize", got, err)
			}
			if got, err := PCM16FrameBytes(test.rate, test.channels, test.duration); got != 0 || !errors.Is(err, ErrInvalidPCM16FrameSize) {
				t.Fatalf("PCM16FrameBytes() = %d, %v; want zero and ErrInvalidPCM16FrameSize", got, err)
			}
		})
	}
}

func TestPCM16FrameSizingReducesValidInt64Intermediate(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("the characterization rate is not representable on 32-bit platforms")
	}
	rate := int((uint64(1) << 61) + 24000)
	samples, err := PCM16FrameSamples(rate, 1, time.Second)
	if err != nil || samples != rate {
		t.Fatalf("PCM16FrameSamples() = %d, %v; want exact rate %d", samples, err, rate)
	}
	if samples == 24000 {
		t.Fatal("rate-duration product wrapped to the old bogus 24000-sample result")
	}
	bytes, err := PCM16FrameBytes(rate, 1, time.Second)
	if err != nil || bytes != rate*2 {
		t.Fatalf("PCM16FrameBytes() = %d, %v; want exact byte count %d", bytes, err, rate*2)
	}
}

func TestPCM16FrameSizingRejectsChannelProductOverflow(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("the characterization channel count is not representable on 32-bit platforms")
	}
	channels := int((uint64(1) << 62) + 1)
	if got, err := PCM16FrameSamples(4, channels, time.Second); got != 0 || !errors.Is(err, ErrInvalidPCM16FrameSize) {
		t.Fatalf("PCM16FrameSamples() = %d, %v; want a checked channel-product error", got, err)
	}
	if got, err := PCM16FrameBytes(4, channels, time.Second); got != 0 || !errors.Is(err, ErrInvalidPCM16FrameSize) {
		t.Fatalf("PCM16FrameBytes() = %d, %v; want a checked channel-product error", got, err)
	}
}

func TestPCM16FrameSizingSeparatesSampleAndByteBounds(t *testing.T) {
	maximumInt := int(^uint(0) >> 1)
	maximumByteSafeSamples := maximumInt / 2

	if got, err := PCM16FrameSamples(maximumInt, 1, time.Second); err != nil || got != maximumInt {
		t.Fatalf("sample MaxInt boundary = %d, %v; want %d and nil", got, err, maximumInt)
	}
	if got, err := PCM16FrameBytes(maximumInt, 1, time.Second); got != 0 || !errors.Is(err, ErrInvalidPCM16FrameSize) {
		t.Fatalf("sample MaxInt byte conversion = %d, %v; want checked byte overflow", got, err)
	}
	if got, err := PCM16FrameSamples(maximumByteSafeSamples, 1, time.Second); err != nil || got != maximumByteSafeSamples {
		t.Fatalf("byte-safe sample boundary = %d, %v; want %d and nil", got, err, maximumByteSafeSamples)
	}
	if got, err := PCM16FrameBytes(maximumByteSafeSamples, 1, time.Second); err != nil || got != maximumByteSafeSamples*2 {
		t.Fatalf("byte-safe frame boundary = %d, %v; want %d", got, err, maximumByteSafeSamples*2)
	}
}

func TestPCM16ByteCapacityChecksProductsWithoutAllocation(t *testing.T) {
	maximumInt := int(^uint(0) >> 1)
	if got, err := PCM16ByteCapacity(0, maximumInt); err != nil || got != 0 {
		t.Fatalf("zero byte capacity = %d, %v; want zero and nil", got, err)
	}
	if got, err := PCM16ByteCapacity(maximumInt/2, 2); err != nil || got != (maximumInt/2)*2 {
		t.Fatalf("largest representable even capacity = %d, %v; want %d", got, err, (maximumInt/2)*2)
	}
	if got, err := PCM16ByteCapacity(maximumInt/2+1, 2); got != 0 || !errors.Is(err, ErrInvalidPCM16FrameSize) {
		t.Fatalf("overflowing byte capacity = %d, %v; want checked error", got, err)
	}
	if got, err := PCM16ByteCapacity(-1, 2); got != 0 || !errors.Is(err, ErrInvalidPCM16FrameSize) {
		t.Fatalf("negative byte capacity = %d, %v; want checked error", got, err)
	}
}

func TestPCM16FramerPreservesSplitSignalAndExactTailAcrossTurns(t *testing.T) {
	framer, err := NewPCM16Framer(16000, 24000, 720)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]int16, 1007)
	for i := range samples {
		samples[i] = int16(i*29%20000 - 10000)
	}
	reference, err := wavio.NewPCM16Resampler(16000, 24000)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := reference.Process(samples, true)
	if err != nil {
		t.Fatal(err)
	}
	for turn := 0; turn < 2; turn++ {
		var got []byte
		for offset := 0; offset < len(samples); offset += 137 {
			end := min(offset+137, len(samples))
			packets, err := framer.Push(codec.EncodePCM16(samples[offset:end]))
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, bytes.Join(packets, nil)...)
		}
		tail, err := framer.Flush()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, bytes.Join(tail, nil)...)
		if !bytes.Equal(got, codec.EncodePCM16(expected)) {
			t.Fatalf("turn %d signal/tail mismatch: bytes=%d want=%d", turn, len(got), len(expected)*2)
		}
	}
	if _, err := framer.Push([]byte{1}); !errors.Is(err, codec.ErrPCM16OddLength) {
		t.Fatalf("malformed PCM accepted: %v", err)
	}
}
