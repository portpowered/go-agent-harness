package evidence

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestMixAccumulatorAnchorsLocalCursorsPerResponse(t *testing.T) {
	format := rooms.AudioFormat{SampleRate: 16000, Channels: 1, FrameDuration: time.Millisecond}
	mix, err := newMixAccumulator(format, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	frame := audio.PCMFrame{
		Samples: []int16{3}, Format: audio.PCM16DeviceFormat(format.SampleRate),
		StreamID: "provider", EndOfResponse: true,
	}
	if err := mix.addSource("alice", 0, frame, true, frame.Samples); err != nil {
		t.Fatalf("add first response: %v", err)
	}
	// The provider restarted its local cursor for a second response. Its
	// arrival offset anchors that response after the first one instead of
	// overlaying it at sample zero.
	frame.Samples = []int16{4}
	if err := mix.addSource("alice", 100*time.Millisecond, frame, true, frame.Samples); err != nil {
		t.Fatalf("add second response: %v", err)
	}
	path := filepath.Join(t.TempDir(), "mix.wav")
	if err := mix.finalize(100*time.Millisecond, path); err != nil {
		t.Fatalf("finalize mix: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, samples, err := wavio.Read(file)
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("read mix: %v", err)
	}
	if len(samples) != 1601 || samples[0] != 3 || samples[1600] != 4 {
		t.Fatalf("mixed response positions = len %d, first %d, second %d; want 1601/3/4", len(samples), samples[0], samples[1600])
	}
}

func TestMixAccumulatorKeepsArrivalAndCursorSeparate(t *testing.T) {
	format := rooms.AudioFormat{SampleRate: 16000, Channels: 1, FrameDuration: time.Millisecond}
	mix, err := newMixAccumulator(format, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	first := audio.PCMFrame{Samples: []int16{5}, Format: audio.PCM16DeviceFormat(format.SampleRate), StreamID: "provider", StartSample: 0}
	if err := mix.addSource("alice", 50*time.Millisecond, first, true, first.Samples); err != nil {
		t.Fatalf("add first frame: %v", err)
	}
	second := first
	second.Samples = []int16{6}
	second.StartSample = 1
	if err := mix.addSource("alice", 200*time.Millisecond, second, true, second.Samples); err != nil {
		t.Fatalf("add delayed frame: %v", err)
	}
	path := filepath.Join(t.TempDir(), "cursor-mix.wav")
	if err := mix.finalize(0, path); err != nil {
		t.Fatalf("finalize cursor mix: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, samples, err := wavio.Read(file)
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("read cursor mix: %v", err)
	}
	if len(samples) != 802 || samples[800] != 5 || samples[801] != 6 {
		t.Fatalf("cursor-aligned samples = len %d values %d/%d, want 802 and [5 6] at 800 despite delayed arrival", len(samples), samples[800], samples[801])
	}
}
