package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const hostToolName = "host_tool"

type failingExecutor struct{ err error }

func (e failingExecutor) Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
	return messages.ToolCallResponse{}, e.err
}

func TestResolveRejectsInvalidRequests(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Resolve(canceled, public.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Resolve = %v, want context.Canceled", err)
	}
	cases := map[string]public.Request{
		"browser tool definitions require an execution route":    {Browser: &public.BrowserSurface{Definitions: []messages.ToolDefinition{{Name: "page"}}}},
		"workdir is required when additional roots are supplied": {Executor: testExecutor{}, AllowPaths: []string{t.TempDir()}},
		"resolve filesystem scope":                               {Executor: testExecutor{}, WorkDir: "relative/workdir"},
	}
	for want, request := range cases {
		if _, err := New().Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("Resolve(%+v) = %v, want %q", request, err, want)
		}
	}
}

func TestResolveExternalExecutorScopesWorkspaceAndInvokes(t *testing.T) {
	workspace := t.TempDir()
	capability, err := New().Resolve(context.Background(), public.Request{
		Executor: testExecutor{}, WorkDir: workspace,
		Definitions: []messages.ToolDefinition{{Name: hostToolName}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if capability.WorkspaceDir == "" || capability.Invoker == nil || capability.Handle == nil {
		t.Fatalf("external capability = %#v, want scoped workspace, invoker and handle", capability)
	}
	result, err := capability.Invoker.Invoke(context.Background(), public.Invocation{ID: "call-1", Name: hostToolName})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.ID != "call-1" {
		t.Fatalf("invocation result = %+v, want the call identity", result)
	}
	definitions, err := capability.Handle.RefreshDefinitions(context.Background())
	if err != nil || len(definitions) != 1 || definitions[0].Name != hostToolName {
		t.Fatalf("handle definitions = %v / %v, want the static surface", definitions, err)
	}
}

func TestInvokerPropagatesExecutorFailure(t *testing.T) {
	toolErr := errors.New("tool exploded")
	if _, err := (invoker{executor: failingExecutor{err: toolErr}}).Invoke(context.Background(), public.Invocation{Name: hostToolName}); !errors.Is(err, toolErr) {
		t.Fatalf("Invoke error = %v, want the executor error", err)
	}
	if _, err := (invoker{}).Invoke(context.Background(), public.Invocation{Name: hostToolName}); err == nil {
		t.Fatal("invoker without executor succeeded")
	}
}

func TestResolveBrowserWithDefaultSurfaceKeepsHostScope(t *testing.T) {
	workspace := t.TempDir()
	capability, err := New().Resolve(context.Background(), public.Request{
		UseDefaultTool: true, WorkDir: workspace,
		Browser: &public.BrowserSurface{Executor: &browserTestExecutor{}, Definitions: []messages.ToolDefinition{{Name: "show_page"}}},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !findDefinition(capability.Definitions, "show_page") || !findDefinition(capability.Definitions, "read_file") {
		t.Fatalf("definitions = %v, want default filesystem tools plus the browser surface", capability.Definitions)
	}
	if capability.WorkspaceDir == "" {
		t.Fatal("browser capability lost the resolved workspace")
	}
}

func TestCapabilityHandleLifecycleRejectsCanceledContext(t *testing.T) {
	initErr := errors.New("browser not ready")
	capability, err := New().Resolve(context.Background(), public.Request{
		Executor: testExecutor{},
		Browser: &public.BrowserSurface{
			Initialize:         func(context.Context) error { return initErr },
			RefreshDefinitions: func(context.Context) ([]messages.ToolDefinition, error) { return nil, initErr },
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := capability.Handle.Initialize(context.Background()); !errors.Is(err, initErr) {
		t.Fatalf("Initialize = %v, want the browser error", err)
	}
	if _, err := capability.Handle.RefreshDefinitions(context.Background()); !errors.Is(err, initErr) {
		t.Fatalf("RefreshDefinitions = %v, want the browser error", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := capability.Handle.Initialize(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Initialize = %v", err)
	}
	if _, err := capability.Handle.RefreshDefinitions(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled RefreshDefinitions = %v", err)
	}
	if err := capability.Handle.Close(); err != nil {
		t.Fatalf("Close without a browser close hook = %v", err)
	}
	var nilHandle *capabilityHandle
	if nilHandle.Initialize(canceled) != nil || nilHandle.Close() != nil {
		t.Fatal("nil handle lifecycle returned an error")
	}
	if definitions, err := nilHandle.RefreshDefinitions(canceled); definitions != nil || err != nil {
		t.Fatalf("nil handle refresh = %v / %v", definitions, err)
	}
}

func TestServiceCleanupCoordinatorReportsClosure(t *testing.T) {
	var calls int
	coordinator := New().NewCleanupCoordinator(func() error { calls++; return nil }, nil)
	if coordinator.IsClosed() {
		t.Fatal("new coordinator reports closed")
	}
	if err := coordinator.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !coordinator.IsClosed() || calls != 1 {
		t.Fatalf("closed=%v calls=%d, want closed after one cleanup", coordinator.IsClosed(), calls)
	}
	bounded := New().NewCleanupCoordinatorWithTimeout(0)
	if err := bounded.Close(); err != nil || !bounded.IsClosed() {
		t.Fatalf("empty bounded coordinator close = %v closed=%v", err, bounded.IsClosed())
	}
	var nilCoordinator *cleanupCoordinator
	if !nilCoordinator.IsClosed() || nilCoordinator.Close() != nil {
		t.Fatal("nil coordinator must report closed without error")
	}
}
