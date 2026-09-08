package embedding_test

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	toolservice "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	toolswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

func TestEmptyToolCapabilityDoesNotDiscoverHostWorkspace(t *testing.T) {
	host := toolswire.NewService()
	capability, err := host.Resolve(context.Background(), toolservice.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if capability.Executor != nil || len(capability.Definitions) != 0 {
		t.Fatal("empty host request acquired implicit tools")
	}
	if capability.WorkspaceDir != "" || len(capability.AdditionalDirs) != 0 {
		t.Fatalf("empty host request acquired filesystem scope: %q %q", capability.WorkspaceDir, capability.AdditionalDirs)
	}
}

func TestToolDefinitionsRequireAnExecutionRoute(t *testing.T) {
	host := toolswire.NewService()
	_, err := host.Resolve(context.Background(), toolservice.Request{
		WorkDir:     t.TempDir(),
		Definitions: []messages.ToolDefinition{{Name: "host_tool", Description: "host-owned capability"}},
	})
	if err == nil {
		t.Fatal("tool definitions were admitted without an executor or default tool surface")
	}
}
