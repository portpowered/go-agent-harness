package wire

import (
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	tools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

func TestNewInteractiveToolPolicyWiresPublicFactory(t *testing.T) {
	factory := NewInteractiveToolPolicy()
	if factory == nil {
		t.Fatal("NewInteractiveToolPolicy returned nil")
	}
	policy, err := factory.Resolve(tools.InteractiveToolPolicyRequest{
		Definitions: []messages.ToolDefinition{{Name: "read_file"}, {Name: "exec"}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if policy.Settings() != (tools.InteractiveToolPolicySettings{
		FastReadTimeout:          5 * time.Second,
		LongRunningTimeout:       20 * time.Second,
		AcknowledgementThreshold: 2 * time.Second,
	}) {
		t.Fatalf("settings = %+v, want documented defaults", policy.Settings())
	}
	if policy.ClassForTool("read_file") != tools.InteractiveToolClassFastRead || policy.ClassForTool("exec") != tools.InteractiveToolClassBoundedLongRunning {
		t.Fatalf("wired classes = read:%s exec:%s", policy.ClassForTool("read_file"), policy.ClassForTool("exec"))
	}
}

func TestNewInteractiveToolPolicyPreservesValidationThroughPublicFactory(t *testing.T) {
	factory := NewInteractiveToolPolicy()
	settings := tools.InteractiveToolPolicySettings{
		FastReadTimeout:          10 * time.Second,
		LongRunningTimeout:       20 * time.Second,
		AcknowledgementThreshold: 2 * time.Second,
	}
	if err := factory.ValidateSettings(settings); err == nil || !strings.Contains(err.Error(), "less than 10s") {
		t.Fatalf("ValidateSettings() = %v, want exact fast-read bound error", err)
	}
	if _, err := factory.Resolve(tools.InteractiveToolPolicyRequest{Settings: settings}); err == nil || !strings.Contains(err.Error(), "fast_read_timeout") {
		t.Fatalf("Resolve() = %v, want validation error", err)
	}
}
