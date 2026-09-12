package consumer

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	tracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func TestExternalConsumerCapturesTwoIndependentTracesAndRejectsMutationCases(t *testing.T) {
	root := t.TempDir()
	first, firstWAV := capture(t, tracewire.NewService(), filepath.Join(root, "first"), []int16{1, 2, 3, 4})
	second, secondWAV := capture(t, tracewire.NewService(), filepath.Join(root, "second"), []int16{1, 2, 3, 5})
	if first == second || string(firstWAV) == string(secondWAV) {
		t.Fatal("independent traces or same-length PCM mutation collapsed")
	}
	for _, bundle := range []string{first, second} {
		for _, name := range []string{"microphone-pre-gate.wav", "microphone-uploaded.wav", "speaker-enqueued.wav", "speaker-rendered.wav", "timeline.jsonl"} {
			if _, err := os.Stat(filepath.Join(bundle, "audio-trace", name)); err != nil {
				t.Fatalf("missing %s in %s: %v", name, bundle, err)
			}
		}
	}

	if _, err := tracewire.NewService().Prepare(sessiontrace.Request{TraceAudio: true}); !errors.Is(err, sessiontrace.ErrClockRequired) {
		t.Fatalf("missing clock error = %v", err)
	}

	prepared, err := tracewire.NewService().Prepare(sessiontrace.Request{TraceAudio: true, RecordDirectory: filepath.Join(root, "duplicate-request"), Clock: clock.NewDeterministic(time.Unix(0, 0).UTC(), time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	duplicateBundle := filepath.Join(root, "duplicate")
	if err := os.MkdirAll(filepath.Join(duplicateBundle, "audio-trace"), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(duplicateBundle, "audio-trace", "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finish(context.Background(), duplicateBundle, true); !errors.Is(err, sessiontrace.ErrDestinationExists) {
		t.Fatalf("duplicate finish error = %v", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("duplicate destination changed: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(prepared.StagedPath(), "timeline.jsonl")); err != nil {
		t.Fatalf("duplicate evidence was not retained: %v", err)
	}
}

func capture(t *testing.T, service sessiontrace.Service, root string, samples []int16) (string, []byte) {
	t.Helper()
	prepared, err := service.Prepare(sessiontrace.Request{TraceAudio: true, RecordDirectory: filepath.Join(root, "requested"), Clock: clock.NewDeterministic(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	binding := prepared.DeviceBinding()
	binding.PreGateSamplesObserver(16000, samples)
	binding.UploadedSamplesObserver(24000, samples)
	if err := binding.PlaybackSamplesObserver(context.Background(), 16000, samples); err != nil {
		t.Fatal(err)
	}
	binding.RenderedSamplesObserver(16000, samples)
	prepared.RuntimeObserver().ObserveSessionRuntime(sessiontrace.SessionRuntimeObservation{Kind: sessiontrace.SessionRuntimeObservationAudioOutput, Tick: 1, Payload: []byte("rendered")})
	bundle := filepath.Join(root, "bundle")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finish(context.Background(), bundle, true); err != nil {
		t.Fatal(err)
	}
	wav, err := os.ReadFile(filepath.Join(bundle, "audio-trace", "microphone-pre-gate.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if len(wav) == 0 || sha256.Sum256(wav) == [32]byte{} {
		t.Fatal("empty microphone evidence")
	}
	return bundle, wav
}
