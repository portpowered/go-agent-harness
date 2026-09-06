package embedding_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	recordingwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording/wire"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
)

type failingFinalTraceStore struct{ cause error }

func (failingFinalTraceStore) LoadTrace(context.Context, string) (*session.TraceRecord, error) {
	return nil, nil
}

type finalizingLiveProvider struct {
	*embeddedLiveProvider
	flush func() error
}

func (provider *finalizingLiveProvider) FlushCapture() error { return provider.flush() }

func TestExternalLiveWaitIncludesCaptureFinalizationFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	cause := errors.New("provider capture write failed")
	provider := &finalizingLiveProvider{embeddedLiveProvider: newEmbeddedLiveProvider()}
	provider.flush = func() error {
		close(started)
		select {
		case <-release:
			return cause
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer closeForTest(t, provider)
	host := sessionwire.NewLiveService(sessionwire.LiveDependencies{
		InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) { return provider, nil },
	})
	handle, err := host.OpenLive(ctx, session.LiveRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeForTest(t, handle)
	if err := handle.Start(ctx); err != nil {
		t.Fatal(err)
	}
	handle.Cancel(context.Canceled)
	finished := make(chan error, 1)
	go func() { finished <- handle.Wait() }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("capture finalization did not start")
	}
	select {
	case err := <-finished:
		t.Fatalf("Wait returned before capture finalized: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-finished:
		if !errors.Is(err, cause) {
			t.Fatalf("Wait error=%v, want capture failure %v", err, cause)
		}
	case <-ctx.Done():
		t.Fatal("Wait did not join capture finalization")
	}
}

func (failingFinalTraceStore) NewTraceID(context.Context) (string, error) {
	return "external-trace", nil
}

func (store failingFinalTraceStore) SaveTrace(ctx context.Context, trace session.TraceRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if trace.Status != session.TraceStatusRunning {
		return store.cause
	}
	return nil
}

func TestExternalHostReportsTerminalTracePersistenceFailure(t *testing.T) {
	cause := errors.New("terminal trace write failed")
	host := sessionwire.NewService(sessionwire.Dependencies{
		Inferencer: &hostInferencer{response: "finished"}, RelaxValidation: true,
		TraceStore: failingFinalTraceStore{cause: cause},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := host.RunIterative(ctx, session.Request{Input: agentloop.ExecuteInput{Message: "hello"}}, session.IterativeRequest{MaxIterations: 1})
	if !errors.Is(err, cause) {
		t.Fatalf("terminal error=%v; want trace persistence failure %v", err, cause)
	}
}

func TestExternalHostRecordsExactPortPCMThroughPublicWire(t *testing.T) {
	base := time.Unix(1_750_000_000, 123)
	source := clock.NewDeterministic(base, time.Millisecond)
	destination := filepath.Join(t.TempDir(), "recording")
	service := recordingwire.NewService(source)
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructor opened evidence: %v", err)
	}
	recorder, err := service.OpenLiveEvidence(recording.LiveEvidenceOptions{Destination: destination})
	if err != nil {
		t.Fatal(err)
	}
	frame := audio.PCMFrame{Samples: []int16{13, -14, 15}, Format: audio.PCM16DeviceFormat(24000), EndOfResponse: true}
	if err := recorder.RecordAudio(t.Context(), session.LiveAudioRecord{Direction: session.LiveRecordAgent, Timestamp: source.Now(), Frame: frame}); err != nil {
		t.Fatal(err)
	}
	terminal := messages.NewSessionCloseValueWithTerminal("embedded", "complete", "complete", messages.TerminalReasonProviderAuthoredCompletion, messages.TerminalProvenanceProvider, messages.TerminalOutputComplete)
	if err := recorder.RecordEvent(t.Context(), session.LiveEvent{Timestamp: source.Now(), Kind: string(session.LiveEventTerminal), Terminal: terminal}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Finalize(t.Context(), nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent provider evidence must be reported: %v", err)
	}
	pcm, err := os.ReadFile(filepath.Join(destination, "audio", "out-000.pcm"))
	if err != nil || !bytes.Equal(pcm, codec.EncodePCM16(frame.Samples)) {
		t.Fatalf("embedded PCM = %v, %v", pcm, err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.RecordingStatus == nil || manifest.RecordingStatus.State != transcript.RecordingStatusPartial {
		t.Fatalf("missing raw provider evidence reported complete: %+v", manifest)
	}
	if manifest.Transport != "runtime" || manifest.ClockBase != base.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("incorrect observation boundary/clock: %+v", manifest)
	}
}

func TestExternalHostStreamsAdditionalArtifactAndDetectsSourceMutation(t *testing.T) {
	t.Run("unchanged", func(t *testing.T) { verifyStreamedArtifact(t, false) })
	t.Run("changed during copy", func(t *testing.T) { verifyStreamedArtifact(t, true) })
}

func verifyStreamedArtifact(t *testing.T, mutate bool) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "capture-source.json")
	original := []byte(`{"capture":"original"}`)
	if err := os.WriteFile(source, original, 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "bundle")
	config := transcript.RecordingConfig{
		Destination: destination, ClientTranscript: []byte(`{"observed":"client"}`), AgentTranscript: []byte(`{"observed":"agent"}`),
		AdditionalArtifacts: []transcript.RecordingArtifact{{Path: "provider.json", SourcePath: source}},
	}
	if mutate {
		config.WriteStream = mutatingArtifactWriter(source)
	}
	err := transcript.WriteRecordingBundle(config)
	if mutate {
		if !errors.Is(err, transcript.ErrRecordingWrite) {
			t.Fatalf("source mutation = %v", err)
		}
		if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("mutated archive published: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(destination, "provider.json"))
	if err != nil || !bytes.Equal(copied, original) {
		t.Fatalf("artifact changed: %s, %v", copied, err)
	}
}

func mutatingArtifactWriter(source string) func(string, io.Reader, os.FileMode) (int64, error) {
	return func(path string, reader io.Reader, mode os.FileMode) (int64, error) {
		if err := os.WriteFile(source, []byte(`{"capture":"changed"}`), mode); err != nil {
			return 0, err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, mode)
		if err != nil {
			return 0, err
		}
		count, copyErr := io.Copy(file, reader)
		return count, errors.Join(copyErr, file.Close())
	}
}

func TestExternalHostRedactsStreamedAdditionalArtifact(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	const secret = "fixture-credential"
	if err := os.WriteFile(source, []byte(`{"credential":"`+secret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "bundle")
	err := transcript.WriteRecordingBundle(transcript.RecordingConfig{
		Destination: destination, Credentials: []string{secret},
		ClientTranscript: []byte(`{"observed":"client"}`), AgentTranscript: []byte(`{"observed":"agent"}`),
		AdditionalArtifacts: []transcript.RecordingArtifact{{Path: "extra.json", SourcePath: source}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "extra.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), transcript.RecordingRedactionMarker) {
		t.Fatal("streamed artifact redaction failed")
	}
}
