package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	roomevidencewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence/wire"
)

const consumerSecret = "consumer-room-secret"

func consumerManifest() roomevidence.Manifest {
	return roomevidence.Manifest{
		SchemaVersion: roomevidence.SchemaVersion,
		Room:          roomevidence.Room{MaxTurns: 1, MaxDuration: 2 * time.Second},
		Participants: []roomevidence.Participant{
			{ID: "speaker", Kind: roomevidence.ParticipantKindAgent, SystemPrompt: "speaker", Provider: "fixture", Model: "room", Tools: []string{}},
			{ID: "listener", Kind: roomevidence.ParticipantKindHuman, SystemPrompt: "listener", Tools: []string{}},
		},
	}
}

func consumerResult() roomevidence.RoomResult {
	return roomevidence.RoomResult{
		TerminationReason: roomevidence.RoomTerminationStopped,
		Participants: map[string]roomevidence.RoomParticipantResult{
			"speaker":  {ID: "speaker", ParticipantID: "speaker", TerminationReason: roomevidence.ParticipantTerminationEnded, TurnsCompleted: 1, Connected: true},
			"listener": {ID: "listener", ParticipantID: "listener", TerminationReason: roomevidence.ParticipantTerminationEnded, Connected: true},
		},
	}
}

func openConsumerRecorder(t *testing.T) (roomevidence.Recorder, string) {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "bundle")
	recorder, err := roomevidencewire.NewService().Open(roomevidence.Options{
		Destination: destination,
		Manifest:    consumerManifest(),
		AudioFormat: roomevidence.AudioFormat{SampleRate: 24000, Channels: 1, FrameDuration: 20 * time.Millisecond},
		Secrets:     []string{consumerSecret},
		StartedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("open service: %v", err)
	}
	return recorder, destination
}

func TestExternalConsumerUsesOnlyPublicRoomEvidencePorts(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "bundle")
	service := roomevidencewire.NewService()
	if err := service.ValidateOutput(destination); err != nil {
		t.Fatalf("validate output: %v", err)
	}
	recorder, err := service.Open(roomevidence.Options{
		Destination: destination,
		Manifest:    consumerManifest(),
		AudioFormat: roomevidence.AudioFormat{SampleRate: 24000, Channels: 1, FrameDuration: 20 * time.Millisecond},
		Secrets:     []string{consumerSecret},
		StartedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("open service: %v", err)
	}
	pcm := []byte{0x10, 0x00, 0x20, 0x00}
	if err := recorder.Participant("speaker").RecordDiagnostic(roomevidence.DiagnosticRecord{Event: "tool_call_end", Fields: map[string]string{"detail": consumerSecret}}); err != nil {
		t.Fatalf("record tool diagnostic: %v", err)
	}
	if err := recorder.Participant("speaker").ObserveSentAudio(pcm); err != nil {
		t.Fatalf("record sent audio: %v", err)
	}
	if err := recorder.Participant("listener").ObserveReceivedAudio(pcm); err != nil {
		t.Fatalf("record received audio: %v", err)
	}
	if err := recorder.Participant("listener").RecordAudioDropped("consumer negative control", len(pcm)); err != nil {
		t.Fatalf("record dropped audio: %v", err)
	}
	if capture := recorder.CapturePath("speaker"); capture == "" {
		t.Fatal("speaker capture path is empty")
	} else if err := os.WriteFile(capture, []byte(`{"provider":"fixture"}`), 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}
	if err := recorder.Finalize(consumerResult(), nil, time.Now().UTC()); err != nil {
		t.Fatalf("finalize service: %v", err)
	}

	manifestData, err := os.ReadFile(filepath.Join(destination, roomevidence.ManifestPath))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(manifestData), consumerSecret) {
		t.Fatalf("manifest leaked secret: %s", manifestData)
	}
	var written struct {
		Finalized         bool `json:"finalized"`
		ArtifactIntegrity map[string]struct {
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"artifact_integrity"`
		Participants map[string]struct {
			Artifacts roomevidence.ArtifactPaths `json:"artifacts"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(manifestData, &written); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if !written.Finalized || len(written.Participants) != 2 {
		t.Fatalf("manifest projection = %+v", written)
	}
	for id, participant := range written.Participants {
		paths := participant.Artifacts
		for _, path := range []string{paths.WAV, paths.Diagnostics, paths.Deltas, paths.SentPCM, paths.ReceivedPCM, paths.Events} {
			if path == "" || filepath.IsAbs(path) || strings.HasPrefix(filepath.Clean(path), "..") {
				t.Fatalf("participant %q has unsafe artifact path %q", id, path)
			}
			data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(path)))
			if err != nil {
				t.Fatalf("read participant %q artifact %s: %v", id, path, err)
			}
			digest := sha256.Sum256(data)
			entry, ok := written.ArtifactIntegrity[path]
			if !ok || entry.Size != int64(len(data)) || entry.SHA256 != hex.EncodeToString(digest[:]) {
				t.Fatalf("integrity for %s = %+v", path, entry)
			}
		}
	}
	timeline, err := os.ReadFile(filepath.Join(destination, roomevidence.TimelinePath))
	if err != nil || !strings.Contains(string(timeline), "audio_input_dropped") {
		t.Fatalf("timeline missing dropped-audio effect: %v %s", err, timeline)
	}
	if err := recorder.Participant("speaker").ObserveAudio(pcm); !errors.Is(err, roomevidence.ErrFinalized) {
		t.Fatalf("post-finalize write = %v, want ErrFinalized", err)
	}
}

func TestExternalConsumerOutputSafety(t *testing.T) {
	service := roomevidencewire.NewService()
	if err := service.ValidateOutput(""); !errors.Is(err, roomevidence.ErrInvalidOutput) {
		t.Fatalf("empty output error = %v", err)
	}
	occupied := t.TempDir()
	if err := os.WriteFile(filepath.Join(occupied, "existing"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateOutput(occupied); !errors.Is(err, roomevidence.ErrOutputNotEmpty) {
		t.Fatalf("occupied output error = %v", err)
	}
}

func TestExternalConsumerRejectsSymlinkTarget(t *testing.T) {
	service := roomevidencewire.NewService()
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := service.ValidateOutput(link); !errors.Is(err, roomevidence.ErrInvalidOutput) {
		t.Fatalf("symlink output error = %v", err)
	}
}

func TestExternalConsumerDetectsSameLengthArtifactMutation(t *testing.T) {
	recorder, destination := openConsumerRecorder(t)
	if err := recorder.Participant("speaker").RecordDiagnostic(roomevidence.DiagnosticRecord{Event: "mutation-fixture", Fields: map[string]string{"value": "original"}}); err != nil {
		t.Fatalf("record fixture: %v", err)
	}
	if err := recorder.Finalize(consumerResult(), nil, time.Now().UTC()); err != nil {
		t.Fatalf("finalize fixture: %v", err)
	}
	manifestData, err := os.ReadFile(filepath.Join(destination, roomevidence.ManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		ArtifactIntegrity map[string]struct {
			SHA256 string `json:"sha256"`
		} `json:"artifact_integrity"`
		Participants map[string]struct {
			Artifacts roomevidence.ArtifactPaths `json:"artifacts"`
		} `json:"participants"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	path := manifest.Participants["speaker"].Artifacts.Diagnostics
	artifactPath := filepath.Join(destination, filepath.FromSlash(path))
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("mutation fixture is empty")
	}
	data[0] ^= 1
	if err := os.WriteFile(artifactPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if manifest.ArtifactIntegrity[path].SHA256 == hex.EncodeToString(digest[:]) {
		t.Fatal("same-length artifact mutation retained the declared digest")
	}
}

func TestExternalConsumerRedactsAndBoundsDiagnosticRecords(t *testing.T) {
	recorder, destination := openConsumerRecorder(t)
	if err := recorder.Participant("speaker").RecordDiagnostic(roomevidence.DiagnosticRecord{Event: "redaction-fixture", Fields: map[string]string{"secret": consumerSecret}}); err != nil {
		t.Fatalf("record redaction fixture: %v", err)
	}
	if err := recorder.Participant("speaker").RecordDiagnostic(roomevidence.DiagnosticRecord{Event: "oversized", Fields: map[string]string{"payload": strings.Repeat("x", 1<<20)}}); err == nil {
		t.Fatal("oversized diagnostic unexpectedly accepted")
	}
	_ = recorder.Finalize(consumerResult(), nil, time.Now().UTC())
	var leaked string
	_ = filepath.WalkDir(destination, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), consumerSecret) {
			leaked = path
		}
		return nil
	})
	if leaked != "" {
		t.Fatalf("secret leaked into %s", leaked)
	}
}
