package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/room"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
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

	result, err := RunRoomWithResult(context.Background(), io.Discard, opts)
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

	for _, id := range ids {
		participantResult, ok := result.Participants[id]
		if !ok {
			t.Fatalf("room result is missing participant %q", id)
		}
		participantManifest, ok := manifest.Participants[id]
		if !ok {
			t.Fatalf("run manifest is missing participant %q", id)
		}
		if participantManifest.CompletedTurns != participantResult.TurnsCompleted || participantManifest.TerminationReason != participantResult.TerminationReason {
			t.Fatalf("participant %q manifest facts = %+v, result = %+v", id, participantManifest, participantResult)
		}
		if manifest.TurnCounts[id] != participantResult.TurnsCompleted {
			t.Fatalf("participant %q turn count = %d, want %d", id, manifest.TurnCounts[id], participantResult.TurnsCompleted)
		}

		wantArtifacts := roomEvidenceArtifactPaths{
			WAV:         "agent-" + id + ".wav",
			Diagnostics: "agent-" + id + ".diagnostics.jsonl",
			Deltas:      "agent-" + id + ".deltas.jsonl",
		}
		if participantManifest.Artifacts != wantArtifacts {
			t.Fatalf("participant %q artifacts = %+v, want %+v", id, participantManifest.Artifacts, wantArtifacts)
		}
		for name, relativePath := range map[string]string{
			"WAV":         participantManifest.Artifacts.WAV,
			"diagnostics": participantManifest.Artifacts.Diagnostics,
			"deltas":      participantManifest.Artifacts.Deltas,
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
			var record selfPlayDiagnosticLine
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
	}
}

func TestRunRoom_PreservesFailedEvidenceAndRedactsSecrets(t *testing.T) {
	const secret = "sk-room-evidence-secret"
	ids := []string{"a", "b", "c"}
	inferencers := map[string]*roomTestInferencer{
		"a": {connectErr: fmt.Errorf("provider authorization: Bearer %s", secret)},
		"b": {events: []messages.StreamMessage{roomTestSessionOpen("b")}},
		"c": {events: []messages.StreamMessage{roomTestSessionOpen("c")}},
	}
	opts, _ := newRoomTestRunOptions(ids, inferencers)
	opts.OutputDir = filepath.Join(t.TempDir(), "failed-room")

	result, err := RunRoomWithResult(context.Background(), io.Discard, opts)
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
	if manifest.TerminationReason != RoomTerminationFailed || manifest.Error == "" {
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

func TestRoomEvidence_RedactsJSONStringsWithoutCorruptingDeltas(t *testing.T) {
	const secret = "sk-json-redaction-secret"
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{{
			ID:           "participant",
			SystemPrompt: "authorization: Bearer " + secret,
			Provider:     "provider",
			Model:        "model",
			APIKeyEnv:    "ROOM_KEY",
			Tools:        []string{},
		}},
	}
	evidence, err := newRoomEvidence(t.TempDir(), manifest, room.DefaultPCM16Format(), []string{secret}, time.Now())
	if err != nil {
		t.Fatalf("newRoomEvidence: %v", err)
	}
	delta := messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewErrorValue("authorization: Bearer " + secret),
	}
	if err := evidence.participant("participant").observeDelta(delta); err != nil {
		t.Fatalf("observeDelta: %v", err)
	}
	if err := evidence.finalize(RoomResult{
		TerminationReason: RoomTerminationFailed,
		Participants: map[string]RoomParticipantResult{
			"participant": {ID: "participant", TerminationReason: ParticipantTerminationError},
		},
	}, errors.New("authorization: Bearer "+secret), time.Now()); err != nil {
		t.Fatalf("finalize evidence: %v", err)
	}

	deltaLines := readRoomEvidenceJSONLLines(t, filepath.Join(evidence.destination, evidence.participant("participant").artifacts.Deltas))
	decodedDelta, err := gwtesting.UnmarshalStreamMessage(deltaLines[0])
	if err != nil {
		t.Fatalf("decode redacted delta: %v", err)
	}
	errorValue, ok := decodedDelta.Value.(*messages.ErrorValue)
	if !ok || strings.Contains(errorValue.Message, secret) || !strings.Contains(errorValue.Message, "[REDACTED]") {
		t.Fatalf("redacted error value = %+v", decodedDelta.Value)
	}
	manifestData := readRoomEvidenceFile(t, filepath.Join(evidence.destination, RoomEvidenceManifestPath))
	if !json.Valid(manifestData) || bytes.Contains(manifestData, []byte(secret)) {
		t.Fatalf("redacted manifest is invalid or contains secret: %s", manifestData)
	}
}

func TestRoomEvidence_FinalizeIsIdempotent(t *testing.T) {
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{{
			ID:           "participant",
			SystemPrompt: "test",
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
	result := RoomResult{TerminationReason: RoomTerminationStopped, Participants: map[string]RoomParticipantResult{
		"participant": {ID: "participant", TerminationReason: ParticipantTerminationEnded},
	}}
	firstErr := evidence.finalize(result, nil, time.Now())
	secondErr := evidence.finalize(RoomResult{TerminationReason: RoomTerminationFailed}, errors.New("must not replace first finalization"), time.Now())
	if firstErr != nil || secondErr != firstErr {
		t.Fatalf("finalize errors = %v/%v, want the same nil result", firstErr, secondErr)
	}
	manifestData := readRoomEvidenceFile(t, filepath.Join(evidence.destination, RoomEvidenceManifestPath))
	var written roomEvidenceManifest
	if err := json.Unmarshal(manifestData, &written); err != nil {
		t.Fatalf("decode finalized manifest: %v", err)
	}
	if written.TerminationReason != RoomTerminationStopped {
		t.Fatalf("second finalize replaced manifest reason with %q", written.TerminationReason)
	}
}

func TestRoomEvidenceManifest_RecordsSanitizedParticipantBrowserTools(t *testing.T) {
	browser := room.DefaultBrowserToolsConfig()
	browser.Connection.CDPURL = "http://127.0.0.1:9222/json/version?query-secret=hide#fragment-secret"
	browser.Connection.WSEndpoint = "ws://127.0.0.1:9222/devtools/browser/browser-secret?query-secret=hide"
	manifest := room.Manifest{
		SchemaVersion: room.SchemaVersion,
		Room:          room.Room{MaxTurns: 1},
		Participants: []room.Participant{
			{ID: "browser", SystemPrompt: "browser", Provider: "openai", Model: "model", APIKeyEnv: "ROOM_KEY", Tools: []string{}, BrowserTools: &browser},
			{ID: "other", SystemPrompt: "other", Provider: "openai", Model: "model", APIKeyEnv: "ROOM_KEY", Tools: []string{}},
		},
	}
	evidence, err := newRoomEvidence(t.TempDir(), manifest, room.DefaultPCM16Format(), nil, time.Now())
	if err != nil {
		t.Fatalf("newRoomEvidence: %v", err)
	}
	if err := evidence.finalize(RoomResult{
		TerminationReason: RoomTerminationStopped,
		Participants: map[string]RoomParticipantResult{
			"browser": {ID: "browser", TerminationReason: ParticipantTerminationEnded},
			"other":   {ID: "other", TerminationReason: ParticipantTerminationEnded},
		},
	}, nil, time.Now()); err != nil {
		t.Fatalf("finalize evidence: %v", err)
	}
	data := readRoomEvidenceFile(t, filepath.Join(evidence.destination, RoomEvidenceManifestPath))
	serialized := string(data)
	if !strings.Contains(serialized, `"browser_tools"`) {
		t.Fatalf("room evidence omitted browser configuration: %s", serialized)
	}
	for _, forbidden := range []string{"query-secret", "fragment-secret", "browser-secret", "token="} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("room evidence leaked browser endpoint material %q: %s", forbidden, serialized)
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
