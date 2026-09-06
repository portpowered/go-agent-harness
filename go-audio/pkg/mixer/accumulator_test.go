package mixer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func TestPCMAccumulatorAlignsOverlapAndClipsAtFinalize(t *testing.T) {
	format := Format{SampleRate: 24000, Channels: 1, FrameDuration: time.Millisecond}
	accumulator, err := NewPCMAccumulator(format, time.Second)
	if err != nil {
		t.Fatalf("NewPCMAccumulator: %v", err)
	}
	if err := accumulator.Add(0, []int16{30000, 100}); err != nil {
		t.Fatalf("Add first source: %v", err)
	}
	if err := accumulator.Add(0, []int16{10000, -100}); err != nil {
		t.Fatalf("Add overlapping source: %v", err)
	}
	path := filepath.Join(t.TempDir(), "mix.wav")
	if err := accumulator.Finalize(2*time.Millisecond, path); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rate, samples, err := wavio.Read(file)
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("close finalized mix: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("Read finalized mix: %v", err)
	}
	if rate != 24000 {
		t.Fatalf("sample rate = %d, want 24000", rate)
	}
	if len(samples) != 48 {
		t.Fatalf("sample count = %d, want 48", len(samples))
	}
	if samples[0] != 32767 || samples[1] != 0 {
		t.Fatalf("overlap samples = %v, want [32767 0]", samples[:2])
	}
}

func TestPCMAccumulatorWritesHonestEmptyWAV(t *testing.T) {
	format := Format{SampleRate: 24000, Channels: 1, FrameDuration: time.Millisecond}
	accumulator, err := NewPCMAccumulator(format, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "empty.wav")
	if err := accumulator.Finalize(0, path); err != nil {
		t.Fatalf("Finalize empty timeline: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := wavio.Inspect(file)
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("close empty mix: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("Inspect empty WAV: %v", err)
	}
	if layout.SampleRate != 24000 || layout.DataBytes != 0 {
		t.Fatalf("empty WAV layout = rate %d data bytes %d, want rate 24000 and no data", layout.SampleRate, layout.DataBytes)
	}
}

func TestPCMAccumulatorRetainsOffsetTailAndReportsBound(t *testing.T) {
	format := Format{SampleRate: 24000, Channels: 1, FrameDuration: time.Millisecond}
	accumulator, err := NewPCMAccumulator(format, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := accumulator.Add(10*time.Millisecond, []int16{12}); err == nil {
		t.Fatal("Add beyond bound succeeded")
	}
	path := filepath.Join(t.TempDir(), "bounded.wav")
	err = accumulator.Finalize(time.Millisecond, path)
	if err == nil || !strings.Contains(err.Error(), "duration bound") {
		t.Fatalf("Finalize error = %v, want duration bound", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, samples, readErr := wavio.Read(file)
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("close bounded mix: %v", closeErr)
	}
	if readErr != nil {
		t.Fatalf("Read bounded mix: %v", readErr)
	}
	if len(samples) != 24 {
		t.Fatalf("bounded sample count = %d, want 24", len(samples))
	}

	longer, err := NewPCMAccumulator(format, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := longer.Add(10*time.Millisecond, []int16{12}); err != nil {
		t.Fatal(err)
	}
	longPath := filepath.Join(t.TempDir(), "tail.wav")
	if err := longer.Finalize(time.Millisecond, longPath); err != nil {
		t.Fatal(err)
	}
	longFile, err := os.Open(longPath)
	if err != nil {
		t.Fatal(err)
	}
	_, tail, readErr := wavio.Read(longFile)
	if closeErr := longFile.Close(); closeErr != nil {
		t.Fatalf("close retained-tail mix: %v", closeErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(tail) != 241 || tail[240] != 12 {
		t.Fatalf("retained tail length/value = %d/%d, want 241/12", len(tail), tail[240])
	}
}

func TestPCMAccumulatorUsesAuthoritativeSampleCursor(t *testing.T) {
	format := Format{SampleRate: 16000, Channels: 1, FrameDuration: time.Millisecond}
	accumulator, err := NewPCMAccumulator(format, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// The frame arrived immediately, but its provider cursor is 100 samples
	// into the source stream. Arrival timing must not erase that lineage.
	if err := accumulator.AddFrame(0, 100, true, []int16{7, 8}); err != nil {
		t.Fatalf("AddFrame: %v", err)
	}
	path := filepath.Join(t.TempDir(), "cursor.wav")
	if err := accumulator.Finalize(0, path); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, samples, err := wavio.Read(file)
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("close cursor mix: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("Read cursor mix: %v", err)
	}
	if len(samples) != 102 || samples[100] != 7 || samples[101] != 8 {
		t.Fatalf("cursor timeline length/samples = %d/%v, want 102/[7 8] at 100", len(samples), samples[100:])
	}
}

func TestPCMAccumulatorReanchorsAfterEndOfResponsePause(t *testing.T) {
	format := Format{SampleRate: 16000, Channels: 1, FrameDuration: time.Millisecond}
	accumulator, err := NewPCMAccumulator(format, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	key := SourceKey{SourceID: "alice", StreamID: "provider", Epoch: 1}
	if err := accumulator.AddSource(key, 0, 0, true, true, []int16{3}); err != nil {
		t.Fatalf("AddSource first response: %v", err)
	}
	// The provider's cursor remains monotonic, but the inter-response pause is
	// represented by the new arrival anchor after EndOfResponse.
	if err := accumulator.AddSource(key, 100*time.Millisecond, 1, true, false, []int16{4}); err != nil {
		t.Fatalf("AddSource second response: %v", err)
	}
	path := filepath.Join(t.TempDir(), "response-pause.wav")
	if err := accumulator.Finalize(100*time.Millisecond, path); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, samples, err := wavio.Read(file)
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("close response-pause mix: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("Read response pause mix: %v", err)
	}
	if len(samples) != 1601 || samples[0] != 3 || samples[1600] != 4 {
		t.Fatalf("response pause positions = len %d, values %d/%d; want 1601/3/4", len(samples), samples[0], samples[1600])
	}
}

func TestPCMAccumulatorBoundsSourceTimelineState(t *testing.T) {
	format := Format{SampleRate: 16000, Channels: 1, FrameDuration: time.Millisecond}
	accumulator, err := NewPCMAccumulator(format, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxSourceTimelines; index++ {
		if err := accumulator.AddSource(SourceKey{SourceID: fmt.Sprintf("source-%d", index)}, 0, 0, true, false, []int16{1}); err != nil {
			t.Fatalf("AddSource %d: %v", index, err)
		}
	}
	err = accumulator.AddSource(SourceKey{SourceID: "overflow"}, 0, 0, true, false, []int16{1})
	if err == nil || !strings.Contains(err.Error(), "source timeline bound") {
		t.Fatalf("source timeline overflow error = %v, want explicit bound error", err)
	}
}
