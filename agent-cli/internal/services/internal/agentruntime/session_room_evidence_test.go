package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	roomevidencewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// These test aliases decode the service-owned recording contract. They keep
// the existing room behavior checks focused on produced artifacts while the
// CLI package no longer owns their schema.
type (
	roomEvidenceManifest            = roomevidence.RunManifest
	roomEvidenceParticipantManifest = roomevidence.ManifestParticipant
	roomEvidenceArtifactPaths       = roomevidence.ArtifactPaths
	roomTimelineEntry               = roomevidence.TimelineEntry
)

type roomEvidenceDiagnosticLine struct {
	Event  string            `json:"event"`
	Fields map[string]string `json:"fields"`
}

func withRoomTestEvidence(options RoomRunOptions) RoomRunOptions {
	options.evidenceService, options.latencyService = roomevidencewire.NewService(), roomevidencewire.NewLatencyService()
	return options
}

const (
	RoomEvidenceManifestPath  = roomevidence.ManifestPath
	RoomEvidenceTimelinePath  = roomevidence.TimelinePath
	roomEvidenceSchemaVersion = roomevidence.SchemaVersion
)

func TestRunRoom_WritesPerParticipantEvidenceAndManifest(t *testing.T) {
	ids := []string{"alpha", "beta", "gamma"}
	inferencers := make(map[string]*roomTestInferencer, len(ids))
	for index, id := range ids {
		inferencers[id] = &roomTestInferencer{events: []messages.StreamMessage{
			roomTestSessionOpen(id),
			roomTestAudioEvent(int16(1000+index), 10),
			roomTestMessageEnd(),
		}}
	}

	outputDir := filepath.Join(t.TempDir(), "room-run")
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.OutputDir = outputDir
	opts.Manifest.Room.MaxTurns = 1
	// The shared room test helper uses a deliberately tiny synthetic sample
	// rate for mixer assertions; evidence is verified with a conventional WAV
	// sample rate here.
	opts.MixerConfig = room.PCM16MixerConfig{}

	result, err := runRoomForTest(context.Background(), io.Discard, opts)
	if err != nil {
		t.Fatalf("RunRoomWithResult: %v", err)
	}
	if result.TerminationReason != RoomTerminationMaxTurnsReached {
		t.Fatalf("room termination reason = %q, want %q", result.TerminationReason, RoomTerminationMaxTurnsReached)
	}

	manifestData := readRoomEvidenceFile(t, filepath.Join(outputDir, RoomEvidenceManifestPath))
	var manifest roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode room run manifest: %v", err)
	}
	if manifest.SchemaVersion != roomEvidenceSchemaVersion {
		t.Fatalf("manifest schema version = %d, want %d", manifest.SchemaVersion, roomEvidenceSchemaVersion)
	}
	if manifest.TerminationReason != result.TerminationReason || manifest.Reason != result.Reason {
		t.Fatalf("manifest room reason = %q/%q, want %q/%q", manifest.TerminationReason, manifest.Reason, result.TerminationReason, result.Reason)
	}
	if manifest.Timing.StartedAt == "" || manifest.Timing.EndedAt == "" || !strings.HasSuffix(manifest.Timing.StartedAt, "Z") || !strings.HasSuffix(manifest.Timing.EndedAt, "Z") {
		t.Fatalf("manifest timing = %+v, want UTC start/end", manifest.Timing)
	}
	if manifest.RoomLatency != roomevidence.LatencyPath {
		t.Fatalf("room latency artifact = %q, want %q", manifest.RoomLatency, roomevidence.LatencyPath)
	}
	if _, err := roomevidencewire.NewLatencyService().ReadBundle(filepath.Join(outputDir, roomevidence.LatencyPath)); err != nil {
		t.Fatalf("read finalized room latency artifact: %v", err)
	}

	for _, id := range ids {
		participantResult, ok := result.Participants[id]
		if !ok {
			t.Fatalf("room result is missing participant %q", id)
		}
		participantManifest, ok := manifest.Participants[id]
		if !ok {
			t.Fatalf("run manifest is missing participant %q", id)
		}
		if participantManifest.CompletedTurns != participantResult.TurnsCompleted || participantManifest.TerminationReason != participantResult.TerminationReason || participantManifest.TerminationTrigger != participantResult.TerminationTrigger || participantManifest.TerminationDisposition != participantResult.TerminationDisposition || participantManifest.Classification != participantResult.Classification || participantManifest.TerminalReason != participantResult.TerminalReason || participantManifest.TerminalProvenance != participantResult.TerminalProvenance || participantManifest.OutputState != participantResult.OutputState {
			t.Fatalf("participant %q manifest facts = %+v, result = %+v", id, participantManifest, participantResult)
		}
		if manifest.TurnCounts[id] != participantResult.TurnsCompleted {
			t.Fatalf("participant %q turn count = %d, want %d", id, manifest.TurnCounts[id], participantResult.TurnsCompleted)
		}

		wantArtifacts := roomEvidenceArtifactPaths{
			WAV:         "agent-" + id + ".wav",
			Diagnostics: "agent-" + id + ".diagnostics.jsonl",
			Deltas:      "agent-" + id + ".deltas.jsonl",
			SentPCM:     filepath.Join("participants", id, "sent.pcm"),
			ReceivedPCM: filepath.Join("participants", id, "received.pcm"),
			Events:      filepath.Join("participants", id, "events.jsonl"),
			Capture:     filepath.Join("participants", id, "capture.json"),
		}
		if participantManifest.Artifacts != wantArtifacts {
			t.Fatalf("participant %q artifacts = %+v, want %+v", id, participantManifest.Artifacts, wantArtifacts)
		}
		for name, relativePath := range map[string]string{
			"WAV":          participantManifest.Artifacts.WAV,
			"diagnostics":  participantManifest.Artifacts.Diagnostics,
			"deltas":       participantManifest.Artifacts.Deltas,
			"sent_pcm":     participantManifest.Artifacts.SentPCM,
			"received_pcm": participantManifest.Artifacts.ReceivedPCM,
			"events":       participantManifest.Artifacts.Events,
			"capture":      participantManifest.Artifacts.Capture,
		} {
			if filepath.IsAbs(relativePath) || filepath.Clean(relativePath) != relativePath || strings.HasPrefix(relativePath, "..") {
				t.Fatalf("participant %q %s path is unsafe: %q", id, name, relativePath)
			}
		}

		wavData := readRoomEvidenceFile(t, filepath.Join(outputDir, participantManifest.Artifacts.WAV))
		_, samples, err := wavio.Read(bytes.NewReader(wavData))
		if err != nil {
			t.Fatalf("decode participant %q WAV: %v", id, err)
		}
		if len(samples) == 0 {
			t.Fatalf("participant %q WAV has no samples", id)
		}

		diagnostics := readRoomEvidenceJSONLLines(t, filepath.Join(outputDir, participantManifest.Artifacts.Diagnostics))
		diagnosticTurns := 0
		for _, line := range diagnostics {
			var record roomEvidenceDiagnosticLine
			if err := json.Unmarshal(line, &record); err != nil {
				t.Fatalf("decode participant %q diagnostic: %v", id, err)
			}
			if record.Event == SessionDiagnosticEventTurn {
				diagnosticTurns++
			}
		}
		if diagnosticTurns != participantResult.TurnsCompleted {
			t.Fatalf("participant %q diagnostic turns = %d, want %d", id, diagnosticTurns, participantResult.TurnsCompleted)
		}

		deltas := readRoomEvidenceJSONLLines(t, filepath.Join(outputDir, participantManifest.Artifacts.Deltas))
		deltaTurns := 0
		for _, line := range deltas {
			message, err := gwtesting.UnmarshalStreamMessage(line)
			if err != nil {
				t.Fatalf("decode participant %q delta: %v", id, err)
			}
			if message.Type == messages.StreamTypeMessageEnd {
				deltaTurns++
			}
		}
		if deltaTurns != participantResult.TurnsCompleted {
			t.Fatalf("participant %q delta turns = %d, want %d", id, deltaTurns, participantResult.TurnsCompleted)
		}

		// events.jsonl is the replay bundle's independently-declared
		// participant event stream (roomReplayArtifactRoleEvents): required
		// by replay admission as its own artifact, distinct from deltas.jsonl,
		// even though it carries the same content today.
		events := readRoomEvidenceJSONLLines(t, filepath.Join(outputDir, participantManifest.Artifacts.Events))
		if len(events) != len(deltas) {
			t.Fatalf("participant %q events.jsonl has %d lines, want %d (matching deltas.jsonl)", id, len(events), len(deltas))
		}
		for index, line := range events {
			if !bytes.Equal(line, deltas[index]) {
				t.Fatalf("participant %q events.jsonl line %d = %s, want %s (matching deltas.jsonl)", id, index, line, deltas[index])
			}
		}
	}
}

func TestRunRoom_PreservesFailedEvidenceAndRedactsSecrets(t *testing.T) {
	const secret = "sk-room-evidence-secret"
	ids := []string{"a", "b", "c"}
	inferencers := map[string]*roomTestInferencer{
		"a": {connectErr: fmt.Errorf("provider authorization: Bearer %s; retry authorization: Bearer %s", secret, secret)},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
		"c": {events: []messages.StreamMessage{roomTestSessionOpen("c")}},
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.OutputDir = filepath.Join(t.TempDir(), "failed-room")
	// Keep this regression on the room-fatal contract path. A participant
	// connection failure is intentionally local now, so use an invalid
	// advertised capability surface to exercise failed-room evidence and
	// credential redaction.
	opts.Manifest.Participants[0].Tools = []string{"requested_tool"}
	opts.ToolCapabilitiesFactory = func(room.Participant) (RoomParticipantToolCapabilities, error) {
		return RoomParticipantToolCapabilities{
			Executor:    roomScopedToolExecutor{participantID: "evidence"},
			Definitions: []messages.ToolDefinition{{Name: "unexpected_tool"}},
		}, nil
	}

	result, err := runRoomForTest(context.Background(), io.Discard, opts)
	if err == nil {
		t.Fatal("failed room returned nil error")
	}
	if result.TerminationReason != RoomTerminationFailed {
		t.Fatalf("room termination reason = %q, want %q", result.TerminationReason, RoomTerminationFailed)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(result.Error, secret) {
		t.Fatalf("room failure leaked API key: err=%q result=%q", err, result.Error)
	}

	manifestPath := filepath.Join(opts.OutputDir, RoomEvidenceManifestPath)
	manifestData := readRoomEvidenceFile(t, manifestPath)
	if bytes.Contains(manifestData, []byte(secret)) {
		t.Fatalf("failed run manifest contains credential material: %s", manifestData)
	}
	var manifest roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode failed room manifest: %v", err)
	}
	if manifest.Finalized || manifest.TerminationReason != RoomTerminationFailed || manifest.Error == "" {
		t.Fatalf("failed manifest outcome = %+v", manifest)
	}
	for _, participant := range manifest.Participants {
		for _, relativePath := range []string{participant.Artifacts.WAV, participant.Artifacts.Diagnostics, participant.Artifacts.Deltas} {
			data := readRoomEvidenceFile(t, filepath.Join(opts.OutputDir, relativePath))
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("participant artifact %q contains credential material: %s", relativePath, data)
			}
		}
	}
}

func readRoomEvidenceFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read room evidence %s: %v", path, err)
	}
	return data
}

func readRoomEvidenceJSONLLines(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	data := readRoomEvidenceFile(t, path)
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) == 0 || (len(lines) == 1 && len(lines[0]) == 0) {
		t.Fatalf("room evidence JSONL %s is empty", path)
	}
	result := make([]json.RawMessage, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !json.Valid(line) {
			t.Fatalf("room evidence JSONL %s contains invalid line %q", path, line)
		}
		result = append(result, append(json.RawMessage(nil), line...))
	}
	return result
}
