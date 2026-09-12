package wire

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
)

func testManifest() rooms.Manifest {
	return rooms.Manifest{
		SchemaVersion: rooms.SchemaVersion,
		Room:          rooms.Room{MaxTurns: 2, MaxDuration: 2 * time.Second},
		Participants: []rooms.Participant{
			{ID: "speaker", Kind: rooms.ParticipantKindAgent, SystemPrompt: "speaker", Provider: "provider", Model: "model", APIKeyEnv: "ROOM_KEY", Tools: []string{}},
			{ID: "listener", Kind: rooms.ParticipantKindHuman, SystemPrompt: "listener", Tools: []string{}, InputDevice: "input", OutputDevice: "output"},
		},
	}
}

func testResult() rooms.RoomResult {
	return rooms.RoomResult{
		TerminationReason: rooms.RoomTerminationStopped,
		Participants: map[string]rooms.RoomParticipantResult{
			"speaker":  {ID: "speaker", ParticipantID: "speaker", TerminationReason: rooms.ParticipantTerminationEnded, TurnsCompleted: 1, Connected: true},
			"listener": {ID: "listener", ParticipantID: "listener", TerminationReason: rooms.ParticipantTerminationEnded, Connected: true},
		},
	}
}

func openRecorder(t *testing.T) (roomevidence.Recorder, string, *clock.Deterministic) {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "bundle")
	base := time.Date(2026, 9, 11, 20, 0, 0, 0, time.UTC)
	source := clock.NewDeterministic(base, time.Millisecond)
	recorder, err := NewService().Open(roomevidence.Options{
		Destination: destination,
		Manifest:    testManifest(),
		AudioFormat: rooms.AudioFormat{SampleRate: 24000, Channels: 1, FrameDuration: 20 * time.Millisecond},
		Secrets:     []string{"sk-service-secret"},
		StartedAt:   base,
		Clock:       source,
	})
	if err != nil {
		t.Fatalf("open recorder: %v", err)
	}
	return recorder, destination, source
}

type effectsManifest struct {
	Finalized   bool `json:"finalized"`
	AudioFormat struct {
		SampleRate int `json:"sample_rate"`
		Channels   int `json:"channels"`
	} `json:"audio_format"`
	ArtifactIntegrity map[string]struct {
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"artifact_integrity"`
	Participants map[string]struct {
		Artifacts roomevidence.ArtifactPaths `json:"artifacts"`
	} `json:"participants"`
}

func assertEffectsBundle(t *testing.T, destination string) {
	t.Helper()
	manifestData, err := os.ReadFile(filepath.Join(destination, roomevidence.ManifestPath))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if bytesContain(manifestData, "sk-service-secret") {
		t.Fatal("manifest leaked a secret")
	}
	var manifest effectsManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if !manifest.Finalized || manifest.AudioFormat.SampleRate != 24000 || manifest.AudioFormat.Channels != 1 {
		t.Fatalf("manifest projection = %+v", manifest)
	}
	for participantID, participant := range manifest.Participants {
		assertParticipantIntegrity(t, destination, participantID, participant.Artifacts, manifest.ArtifactIntegrity)
	}
	timeline, err := os.ReadFile(filepath.Join(destination, roomevidence.TimelinePath))
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	if !bytesContain(timeline, "audio_input_dropped") {
		t.Fatalf("timeline omitted dropped-audio effect: %s", timeline)
	}
	if !bytesContain(timeline, "received_speech_start") {
		t.Fatalf("timeline omitted received speech effect: %s", timeline)
	}
}

func assertParticipantIntegrity(t *testing.T, destination, participantID string, artifacts roomevidence.ArtifactPaths, integrity map[string]struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}) {
	t.Helper()
	for _, path := range []string{artifacts.WAV, artifacts.Diagnostics, artifacts.Deltas, artifacts.SentPCM, artifacts.ReceivedPCM, artifacts.Events} {
		if path == "" {
			t.Fatalf("participant %q has an empty artifact path", participantID)
		}
		data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		digest := sha256.Sum256(data)
		entry, ok := integrity[path]
		if !ok {
			t.Fatalf("integrity omits %s", path)
		}
		if entry.Size != int64(len(data)) || !strings.EqualFold(entry.SHA256, hex.EncodeToString(digest[:])) {
			t.Fatalf("integrity for %s = %+v", path, entry)
		}
	}
}

func observeConcurrent(t *testing.T, participant roomevidence.ParticipantRecorder, pcm []byte) {
	if err := participant.ObserveAudio(pcm); err != nil && !errors.Is(err, roomevidence.ErrFinalized) {
		t.Errorf("concurrent participant audio: %v", err)
	}
	if err := participant.ObserveSentStream(pcm); err != nil && !errors.Is(err, roomevidence.ErrFinalized) {
		t.Errorf("concurrent sent stream: %v", err)
	}
	if err := participant.ObserveDelta(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("bounded")}); err != nil && !errors.Is(err, roomevidence.ErrFinalized) {
		t.Errorf("concurrent delta: %v", err)
	}
}

func TestServiceRecordsEffectsAndIntegrity(t *testing.T) {
	recorder, destination, source := openRecorder(t)
	speaker := recorder.Participant("speaker")
	listener := recorder.Participant("listener")
	pcm := []byte{0x34, 0x12, 0x78, 0x56}
	if err := speaker.ObserveDelta(messages.StreamMessage{Type: messages.StreamTypeToolCallStart}); err != nil {
		t.Fatalf("observe tool delta: %v", err)
	}
	if err := speaker.ObserveSentAudio(pcm); err != nil {
		t.Fatalf("observe speaker audio: %v", err)
	}
	if err := listener.ObserveReceivedAudio(pcm); err != nil {
		t.Fatalf("observe listener audio: %v", err)
	}
	if err := listener.RecordAudioDropped("test drop", len(pcm)); err != nil {
		t.Fatalf("record dropped audio: %v", err)
	}
	if err := listener.RecordDiagnostic(roomevidence.DiagnosticRecord{Event: "tool_call_end", Fields: map[string]string{"message": "sk-service-secret"}}); err != nil {
		t.Fatalf("record tool diagnostic: %v", err)
	}
	source.AdvanceBy(250 * time.Millisecond)
	if path := recorder.CapturePath("speaker"); path == "" {
		t.Fatal("agent capture path is empty")
	} else if err := os.WriteFile(path, []byte(`{"captured":true}`), 0o600); err != nil {
		t.Fatalf("write provider capture: %v", err)
	}
	if err := recorder.Finalize(testResult(), nil, source.Now()); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if err := recorder.Finalize(rooms.RoomResult{TerminationReason: rooms.RoomTerminationFailed}, errors.New("replacement"), source.Now()); err != nil {
		t.Fatalf("idempotent finalize returned an error: %v", err)
	}

	assertEffectsBundle(t, destination)
}

func TestServiceMixSumsOverlapAndPadsToFinalSpan(t *testing.T) {
	recorder, destination, source := openRecorder(t)
	chunk := make([]byte, 10)
	for index := 0; index < 5; index++ {
		binary.LittleEndian.PutUint16(chunk[index*2:], uint16(int16(10000)))
	}
	if err := recorder.Participant("speaker").ObserveSentStream(chunk); err != nil {
		t.Fatalf("first sent stream: %v", err)
	}
	if err := recorder.Participant("speaker").ObserveSentStream(chunk); err != nil {
		t.Fatalf("overlapping sent stream: %v", err)
	}
	source.AdvanceBy(2 * time.Second)
	if err := recorder.Finalize(testResult(), nil, source.Now()); err != nil {
		t.Fatalf("finalize mix: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, roomevidence.MixPath))
	if err != nil {
		t.Fatalf("read mix: %v", err)
	}
	rate, samples, err := wavio.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode mix: %v", err)
	}
	if rate != 24000 || len(samples) != 48000 {
		t.Fatalf("mix format/span = %d/%d, want 24000/48000", rate, len(samples))
	}
	if samples[0] != 20000 {
		t.Fatalf("overlap was not summed: first sample=%d", samples[0])
	}
	for _, sample := range samples[5:] {
		if sample != 0 {
			t.Fatalf("mix padding is not silent: sample=%d", sample)
		}
	}
}

func TestServiceConcurrentObservationAndFinalize(t *testing.T) {
	recorder, _, source := openRecorder(t)
	participant := recorder.Participant("speaker")
	pcm := []byte{0x01, 0x00, 0x02, 0x00}
	var group sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := 0; index < 20; index++ {
				observeConcurrent(t, participant, pcm)
			}
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		if err := recorder.Finalize(testResult(), nil, source.Now()); err != nil {
			t.Errorf("concurrent finalize: %v", err)
		}
	}()
	group.Wait()
	if err := recorder.Close(); err != nil {
		t.Fatalf("idempotent close after concurrent finalize: %v", err)
	}
}

func TestServiceRejectsPostFinalizeAndRetainsFirstTypedError(t *testing.T) {
	recorder, _, source := openRecorder(t)
	first := errors.New("first sink failure")
	second := errors.New("second sink failure")
	recorder.MarkError("speaker", "agent-speaker.deltas.jsonl", first)
	recorder.MarkError("speaker", "agent-speaker.wav", second)
	finalErr := recorder.Finalize(testResult(), nil, source.Now())
	if !errors.Is(finalErr, first) {
		t.Fatalf("final error %v does not preserve first cause", finalErr)
	}
	health := recorder.Health()
	if health.Status == nil || health.Status.State != "partial" || !strings.Contains(health.Status.Reason, "first sink failure") || strings.Contains(health.Status.Reason, "second sink failure") {
		t.Fatalf("health = %+v", health)
	}
	if err := recorder.Participant("speaker").ObserveAudio([]byte{0, 0}); !errors.Is(err, roomevidence.ErrFinalized) {
		t.Fatalf("post-finalize write = %v, want ErrFinalized", err)
	}
	if err := recorder.Finalize(rooms.RoomResult{TerminationReason: rooms.RoomTerminationFailed}, second, source.Now()); !errors.Is(err, first) {
		t.Fatalf("second finalize = %v, want cached first error", err)
	}
}

func TestServiceOutputSafety(t *testing.T) {
	service := NewService()
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
	if err := service.ValidateOutput(filepath.Join(occupied, "new-bundle")); err != nil {
		t.Fatalf("new output validation: %v", err)
	}
}

func bytesContain(data []byte, value string) bool { return strings.Contains(string(data), value) }
