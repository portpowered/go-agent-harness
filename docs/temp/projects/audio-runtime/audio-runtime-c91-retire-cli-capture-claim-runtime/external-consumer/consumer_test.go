package consumer_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim"
	captureclaimwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim/wire"
)

func TestExternalConsumerUsesOnlyPublicCaptureClaimContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "capture.json")
	service := captureclaimwire.NewService(captureclaim.Dependencies{})
	claim, err := service.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	peer, peerErr := service.Acquire(path)
	if peer != nil || !errors.Is(peerErr, captureclaim.ErrDestinationClaimed) {
		t.Fatalf("peer claim=%v err=%v, want claimed", peer, peerErr)
	}
	if strings.Contains(strings.ToLower(peerErr.Error()), "secret-canary") {
		t.Fatalf("peer error leaked holder data: %v", peerErr)
	}
	if err := claim.Publish(func(tempPath string) error {
		return os.WriteFile(tempPath, []byte("consumer bytes"), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, []byte("consumer bytes")) {
		t.Fatalf("published bytes=%q err=%v", got, err)
	}
	if err := claim.Release(); err != nil {
		t.Fatal(err)
	}
	if err := claim.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + captureclaim.Suffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sidecar after release=%v", err)
	}
	if _, err := service.Acquire(path); !errors.Is(err, captureclaim.ErrDestinationOccupied) {
		t.Fatalf("reacquire error=%v, want occupied", err)
	}
}
