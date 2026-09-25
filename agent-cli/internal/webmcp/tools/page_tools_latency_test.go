package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// latencyBroker wraps recordingBroker with injected per-operation latency,
// modeling a loaded machine or cold attach (gate probe 11).
type latencyBroker struct {
	recordingBroker
	listToolsDelay time.Duration
	invokeDelay    time.Duration
}

func (b *latencyBroker) ListTools(ctx context.Context, options webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	select {
	case <-time.After(b.listToolsDelay):
	case <-ctx.Done():
		return webmcp.ToolCatalogSnapshot{}, ctx.Err()
	}
	return b.recordingBroker.ListTools(ctx, options)
}

func (b *latencyBroker) Invoke(ctx context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	select {
	case <-time.After(b.invokeDelay):
	case <-ctx.Done():
		return webmcp.InvokeResult{}, ctx.Err()
	}
	return b.recordingBroker.Invoke(ctx, request)
}

// A slow setup phase (cold dial/attach/enable/catalog) is classified as slow
// browser setup - the page tool never ran - instead of silently consuming the
// interactive budget and blaming the tool.
func TestPageToolSlowSetupClassifiedDistinctly(t *testing.T) {
	t.Parallel()
	// ListTools outlasts the whole call, so only the setup reserve (one third
	// of the remaining budget) decides the outcome; the scale is irrelevant.
	broker := &latencyBroker{listToolsDelay: 2 * time.Second}
	broker.catalog = pageCatalog()
	set := NewBrokerToolSet(broker)
	set.SetReservedToolNames([]string{"exec"})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Millisecond)
	defer cancel()
	response, err := set.Executor().Execute(ctx, messages.ToolCall{ID: "slow-setup", Name: "get_cube_state", Arguments: `{}`})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var envelope webmcp.ToolResultEnvelope
	if err := json.Unmarshal([]byte(response.Content), &envelope); err != nil || envelope.OK || envelope.Error == nil {
		t.Fatalf("slow setup = %s, want failure envelope", response.Content)
	}
	if envelope.Error.Code != string(webmcp.ErrorTargetAttachFailed) || envelope.Error.Details["phase"] != "setup_timeout" {
		t.Fatalf("slow setup classified as %+v, want target_attach_failed/setup_timeout", envelope.Error)
	}
	if !strings.Contains(envelope.Error.Message, "page tool itself never ran") {
		t.Fatalf("setup-timeout message does not exonerate the page tool: %q", envelope.Error.Message)
	}
}

// With the long-running budget, a realistic cold-start plus a real page-tool
// delay completes: setup latency and a multi-second invoke both fit.
func TestPageToolColdStartFitsLongRunningBudget(t *testing.T) {
	t.Parallel()
	// Scaled 10x down from a 700ms setup and 900ms invoke under a 4s budget:
	// the setup reserve is proportional, so the ratios are what is tested.
	broker := &latencyBroker{listToolsDelay: 70 * time.Millisecond, invokeDelay: 90 * time.Millisecond}
	broker.catalog = pageCatalog()
	broker.invokeResult = webmcp.InvokeResult{InvocationID: "inv-cold", State: webmcp.InvocationCompleted, Output: json.RawMessage(`{"solved":true}`)}
	set := NewBrokerToolSet(broker)
	set.SetReservedToolNames([]string{"exec"})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	started := time.Now()
	response, err := set.Executor().Execute(ctx, messages.ToolCall{ID: "cold-ok", Name: "get_cube_state", Arguments: `{}`})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var envelope webmcp.ToolResultEnvelope
	if err := json.Unmarshal([]byte(response.Content), &envelope); err != nil || !envelope.OK {
		t.Fatalf("cold start under long budget = %s (after %s), want success", response.Content, time.Since(started))
	}
}

// closeAtTestEnd releases a broker or browser fixture when a test finishes.
// Scripted fixtures close cleanly, so a close failure is a real teardown defect.
func closeAtTestEnd(t testing.TB, closer interface{ Close() error }) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("close test fixture: %v", err)
	}
}

// mustAs asserts a fixture value's concrete type and fails the test otherwise.
func mustAs[T any](t testing.TB, value any) T {
	t.Helper()
	typed, ok := value.(T)
	if !ok {
		t.Fatalf("fixture value = %T, want %T", value, typed)
	}
	return typed
}
