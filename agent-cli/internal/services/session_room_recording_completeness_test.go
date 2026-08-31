package services

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

// TestRunRoom_RecordingBundleCompleteness is the hermetic (scripted
// providers, no network) regression test for the room recording bundle
// described in agent-cli/docs/room-recording-bundle.md. It exercises a real
// two-participant fan-out so participant "b" genuinely receives participant
// "a"'s spoken audio through the room mixer, then asserts all four new
// artifacts exist and are correct:
//  1. participants/<id>/sent.pcm and received.pcm for both directions.
//  2. every recorded event (deltas, diagnostics) carries a wall-clock field,
//     and manifest.clock_base is anchored to the real room start.
//  3. room-timeline.jsonl orders overlapping speech correctly.
//  4. room-mix.wav duration matches the room's wall-clock span.
func TestRunRoom_RecordingBundleCompleteness(t *testing.T) {
	ids := []string{"a", "b"}
	inferencers := map[string]*roomTestInferencer{
		"a": {events: []messages.StreamMessage{roomTestSessionOpen("a")}},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.OutputDir = filepath.Join(t.TempDir(), "room-run")
	// room-mix.wav is decoded with wavio.Read below, which only accepts
	// conventional production sample rates; use the room's real default
	// format instead of the test helper's tiny synthetic one.
	opts.MixerConfig = room.PCM16MixerConfig{}

	fanned := make(chan [2]string, 8)
	opts.onParticipantAudioFanned = func(sourceID, targetID string, _ []byte) {
		fanned <- [2]string{sourceID, targetID}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan roomTestRunOutcome, 1)
	go func() {
		result, err := RunRoomWithResult(ctx, io.Discard, opts)
		outcome <- roomTestRunOutcome{result: result, err: err}
	}()

	sessionA := waitRoomRecordingTestSession(t, inferencers["a"])
	push := func(msg messages.StreamMessage) {
		t.Helper()
		if !sessionA.receive.Write(ctx, msg) {
			t.Fatal("scripted provider session stopped before recording could observe every event")
		}
	}

	// Drive one full spoken turn on "a": response boundary, an audible
	// audio segment (which the room must fan into "b"'s mixer), then the
	// response's terminal boundary.
	push(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
	push(messages.StreamMessage{Type: messages.StreamTypeAudioStart, Role: messages.RoleAssistant, Value: messages.NewAudioStartValue()})
	push(roomTestAudioSignalEvent(12000, 240))
	push(roomTestAudioSignalEvent(12000, 240))

	fanDeadline := time.NewTimer(2 * time.Second)
	defer fanDeadline.Stop()
waitFan:
	for {
		select {
		case pair := <-fanned:
			if pair == [2]string{"a", "b"} {
				break waitFan
			}
		case <-fanDeadline.C:
			t.Fatal("a's spoken audio was not fanned to b before the deadline")
		}
	}
	// Fan-out only enqueues into b's mixer input; actual delivery to b (and
	// therefore to received.pcm) is driven by the mixer's own cadence timer,
	// so give it a little real time to drain several frames.
	time.Sleep(150 * time.Millisecond)

	push(messages.StreamMessage{Type: messages.StreamTypeAudioEnd, Role: messages.RoleAssistant, Value: messages.NewAudioEndValue()})
	push(roomTestMessageEnd())
	time.Sleep(50 * time.Millisecond)
	cancel()

	var got roomTestRunOutcome
	select {
	case got = <-outcome:
	case <-time.After(3 * time.Second):
		t.Fatal("room did not terminate after cancellation")
	}
	if got.err != nil {
		t.Fatalf("room run returned an error: %v", got.err)
	}

	manifestData := readRoomEvidenceFile(t, filepath.Join(opts.OutputDir, RoomEvidenceManifestPath))
	var manifest roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode room manifest: %v", err)
	}
	if manifest.Timing.ClockBase == "" || strings.HasPrefix(manifest.Timing.ClockBase, "1970-01-01") {
		t.Fatalf("manifest.clock_base = %q, want a real, non-epoch room start time", manifest.Timing.ClockBase)
	}
	if manifest.AudioFormat.SampleRate <= 0 {
		t.Fatalf("manifest.audio_format = %+v, want a positive sample rate", manifest.AudioFormat)
	}

	// (1) Both directions per participant.
	aArtifacts := manifest.Participants["a"].Artifacts
	bArtifacts := manifest.Participants["b"].Artifacts
	sentA := readRoomEvidenceFile(t, filepath.Join(opts.OutputDir, aArtifacts.SentPCM))
	if len(sentA) == 0 {
		t.Fatal("a's sent.pcm is empty even though a spoke")
	}
	receivedB := readRoomEvidenceFile(t, filepath.Join(opts.OutputDir, bArtifacts.ReceivedPCM))
	if len(receivedB) == 0 {
		t.Fatal("b's received.pcm is empty even though the other participant (a) spoke")
	}
	hasSignal := false
	for _, value := range receivedB {
		if value != 0 {
			hasSignal = true
			break
		}
	}
	if !hasSignal {
		t.Fatal("b's received.pcm contains only silence even though a spoke")
	}

	// (2) every recorded event carries a wall-clock field.
	deltaLines := readRoomEvidenceJSONLLines(t, filepath.Join(opts.OutputDir, aArtifacts.Deltas))
	if len(deltaLines) == 0 {
		t.Fatal("a has no recorded delta events")
	}
	for _, line := range deltaLines {
		assertRoomEvidenceWallClockFields(t, "delta", line)
	}
	diagnosticsLines := readRoomEvidenceJSONLLines(t, filepath.Join(opts.OutputDir, aArtifacts.Diagnostics))
	for _, line := range diagnosticsLines {
		assertRoomEvidenceWallClockFields(t, "diagnostic", line)
	}

	// (3) room-timeline.jsonl is chronologically ordered and orders
	// overlapping speech correctly: a's speech_start must land at or before
	// b's received_speech_start, which must land at or before a's
	// speech_end -- i.e. b's received speech is contained within a's
	// spoken segment, exactly as the scripted overlap requires.
	timelineLines := readRoomEvidenceJSONLLines(t, filepath.Join(opts.OutputDir, manifest.RoomTimeline))
	if len(timelineLines) == 0 {
		t.Fatal("room-timeline.jsonl is empty")
	}
	entries := make([]roomTimelineEntry, 0, len(timelineLines))
	for _, line := range timelineLines {
		var entry roomTimelineEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode room-timeline.jsonl entry %s: %v", line, err)
		}
		entries = append(entries, entry)
	}
	for index := 1; index < len(entries); index++ {
		if entries[index].TOffsetMS < entries[index-1].TOffsetMS {
			t.Fatalf("room-timeline.jsonl is not chronologically ordered at index %d: %+v then %+v", index, entries[index-1], entries[index])
		}
	}
	indexOf := func(event, participant string) int {
		for index, entry := range entries {
			if entry.Event == event && entry.Participant == participant {
				return index
			}
		}
		return -1
	}
	speechStart := indexOf("speech_start", "a")
	receivedStart := indexOf("received_speech_start", "b")
	speechEnd := indexOf("speech_end", "a")
	if speechStart == -1 || receivedStart == -1 || speechEnd == -1 {
		t.Fatalf("room-timeline.jsonl missing expected speech transitions: a.speech_start=%d b.received_speech_start=%d a.speech_end=%d; entries=%+v", speechStart, receivedStart, speechEnd, entries)
	}
	if !(speechStart <= receivedStart && receivedStart <= speechEnd) {
		t.Fatalf("room-timeline.jsonl does not order overlapping speech correctly: a.speech_start=%d b.received_speech_start=%d a.speech_end=%d", speechStart, receivedStart, speechEnd)
	}

	// (4) room-mix.wav duration matches the room's wall-clock span.
	mixData := readRoomEvidenceFile(t, filepath.Join(opts.OutputDir, manifest.RoomMix))
	sampleRate, samples, err := wavio.Read(bytes.NewReader(mixData))
	if err != nil {
		t.Fatalf("decode room-mix.wav: %v", err)
	}
	started, err := time.Parse(time.RFC3339Nano, manifest.Timing.StartedAt)
	if err != nil {
		t.Fatalf("parse manifest started_at: %v", err)
	}
	ended, err := time.Parse(time.RFC3339Nano, manifest.Timing.EndedAt)
	if err != nil {
		t.Fatalf("parse manifest ended_at: %v", err)
	}
	wantSamples := int(ended.Sub(started).Seconds() * float64(sampleRate))
	tolerance := sampleRate / 5 // 200ms
	if diff := len(samples) - wantSamples; diff < -tolerance || diff > tolerance {
		t.Fatalf("room-mix.wav sample count = %d, want close to %d (room span %s at %d Hz)", len(samples), wantSamples, ended.Sub(started), sampleRate)
	}
}

func assertRoomEvidenceWallClockFields(t *testing.T, kind string, line json.RawMessage) {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(line, &envelope); err != nil {
		t.Fatalf("decode %s event: %v", kind, err)
	}
	if _, ok := envelope["t_offset_ms"]; !ok {
		t.Fatalf("%s event missing t_offset_ms: %s", kind, line)
	}
	if _, ok := envelope["t_unix_ms"]; !ok {
		t.Fatalf("%s event missing t_unix_ms: %s", kind, line)
	}
}

func waitRoomRecordingTestSession(t *testing.T, inferencer *roomTestInferencer) *roomTestSession {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sessions := inferencer.sessionsSnapshot(); len(sessions) == 1 {
			return sessions[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("participant session did not connect before the deadline")
	return nil
}
