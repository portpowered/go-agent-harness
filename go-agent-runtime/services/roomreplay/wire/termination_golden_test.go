package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
)

type longConversationTerminationManifest struct {
	Finalized         bool                        `json:"finalized"`
	TerminationReason rooms.RoomTerminationReason `json:"termination_reason"`
	Reason            rooms.RoomTerminationReason `json:"reason"`
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
	TerminationReason      rooms.ParticipantTerminationReason     `json:"termination_reason"`
	Reason                 rooms.ParticipantTerminationReason     `json:"reason"`
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

// The fixture records the bound-shutdown vocabulary written by room evidence.
const (
	fixtureDispositionCompleted   = "completed"
	fixtureTriggerMaxTurnsReached = "max_turns_reached"
)

const (
	longConversationTerminationParticipantA        = "agent-a"
	longConversationTerminationParticipantB        = "agent-b"
	longConversationTerminationTurnsPerParticipant = 4
)

func TestLongConversationTerminationGoldenReplaysCleanly(t *testing.T) {
	fixture := longConversationTerminationFixturePath()
	bundle, err := roomReplayAudioTestService().LoadAudioBundle(fixture)
	if err != nil {
		t.Fatalf("load post-fix long-conversation termination bundle: %v", err)
	}
	// Loading the same committed capture a second time is the replay control:
	// admission rechecks every artifact digest and provider capture before any
	// runtime is built, so terminal evidence cannot depend on a mutable read.
	replayed, err := roomReplayAudioTestService().LoadAudioBundle(fixture)
	if err != nil {
		t.Fatalf("replay post-fix long-conversation termination bundle: %v", err)
	}
	if !reflect.DeepEqual(bundle.Plan.Timeline, replayed.Plan.Timeline) {
		t.Fatal("room timeline changed between fixture admission and replay")
	}

	manifest := loadLongConversationTerminationManifest(t, fixture)
	if !manifest.Finalized || manifest.TerminationReason != rooms.RoomTerminationMaxTurnsReached || manifest.Reason != rooms.RoomTerminationMaxTurnsReached || manifest.Error != "" {
		t.Fatalf("termination golden manifest = %+v, want finalized clean max-turn outcome", manifest)
	}
	if manifest.Bounds.MaxTurns != longConversationTerminationTurnsPerParticipant || manifest.RoomTimeline != roomevidence.TimelinePath {
		t.Fatalf("termination golden bounds/timeline = %+v/%q, want max_turns=%d and %q", manifest.Bounds, manifest.RoomTimeline, longConversationTerminationTurnsPerParticipant, roomevidence.TimelinePath)
	}
	assertLongConversationTimeline(t, bundle.Plan.Timeline)
	for _, participantID := range []string{longConversationTerminationParticipantA, longConversationTerminationParticipantB} {
		assertLongConversationParticipant(t, fixture, bundle, manifest, participantID)
	}
}

// longConversationTimeline tallies the terminal shape of the room timeline.
type longConversationTimeline struct {
	turnOrder  []string
	bound      map[string]int
	terminated map[string]int
}

func assertLongConversationTimeline(t *testing.T, timeline []roomreplay.RoomReplayTimelineEvent) {
	t.Helper()
	tally := longConversationTimeline{bound: map[string]int{}, terminated: map[string]int{}}
	for index, timelineEvent := range timeline {
		tallyLongConversationEvent(t, index, timelineEvent, &tally)
	}
	a, b := longConversationTerminationParticipantA, longConversationTerminationParticipantB
	wantOrder := []string{a, b, a, b, a, b, a, b}
	if !reflect.DeepEqual(tally.turnOrder, wantOrder) {
		t.Fatalf("termination golden turn order = %v, want %v", tally.turnOrder, wantOrder)
	}
	for _, participantID := range []string{a, b} {
		if tally.bound[participantID] != 1 || tally.terminated[participantID] != 1 {
			t.Fatalf("timeline terminal events for %q = bound:%d terminated:%d, want one of each", participantID, tally.bound[participantID], tally.terminated[participantID])
		}
	}
}

func tallyLongConversationEvent(t *testing.T, index int, timelineEvent roomreplay.RoomReplayTimelineEvent, tally *longConversationTimeline) {
	t.Helper()
	raw := strings.ToLower(string(timelineEvent.Raw))
	if strings.Contains(raw, "session_failure") || strings.Contains(raw, "incomplete-final-response") {
		t.Fatalf("timeline event %d contains teardown failure text: %s", index+1, timelineEvent.Raw)
	}
	switch timelineEvent.Type {
	case "turn_completed":
		tally.turnOrder = append(tally.turnOrder, timelineEvent.ParticipantID)
	case "room_bound_shutdown":
		tally.bound[timelineEvent.ParticipantID]++
		assertLongConversationTerminalFields(t, timelineEvent.Raw, longConversationTerminalFieldsForGolden())
	case "participant_terminated":
		tally.terminated[timelineEvent.ParticipantID]++
		assertLongConversationTerminalFields(t, timelineEvent.Raw, longConversationTerminalFieldsForGolden())
	case "provider_error":
		t.Fatalf("termination golden timeline contains provider error: %s", timelineEvent.Raw)
	case "run_terminated":
		var record longConversationEvidenceRecord
		if err := json.Unmarshal(timelineEvent.Raw, &record); err != nil {
			t.Fatalf("decode run_terminated timeline event: %v", err)
		}
		if record.Fields["reason"] != string(rooms.RoomTerminationMaxTurnsReached) {
			t.Fatalf("run_terminated fields = %v, want max_turns_reached", record.Fields)
		}
	}
}

func assertLongConversationParticipant(t *testing.T, fixture string, bundle roomreplay.RoomReplayAudioBundle, manifest longConversationTerminationManifest, participantID string) {
	t.Helper()
	participant, ok := manifest.Participants[participantID]
	if !ok {
		t.Fatalf("termination golden manifest missing participant %q", participantID)
	}
	if participant.ID != participantID || !participant.Connected || participant.CompletedTurns != longConversationTerminationTurnsPerParticipant || participant.TerminationReason != rooms.ParticipantTerminationEnded || participant.Reason != rooms.ParticipantTerminationEnded || participant.Error != "" {
		t.Fatalf("termination golden participant %q = %+v, want connected clean participant", participantID, participant)
	}
	assertLongConversationParticipantFields(t, participant, longConversationTerminalFieldsForGolden())
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
		if terminalCount := countLongConversationTerminalRecords(t, participantID, role, records); terminalCount != 1 {
			t.Fatalf("termination golden participant %q %s terminal records = %d, want one", participantID, role, terminalCount)
		}
	}
}

func countLongConversationTerminalRecords(t *testing.T, participantID, role string, records []longConversationEvidenceRecord) int {
	t.Helper()
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
			assertLongConversationTerminalRecordFields(t, record, longConversationTerminalFieldsForGolden())
		}
	}
	return terminalCount
}

func longConversationTerminalFieldsForGolden() map[string]string {
	return map[string]string{
		"termination_trigger":     fixtureTriggerMaxTurnsReached,
		"termination_disposition": fixtureDispositionCompleted,
		"classification":          "",
		"terminal_reason":         "provider_authored_completion",
		"terminal_provenance":     "provider",
		"output_state":            "complete",
		"reason":                  string(rooms.ParticipantTerminationEnded),
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
	data, err := os.ReadFile(filepath.Join(fixture, roomevidence.ManifestPath))
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
	return filepath.Join(filepath.Dir(filename), "testdata", "room-audio", "long-conversation-termination")
}
