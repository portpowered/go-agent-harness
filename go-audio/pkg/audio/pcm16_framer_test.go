package audio_test

import (
	"bytes"
	"errors"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"testing"
)

func TestPCM16FramerPreservesSplitSignalAndExactTailAcrossTurns(t *testing.T) {
	framer, err := audio.NewPCM16Framer(16000, 24000, 720)
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
