package evidence

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/mixer"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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
		if err := os.WriteFile(recorder.CapturePath(participantID), replayCaptureBytes(participantID, "offline", "fixture"), 0o600); err != nil {
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
	manifestData, err := os.ReadFile(filepath.Join(output, rooms.RoomReplayBundleManifestPath))
	if err != nil {
		t.Fatalf("read replay manifest: %v", err)
	}
	var replayManifest struct {
		Participants map[string]struct {
			Artifacts map[string]json.RawMessage `json:"artifacts"`
		} `json:"participants"`
		RoomTimeline string `json:"room_timeline"`
	}
	if err := json.Unmarshal(manifestData, &replayManifest); err != nil {
		t.Fatalf("decode replay manifest: %v", err)
	}
	timelineData, err := os.ReadFile(filepath.Join(output, rooms.RoomEvidenceTimelinePath))
	if err != nil {
		t.Fatalf("read room timeline: %v", err)
	}
	if len(replayManifest.Participants) != 2 || strings.Count(string(timelineData), "\n") < 3 {
		t.Fatalf("manifest participants/timeline = %d/%d, want 2 and lifecycle records", len(replayManifest.Participants), strings.Count(string(timelineData), "\n"))
	}
	for _, participant := range replayManifest.Participants {
		if len(participant.Artifacts) == 0 {
			t.Fatal("participant artifacts are empty")
		}
		break
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
	if replayManifest.RoomTimeline == "" {
		t.Fatal("replay manifest did not retain room timeline artifact")
	}
	if _, err := os.Stat(latencyPath); err != nil {
		t.Fatalf("room latency artifact is not retained: %v", err)
	}
}

func replayCaptureBytes(sessionID, provider, model string) []byte {
	capture := gatewaytesting.SessionCapture{
		Version:  gatewaytesting.SessionCaptureVersion,
		Provider: gatewaytesting.SessionProviderMetadata{Name: provider, Model: model},
		Session: gatewaytesting.SessionMetadata{
			ID: sessionID, FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic,
		},
		Records: []gatewaytesting.CapturedSessionEvent{{
			Sequence: 1, Direction: gatewaytesting.DirectionServerToClient,
			Type: "session.created", PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
			Payload: json.RawMessage(`{"type":"session.created","session_id":"replay"}`),
		}},
	}
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		panic(err)
	}
	return data
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
