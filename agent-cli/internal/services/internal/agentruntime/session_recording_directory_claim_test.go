package agentruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestRunSessionWithRecordingDirectoryConcurrentClaimHasOneProviderConnection(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	connectErr := errors.New("provider connection stopped by test")
	inferencer := &blockingDirectoryClaimInferencer{
		connected: make(chan struct{}),
		release:   make(chan struct{}),
		err:       connectErr,
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- RunSessionWithRecordingDirectory(context.Background(), io.Discard, SessionRunOptions{
				Provider:          config.ProviderOpenAI,
				Model:             "gpt-realtime",
				APIKey:            "test-key",
				ConfigDir:         t.TempDir(),
				SessionInferencer: inferencer,
			}, destination)
		}()
	}
	close(start)

	select {
	case <-inferencer.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("neither contender reached the provider connection seam")
	}

	var loserErr error
	select {
	case loserErr = <-results:
	case <-time.After(2 * time.Second):
		t.Fatal("directory-claim loser did not fail before the winner was released")
	}
	if !errors.Is(loserErr, ErrSessionRecordingDirectoryClaimed) {
		t.Fatalf("loser error = %v, want directory claim conflict", loserErr)
	}
	if !containsPath(loserErr, destination) {
		t.Fatalf("loser error = %v, want cleaned destination path", loserErr)
	}
	for _, identity := range []string{"pid=", "host=", "started_at="} {
		if !strings.Contains(loserErr.Error(), identity) {
			t.Fatalf("loser error = %v, want holder identity field %q", loserErr, identity)
		}
	}

	close(inferencer.release)
	wait.Wait()
	winnerErr := <-results
	if !errors.Is(winnerErr, connectErr) {
		t.Fatalf("winner error = %v, want provider connection error", winnerErr)
	}
	if got := inferencer.connects.Load(); got != 1 {
		t.Fatalf("provider connections = %d, want exactly one", got)
	}
	if _, err := os.Stat(destination + sessionRecordingDirectoryClaimSuffix); !os.IsNotExist(err) {
		t.Fatalf("directory claim sidecar = %v, want cleaned after both runs", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("failed recording destination = %v, want absent", err)
	}
}

func TestSessionRecordingDirectoryClaimRejectsSymlinkAndNonDirectoryBeforeConnect(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(t.TempDir(), "recording")
		if err := os.Symlink(target, destination); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		inferencer := &countingSessionRecordingInferencer{}
		err := RunSessionWithRecordingDirectory(context.Background(), io.Discard, SessionRunOptions{
			Provider:          config.ProviderOpenAI,
			Model:             "gpt-realtime",
			APIKey:            "test-key",
			ConfigDir:         t.TempDir(),
			SessionInferencer: inferencer,
		}, destination)
		if !errors.Is(err, ErrSessionRecordingDirectorySymlink) {
			t.Fatalf("error = %v, want symlink classification", err)
		}
		if inferencer.connects != 0 {
			t.Fatalf("connects = %d, want zero", inferencer.connects)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("symlink target was changed: %v", err)
		}
	})

	t.Run("non-directory", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "recording")
		original := []byte("keep this destination")
		if err := os.WriteFile(destination, original, 0o600); err != nil {
			t.Fatal(err)
		}
		inferencer := &countingSessionRecordingInferencer{}
		err := RunSessionWithRecordingDirectory(context.Background(), io.Discard, SessionRunOptions{
			Provider:          config.ProviderOpenAI,
			Model:             "gpt-realtime",
			APIKey:            "test-key",
			ConfigDir:         t.TempDir(),
			SessionInferencer: inferencer,
		}, destination)
		if !errors.Is(err, ErrSessionRecordingDirectoryNotDirectory) {
			t.Fatalf("error = %v, want non-directory classification", err)
		}
		if inferencer.connects != 0 {
			t.Fatalf("connects = %d, want zero", inferencer.connects)
		}
		if got, readErr := os.ReadFile(destination); readErr != nil || string(got) != string(original) {
			t.Fatalf("destination = %q, err %v, want unchanged %q", got, readErr, original)
		}
	})
}

func TestSessionRecordingDirectoryClaimRetainsOwnershipThroughFinalization(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	claim, err := acquireSessionRecordingDirectoryClaim(destination)
	if err != nil {
		t.Fatalf("acquire directory claim: %v", err)
	}
	defer func() { _ = claim.release() }()

	recording := newSessionDirectoryRecording(destination, sessionRuntimePlan{provider: sessionProviderOpenAI}, SessionRunOptions{
		Model:                   "gpt-realtime",
		recordingDirectoryClaim: claim,
	})
	writeSyntheticRecordingTranscript(t, recording, "client\n", "agent\n")
	if err := recording.Finalize(); err != nil {
		t.Fatalf("finalize recording: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "manifest.json")); err != nil {
		t.Fatalf("manifest missing after finalization: %v", err)
	}
	if _, err := os.Stat(destination + sessionRecordingDirectoryClaimSuffix); err != nil {
		t.Fatalf("claim sidecar disappeared before release: %v", err)
	}
	if err := claim.release(); err != nil {
		t.Fatalf("release directory claim: %v", err)
	}
	if _, err := os.Stat(destination + sessionRecordingDirectoryClaimSuffix); !os.IsNotExist(err) {
		t.Fatalf("claim sidecar after release = %v, want absent", err)
	}
}

func TestSessionRecordingDirectoryClaimLostPreventsPublication(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "recording")
	claim, err := acquireSessionRecordingDirectoryClaim(destination)
	if err != nil {
		t.Fatalf("acquire directory claim: %v", err)
	}
	defer func() { _ = claim.release() }()
	recording := newSessionDirectoryRecording(destination, sessionRuntimePlan{provider: sessionProviderOpenAI}, SessionRunOptions{
		Model:                   "gpt-realtime",
		recordingDirectoryClaim: claim,
	})
	writeSyntheticRecordingTranscript(t, recording, "client\n", "agent\n")
	if err := os.Remove(claim.lockPath); err != nil {
		t.Fatalf("remove claim sidecar: %v", err)
	}
	err = recording.Finalize()
	if !errors.Is(err, ErrSessionRecordingDirectoryClaimLost) {
		t.Fatalf("finalize after claim loss = %v, want claim-loss error", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination after claim loss = %v, want absent", statErr)
	}
}

func containsPath(err error, path string) bool {
	return err != nil && strings.Contains(err.Error(), filepath.Clean(path))
}

type blockingDirectoryClaimInferencer struct {
	connected chan struct{}
	release   chan struct{}
	err       error
	connects  atomic.Int32
	once      sync.Once
}

func (i *blockingDirectoryClaimInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connects.Add(1)
	i.once.Do(func() { close(i.connected) })
	select {
	case <-i.release:
		return nil, i.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
