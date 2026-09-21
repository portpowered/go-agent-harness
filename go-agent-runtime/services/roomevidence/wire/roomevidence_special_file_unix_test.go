//go:build !windows

package wire

import (
	"context"
	"encoding/base64"
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

func TestServiceRejectsFIFOReplayFiles(t *testing.T) {
	if payload, ok := fifoReplayChildPayload(os.Args); ok {
		t.Run(payload, func(t *testing.T) {
			decoded, err := base64.RawURLEncoding.DecodeString(payload)
			if err != nil {
				t.Fatalf("decode FIFO child arguments: %v", err)
			}
			arguments := strings.Split(string(decoded), "\x00")
			if len(arguments) != 3 {
				t.Fatalf("FIFO child arguments = %d values, want mode, root and path", len(arguments))
			}
			runFIFOReplayChild(t, arguments[0], arguments[1], arguments[2])
		})
		return
	}
	for _, mode := range []string{"manifest", "artifact", "replay"} {
		t.Run(mode, func(t *testing.T) {
			assertFIFOReplayMode(t, mode)
		})
	}
}

func fifoReplayChildPayload(arguments []string) (string, bool) {
	for index, argument := range arguments {
		var pattern string
		switch {
		case argument == "-test.run" && index+1 < len(arguments):
			pattern = arguments[index+1]
		case strings.HasPrefix(argument, "-test.run="):
			pattern = strings.TrimPrefix(argument, "-test.run=")
		}
		const prefix = "^TestServiceRejectsFIFOReplayFiles/"
		if strings.HasPrefix(pattern, prefix) && strings.HasSuffix(pattern, "$") {
			payload := strings.TrimSuffix(strings.TrimPrefix(pattern, prefix), "$")
			return payload, payload != ""
		}
	}
	return "", false
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
	assertFIFOChildFailsClosed(t, mode, destination, fifoPath)
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
		// Keep the manifest valid so the child reaches the bounded replay reader.
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

func assertFIFOChildFailsClosed(t *testing.T, mode, destination, fifoPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	payload := base64.RawURLEncoding.EncodeToString([]byte(strings.Join([]string{mode, destination, fifoPath}, "\x00")))
	selector := "^TestServiceRejectsFIFOReplayFiles/" + payload + "$"
	command := exec.CommandContext(ctx, os.Args[0], "-test.run", selector)
	output, err := command.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("FIFO %s handling exceeded bounded deadline", mode)
	}
	if err != nil {
		t.Fatalf("FIFO %s handling failed: %v\n%s", mode, err, output)
	}
}

func runFIFOReplayChild(t *testing.T, mode, destination, fifoPath string) {
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
