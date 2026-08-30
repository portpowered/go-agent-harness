package services

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

func TestInjectRoomWallClock_AddsOffsetAndUnixFields(t *testing.T) {
	stamped, err := injectRoomWallClock([]byte(`{"type":"AUDIO.DELTA"}`), 1500*time.Millisecond, 1700000000123)
	if err != nil {
		t.Fatalf("injectRoomWallClock: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(stamped, &decoded); err != nil {
		t.Fatalf("decode stamped record: %v", err)
	}
	if decoded["type"] != "AUDIO.DELTA" {
		t.Fatalf("stamped record lost original field: %+v", decoded)
	}
	offset, ok := decoded["t_offset_ms"].(float64)
	if !ok || offset != 1500 {
		t.Fatalf("t_offset_ms = %v, want 1500", decoded["t_offset_ms"])
	}
	unixMs, ok := decoded["t_unix_ms"].(float64)
	if !ok || int64(unixMs) != 1700000000123 {
		t.Fatalf("t_unix_ms = %v, want 1700000000123", decoded["t_unix_ms"])
	}
}

func TestRoomSpeechTracker_TransitionsOnlyAtEdges(t *testing.T) {
	tracker := &roomSpeechTracker{}
	if event := tracker.transition(false); event != "" {
		t.Fatalf("silence-to-silence transition = %q, want none", event)
	}
	if event := tracker.transition(true); event != "start" {
		t.Fatalf("silence-to-signal transition = %q, want start", event)
	}
	if event := tracker.transition(true); event != "" {
		t.Fatalf("signal-to-signal transition = %q, want none", event)
	}
	if event := tracker.transition(false); event != "end" {
		t.Fatalf("signal-to-silence transition = %q, want end", event)
	}
}

func TestRoomMixBuffer_SumsOverlapAndPadsToSpan(t *testing.T) {
	const sampleRate = 24000 // a wavio-supported production rate
	buffer := newRoomMixBuffer(sampleRate)

	// Two participants speaking the same 5 samples starting at t=0 must sum,
	// not concatenate or overwrite.
	chunk := roomPCM16(10000, 5)
	buffer.mixAt(0, chunk)
	buffer.mixAt(0, chunk)

	path := filepath.Join(t.TempDir(), "room-mix.wav")
	span := 2 * time.Second // room ran longer than any recorded audio
	if err := buffer.finalize(span, path); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read room-mix.wav: %v", err)
	}
	decodedRate, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode room-mix.wav: %v", err)
	}
	if decodedRate != sampleRate {
		t.Fatalf("room-mix.wav sample rate = %d, want %d", decodedRate, sampleRate)
	}
	wantSamples := sampleRate * 2 // padded to the full 2s span
	if len(samples) != wantSamples {
		t.Fatalf("room-mix.wav sample count = %d, want %d (padded to room span)", len(samples), wantSamples)
	}
	if samples[0] != 20000 {
		t.Fatalf("overlapping chunks were not summed: first sample = %d, want 20000", samples[0])
	}
	for _, sample := range samples[5:] {
		if sample != 0 {
			t.Fatalf("room-mix.wav pad region is not silent: sample = %d", sample)
		}
	}
}

func TestRoomParticipantEvidence_RecordAudioDroppedIsExplicitNotSilent(t *testing.T) {
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{{
			ID:           "listener",
			SystemPrompt: "listens",
			Provider:     "provider",
			Model:        "model",
			APIKeyEnv:    "ROOM_KEY",
			Tools:        []string{},
		}},
	}
	evidence, err := newRoomEvidence(t.TempDir(), manifest, room.DefaultPCM16Format(), nil, time.Now())
	if err != nil {
		t.Fatalf("newRoomEvidence: %v", err)
	}
	participant := evidence.participant("listener")
	participant.recordAudioDropped("send mixed PCM: session not in duplex mode", 480)

	if err := evidence.finalize(RoomResult{
		TerminationReason: RoomTerminationStopped,
		Participants: map[string]RoomParticipantResult{
			"listener": {ID: "listener", TerminationReason: ParticipantTerminationEnded},
		},
	}, nil, time.Now()); err != nil {
		t.Fatalf("finalize evidence: %v", err)
	}

	diagnostics := readRoomEvidenceJSONLLines(t, filepath.Join(evidence.destination, participant.artifacts.Diagnostics))
	found := false
	for _, line := range diagnostics {
		var record selfPlayDiagnosticLine
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode diagnostic: %v", err)
		}
		if record.Event == "room.audio.input_dropped" {
			found = true
			if record.Fields["bytes"] != "480" {
				t.Fatalf("dropped-audio diagnostic fields = %+v, want bytes=480", record.Fields)
			}
		}
	}
	if !found {
		t.Fatalf("diagnostics.jsonl has no explicit room.audio.input_dropped record: %v", diagnostics)
	}

	timeline := readRoomEvidenceJSONLLines(t, filepath.Join(evidence.destination, RoomEvidenceTimelinePath))
	timelineFound := false
	for _, line := range timeline {
		var entry roomTimelineEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("decode timeline entry: %v", err)
		}
		if entry.Event == "audio_input_dropped" && entry.Participant == "listener" {
			timelineFound = true
		}
	}
	if !timelineFound {
		t.Fatalf("room-timeline.jsonl has no audio_input_dropped entry: %v", timeline)
	}
}
