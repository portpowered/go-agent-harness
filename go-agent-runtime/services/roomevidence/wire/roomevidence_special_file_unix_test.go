//go:build !windows

package wire

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
)

// TestServiceRejectsFIFOReplayFiles runs admission in-process against a FIFO.
// Every reader rejects non-regular files by Lstat before opening them, so each
// call must return promptly. A regression would block in open(2); the bounded
// wait reports it and then releases the reader by opening the write end.
func TestServiceRejectsFIFOReplayFiles(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"manifest", "artifact", "replay"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			assertFIFOReplayMode(t, mode)
		})
	}
}

func assertFIFOReplayMode(t *testing.T, mode string) {
	t.Helper()
	destination, recorder := finalizedReplayBundle(t)
	fifoPath := filepath.Join(destination, "replay-fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Remove(fifoPath); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove replay FIFO: %v", err)
		}
	})
	fifoPath = configureFIFOReplay(t, mode, destination, recorder, fifoPath)
	assertFIFOLoadFailsClosed(t, mode, destination, fifoPath)
}

func configureFIFOReplay(t *testing.T, mode, destination string, recorder roomevidence.Recorder, fifoPath string) string {
	t.Helper()
	switch mode {
	case "manifest":
		return replaceWithFIFO(t, fifoPath, filepath.Join(destination, roomevidence.ManifestPath))
	case "artifact":
		path := filepath.Join(destination, filepath.FromSlash(recorder.Artifacts("speaker").SentPCM))
		return replaceWithFIFO(t, fifoPath, path)
	case "replay":
		// Keep the manifest valid so the load reaches the bounded replay reader.
		return fifoPath
	default:
		t.Fatalf("unknown FIFO mode %q", mode)
		return ""
	}
}

func replaceWithFIFO(t *testing.T, fifoPath, path string) string {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove replay file: %v", err)
	}
	if err := os.Rename(fifoPath, path); err != nil {
		t.Fatalf("replace replay file with FIFO: %v", err)
	}
	return path
}

func assertFIFOLoadFailsClosed(t *testing.T, mode, destination, fifoPath string) {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- loadFIFOReplay(mode, destination, fifoPath) }()
	var err error
	select {
	case err = <-result:
	case <-time.After(2 * time.Second):
		// Unblock a reader stuck opening the FIFO before failing the test.
		if writer, openErr := os.OpenFile(fifoPath, os.O_WRONLY|syscall.O_NONBLOCK, 0); openErr == nil {
			t.Logf("release FIFO writer: %v", writer.Close())
		}
		t.Fatalf("FIFO %s handling exceeded bounded deadline", mode)
	}
	var setup fifoSetupError
	if errors.As(err, &setup) {
		t.Fatalf("admit intact replay bundle: %v", setup.err)
	}
	if err == nil || (!errors.Is(err, roomevidence.ErrInvalidRoomReplayBundle) && !errors.Is(err, roomevidence.ErrRoomReplayBundleIncomplete)) {
		t.Fatalf("FIFO %s error = %v, want a public replay failure", mode, err)
	}
	if strings.Contains(err.Error(), fifoPath) {
		t.Fatalf("FIFO %s error leaked path %q: %v", mode, fifoPath, err)
	}
}

// fifoSetupError marks a failure to admit the intact bundle before the FIFO
// is reached.
type fifoSetupError struct{ err error }

func (e fifoSetupError) Error() string { return e.err.Error() }

func loadFIFOReplay(mode, destination, fifoPath string) error {
	service := newTestService()
	plan, err := service.LoadPlan(destination)
	if mode != "replay" {
		return err
	}
	if err != nil {
		return fifoSetupError{err: err}
	}
	for participantIndex := range plan.Participants {
		for artifactIndex := range plan.Participants[participantIndex].Artifacts {
			artifact := &plan.Participants[participantIndex].Artifacts[artifactIndex]
			if artifact.Role == "sent_pcm" {
				artifact.AbsolutePath = fifoPath
			}
		}
	}
	_, err = service.Load(plan)
	return err
}
