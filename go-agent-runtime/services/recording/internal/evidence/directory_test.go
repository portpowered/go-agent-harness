package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

func newEvidenceRecorder(t *testing.T) *directoryRecorder {
	t.Helper()
	r, err := newDirectoryRecorder(recording.LiveEvidenceOptions{Destination: filepath.Join(t.TempDir(), "capture"), ClockBase: evidenceTime(), WallClockStart: evidenceTime()}, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	// A completed fake provider writes its actual fixture observation. Tests
	// exercising missing evidence remove this source explicitly.
	if err := os.WriteFile(r.ProviderCapturePath(), []byte(`{"fixture_observation":"session.created"}`), evidenceFileMode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Finalize(t.Context(), nil); err != nil {
			t.Logf("recording cleanup: %v", err)
		}
	})
	return r
}

func evidenceTime() time.Time { return time.Unix(1_750_000_000, 123) }

func recordEvidenceText(t *testing.T, r *directoryRecorder, text string) {
	t.Helper()
	err := r.RecordMessage(t.Context(), session.LiveRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Message: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(text)}})
	if err != nil {
		t.Fatal(err)
	}
}

func recordEvidenceTerminal(t *testing.T, r *directoryRecorder) {
	t.Helper()
	terminal := messages.NewSessionCloseValueWithTerminal("fixture", "completed", "completed", messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)
	if err := r.RecordEvent(t.Context(), session.LiveEvent{Kind: string(session.LiveEventTerminal), Timestamp: evidenceTime(), Terminal: terminal}); err != nil {
		t.Fatal(err)
	}
}

func readEvidenceFile(t *testing.T, r *directoryRecorder, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.destination, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func evidenceManifest(t *testing.T, r *directoryRecorder) transcript.RecordingManifest {
	t.Helper()
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(readEvidenceFile(t, r, "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func blockEvidenceWriter(r *directoryRecorder) (<-chan struct{}, func()) {
	entered, release := make(chan struct{}), make(chan struct{})
	var writeOnce, releaseOnce sync.Once
	r.writeSpool = func(file *os.File, data []byte) error {
		writeOnce.Do(func() { close(entered); <-release })
		return writeAll(file, data)
	}
	return entered, func() { releaseOnce.Do(func() { close(release) }) }
}

func waitForEvidenceWriter(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not receive initial observation")
	}
}

func TestDirectoryRecorderAdmissionDoesNotWaitForDiskAndOwnsPayloads(t *testing.T) {
	r := newEvidenceRecorder(t)
	entered, release := blockEvidenceWriter(r)
	defer release()
	recordEvidenceText(t, r, "first")
	waitForEvidenceWriter(t, entered)
	value := messages.NewTextDeltaValue("immutable")
	if err := r.RecordMessage(t.Context(), session.LiveRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Message: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: value}}); err != nil {
		t.Fatal(err)
	}
	value.Content = "mutated"
	samples := []int16{1, -2, 32767}
	frame := sharedaudio.PCMFrame{Samples: samples, Format: sharedaudio.PCM16DeviceFormat(24000), Sequence: 7, StartSample: 42, Epoch: 3, EndOfResponse: true}
	admitted := make(chan error, 1)
	go func() {
		admitted <- r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: evidenceTime(), Frame: frame})
	}()
	select {
	case err := <-admitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("audio admission waited for blocked disk writer")
	}
	samples[0] = 99
	recordEvidenceTerminal(t, r)
	release()
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if got := readEvidenceFile(t, r, "audio/out-000.pcm"); !bytes.Equal(got, codec.EncodePCM16([]int16{1, -2, 32767})) {
		t.Fatalf("recorded PCM changed: %v", got)
	}
	log := string(readEvidenceFile(t, r, "session-log.jsonl"))
	if !strings.Contains(log, "firstimmutable") || strings.Contains(log, "mutated") {
		t.Fatalf("async recorder retained caller mutation: %s", log)
	}
	assertEvidenceAudioBoundary(t, r, frame)
	if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Frame: frame, Timestamp: evidenceTime()}); !errors.Is(err, recording.ErrLiveEvidenceClosed) {
		t.Fatalf("late admission = %v", err)
	}
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatalf("repeated finalize = %v", err)
	}
}

func assertEvidenceAudioBoundary(t *testing.T, r *directoryRecorder, want sharedaudio.PCMFrame) {
	t.Helper()
	for _, line := range bytes.Split(readEvidenceFile(t, r, "client.transcript.jsonl"), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		record, err := transcript.Decode(line)
		if err != nil {
			t.Fatal(err)
		}
		if record.Stream != transcript.StreamRuntimeAudio {
			continue
		}
		var boundary evidenceAudioBoundary
		if err := json.Unmarshal(record.Payload, &boundary); err != nil {
			t.Fatal(err)
		}
		if boundary.Frame.Format != want.Format || boundary.Frame.Sequence != want.Sequence || boundary.Frame.Epoch != want.Epoch || boundary.Frame.StartSample != want.StartSample || !boundary.Frame.EndOfResponse || boundary.SampleCount != len(want.Samples) || boundary.ByteOffset != 0 {
			t.Fatalf("frame metadata changed: %+v", boundary)
		}
		if record.Timestamp != evidenceTime().UTC().Format(time.RFC3339Nano) {
			t.Fatalf("observation timestamp = %s", record.Timestamp)
		}
		return
	}
	t.Fatal("missing audio boundary index")
}

func TestDirectoryRecorderMissingTerminalCannotClaimProviderCompletion(t *testing.T) {
	r := newEvidenceRecorder(t)
	recordEvidenceText(t, r, "unfinished")
	if err := r.Finalize(t.Context(), nil); err == nil {
		t.Fatal("missing terminal reported complete")
	}
	manifest := evidenceManifest(t, r)
	if manifest.RecordingStatus == nil || manifest.RecordingStatus.State != transcript.RecordingStatusPartial || manifest.Terminal != nil {
		t.Fatalf("fabricated terminal evidence: %+v", manifest)
	}
	if _, err := os.Stat(r.lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claim was not released: %v", err)
	}
}

func TestDirectoryRecorderOverflowIsDurablyPartial(t *testing.T) {
	r := newEvidenceRecorder(t)
	entered, release := blockEvidenceWriter(r)
	defer release()
	recordEvidenceText(t, r, "first")
	waitForEvidenceWriter(t, entered)
	for range directoryEvidenceQueueCapacity + 1 {
		recordEvidenceText(t, r, "queued")
	}
	recordEvidenceTerminal(t, r)
	release()
	if err := r.Finalize(t.Context(), nil); err == nil {
		t.Fatal("queue overflow was lost")
	}
	manifest := evidenceManifest(t, r)
	if manifest.RecordingStatus == nil || manifest.RecordingStatus.State != transcript.RecordingStatusPartial {
		t.Fatal("overflow bundle claims complete")
	}
}

func TestDirectoryRecorderDiskFailurePreservesCauseAndPartialManifest(t *testing.T) {
	r := newEvidenceRecorder(t)
	failure := errors.New("fixture disk full")
	writes := 0
	r.writeSpool = func(file *os.File, data []byte) error {
		writes++
		if writes > 1 {
			return failure
		}
		return writeAll(file, data)
	}
	recordEvidenceText(t, r, "first")
	recordEvidenceTerminal(t, r)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Finalize(ctx, nil); !errors.Is(err, failure) {
		t.Fatalf("finalize lost disk error: %v", err)
	}
	manifest := evidenceManifest(t, r)
	if manifest.RecordingStatus == nil || manifest.RecordingStatus.State != transcript.RecordingStatusPartial {
		t.Fatal("disk failure bundle claims complete")
	}
}

func TestDirectoryRecorderRetainsLifecycleErrorTextAndCorrelation(t *testing.T) {
	r := newEvidenceRecorder(t)
	err := r.RecordEvent(t.Context(), session.LiveEvent{Kind: "tool.failure", Timestamp: evidenceTime(), ToolCallID: "call-fixture", ResponseID: "response-fixture", Error: errors.New("execution timed out")})
	if err != nil {
		t.Fatal(err)
	}
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range bytes.Split(readEvidenceFile(t, r, "agent.transcript.jsonl"), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		record, err := transcript.Decode(line)
		if err != nil {
			t.Fatal(err)
		}
		if record.Stream == transcript.StreamRuntimeEvent && bytes.Contains(record.Payload, []byte("execution timed out")) {
			found = bytes.Contains(record.Payload, []byte("call-fixture")) && bytes.Contains(record.Payload, []byte("response-fixture"))
		}
	}
	if !found {
		t.Fatal("lost tool timeout cause or correlation")
	}
}

func TestDirectoryRecorderTurnOffsetsAndToolResultsRemainCorrelated(t *testing.T) {
	r := newEvidenceRecorder(t)
	send := func(direction session.LiveRecordDirection, msg messages.StreamMessage) {
		t.Helper()
		if err := r.RecordMessage(t.Context(), session.LiveRecord{Direction: direction, Timestamp: evidenceTime(), Message: msg}); err != nil {
			t.Fatal(err)
		}
	}
	audio := func(direction session.LiveRecordDirection, samples []int16) {
		t.Helper()
		if err := r.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: direction, Timestamp: evidenceTime(), Frame: sharedaudio.PCMFrame{Samples: samples, Format: sharedaudio.PCM16DeviceFormat(24000)}}); err != nil {
			t.Fatal(err)
		}
	}
	send(session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleUser, Value: messages.NewTextDeltaValue("question")})
	audio(session.LiveRecordClient, []int16{1, 2})
	send(session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Value: messages.NewToolCallEndValue("call-summary", "resume", `{"video":"fixture"}`)})
	send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	send(session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleTool, ToolCallId: "call-summary", Value: messages.NewTextDeltaValue("tool result")})
	audio(session.LiveRecordAgent, []int16{3, 4})
	recordEvidenceText(t, r, "answer")
	send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	audio(session.LiveRecordClient, []int16{5})
	send(session.LiveRecordClient, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	audio(session.LiveRecordAgent, []int16{6})
	recordEvidenceText(t, r, "next answer")
	send(session.LiveRecordAgent, messages.StreamMessage{Type: messages.StreamTypeMessageEnd})
	recordEvidenceTerminal(t, r)
	if err := r.Finalize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	var entries []evidenceLogEntry
	for _, line := range bytes.Split(bytes.TrimSpace(readEvidenceFile(t, r, "session-log.jsonl")), []byte{'\n'}) {
		var entry evidenceLogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if len(entries) != 2 {
		t.Fatalf("tool continuation split conversation into %d turns", len(entries))
	}
	first, second := entries[0], entries[1]
	if first.Input.Text != "question" || first.Response.Text != "answer" || !first.Input.Committed || !first.Response.Complete {
		t.Fatalf("first turn = %+v", first)
	}
	if len(first.ToolEvents) != 2 || first.ToolEvents[0].Arguments != `{"video":"fixture"}` || first.ToolEvents[1].Content != "tool result" || first.ToolEvents[1].ToolCallID != "call-summary" || first.ToolEvents[0].Sequence == 0 {
		t.Fatalf("tool evidence = %+v", first.ToolEvents)
	}
	if second.Input.AudioOffsetBytes != 4 || second.Response.AudioOffsetBytes != 4 || second.Input.AudioBytes != 2 || second.Response.AudioBytes != 2 {
		t.Fatalf("second turn offsets = %+v", second)
	}
	assertEvidencePCM(t, r, "audio/in-000.pcm", []int16{1, 2, 5})
	assertEvidencePCM(t, r, "audio/out-000.pcm", []int16{3, 4, 6})
}

func assertEvidencePCM(t *testing.T, r *directoryRecorder, path string, samples []int16) {
	t.Helper()
	if got := readEvidenceFile(t, r, path); !bytes.Equal(got, codec.EncodePCM16(samples)) {
		t.Fatalf("%s PCM lost tail: %v", path, got)
	}
}
