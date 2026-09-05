package recording

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestTraceReplaySplitsBlocksAndAdvancesDeterministicClock(t *testing.T) {
	directory := newRecordingFixture(t)
	replay, err := OpenReplay(directory)
	if err != nil {
		t.Fatalf("open replay: %v", err)
	}

	var audioFrames int
	var totalSamples int
	var lastElapsed time.Duration
	for {
		event, frame, nextErr := replay.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatalf("next replay event: %v", nextErr)
		}
		if replay.Clock.Elapsed() < lastElapsed {
			t.Fatalf("replay clock moved backward from %s to %s", lastElapsed, replay.Clock.Elapsed())
		}
		lastElapsed = replay.Clock.Elapsed()
		if event.Kind != "audio" {
			if frame != nil {
				t.Fatalf("control event %q returned a frame", event.Kind)
			}
			continue
		}
		audioFrames++
		if frame == nil || frame.Format.SampleRate != 16000 || frame.StreamID != "microphone_pre_gate" {
			t.Fatalf("replayed frame = %+v, want 16 kHz microphone frame", frame)
		}
		totalSamples += len(frame.Samples)
	}
	if audioFrames != 2 {
		t.Fatalf("audio frame count = %d, want two bounded blocks", audioFrames)
	}
	if totalSamples != MaxBlockSamples+904 {
		t.Fatalf("replayed sample count = %d, want %d", totalSamples, MaxBlockSamples+904)
	}
	if lastElapsed != 37*time.Millisecond {
		t.Fatalf("final replay elapsed = %s, want 37ms", lastElapsed)
	}
}

func TestOpenReplayRejectsPCMHashCorruption(t *testing.T) {
	directory := newRecordingFixture(t)
	lines := readTimeline(t, directory)
	for index := range lines {
		var event Event
		if err := json.Unmarshal(lines[index], &event); err != nil {
			t.Fatalf("decode timeline line %d: %v", index, err)
		}
		if event.Kind == "audio" {
			event.PCMHash = "0000000000000000000000000000000000000000000000000000000000000000"
			lines[index], _ = json.Marshal(event)
			break
		}
	}
	writeTimeline(t, directory, lines)
	if _, err := OpenReplay(directory); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("corrupt PCM hash error = %v, want ErrIncomplete", err)
	}
}

func TestOpenReplayRejectsPCMByteCorruption(t *testing.T) {
	directory := newRecordingFixture(t)
	path := filepath.Join(directory, "microphone-pre-gate.wav")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) <= 44 {
		t.Fatalf("fixture WAV has no PCM payload: %d bytes", len(data))
	}
	data[44] ^= 0x01
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReplay(directory); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("corrupt PCM bytes error = %v, want ErrIncomplete", err)
	}
}

func TestOpenReplayRejectsTimestampCorruption(t *testing.T) {
	directory := newRecordingFixture(t)
	lines := readTimeline(t, directory)
	var event Event
	if err := json.Unmarshal(lines[1], &event); err != nil {
		t.Fatal(err)
	}
	event.Timestamp = time.Unix(999, 0).UTC().Format(time.RFC3339Nano)
	lines[1], _ = json.Marshal(event)
	writeTimeline(t, directory, lines)
	if _, err := OpenReplay(directory); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("corrupt timestamp error = %v, want ErrIncomplete", err)
	}
}

func TestOpenReplayRejectsMissingOrUncleanTerminal(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Event) bool
	}{
		{name: "missing terminal", edit: nil},
		{name: "unclean terminal", edit: func(event *Event) bool { event.Clean = false; return true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := newRecordingFixture(t)
			lines := readTimeline(t, directory)
			if test.edit == nil {
				lines = lines[:len(lines)-1]
			} else {
				var event Event
				if err := json.Unmarshal(lines[len(lines)-1], &event); err != nil {
					t.Fatalf("decode terminal: %v", err)
				}
				test.edit(&event)
				lines[len(lines)-1], _ = json.Marshal(event)
			}
			writeTimeline(t, directory, lines)
			if _, err := OpenReplay(directory); !errors.Is(err, ErrIncomplete) {
				t.Fatalf("terminal validation error = %v, want ErrIncomplete", err)
			}
		})
	}
}

func TestNewTraceProtectsExistingOutputs(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "microphone-pre-gate.wav"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTrace(directory, clock.NewDeterministic(time.Unix(100, 0), time.Millisecond)); err == nil {
		t.Fatal("NewTrace reused a stale WAV output")
	}
}

func TestTraceCaptureCloseIsSafeConcurrently(t *testing.T) {
	directory := t.TempDir()
	trace, err := NewTrace(directory, clock.NewDeterministic(time.Unix(100, 0), time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	frame := make([]int16, 256)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := 0; index < 100; index++ {
				trace.CaptureMicrophonePreGate(16000, frame)
			}
		}()
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- trace.Close() }()
	wg.Wait()
	if err := <-closeDone; err != nil && !errors.Is(err, ErrIncomplete) {
		t.Fatalf("concurrent close error = %v", err)
	}
}

func TestTracePreservesRuntimeAudioLineage(t *testing.T) {
	directory := t.TempDir()
	source := clock.NewDeterministic(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Millisecond)
	trace, err := NewTrace(directory, source)
	if err != nil {
		t.Fatal(err)
	}
	trace.ObserveRuntime(RuntimeEvent{
		Kind: "audio_output", ResponseID: "response-7", StreamID: "stream-2", LoopPassID: 3, Epoch: 11,
	})
	if err := trace.Close(); err != nil {
		t.Fatalf("close trace: %v", err)
	}
	var found Event
	for _, line := range readTimeline(t, directory) {
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event.Kind == "runtime" && event.RuntimeKind == "audio_output" {
			found = event
		}
	}
	if found.ResponseID != "response-7" || found.StreamID != "stream-2" || found.LoopPassID != 3 || found.Epoch != 11 {
		t.Fatalf("audio lineage = %+v", found)
	}
}

func newRecordingFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	source := clock.NewDeterministic(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Millisecond)
	trace, err := NewTrace(directory, source)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]int16, MaxBlockSamples+904)
	for index := range samples {
		samples[index] = int16(index%30000 - 15000)
	}
	trace.CaptureMicrophonePreGate(16000, samples)
	source.AdvanceBy(37 * time.Millisecond)
	trace.ObserveRuntime(RuntimeEvent{Kind: "fixture", Payload: []byte("ok")})
	if err := trace.Close(); err != nil {
		t.Fatalf("close fixture trace: %v", err)
	}
	return directory
}

func readTimeline(t *testing.T, directory string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "timeline.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var lines [][]byte
	for _, line := range splitLines(data) {
		if len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	return lines
}

func writeTimeline(t *testing.T, directory string, lines [][]byte) {
	t.Helper()
	data := make([]byte, 0)
	for _, line := range lines {
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(filepath.Join(directory, "timeline.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for index, value := range data {
		if value == '\n' {
			lines = append(lines, data[start:index])
			start = index + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
