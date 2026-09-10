package main

import (
	"context"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sessionwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/wire"
)

func TestPublicWireInstructionConsumerUsesOnlyNormalizedValues(t *testing.T) {
	service := sessionwire.NewInstructionService()
	loader := &loader{files: map[string][]byte{"/workspace/AGENTS.md": []byte("agents")}, summary: "skills"}
	result, err := service.Resolve(context.Background(), session.InstructionRequest{WorkspaceDir: "/workspace", Loader: loader})
	if err != nil {
		t.Fatal(err)
	}
	if result.Instructions != "agents\n\n---\n\nskills" {
		t.Fatalf("resolved instructions = %q", result.Instructions)
	}

	got := service.Compose(session.InstructionComposition{
		Instructions:        result.Instructions,
		ToolDefinitions:     []messages.ToolDefinition{{Name: "custom_sight"}},
		BrowserToolsEnabled: true,
		PageSightToolID:     "custom_sight",
	})
	for _, marker := range []string{"Tool-grounding requirements:", "WebMCP tab selection calibration:", "Sight routing requirements:"} {
		if !strings.Contains(got, marker) {
			t.Fatalf("composition missing %q: %q", marker, got)
		}
	}
}
