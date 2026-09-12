package consumer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestPublicRecordingWireConsumer(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "public-recording")
	source := clock.NewDeterministic(time.Date(2032, 3, 4, 5, 6, 7, 0, time.UTC), time.Nanosecond)
	service := recordingwire.NewSessionService(source)
	events := make(chan recording.BrowserEvent, 1)
	recorder, err := service.OpenSession(recording.SessionOptions{
		Destination: destination, SessionID: "consumer-session", Provider: "fixture", Model: "fixture-model", Transport: "embedded",
		Credentials: []string{"consumer-secret"}, Browser: recording.BrowserOptions{Enabled: true, Events: events},
	})
	if err != nil {
		t.Fatalf("open public recording: %v", err)
	}
	ctx := context.Background()
	input := messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleUser, Value: messages.NewAudioDeltaValue([]byte{1, 0})}
	output := messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{2, 0})}
	for _, message := range []messages.StreamMessage{input, {Type: messages.StreamTypeMessageEnd, Role: messages.RoleUser}} {
		if err := recorder.ObserveMessage(ctx, message, recording.SessionMessageFromClient); err != nil {
			t.Fatalf("observe client message: %v", err)
		}
	}
	for _, message := range []messages.StreamMessage{{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("public")}, output, {Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant}} {
		if err := recorder.ObserveMessage(ctx, message, recording.SessionMessageFromAgent); err != nil {
			t.Fatalf("observe agent message: %v", err)
		}
	}
	call := messages.ToolCall{ID: "tool-1", Name: "fixture", Arguments: `{"value":1}`}
	if err := recorder.ObserveToolCall(ctx, call); err != nil {
		t.Fatalf("observe public tool call: %v", err)
	}
	if err := recorder.ObserveToolResult(ctx, call, messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "ok"}, false); err != nil {
		t.Fatalf("observe public tool result: %v", err)
	}
	events <- recording.BrowserEvent{Type: "target_attached", BrowserID: "b", TargetID: "t"}
	close(events)
	if err := recorder.RecordTerminalSummary(transcript.RecordingTerminalSummary{
		Reason: "complete", Classification: "success", TerminalReason: messages.TerminalReasonSessionClose,
		TerminalProvenance: messages.TerminalProvenanceSession, OutputState: messages.TerminalOutputComplete,
	}); err != nil {
		t.Fatalf("record public terminal: %v", err)
	}
	if err := recorder.Finalize(ctx, nil); err != nil {
		t.Fatalf("finalize public recording: %v", err)
	}
	manifest := readManifest(t, destination)
	if manifest.Transport != "embedded" || manifest.Model != "fixture-model" || manifest.Terminal == nil {
		t.Fatalf("public manifest = %+v", manifest)
	}
	expectedArtifacts := []string{"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/in-000.pcm", "audio/out-000.pcm", transcript.BrowserArtifactDefaultPath}
	for _, path := range expectedArtifacts {
		if !containsArtifact(manifest, path) {
			t.Fatalf("public manifest missing %q: %+v", path, manifest.Artifacts)
		}
	}
	gotArtifacts := make([]string, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		gotArtifacts = append(gotArtifacts, artifact.Path)
	}
	if !equalStrings(gotArtifacts, expectedArtifacts) {
		t.Fatalf("public manifest artifact order = %v, want %v", gotArtifacts, expectedArtifacts)
	}
	assertDigest(t, destination, manifest)
	assertBytes(t, filepath.Join(destination, "audio/in-000.pcm"), []byte{1, 0})
	assertBytes(t, filepath.Join(destination, "audio/out-000.pcm"), []byte{2, 0})

	claimedDestination := filepath.Join(t.TempDir(), "claim")
	first, err := service.OpenSession(recording.SessionOptions{Destination: claimedDestination})
	if err != nil {
		t.Fatalf("open claim fixture: %v", err)
	}
	if _, err := service.OpenSession(recording.SessionOptions{Destination: claimedDestination}); !errors.Is(err, recording.ErrLiveEvidenceClaimed) {
		t.Fatalf("claim error = %v, want typed claim error", err)
	}
	if err := first.RecordTerminalSummary(transcript.RecordingTerminalSummary{
		Reason: "claim", Classification: "success", TerminalReason: messages.TerminalReasonSessionClose,
		TerminalProvenance: messages.TerminalProvenanceSession, OutputState: messages.TerminalOutputNone,
	}); err != nil {
		t.Fatalf("record claim terminal: %v", err)
	}
	if err := first.Finalize(ctx, nil); err != nil {
		t.Fatalf("finalize claim fixture: %v", err)
	}
}

func readManifest(t *testing.T, destination string) transcript.RecordingManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return manifest
}

func containsArtifact(manifest transcript.RecordingManifest, path string) bool {
	for _, artifact := range manifest.Artifacts {
		if artifact.Path == path {
			return true
		}
	}
	return false
}

func assertDigest(t *testing.T, destination string, manifest transcript.RecordingManifest) {
	t.Helper()
	for _, artifact := range manifest.Artifacts {
		data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read artifact %s: %v", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			t.Fatalf("digest %s = %s, want %s", artifact.Path, got, artifact.SHA256)
		}
	}
}

func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s = %x, want %x", path, got, want)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
