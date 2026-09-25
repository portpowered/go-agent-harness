package wire

import (
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplay"
)

func TestServiceValidateOutputRejectsSourceAndAllowsExternalDestination(t *testing.T) {
	source := t.TempDir()
	plan := roomreplay.RoomReplayPlan{BundlePath: source}
	service := NewService(nil)
	if err := service.ValidateOutput(plan, filepath.Join(source, "output")); err == nil {
		t.Fatal("source child was accepted as replay output")
	}
	if err := service.ValidateOutput(plan, filepath.Join(t.TempDir(), "output")); err != nil {
		t.Fatalf("external replay output rejected: %v", err)
	}
}
