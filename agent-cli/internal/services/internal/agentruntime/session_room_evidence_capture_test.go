package agentruntime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func (s *roomTestSession) publish(events ...messages.StreamMessage) {
	for _, event := range events {
		if !s.receive.Write(context.Background(), event) {
			panic("room test session could not publish event")
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
