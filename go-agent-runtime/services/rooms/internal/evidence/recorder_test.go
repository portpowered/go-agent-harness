package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
)

func TestRecorderWritesReplayCompatibleBundleWithEmptyStreams(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC), time.Millisecond)
	manifest := evidenceTestManifest()
	output := t.TempDir()
	recorder, err := NewRecorder(output, manifest, mixer.DefaultFormat(), clock.Now(), clock)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	recorder.SetReady(rooms.RoomParticipantReady{ParticipantID: "alice", Kind: rooms.ParticipantKindAgent, Provider: "offline", Model: "fixture"})
	recorder.RecordSource("alice", audio.PCMFrame{Samples: []int16{1, 2, 3}})
	recorder.RecordReceived("bob", audio.PCMFrame{Samples: []int16{4, 5}})
	if err := recorder.Publish(context.Background(), "alice", session.LiveEvent{Sequence: 1, Kind: "response.audio_transcript.delta", Text: "hello"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	for _, participantID := range []string{"alice", "bob"} {
		if err := os.WriteFile(recorder.CapturePath(participantID), []byte("[]\n"), 0o600); err != nil {
			t.Fatalf("write provider capture %q: %v", participantID, err)
		}
	}
	clock.AdvanceBy(30 * time.Millisecond)
	result := rooms.RoomResult{TerminationReason: rooms.RoomTerminationStopped, Participants: map[string]rooms.RoomParticipantResult{
		"alice": {ID: "alice", ParticipantID: "alice", TerminationReason: rooms.ParticipantTerminationEnded, TurnsCompleted: 1},
		"bob":   {ID: "bob", ParticipantID: "bob", TerminationReason: rooms.ParticipantTerminationEnded},
	}}
	if err := recorder.Finalize(result, nil, clock.Now()); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, rooms.RoomReplayBundleManifestPath)); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	plan, err := New().Load(output)
	if err != nil {
		t.Fatalf("Load finalized room evidence: %v", err)
	}
	if len(plan.Participants) != 2 || len(plan.Timeline) < 3 {
		t.Fatalf("loaded participants/timeline = %d/%d, want 2 and lifecycle records", len(plan.Participants), len(plan.Timeline))
	}
	if plan.Participants[0].CapturePath == "" {
		t.Fatal("agent capture path is empty")
	}
	latencyPath := filepath.Join(output, rooms.RoomLatencyArtifactPath)
	latencyData, err := os.ReadFile(latencyPath)
	if err != nil {
		t.Fatalf("room latency artifact missing: %v", err)
	}
	var latencyBundle struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(latencyData, &latencyBundle); err != nil {
		t.Fatalf("decode room latency artifact: %v", err)
	}
	if latencyBundle.SchemaVersion != rooms.RoomLatencyBundleSchemaVersion {
		t.Fatalf("latency schema version = %d, want %d", latencyBundle.SchemaVersion, rooms.RoomLatencyBundleSchemaVersion)
	}
	if plan.RoomLatencyPath == "" {
		t.Fatal("loaded replay plan did not retain room latency artifact")
	}
	for _, participant := range plan.Participants {
		if len(participant.Artifacts) == 0 {
			t.Fatalf("participant %q has no artifacts", participant.ID)
		}
	}
}

func TestRecorderMarksOversizedAudioAsPartialWithoutFabricatingPCM(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC), time.Millisecond)
	output := t.TempDir()
	recorder, err := NewRecorder(output, evidenceTestManifest(), mixer.DefaultFormat(), clock.Now(), clock)
	if err != nil {
		t.Fatal(err)
	}
	recorder.RecordSource("alice", audio.PCMFrame{Samples: make([]int16, recorder.maxFrameSamples+1)})
	result := rooms.RoomResult{TerminationReason: rooms.RoomTerminationStopped, Participants: map[string]rooms.RoomParticipantResult{
		"alice": {ID: "alice", ParticipantID: "alice", TerminationReason: rooms.ParticipantTerminationEnded},
		"bob":   {ID: "bob", ParticipantID: "bob", TerminationReason: rooms.ParticipantTerminationEnded},
	}}
	if err := recorder.Finalize(result, nil, clock.Now()); err == nil {
		t.Fatal("Finalize unexpectedly succeeded for an oversized audio frame")
	}
	status, degraded := recorder.Status()
	if status == nil || status.State != "partial" || len(degraded) == 0 {
		t.Fatalf("recording status/degraded artifacts = %+v/%v, want partial evidence", status, degraded)
	}
	if _, err := New().Load(output); err == nil || !errors.Is(err, rooms.ErrReplayBundleIncomplete) {
		t.Fatalf("Load partial evidence error = %v, want ErrReplayBundleIncomplete", err)
	}
}

func TestRecorderMarksLiveEventOverflowAsPartial(t *testing.T) {
	clock := platformclock.NewDeterministic(time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC), time.Millisecond)
	output := t.TempDir()
	recorder, err := NewRecorder(output, evidenceTestManifest(), mixer.DefaultFormat(), clock.Now(), clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Publish(context.Background(), "alice", session.LiveEvent{
		Sequence: 4, Kind: string(session.LiveEventOverflow), Dropped: 3,
	}); err != nil {
		t.Fatalf("Publish overflow: %v", err)
	}
	result := rooms.RoomResult{TerminationReason: rooms.RoomTerminationStopped, Participants: map[string]rooms.RoomParticipantResult{
		"alice": {ID: "alice", ParticipantID: "alice", TerminationReason: rooms.ParticipantTerminationEnded},
		"bob":   {ID: "bob", ParticipantID: "bob", TerminationReason: rooms.ParticipantTerminationEnded},
	}}
	if err := recorder.Finalize(result, nil, clock.Now()); err == nil {
		t.Fatal("Finalize unexpectedly succeeded after live-event overflow")
	}
	status, degraded := recorder.Status()
	if status == nil || status.State != "partial" || len(degraded) == 0 {
		t.Fatalf("recording status/degraded artifacts = %+v/%v, want partial evidence", status, degraded)
	}
	if _, err := New().Load(output); err == nil || !errors.Is(err, rooms.ErrReplayBundleIncomplete) {
		t.Fatalf("Load overflow evidence error = %v, want ErrReplayBundleIncomplete", err)
	}
}

func evidenceTestManifest() rooms.Manifest {
	return rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{MaxDuration: time.Second},
		Participants: []rooms.Participant{
			{ID: "alice", Kind: rooms.ParticipantKindAgent, SystemPrompt: "alice", Provider: "offline", Model: "fixture", APIKeyEnv: "ALICE", Tools: []string{}, OpeningPrompt: "hello"},
			{ID: "bob", Kind: rooms.ParticipantKindAgent, SystemPrompt: "bob", Provider: "offline", Model: "fixture", APIKeyEnv: "BOB", Tools: []string{}},
		},
	}
}
