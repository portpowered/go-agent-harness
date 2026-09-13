package wire

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/captureclaim"
)

func TestNewServiceConstructsIndependentPublicServices(t *testing.T) {
	first := NewService(captureclaim.Dependencies{})
	second := NewService(captureclaim.Dependencies{})
	firstPath := filepath.Join(t.TempDir(), "first.json")
	secondPath := filepath.Join(t.TempDir(), "second.json")

	firstClaim, err := first.Acquire(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondClaim, err := second.Acquire(secondPath)
	if err != nil {
		releaseClaimForTest(t, firstClaim)
		t.Fatal(err)
	}
	if err := firstClaim.Publish(func(path string) error { return os.WriteFile(path, []byte("first"), 0o600) }); err != nil {
		releaseClaimForTest(t, firstClaim)
		releaseClaimForTest(t, secondClaim)
		t.Fatal(err)
	}
	if err := secondClaim.Publish(func(path string) error { return os.WriteFile(path, []byte("second"), 0o600) }); err != nil {
		releaseClaimForTest(t, firstClaim)
		releaseClaimForTest(t, secondClaim)
		t.Fatal(err)
	}
	if err := firstClaim.Release(); err != nil {
		releaseClaimForTest(t, secondClaim)
		t.Fatal(err)
	}
	if err := secondClaim.Release(); err != nil {
		t.Fatal(err)
	}

	if _, err := first.Acquire(firstPath); !errors.Is(err, captureclaim.ErrDestinationOccupied) {
		t.Fatalf("first service reacquire error = %v, want occupied", err)
	}
	if _, err := second.Acquire(secondPath); !errors.Is(err, captureclaim.ErrDestinationOccupied) {
		t.Fatalf("second service reacquire error = %v, want occupied", err)
	}
}

func releaseClaimForTest(t *testing.T, claim captureclaim.Claim) {
	t.Helper()
	if err := claim.Release(); err != nil {
		t.Errorf("release claim: %v", err)
	}
}
