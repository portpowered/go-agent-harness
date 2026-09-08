package runtimeadapter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

type adapterTestService struct {
	calls        atomic.Int32
	config       *config.Config
	capabilities serviceTools.Capabilities
}

func (s *adapterTestService) Resolve(cfg *config.Config) (serviceTools.Capabilities, error) {
	s.calls.Add(1)
	s.config = cfg
	return s.capabilities, nil
}

type adapterTestExecutor struct {
	calls atomic.Int32
}

func (e *adapterTestExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.calls.Add(1)
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: call.Arguments}, nil
}

func TestRuntimeToolServiceAdapterMapsRequestAndRetainsCapabilityLifecycle(t *testing.T) {
	closeErr := errors.New("capability close failed")
	executor := &adapterTestExecutor{}
	definitions := []messages.ToolDefinition{{Name: "custom", Parameters: []messages.ToolParameter{{Name: "value"}}}}
	var initializeCalls, refreshCalls, closeCalls atomic.Int32
	host := &adapterTestService{capabilities: serviceTools.Capabilities{
		Executor:    executor,
		Definitions: definitions,
		Initialize: func(ctx context.Context) error {
			initializeCalls.Add(1)
			return ctx.Err()
		},
		RefreshDefinitionsWithError: func(context.Context) ([]messages.ToolDefinition, error) {
			refreshCalls.Add(1)
			return []messages.ToolDefinition{{Name: "refreshed"}}, nil
		},
		Close: func() error {
			closeCalls.Add(1)
			return closeErr
		},
	}}
	adapter := New(host, runtimeToolsWire.NewService())
	workDir := t.TempDir()
	capability, err := adapter.Resolve(context.Background(), runtimeTools.Request{
		WorkDir:    workDir,
		AllowPaths: []string{"extra"},
		Selections: []runtimeTools.ToolSelection{{ID: "custom", Enabled: true}},
		Exec: runtimeTools.ExecPolicy{
			EnableDenyPatterns: true,
			CustomDenyPatterns: []string{"rm -rf"},
		},
	})
	if err != nil {
		t.Fatalf("resolve adapted tool service: %v", err)
	}
	if got := host.calls.Load(); got != 1 {
		t.Fatalf("host resolve calls = %d, want one", got)
	}
	assertAdapterRequestConfig(t, host, workDir)
	if len(capability.Definitions) != 1 || capability.Definitions[0].Name != "custom" {
		t.Fatalf("adapted definitions = %#v, want custom definition", capability.Definitions)
	}
	definitions[0].Name = "mutated"
	if capability.Definitions[0].Name != "custom" {
		t.Fatalf("adapted definitions share host storage: %#v", capability.Definitions)
	}
	if capability.Handle == nil {
		t.Fatal("adapted capability handle is nil")
	}
	if err := capability.Handle.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize adapted capability: %v", err)
	}
	refreshed, err := capability.Handle.RefreshDefinitions(context.Background())
	if err != nil {
		t.Fatalf("refresh adapted capability: %v", err)
	}
	if len(refreshed) != 1 || refreshed[0].Name != "refreshed" {
		t.Fatalf("refreshed definitions = %#v, want refreshed definition", refreshed)
	}
	if err := capability.Handle.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("close error = %v, want %v", err, closeErr)
	}
	if err := capability.Handle.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("second close error = %v, want retained %v", err, closeErr)
	}
	if initializeCalls.Load() != 1 || refreshCalls.Load() != 1 || closeCalls.Load() != 1 {
		t.Fatalf("lifecycle calls = init:%d refresh:%d close:%d, want 1 each", initializeCalls.Load(), refreshCalls.Load(), closeCalls.Load())
	}
	if _, err := capability.Invoker.Invoke(context.Background(), runtimeTools.Invocation{ID: "call", Name: "custom", Arguments: "{}"}); err != nil {
		t.Fatalf("invoke adapted capability: %v", err)
	}
	if executor.calls.Load() != 1 {
		t.Fatalf("executor calls = %d, want one", executor.calls.Load())
	}
}

func assertAdapterRequestConfig(t *testing.T, host *adapterTestService, workDir string) {
	t.Helper()
	if host.config == nil || host.config.FilesystemWorkDir != workDir {
		t.Fatalf("resolved config workdir = %#v, want %q", host.config, workDir)
	}
	if len(host.config.FilesystemAllowPaths) != 1 || host.config.FilesystemAllowPaths[0] != "extra" {
		t.Fatalf("resolved allow paths = %#v, want [extra]", host.config.FilesystemAllowPaths)
	}
	if !host.config.Tools.Exec.EnableDenyPatterns || len(host.config.Tools.Exec.CustomDenyPatterns) != 1 || host.config.Tools.Exec.CustomDenyPatterns[0] != "rm -rf" {
		t.Fatalf("resolved exec policy = %#v, want mapped policy", host.config.Tools.Exec)
	}
}
