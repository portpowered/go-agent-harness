package wire

import (
	"path/filepath"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/roomreplaybundle"
)

func TestNewServiceReturnsIndependentAdmissionServices(t *testing.T) {
	first := NewService()
	second := NewService()
	if first == nil || second == nil {
		t.Fatal("NewService returned nil")
	}
	if first == second {
		t.Fatal("NewService returned shared service state")
	}
	var _ roomreplaybundle.Service = first
}

func TestServiceValidateOutputRejectsSourceAndAllowsExternalDestination(t *testing.T) {
	source := t.TempDir()
	plan := roomreplaybundle.RoomReplayPlan{BundlePath: source}
	service := NewService()
	if err := service.ValidateOutput(plan, filepath.Join(source, "output")); err == nil {
		t.Fatal("source child was accepted as replay output")
	}
	if err := service.ValidateOutput(plan, filepath.Join(t.TempDir(), "output")); err != nil {
		t.Fatalf("external replay output rejected: %v", err)
	}
}
