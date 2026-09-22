package wire

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
)

func TestServiceAnalyzesRecordedBundle(t *testing.T) {
	destination, _ := finalizedReplayBundle(t)
	service := NewService()
	plan, err := service.LoadPlan(destination)
	if err != nil {
		t.Fatalf("load replay plan: %v", err)
	}
	bundle, err := service.Load(plan)
	if err != nil {
		t.Fatalf("load recorded bundle: %v", err)
	}
	analysis, err := service.Analyze(bundle)
	if err != nil {
		t.Fatalf("analyze recorded bundle: %v", err)
	}
	if len(analysis.Result.Streams) == 0 {
		t.Fatal("analysis omitted recorded streams")
	}
	for _, participant := range bundle.Participants {
		streamID := participant.WAV.StreamID
		found := false
		for _, stream := range analysis.Result.Streams {
			if stream.StreamID == streamID {
				found = true
				if stream.SampleCount != len(participant.WAV.Samples) {
					t.Fatalf("analysis sample count for %q = %d, want %d", streamID, stream.SampleCount, len(participant.WAV.Samples))
				}
				break
			}
		}
		if !found {
			t.Fatalf("analysis omitted recorded WAV stream %q", streamID)
		}
	}
}

func TestServiceRejectsDirectAndParentSymlinkedReplayArtifacts(t *testing.T) {
	t.Run("direct artifact", func(t *testing.T) {
		destination, recorder := finalizedReplayBundle(t)
		artifact := recorder.Artifacts("speaker").SentPCM
		outside := filepath.Join(t.TempDir(), "sent.pcm")
		service := NewService()
		plan, err := service.LoadPlan(destination)
		if err != nil {
			t.Fatalf("admit intact replay bundle: %v", err)
		}
		replaceWithSymlink(t, filepath.Join(destination, filepath.FromSlash(artifact)), outside)

		assertReplaySymlinkRejected(t, service, destination, outside)
		if _, err := service.Load(plan); err == nil {
			t.Fatal("symlinked replay artifact was loaded")
		} else {
			assertReplaySymlinkError(t, err, outside)
		}
	})

	t.Run("parent directory", func(t *testing.T) {
		destination, _ := finalizedReplayBundle(t)
		participantDirectory := filepath.Join(destination, "participants", "speaker")
		outside := filepath.Join(t.TempDir(), "speaker")
		service := NewService()
		plan, err := service.LoadPlan(destination)
		if err != nil {
			t.Fatalf("admit intact replay bundle: %v", err)
		}
		if err := os.Rename(participantDirectory, outside); err != nil {
			t.Fatalf("move participant directory outside bundle: %v", err)
		}
		if err := os.Symlink(outside, participantDirectory); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		assertReplaySymlinkRejected(t, service, destination, outside)
		if _, err := service.Load(plan); err == nil {
			t.Fatal("symlinked replay artifact was loaded")
		} else {
			assertReplaySymlinkError(t, err, outside)
		}
	})
}

func TestServiceRejectsDirectoryReplayArtifact(t *testing.T) {
	destination, recorder := finalizedReplayBundle(t)
	artifact := recorder.Artifacts("speaker").SentPCM
	path := filepath.Join(destination, filepath.FromSlash(artifact))
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove replay artifact: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("replace replay artifact with directory: %v", err)
	}
	_, err := NewService().LoadPlan(destination)
	if err == nil || !errors.Is(err, roomevidence.ErrInvalidRoomReplayBundle) {
		t.Fatalf("directory replay artifact error = %v, want invalid bundle", err)
	}
}

func TestServiceRejectsUnsupportedPCMEncoding(t *testing.T) {
	t.Run("manifest", func(t *testing.T) {
		destination, _ := finalizedReplayBundle(t)
		replaceReplayManifestEncoding(t, destination, "float32")
		_, err := NewService().LoadPlan(destination)
		if err == nil || !errors.Is(err, roomevidence.ErrInvalidRoomReplayBundle) {
			t.Fatalf("unsupported manifest encoding error = %v, want invalid replay bundle", err)
		}
	})
	t.Run("caller plan", func(t *testing.T) {
		destination, _ := finalizedReplayBundle(t)
		service := NewService()
		plan, err := service.LoadPlan(destination)
		if err != nil {
			t.Fatalf("admit intact replay bundle: %v", err)
		}
		plan.PCMFormat.Encoding = "float32"
		if _, err := service.Load(plan); err == nil || !errors.Is(err, roomevidence.ErrInvalidRoomReplayBundle) {
			t.Fatalf("unsupported caller plan encoding error = %v, want invalid replay bundle", err)
		}
	})
}

func replaceReplayManifestEncoding(t *testing.T, destination, encoding string) {
	t.Helper()
	path := filepath.Join(destination, roomevidence.ManifestPath)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replay manifest: %v", err)
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode replay manifest: %v", err)
	}
	var format map[string]json.RawMessage
	formatData, ok := manifest["pcm_format"]
	if !ok {
		formatData = manifest["audio_format"]
	}
	if err := json.Unmarshal(formatData, &format); err != nil {
		t.Fatalf("decode replay PCM format: %v", err)
	}
	encoded, err := json.Marshal(encoding)
	if err != nil {
		t.Fatalf("encode replay PCM format: %v", err)
	}
	format["encoding"] = encoded
	manifest["pcm_format"], err = json.Marshal(format)
	if err != nil {
		t.Fatalf("encode replay PCM format: %v", err)
	}
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode replay manifest: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write replay manifest: %v", err)
	}
}

func finalizedReplayBundle(t *testing.T) (string, roomevidence.Recorder) {
	t.Helper()
	recorder, destination, source := openRecorder(t)
	pcm := []byte{0x34, 0x12, 0x78, 0x56}
	for _, participantID := range []string{"speaker", "listener"} {
		if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationDiagnostic, ParticipantID: participantID, Diagnostic: roomevidence.DiagnosticRecord{Event: "replay_fixture"}}); err != nil {
			t.Fatalf("record replay diagnostic for %s: %v", participantID, err)
		}
		if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationDelta, ParticipantID: participantID, StreamMessage: messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue(pcm)}}); err != nil {
			t.Fatalf("record replay delta for %s: %v", participantID, err)
		}
		if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationParticipantAudio, ParticipantID: participantID, PCM: pcm}); err != nil {
			t.Fatalf("record replay WAV for %s: %v", participantID, err)
		}
		if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationSentStream, ParticipantID: participantID, PCM: pcm}); err != nil {
			t.Fatalf("record replay sent stream for %s: %v", participantID, err)
		}
		if err := recorder.Observe(roomevidence.Observation{Kind: roomevidence.ObservationReceivedParticipantAudio, ParticipantID: participantID, PCM: pcm}); err != nil {
			t.Fatalf("record replay received stream for %s: %v", participantID, err)
		}
	}
	if capture := recorder.Artifacts("speaker").Capture; capture != "" {
		if err := os.WriteFile(filepath.Join(destination, filepath.FromSlash(capture)), []byte(`{"captured":true}`), 0o600); err != nil {
			t.Fatalf("write replay capture: %v", err)
		}
	}
	source.AdvanceBy(time.Second)
	if _, err := finalizeForTest(recorder, testResult(), nil, source.Now()); err != nil {
		t.Fatalf("finalize replay bundle: %v", err)
	}
	return destination, recorder
}

func replaceWithSymlink(t *testing.T, path, outside string) {
	t.Helper()
	if err := os.Rename(path, outside); err != nil {
		t.Fatalf("move artifact outside bundle: %v", err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}

func assertReplaySymlinkRejected(t *testing.T, service roomevidence.Service, destination, outside string) {
	t.Helper()
	_, err := service.LoadPlan(destination)
	if err == nil {
		t.Fatal("symlinked replay bundle was admitted")
	}
	assertReplaySymlinkError(t, err, outside)
}

func assertReplaySymlinkError(t *testing.T, err error, outside string) {
	t.Helper()
	if !errors.Is(err, roomevidence.ErrInvalidRoomReplayBundle) {
		t.Fatalf("symlink error = %v, want invalid replay bundle", err)
	}
	var bundleErr *roomevidence.BundleError
	if !errors.As(err, &bundleErr) || bundleErr.Kind != roomevidence.BundleMismatch {
		t.Fatalf("symlink error = %v, want typed mismatch bundle error", err)
	}
	if !errors.Is(err, gateway.ErrReplayMismatch) || !errors.Is(err, providers.ErrReplayMismatch) {
		t.Fatalf("symlink error = %v, want gateway and provider replay classifications", err)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v, want stable symlink diagnostic", err)
	}
	if strings.Contains(err.Error(), outside) {
		t.Fatalf("symlink error leaked external path %q: %v", outside, err)
	}
}
