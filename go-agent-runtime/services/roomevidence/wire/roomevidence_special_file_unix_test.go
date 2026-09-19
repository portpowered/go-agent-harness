//go:build !windows

package wire

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomevidence"
)

const (
	roomEvidenceFIFOChildEnv = "ROOM_EVIDENCE_FIFO_CHILD"
	roomEvidenceFIFOModeEnv  = "ROOM_EVIDENCE_FIFO_MODE"
	roomEvidenceFIFOPathEnv  = "ROOM_EVIDENCE_FIFO_PATH"
	roomEvidenceFIFORootEnv  = "ROOM_EVIDENCE_FIFO_ROOT"
)

func TestServiceRejectsFIFOReplayFiles(t *testing.T) {
	if os.Getenv(roomEvidenceFIFOChildEnv) == "1" {
		runFIFOReplayChild(t)
		return
	}

	for _, mode := range []string{"manifest", "artifact", "replay"} {
		t.Run(mode, func(t *testing.T) {
			destination, recorder := finalizedReplayBundle(t)
			fifoPath := filepath.Join(destination, "replay-fifo")
			if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
				t.Skipf("FIFO unavailable: %v", err)
			}
			defer os.Remove(fifoPath)

			path := fifoPath
			switch mode {
			case "manifest":
				path = filepath.Join(destination, roomevidence.ManifestPath)
			case "artifact":
				path = filepath.Join(destination, filepath.FromSlash(recorder.Participant("speaker").Artifacts().SentPCM))
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove replay artifact: %v", err)
				}
				if err := os.Rename(fifoPath, path); err != nil {
					t.Fatalf("replace replay artifact with FIFO: %v", err)
				}
				fifoPath = path
			case "replay":
				// Keep the manifest valid so the child reaches the service's
				// bounded replay reader, then point an admitted artifact at the
				// FIFO inside the bundle.
			}
			if mode == "manifest" {
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove manifest: %v", err)
				}
				if err := os.Rename(fifoPath, path); err != nil {
					t.Fatalf("replace manifest with FIFO: %v", err)
				}
				fifoPath = path
			}
			assertFIFOChildFailsClosed(t, mode, destination, fifoPath)
		})
	}
}

func assertFIFOChildFailsClosed(t *testing.T, mode, destination, fifoPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestServiceRejectsFIFOReplayFiles$")
	command.Env = append(os.Environ(),
		roomEvidenceFIFOChildEnv+"=1",
		roomEvidenceFIFOModeEnv+"="+mode,
		roomEvidenceFIFOPathEnv+"="+fifoPath,
		roomEvidenceFIFORootEnv+"="+destination,
	)
	output, err := command.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("FIFO %s handling exceeded bounded deadline", mode)
	}
	if err != nil {
		t.Fatalf("FIFO %s handling failed: %v\n%s", mode, err, output)
	}
}

func runFIFOReplayChild(t *testing.T) {
	mode := os.Getenv(roomEvidenceFIFOModeEnv)
	destination := os.Getenv(roomEvidenceFIFORootEnv)
	fifoPath := os.Getenv(roomEvidenceFIFOPathEnv)
	service := NewService()
	var err error
	switch mode {
	case "manifest", "artifact":
		_, err = service.LoadPlan(destination)
	case "replay":
		plan, planErr := service.LoadPlan(destination)
		if planErr != nil {
			t.Fatalf("admit intact replay bundle: %v", planErr)
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
	default:
		t.Fatalf("unknown FIFO test mode %q", mode)
	}
	if err == nil || (!errors.Is(err, roomevidence.ErrInvalidRoomReplayBundle) && !errors.Is(err, roomevidence.ErrRoomReplayBundleIncomplete)) {
		t.Fatalf("FIFO %s error = %v, want a public replay failure", mode, err)
	}
	if err != nil && strings.Contains(err.Error(), fifoPath) {
		t.Fatalf("FIFO %s error leaked path %q: %v", mode, fifoPath, err)
	}
}
