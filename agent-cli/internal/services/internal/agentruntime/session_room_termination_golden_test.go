package agentruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type longConversationTerminationManifest struct {
	Finalized         bool                  `json:"finalized"`
	TerminationReason RoomTerminationReason `json:"termination_reason"`
	Reason            RoomTerminationReason `json:"reason"`
	Bounds            struct {
		MaxTurns int `json:"max_turns"`
	} `json:"bounds"`
	Participants map[string]longConversationParticipant `json:"participants"`
	TurnCounts   map[string]int                         `json:"turn_counts"`
	RoomTimeline string                                 `json:"room_timeline"`
	Error        string                                 `json:"error"`
}

type longConversationParticipant struct {
	ID                     string                                 `json:"id"`
	CompletedTurns         int                                    `json:"completed_turns"`
	TerminationReason      ParticipantTerminationReason           `json:"termination_reason"`
	Reason                 ParticipantTerminationReason           `json:"reason"`
	TerminationTrigger     string                                 `json:"termination_trigger"`
	TerminationDisposition string                                 `json:"termination_disposition"`
	Classification         string                                 `json:"classification"`
	TerminalReason         string                                 `json:"terminal_reason"`
	TerminalProvenance     string                                 `json:"terminal_provenance"`
	OutputState            string                                 `json:"output_state"`
	Connected              bool                                   `json:"connected"`
	Error                  string                                 `json:"error"`
	Artifacts              map[string]longConversationArtifactRef `json:"artifacts"`
}

type longConversationArtifactRef struct {
	Path string `json:"path"`
}

type longConversationEvidenceRecord struct {
	Event         string            `json:"event"`
	Type          string            `json:"type"`
	ParticipantID string            `json:"participant_id"`
	TurnID        string            `json:"turn_id"`
	Fields        map[string]string `json:"fields"`
}

const (
	longConversationTerminationParticipantA        = "agent-a"
	longConversationTerminationParticipantB        = "agent-b"
	longConversationTerminationTurnsPerParticipant = 4
)

func TestLongConversationTerminationGoldenReplaysCleanly(t *testing.T) {
	fixture := longConversationTerminationFixturePath()
	bundle, err := LoadRoomReplayAudioBundle(fixture)
	if err != nil {
		t.Fatalf("load post-fix long-conversation termination bundle: %v", err)
	}
	// Loading the same committed capture a second time is the replay control:
	// admission rechecks every artifact digest and provider capture before any
	// runtime is built, so terminal evidence cannot depend on a mutable read.
	replayed, err := LoadRoomReplayAudioBundle(fixture)
	if err != nil {
		t.Fatalf("replay post-fix long-conversation termination bundle: %v", err)
	}
	if !reflect.DeepEqual(bundle.Plan.Timeline, replayed.Plan.Timeline) {
		t.Fatal("room timeline changed between fixture admission and replay")
	}

	manifest := loadLongConversationTerminationManifest(t, fixture)
	if !manifest.Finalized || manifest.TerminationReason != RoomTerminationMaxTurnsReached || manifest.Reason != RoomTerminationMaxTurnsReached || manifest.Error != "" {
		t.Fatalf("termination golden manifest = %+v, want finalized clean max-turn outcome", manifest)
	}
	if manifest.Bounds.MaxTurns != longConversationTerminationTurnsPerParticipant || manifest.RoomTimeline != RoomEvidenceTimelinePath {
		t.Fatalf("termination golden bounds/timeline = %+v/%q, want max_turns=%d and %q", manifest.Bounds, manifest.RoomTimeline, longConversationTerminationTurnsPerParticipant, RoomEvidenceTimelinePath)
	}

	wantOrder := []string{longConversationTerminationParticipantA, longConversationTerminationParticipantB, longConversationTerminationParticipantA, longConversationTerminationParticipantB, longConversationTerminationParticipantA, longConversationTerminationParticipantB, longConversationTerminationParticipantA, longConversationTerminationParticipantB}
	turnOrder := make([]string, 0, len(wantOrder))
	boundEvents := make(map[string]int, 2)
	terminatedEvents := make(map[string]int, 2)
	for index, timelineEvent := range bundle.Plan.Timeline {
		if strings.Contains(strings.ToLower(string(timelineEvent.Raw)), "session_failure") || strings.Contains(strings.ToLower(string(timelineEvent.Raw)), "incomplete-final-response") {
			t.Fatalf("timeline event %d contains teardown failure text: %s", index+1, timelineEvent.Raw)
		}
		switch timelineEvent.Type {
		case "turn_completed":
			turnOrder = append(turnOrder, timelineEvent.ParticipantID)
		case "room_bound_shutdown":
			boundEvents[timelineEvent.ParticipantID]++
			assertLongConversationTerminalFields(t, timelineEvent.Raw, longConversationTerminalFieldsForGolden())
		case "participant_terminated":
			terminatedEvents[timelineEvent.ParticipantID]++
			assertLongConversationTerminalFields(t, timelineEvent.Raw, longConversationTerminalFieldsForGolden())
		case "provider_error":
			t.Fatalf("termination golden timeline contains provider error: %s", timelineEvent.Raw)
		case "run_terminated":
			var record longConversationEvidenceRecord
			if err := json.Unmarshal(timelineEvent.Raw, &record); err != nil {
				t.Fatalf("decode run_terminated timeline event: %v", err)
			}
			if record.Fields["reason"] != string(RoomTerminationMaxTurnsReached) {
				t.Fatalf("run_terminated fields = %v, want max_turns_reached", record.Fields)
			}
		}
	}
	if !reflect.DeepEqual(turnOrder, wantOrder) {
		t.Fatalf("termination golden turn order = %v, want %v", turnOrder, wantOrder)
	}
	for _, participantID := range []string{longConversationTerminationParticipantA, longConversationTerminationParticipantB} {
		if boundEvents[participantID] != 1 || terminatedEvents[participantID] != 1 {
			t.Fatalf("timeline terminal events for %q = bound:%d terminated:%d, want one of each", participantID, boundEvents[participantID], terminatedEvents[participantID])
		}
	}

	wantFields := longConversationTerminalFieldsForGolden()
	for _, participantID := range []string{longConversationTerminationParticipantA, longConversationTerminationParticipantB} {
		participant, ok := manifest.Participants[participantID]
		if !ok {
			t.Fatalf("termination golden manifest missing participant %q", participantID)
		}
		if participant.ID != participantID || !participant.Connected || participant.CompletedTurns != longConversationTerminationTurnsPerParticipant || participant.TerminationReason != ParticipantTerminationEnded || participant.Reason != ParticipantTerminationEnded || participant.Error != "" {
			t.Fatalf("termination golden participant %q = %+v, want connected clean participant", participantID, participant)
		}
		assertLongConversationParticipantFields(t, participant, wantFields)
		if manifest.TurnCounts[participantID] != longConversationTerminationTurnsPerParticipant {
			t.Fatalf("termination golden turn count for %q = %d, want %d", participantID, manifest.TurnCounts[participantID], longConversationTerminationTurnsPerParticipant)
		}

		planParticipant, ok := bundle.Plan.Participant(participantID)
		if !ok || planParticipant.RecordedTurnCount != longConversationTerminationTurnsPerParticipant {
			t.Fatalf("replayed participant %q = %+v, want %d recorded turns", participantID, planParticipant, longConversationTerminationTurnsPerParticipant)
		}
		if len(participant.Artifacts) == 0 {
			t.Fatalf("termination golden participant %q has no replay artifacts", participantID)
		}
		for _, role := range []string{"events", "diagnostics"} {
			artifact, ok := participant.Artifacts[role]
			if !ok || artifact.Path == "" {
				t.Fatalf("termination golden participant %q missing %s artifact", participantID, role)
			}
			records := readLongConversationEvidenceRecords(t, filepath.Join(fixture, filepath.FromSlash(artifact.Path)))
			terminalCount := 0
			for _, record := range records {
				name := record.Event
				if name == "" {
					name = record.Type
				}
				if strings.Contains(strings.ToLower(name), "failure") || strings.Contains(strings.ToLower(name), "incomplete-final-response") {
					t.Fatalf("termination golden participant %q %s contains failure event: %+v", participantID, role, record)
				}
				if name == "room_bound_shutdown" || name == "participant_terminated" {
					terminalCount++
					assertLongConversationTerminalRecordFields(t, record, wantFields)
				}
			}
			if role == "diagnostics" && terminalCount != 1 {
				t.Fatalf("termination golden participant %q diagnostics terminal records = %d, want one", participantID, terminalCount)
			}
			if role == "events" && terminalCount != 1 {
				t.Fatalf("termination golden participant %q events terminal records = %d, want one", participantID, terminalCount)
			}
		}
	}
}

func longConversationTerminalFieldsForGolden() map[string]string {
	return map[string]string{
		"termination_trigger":     ParticipantTerminationTriggerMaxTurnsReached,
		"termination_disposition": ParticipantTerminationDispositionCompleted,
		"classification":          "",
		"terminal_reason":         "provider_authored_completion",
		"terminal_provenance":     "provider",
		"output_state":            "complete",
		"reason":                  string(ParticipantTerminationEnded),
	}
}

func assertLongConversationParticipantFields(t *testing.T, participant longConversationParticipant, want map[string]string) {
	t.Helper()
	got := map[string]string{
		"termination_trigger":     participant.TerminationTrigger,
		"termination_disposition": participant.TerminationDisposition,
		"classification":          participant.Classification,
		"terminal_reason":         participant.TerminalReason,
		"terminal_provenance":     participant.TerminalProvenance,
		"output_state":            participant.OutputState,
		"reason":                  string(participant.Reason),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("participant terminal fields = %v, want %v", got, want)
	}
}

func assertLongConversationTerminalFields(t *testing.T, raw json.RawMessage, want map[string]string) {
	t.Helper()
	var record longConversationEvidenceRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode terminal timeline event: %v", err)
	}
	assertLongConversationTerminalRecordFields(t, record, want)
}

func assertLongConversationTerminalRecordFields(t *testing.T, record longConversationEvidenceRecord, want map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(record.Fields, want) {
		t.Fatalf("terminal evidence fields = %v, want %v", record.Fields, want)
	}
}

func loadLongConversationTerminationManifest(t *testing.T, fixture string) longConversationTerminationManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture, RoomEvidenceManifestPath))
	if err != nil {
		t.Fatalf("read termination golden manifest: %v", err)
	}
	var manifest longConversationTerminationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode termination golden manifest: %v", err)
	}
	return manifest
}

func readLongConversationEvidenceRecords(t *testing.T, path string) []longConversationEvidenceRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read long-conversation evidence %q: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	result := make([]longConversationEvidenceRecord, 0, len(lines))
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record longConversationEvidenceRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode long-conversation evidence %q line %d: %v", path, index+1, err)
		}
		result = append(result, record)
	}
	return result
}

func longConversationTerminationFixturePath() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..", "testdata", "room-audio", "long-conversation-termination")
}
