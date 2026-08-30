package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Gate probe 11 regression: first-class page tools on a pre-selected tab died
// at the 5s fast/read budget (classification=interactive_tool_timeout) while
// the browser was healthy - a composed page invoke spans a CDP round trip and,
// cold, the full dial+attach+enable+catalog. Page tools and the stable WebMCP
// broker tools belong to the bounded long-running class.
func TestInteractivePolicyAdmitsBrowserToolsAsLongRunning(t *testing.T) {
	base := []messages.ToolDefinition{
		{Name: "read_file"},
		{Name: webmcp.SelectTabToolName},
		{Name: webmcp.InvokeToolName},
	}
	full := append(append([]messages.ToolDefinition(nil), base...),
		messages.ToolDefinition{Name: "get_cube_state"},
		messages.ToolDefinition{Name: "queue_cube_moves"},
	)
	policy, err := NewInteractiveToolPolicyForSession(config.DefaultInteractiveToolConfig(), full, base, true)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	for _, name := range []string{"get_cube_state", "queue_cube_moves", webmcp.SelectTabToolName, webmcp.InvokeToolName} {
		if class := policy.ClassForTool(name); class != InteractiveToolClassBoundedLongRunning {
			t.Fatalf("class(%s) = %s, want bounded long-running", name, class)
		}
		if timeout := policy.TimeoutForTool(name); timeout != config.DefaultInteractiveLongRunningTimeout {
			t.Fatalf("timeout(%s) = %s, want the long-running budget", name, timeout)
		}
	}
	if class := policy.ClassForTool("read_file"); class != InteractiveToolClassFastRead {
		t.Fatalf("class(read_file) = %s, want fast/read", class)
	}
	// A page tool registered mid-session (dynamic publisher) is not in the
	// snapshot; browser sessions keep it on the bounded budget.
	if class := policy.ClassForTool("create_document"); class != InteractiveToolClassBoundedLongRunning {
		t.Fatalf("class(mid-session page tool) = %s, want bounded long-running", class)
	}
	// Without browser dynamics the unknown-name fallback stays fast/read.
	staticPolicy, err := NewInteractiveToolPolicyForSession(config.DefaultInteractiveToolConfig(), base, base, false)
	if err != nil {
		t.Fatalf("static policy: %v", err)
	}
	if class := staticPolicy.ClassForTool("mystery"); class != InteractiveToolClassFastRead {
		t.Fatalf("static fallback = %s, want fast/read", class)
	}
}

type slowExecutor struct{ delay time.Duration }

func (e slowExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	select {
	case <-time.After(e.delay):
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: `{"ok":true}`}, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

// The timeout result names the expired budget so a 5s fast/read expiry is
// distinguishable from a 20s long-running expiry in transcripts and recdirs.
func TestSessionToolTimeoutNamesExpiredBudget(t *testing.T) {
	executor := newSessionToolExecutorWithTimeout(slowExecutor{delay: 5 * time.Second}, 150*time.Millisecond)
	response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "t1", Name: "get_cube_state"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(response.Content, SessionToolTimeoutClassification) {
		t.Fatalf("timeout content lacks classification: %s", response.Content)
	}
	if !strings.Contains(response.Content, "after 150ms") {
		t.Fatalf("timeout content does not name the expired budget: %s", response.Content)
	}
}
