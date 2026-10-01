//go:build amd64 || arm64 || loong64 || mips64 || mips64le || ppc64 || ppc64le || riscv64 || s390x || wasm

// The characterization values below are only representable when int is 64
// bits wide, so these tests are built for 64-bit architectures alone.

package audio

import (
	"errors"
	"testing"
	"time"
)

func TestPCM16FrameSizingReducesValidInt64Intermediate(t *testing.T) {
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
	channels := int((uint64(1) << 62) + 1)
	if got, err := PCM16FrameSamples(4, channels, time.Second); got != 0 || !errors.Is(err, ErrInvalidPCM16FrameSize) {
		t.Fatalf("PCM16FrameSamples() = %d, %v; want a checked channel-product error", got, err)
	}
	if got, err := PCM16FrameBytes(4, channels, time.Second); got != 0 || !errors.Is(err, ErrInvalidPCM16FrameSize) {
		t.Fatalf("PCM16FrameBytes() = %d, %v; want a checked channel-product error", got, err)
	}
}
