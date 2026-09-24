package wire

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

func TestPublicWireServiceResolvesHostInstructions(t *testing.T) {
	service := NewService(Dependencies{})
	if service == nil {
		t.Fatal("NewService returned nil")
	}
	got, err := service.ResolveInstructions(context.Background(), sessionturn.InstructionRequest{Text: "preserve the user's terminology"})
	if err != nil || got != "preserve the user's terminology" {
		t.Fatalf("ResolveInstructions = %q, %v; want host-supplied instructions", got, err)
	}
}
