package agentruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func TestInteractiveToolPolicyDefaultsAndClassSelection(t *testing.T) {
	policy, err := ResolveInteractiveToolPolicy(nil, []messages.ToolDefinition{
		{Name: "read_file"},
		{Name: "exec"},
		{Name: "unclassified"},
	})
	if err != nil {
		t.Fatalf("ResolveInteractiveToolPolicy(): %v", err)
	}
	if policy.FastReadTimeout != config.DefaultInteractiveFastReadTimeout || policy.LongRunningTimeout != config.DefaultInteractiveLongRunningTimeout || policy.AcknowledgementThreshold != config.DefaultInteractiveAcknowledgementThreshold {
		t.Fatalf("policy budgets = %+v, want documented defaults", policy)
	}
	if got := policy.ClassForTool("read_file"); got != InteractiveToolClassFastRead {
		t.Fatalf("read_file class = %q, want fast/read", got)
	}
	if got := policy.ClassForTool("exec"); got != InteractiveToolClassBoundedLongRunning {
		t.Fatalf("exec class = %q, want bounded-long-running", got)
	}
	if got := policy.ClassForTool("unclassified"); got != InteractiveToolClassFastRead {
		t.Fatalf("unclassified class = %q, want fast/read", got)
	}
	if got := policy.ClassForTool("not-advertised"); got != InteractiveToolClassFastRead {
		t.Fatalf("unknown class = %q, want fast/read fallback", got)
	}
	if policy.TimeoutForTool("read_file") != 5*time.Second || policy.TimeoutForTool("exec") != 20*time.Second || policy.TimeoutForTool("not-advertised") != 5*time.Second {
		t.Fatalf("policy timeout selection did not use class budgets")
	}
}

func TestInteractiveToolPolicyHonorsOverrides(t *testing.T) {
	settings := config.InteractiveToolConfig{
		FastReadTimeout:          7 * time.Second,
		LongRunningTimeout:       15 * time.Second,
		AcknowledgementThreshold: 1200 * time.Millisecond,
	}
	policy, err := NewInteractiveToolPolicy(settings, []messages.ToolDefinition{{Name: "sleep"}})
	if err != nil {
		t.Fatalf("NewInteractiveToolPolicy(): %v", err)
	}
	if policy.FastReadTimeout != 7*time.Second || policy.LongRunningTimeout != 15*time.Second || policy.AcknowledgementThreshold != 1200*time.Millisecond {
		t.Fatalf("policy = %+v, want explicit settings", policy)
	}
	if policy.TimeoutForTool("sleep") != 15*time.Second {
		t.Fatalf("sleep timeout = %s, want long-running override", policy.TimeoutForTool("sleep"))
	}
}

func TestInteractiveToolExecutorUsesIndependentSessionBudgets(t *testing.T) {
	policyA, err := NewInteractiveToolPolicy(config.InteractiveToolConfig{
		FastReadTimeout:          3 * time.Second,
		LongRunningTimeout:       11 * time.Second,
		AcknowledgementThreshold: time.Second,
	}, []messages.ToolDefinition{{Name: "read_file"}, {Name: "exec"}})
	if err != nil {
		t.Fatalf("policy A: %v", err)
	}
	policyB, err := NewInteractiveToolPolicy(config.InteractiveToolConfig{
		FastReadTimeout:          7 * time.Second,
		LongRunningTimeout:       19 * time.Second,
		AcknowledgementThreshold: 2 * time.Second,
	}, []messages.ToolDefinition{{Name: "read_file"}, {Name: "exec"}})
	if err != nil {
		t.Fatalf("policy B: %v", err)
	}

	inner := sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return messages.ToolCallResponse{}, errors.New("missing per-call deadline")
		}
		return messages.ToolCallResponse{Content: time.Until(deadline).String()}, nil
	})
	executorA := newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntent(inner, &policyA, 0, nil, nil)
	executorB := newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntent(inner, &policyB, 0, nil, nil)

	responseA, err := executorA.Execute(context.Background(), messages.ToolCall{ID: "a", Name: "read_file"})
	if err != nil {
		t.Fatalf("executor A: %v", err)
	}
	responseB, err := executorB.Execute(context.Background(), messages.ToolCall{ID: "b", Name: "read_file"})
	if err != nil {
		t.Fatalf("executor B: %v", err)
	}
	remainingA, err := time.ParseDuration(responseA.Content)
	if err != nil {
		t.Fatalf("parse A remaining deadline %q: %v", responseA.Content, err)
	}
	remainingB, err := time.ParseDuration(responseB.Content)
	if err != nil {
		t.Fatalf("parse B remaining deadline %q: %v", responseB.Content, err)
	}
	if remainingA < 2*time.Second || remainingA > 3*time.Second {
		t.Fatalf("A deadline remaining = %s, want close to 3s", remainingA)
	}
	if remainingB < 6*time.Second || remainingB > 7*time.Second {
		t.Fatalf("B deadline remaining = %s, want close to 7s", remainingB)
	}
}

func TestPlanSessionRuntimeThreadsResolvedInteractivePolicyBeforeProviderSetup(t *testing.T) {
	settings := config.DefaultInteractiveToolConfig()
	settings.FastReadTimeout = 4 * time.Second
	settings.LongRunningTimeout = 18 * time.Second
	cfg := &config.Config{Tools: config.ToolsConfig{Interactive: settings}}

	plan, err := planSessionRuntime(SessionRunOptions{
		ReplayPath:        "unused.json",
		LoadedConfig:      cfg,
		SessionInferencer: stubPlanSessionInferencer{},
		ToolExecutor: sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
			return messages.ToolCallResponse{}, nil
		}),
		ToolDefinitions: []messages.ToolDefinition{{Name: "exec"}, {Name: "read_file"}},
	})
	if err != nil {
		t.Fatalf("planSessionRuntime: %v", err)
	}
	if plan.interactivePolicy == nil || plan.loop.InteractiveToolPolicy == nil {
		t.Fatal("plan did not retain the interactive policy snapshot")
	}
	if plan.interactivePolicy.FastReadTimeout != 4*time.Second || plan.loop.InteractiveToolPolicy.LongRunningTimeout != 18*time.Second {
		t.Fatalf("plan policy = %+v, want explicit settings", *plan.interactivePolicy)
	}
}

func TestPlanSessionRuntimeLoadsInteractivePolicyFromConfigDir(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, config.ConfigFileName)
	if err := os.WriteFile(configPath, []byte(`tools:
  interactive:
    fast_read_timeout: 6s
    long_running_timeout: 16s
    acknowledgement_threshold: 1500ms
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	plan, err := planSessionRuntime(SessionRunOptions{
		ConfigDir:         dir,
		ReplayPath:        "unused.json",
		SessionInferencer: stubPlanSessionInferencer{},
		ToolExecutor: sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
			return messages.ToolCallResponse{}, nil
		}),
		ToolDefinitions: []messages.ToolDefinition{{Name: "read_file"}},
	})
	if err != nil {
		t.Fatalf("planSessionRuntime: %v", err)
	}
	if plan.interactivePolicy == nil || plan.interactivePolicy.FastReadTimeout != 6*time.Second || plan.interactivePolicy.LongRunningTimeout != 16*time.Second || plan.interactivePolicy.AcknowledgementThreshold != 1500*time.Millisecond {
		t.Fatalf("plan policy = %+v, want ConfigDir policy", plan.interactivePolicy)
	}
}

func TestPlanSessionRuntimeRejectsInvalidInteractiveConfigBeforeProviderSetup(t *testing.T) {
	settings := config.DefaultInteractiveToolConfig()
	settings.FastReadTimeout = 10 * time.Second
	providerCalls := 0
	factory := defaultSessionRuntimeFactory
	factory.newGrokSessionWithTools = func(config.GrokConfig, transport.Dialer, []messages.ToolDefinition) (messages.SessionInferencer, error) {
		providerCalls++
		return nil, errors.New("provider must not be built")
	}

	_, err := planSessionRuntimeWithFactory(SessionRunOptions{
		LoadedConfig:    &config.Config{Tools: config.ToolsConfig{Interactive: settings}},
		Provider:        config.ProviderGrok,
		RecordPath:      "capture.json",
		ToolDefinitions: []messages.ToolDefinition{{Name: "read_file"}},
	}, factory)
	if err == nil || !strings.Contains(err.Error(), "fast_read_timeout") {
		t.Fatalf("plan error = %v, want fast-read validation", err)
	}
	if providerCalls != 0 {
		t.Fatalf("provider setup calls = %d after invalid config, want zero", providerCalls)
	}
}
