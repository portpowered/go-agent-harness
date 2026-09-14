package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

func interactiveToolPolicySettings(settings config.InteractiveToolConfig) runtimeTools.InteractiveToolPolicySettings {
	return runtimeTools.InteractiveToolPolicySettings{
		FastReadTimeout:          settings.FastReadTimeout,
		LongRunningTimeout:       settings.LongRunningTimeout,
		AcknowledgementThreshold: settings.AcknowledgementThreshold,
	}
}

func newTestInteractiveToolPolicy(settings config.InteractiveToolConfig, definitions []messages.ToolDefinition) (runtimeTools.InteractiveToolPolicy, error) {
	return runtimeToolsWire.NewInteractiveToolPolicy().Resolve(runtimeTools.InteractiveToolPolicyRequest{
		Settings:    interactiveToolPolicySettings(settings),
		Definitions: append([]messages.ToolDefinition(nil), definitions...),
	})
}

func TestInteractiveToolPolicyDefaultsAndClassSelection(t *testing.T) {
	policy, err := newTestInteractiveToolPolicy(config.DefaultInteractiveToolConfig(), []messages.ToolDefinition{
		{Name: "read_file"},
		{Name: "exec"},
		{Name: "unclassified"},
	})
	if err != nil {
		t.Fatalf("resolve policy: %v", err)
	}
	settings := policy.Settings()
	wantSettings := runtimeTools.InteractiveToolPolicySettings{
		FastReadTimeout:          5 * time.Second,
		LongRunningTimeout:       20 * time.Second,
		AcknowledgementThreshold: 2 * time.Second,
	}
	if settings != wantSettings {
		t.Fatalf("policy settings = %+v, want %+v", settings, wantSettings)
	}
	if got := policy.ClassForTool("read_file"); got != runtimeTools.InteractiveToolClassFastRead {
		t.Fatalf("read_file class = %q, want fast/read", got)
	}
	if got := policy.ClassForTool("exec"); got != runtimeTools.InteractiveToolClassBoundedLongRunning {
		t.Fatalf("exec class = %q, want bounded-long-running", got)
	}
	if got := policy.ClassForTool("unclassified"); got != runtimeTools.InteractiveToolClassFastRead {
		t.Fatalf("unclassified class = %q, want fast/read", got)
	}
	if got := policy.ClassForTool("not-advertised"); got != runtimeTools.InteractiveToolClassFastRead {
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
	policy, err := newTestInteractiveToolPolicy(settings, []messages.ToolDefinition{{Name: "sleep"}})
	if err != nil {
		t.Fatalf("resolve policy: %v", err)
	}
	if got := policy.Settings(); got != interactiveToolPolicySettings(settings) {
		t.Fatalf("policy settings = %+v, want explicit settings", got)
	}
	if policy.TimeoutForTool("sleep") != 15*time.Second {
		t.Fatalf("sleep timeout = %s, want long-running override", policy.TimeoutForTool("sleep"))
	}
}

func TestInteractiveToolPolicyCloneAndFactoryInstanceIsolation(t *testing.T) {
	definitions := []messages.ToolDefinition{{Name: "read_file"}, {Name: "exec"}}
	factoryA := runtimeToolsWire.NewInteractiveToolPolicy()
	factoryB := runtimeToolsWire.NewInteractiveToolPolicy()
	policyA, err := factoryA.Resolve(runtimeTools.InteractiveToolPolicyRequest{Definitions: definitions})
	if err != nil {
		t.Fatalf("resolve policy A: %v", err)
	}
	policyB, err := factoryB.Resolve(runtimeTools.InteractiveToolPolicyRequest{Definitions: definitions})
	if err != nil {
		t.Fatalf("resolve policy B: %v", err)
	}
	clone := policyA.Clone()
	if clone == nil || clone == policyA {
		t.Fatal("Clone returned the original or a nil policy")
	}
	if clone.Settings() != policyA.Settings() || policyB.Settings() != policyA.Settings() {
		t.Fatalf("isolated policy settings differ: clone=%+v A=%+v B=%+v", clone.Settings(), policyA.Settings(), policyB.Settings())
	}
	if clone.ClassForTool("exec") != runtimeTools.InteractiveToolClassBoundedLongRunning || policyB.ClassForTool("read_file") != runtimeTools.InteractiveToolClassFastRead {
		t.Fatal("clone or independent factory lost its classification snapshot")
	}
}

func TestInteractiveToolExecutorUsesIndependentSessionBudgets(t *testing.T) {
	policyA, err := newTestInteractiveToolPolicy(config.InteractiveToolConfig{
		FastReadTimeout:          3 * time.Second,
		LongRunningTimeout:       11 * time.Second,
		AcknowledgementThreshold: time.Second,
	}, []messages.ToolDefinition{{Name: "read_file"}, {Name: "exec"}})
	if err != nil {
		t.Fatalf("policy A: %v", err)
	}
	policyB, err := newTestInteractiveToolPolicy(config.InteractiveToolConfig{
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
	executorA := newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntent(inner, policyA, 0, nil, nil)
	executorB := newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntent(inner, policyB, 0, nil, nil)

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
	definitions := []messages.ToolDefinition{{Name: "exec"}, {Name: "read_file"}}
	policy, err := newTestInteractiveToolPolicy(settings, definitions)
	if err != nil {
		t.Fatalf("resolve policy: %v", err)
	}

	plan, err := planSessionRuntime(SessionRunOptions{ModelCatalog: testModelCatalog(),
		ReplayPath:            "unused.json",
		InteractiveToolPolicy: policy,
		SessionInferencer:     stubPlanSessionInferencer{},
		ToolExecutor: sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
			return messages.ToolCallResponse{}, nil
		}),
		ToolDefinitions: definitions,
	})
	if err != nil {
		t.Fatalf("planSessionRuntime: %v", err)
	}
	if plan.interactivePolicy == nil || plan.loop.InteractiveToolPolicy == nil {
		t.Fatal("plan did not retain the interactive policy snapshot")
	}
	if plan.interactivePolicy.Settings().FastReadTimeout != 4*time.Second || plan.loop.InteractiveToolPolicy.Settings().LongRunningTimeout != 18*time.Second {
		t.Fatalf("plan policy settings = %+v/%+v, want explicit settings", plan.interactivePolicy.Settings(), plan.loop.InteractiveToolPolicy.Settings())
	}
}

type invalidInteractiveToolPolicy struct{}

func (invalidInteractiveToolPolicy) Settings() runtimeTools.InteractiveToolPolicySettings {
	return runtimeTools.InteractiveToolPolicySettings{}
}

func (invalidInteractiveToolPolicy) ClassForTool(string) runtimeTools.InteractiveToolClass {
	return runtimeTools.InteractiveToolClassFastRead
}

func (invalidInteractiveToolPolicy) TimeoutForTool(string) time.Duration { return 0 }

func (p invalidInteractiveToolPolicy) Clone() runtimeTools.InteractiveToolPolicy { return p }

func (invalidInteractiveToolPolicy) Validate() error {
	return errors.New("tools.interactive.fast_read_timeout must be positive and less than 10s; got 0s")
}

func TestPlanSessionRuntimeRejectsInvalidInteractivePolicyBeforeProviderSetup(t *testing.T) {
	providerCalls := 0
	factory := defaultSessionRuntimeFactory
	factory.newGrokSessionWithTools = func(config.GrokConfig, transport.Dialer, []messages.ToolDefinition) (messages.SessionInferencer, error) {
		providerCalls++
		return nil, errors.New("provider must not be built")
	}

	_, err := planSessionRuntimeWithFactory(SessionRunOptions{ModelCatalog: testModelCatalog(),
		InteractiveToolPolicy: invalidInteractiveToolPolicy{},
		Provider:              config.ProviderGrok,
		RecordPath:            "capture.json",
		ToolDefinitions:       []messages.ToolDefinition{{Name: "read_file"}},
	}, factory)
	if err == nil || !strings.Contains(err.Error(), "fast_read_timeout") {
		t.Fatalf("plan error = %v, want fast-read validation", err)
	}
	if providerCalls != 0 {
		t.Fatalf("provider setup calls = %d after invalid policy, want zero", providerCalls)
	}
}
