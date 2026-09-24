package agentruntime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	roomevidencewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
)

func (s *roomTestSession) publish(events ...messages.StreamMessage) {
	for _, event := range events {
		if !s.receive.Write(context.Background(), event) {
			panic("room test session could not publish event")
		}
	}
}

func TestRoomEvidence_RecordsAudioDroppedIsExplicitNotSilent(t *testing.T) {
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
	outputDir := t.TempDir()
	service := roomevidencewire.NewService()
	preparedDir, err := service.PrepareOutput(filepath.Join(outputDir, "run"))
	if err != nil {
		t.Fatalf("prepare room evidence: %v", err)
	}
	evidence, err := service.Open(roomevidence.RecordingRequest{
		Destination: preparedDir,
		Manifest:    manifest,
		AudioFormat: roomevidence.AudioFormat{SampleRate: 24000, Channels: 1, FrameDuration: 20 * time.Millisecond},
		StartedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("open room evidence: %v", err)
	}
	cleanupRoomEvidence(t, evidence.Close)
	if err := evidence.Observe(roomevidence.Observation{
		Kind: roomevidence.ObservationAudioDropped, ParticipantID: "listener",
		Artifact: "send mixed PCM: session not in duplex mode", DroppedBytes: 480,
	}); err != nil {
		t.Fatalf("record audio drop: %v", err)
	}

	if _, err := evidence.Finalize(roomevidence.Finalization{
		Room: roomevidence.RoomResult{
			TerminationReason: roomevidence.RoomTerminationStopped,
			Participants: map[string]roomevidence.RoomParticipantResult{
				"listener": {ID: "listener", TerminationReason: roomevidence.ParticipantTerminationEnded},
			},
		},
		EndedAt: time.Now(),
	}); err != nil {
		t.Fatalf("finalize evidence: %v", err)
	}

	artifacts := evidence.Artifacts("listener")
	diagnostics := readRoomEvidenceJSONLLines(t, filepath.Join(preparedDir, artifacts.Diagnostics))
	found := false
	for _, line := range diagnostics {
		var record roomEvidenceDiagnosticLine
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

	timeline := readRoomEvidenceJSONLLines(t, filepath.Join(preparedDir, RoomEvidenceTimelinePath))
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

func cleanupRoomEvidence(t *testing.T, closeEvidence func() error) {
	t.Helper()
	t.Cleanup(func() {
		if err := closeEvidence(); err != nil {
			t.Errorf("close room evidence: %v", err)
		}
	})
}
