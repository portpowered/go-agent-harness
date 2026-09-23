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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
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
	recorder, err := NewService().Open(roomevidence.RecordingRequest{
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

func observeConcurrent(t *testing.T, recorder roomevidence.Recorder, participantID string, pcm []byte) {
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationParticipantAudio, ParticipantID: participantID, PCM: pcm}); err != nil && !errors.Is(err, roomevidence.ErrFinalized) {
		t.Errorf("concurrent participant audio: %v", err)
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationSentStream, ParticipantID: participantID, PCM: pcm}); err != nil && !errors.Is(err, roomevidence.ErrFinalized) {
		t.Errorf("concurrent sent stream: %v", err)
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationDelta, ParticipantID: participantID, StreamMessage: messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("bounded")}}); err != nil && !errors.Is(err, roomevidence.ErrFinalized) {
		t.Errorf("concurrent delta: %v", err)
	}
}

func TestServiceRecordsEffectsAndIntegrity(t *testing.T) {
	recorder, destination, source := openRecorder(t)
	pcm := []byte{0x34, 0x12, 0x78, 0x56}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationDelta, ParticipantID: "speaker", StreamMessage: messages.StreamMessage{Type: messages.StreamTypeToolCallStart}}); err != nil {
		t.Fatalf("observe tool delta: %v", err)
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationSentAudio, ParticipantID: "speaker", PCM: pcm}); err != nil {
		t.Fatalf("observe speaker audio: %v", err)
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationReceivedParticipantAudio, ParticipantID: "listener", PCM: pcm}); err != nil {
		t.Fatalf("observe listener audio: %v", err)
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationAudioDropped, ParticipantID: "listener", Artifact: "test drop", DroppedBytes: len(pcm)}); err != nil {
		t.Fatalf("record dropped audio: %v", err)
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationDiagnostic, ParticipantID: "listener", Diagnostic: roomevidence.DiagnosticRecord{Event: "tool_call_end", Fields: map[string]string{"message": "sk-service-secret"}}}); err != nil {
		t.Fatalf("record tool diagnostic: %v", err)
	}
	const timelineSecret = "sk-service-secret"
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationTimeline, At: source.Now(), Event: "timeline-" + timelineSecret, ParticipantID: timelineSecret, Fields: map[string]string{"message": timelineSecret}}); err != nil {
		t.Fatalf("record arbitrary timeline: %v", err)
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationLiveEvent, ParticipantID: "speaker", LiveEvent: session.LiveEvent{
		Kind: string(session.LiveEventText), Text: timelineSecret, ResponseID: timelineSecret,
		Error: errors.New(timelineSecret), Timestamp: source.Now(),
	}}); err != nil {
		t.Fatalf("record live event: %v", err)
	}
	source.AdvanceBy(250 * time.Millisecond)
	if artifact := recorder.Artifacts("speaker").Capture; artifact == "" {
		t.Fatal("agent capture path is empty")
	} else if err := os.WriteFile(filepath.Join(destination, filepath.FromSlash(artifact)), []byte(`{"captured":true}`), 0o600); err != nil {
		t.Fatalf("write provider capture: %v", err)
	}
	if _, err := finalizeForTest(recorder, testResult(), nil, source.Now()); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if _, err := finalizeForTest(recorder, rooms.RoomResult{TerminationReason: rooms.RoomTerminationFailed}, errors.New("replacement"), source.Now()); err != nil {
		t.Fatalf("idempotent finalize returned an error: %v", err)
	}

	assertEffectsBundle(t, destination)
	for _, path := range []string{
		filepath.Join(destination, roomevidence.TimelinePath),
		filepath.Join(destination, "agent-speaker.diagnostics.jsonl"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read redaction evidence %s: %v", path, err)
		}
		if bytesContain(data, timelineSecret) {
			t.Fatalf("evidence %s leaked live timeline secret", path)
		}
		if !bytesContain(data, "[REDACTED]") {
			t.Fatalf("evidence %s omitted redaction marker", path)
		}
	}
}

func TestServiceMixSumsOverlapAndPadsToFinalSpan(t *testing.T) {
	recorder, destination, source := openRecorder(t)
	chunk := make([]byte, 10)
	for index := 0; index < 5; index++ {
		binary.LittleEndian.PutUint16(chunk[index*2:], uint16(int16(10000)))
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationSentStream, ParticipantID: "speaker", PCM: chunk}); err != nil {
		t.Fatalf("first sent stream: %v", err)
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationSentStream, ParticipantID: "speaker", PCM: chunk}); err != nil {
		t.Fatalf("overlapping sent stream: %v", err)
	}
	source.AdvanceBy(2 * time.Second)
	if _, err := finalizeForTest(recorder, testResult(), nil, source.Now()); err != nil {
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
	pcm := []byte{0x01, 0x00, 0x02, 0x00}
	var group sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := 0; index < 20; index++ {
				observeConcurrent(t, recorder, "speaker", pcm)
			}
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		if _, err := finalizeForTest(recorder, testResult(), nil, source.Now()); err != nil {
			t.Errorf("concurrent finalize: %v", err)
		}
	}()
	group.Wait()
	if err := recorder.Close(); err != nil {
		t.Fatalf("idempotent close after concurrent finalize: %v", err)
	}
}

func TestServiceConcurrentCloseIsBoundedAndIdempotent(t *testing.T) {
	recorder, destination, _ := openRecorder(t)
	const callers = 16
	start := make(chan struct{})
	ready := make(chan struct{}, callers)
	results := make(chan error, callers)
	for range callers {
		go func() {
			ready <- struct{}{}
			<-start
			results <- recorder.Close()
		}()
	}
	for range callers {
		<-ready
	}
	close(start)

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for range callers {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("concurrent close: %v", err)
			}
		case <-deadline.C:
			t.Fatal("concurrent Close calls did not finish within 5s")
		}
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("idempotent close after concurrent closes: %v", err)
	}

	manifestData, err := os.ReadFile(filepath.Join(destination, roomevidence.ManifestPath))
	if err != nil {
		t.Fatalf("read manifest finalized by Close: %v", err)
	}
	var manifest effectsManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode manifest finalized by Close: %v", err)
	}
	if !manifest.Finalized {
		t.Fatal("concurrent Close returned before the room evidence manifest was finalized")
	}
}

func TestServiceRejectsPostFinalizeAndRetainsFirstTypedError(t *testing.T) {
	recorder, _, source := openRecorder(t)
	first := errors.New("first sink failure")
	second := errors.New("second sink failure")
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationError, ParticipantID: "speaker", Artifact: "agent-speaker.deltas.jsonl", Err: first}); !errors.Is(err, first) {
		t.Fatalf("first sink failure = %v, want %v", err, first)
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationError, ParticipantID: "speaker", Artifact: "agent-speaker.wav", Err: second}); !errors.Is(err, first) {
		t.Fatalf("second sink failure = %v, want retained first failure %v", err, first)
	}
	_, finalErr := finalizeForTest(recorder, testResult(), nil, source.Now())
	if !errors.Is(finalErr, first) {
		t.Fatalf("final error %v does not preserve first cause", finalErr)
	}
	health := recorder.Health()
	if health.Status == nil || health.Status.State != "partial" || !strings.Contains(health.Status.Reason, "first sink failure") || strings.Contains(health.Status.Reason, "second sink failure") {
		t.Fatalf("health = %+v", health)
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationParticipantAudio, ParticipantID: "speaker", PCM: []byte{0, 0}}); !errors.Is(err, roomevidence.ErrFinalized) {
		t.Fatalf("post-finalize write = %v, want ErrFinalized", err)
	}
	if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationLiveEvent, ParticipantID: "speaker", LiveEvent: session.LiveEvent{Kind: string(session.LiveEventText), Text: "late"}}); !errors.Is(err, roomevidence.ErrFinalized) {
		t.Fatalf("post-finalize live event = %v, want ErrFinalized", err)
	}
	if _, err := finalizeForTest(recorder, rooms.RoomResult{TerminationReason: rooms.RoomTerminationFailed}, second, source.Now()); !errors.Is(err, first) {
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
	unwritable := filepath.Join(t.TempDir(), "unwritable")
	if err := os.Mkdir(unwritable, 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(unwritable, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(unwritable, 0o700); err != nil {
				t.Errorf("restore writable output permissions: %v", err)
			}
		})
		if err := service.ValidateOutput(unwritable); !errors.Is(err, roomevidence.ErrInvalidOutput) {
			t.Fatalf("unwritable output error = %v, want invalid output", err)
		}
	}
	redirected := filepath.Join(t.TempDir(), "redirected")
	outside := t.TempDir()
	if err := os.Symlink(outside, redirected); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := service.ValidateOutput(filepath.Join(redirected, "bundle")); !errors.Is(err, roomevidence.ErrInvalidOutput) || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked output parent error = %v, want invalid symlink output", err)
	}
}

func finalizeForTest(recorder roomevidence.Recorder, result rooms.RoomResult, runErr error, endedAt time.Time) (roomevidence.Result, error) {
	return recorder.Finalize(roomevidence.Finalization{Room: result, Err: runErr, EndedAt: endedAt})
}

func bytesContain(data []byte, value string) bool { return strings.Contains(string(data), value) }
