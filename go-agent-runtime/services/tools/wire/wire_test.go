package wire

import (
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func TestInteractiveToolPolicyClassifiesAdvertisedSurface(t *testing.T) {
	policy, err := NewInteractiveToolPolicy().Resolve(tools.InteractiveToolPolicyRequest{
		Definitions:     []messages.ToolDefinition{{Name: "read_file"}, {Name: "exec"}, {Name: "page_tool"}},
		BaseDefinitions: []messages.ToolDefinition{{Name: "read_file"}, {Name: "exec"}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for name, want := range map[string]time.Duration{
		"read_file": tools.DefaultInteractiveFastReadTimeout,
		"exec":      tools.DefaultInteractiveLongRunningTimeout,
		"page_tool": tools.DefaultInteractiveLongRunningTimeout,
	} {
		if got := policy.TimeoutForTool(name); got != want {
			t.Fatalf("timeout for %q = %s, want %s", name, got, want)
		}
	}
}

func TestServiceCleanupCoordinatorRunsEachCleanupOnce(t *testing.T) {
	calls := 0
	coordinator := NewService().NewCleanupCoordinator(func() error { calls++; return nil })
	if err := coordinator.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := coordinator.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if !coordinator.IsClosed() || calls != 1 {
		t.Fatalf("closed=%v cleanups=%d, want closed with one cleanup", coordinator.IsClosed(), calls)
	}
}
