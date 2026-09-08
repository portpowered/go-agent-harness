package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

type coverageToolExecutor struct {
	response messages.ToolCallResponse
	err      error
	call     messages.ToolCall
}

func (e *coverageToolExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.call = call
	return e.response, e.err
}

type coveragePermissionRechecker struct {
	supported  bool
	permission runtimeTools.DisplayPermission
	err        error
}

func (r coveragePermissionRechecker) ScreenRecordingPermissionRecheckSupported() bool {
	return r.supported
}

func (r coveragePermissionRechecker) RecheckScreenRecordingPermission(context.Context) (runtimeTools.DisplayPermission, error) {
	return r.permission, r.err
}

func TestRuntimeDisplayExecutorPreservesExecutionAndPermissionContract(t *testing.T) {
	inner := &coverageToolExecutor{response: messages.ToolCallResponse{ToolCallID: "call-1", Name: "show", Content: "ok"}}
	cause := errors.New("permission probe failed")
	executor := &runtimeDisplayExecutor{
		inner: inner,
		rechecker: coveragePermissionRechecker{
			supported:  true,
			permission: runtimeTools.DisplayPermission{State: runtimeTools.DisplayPermissionDenied, Reason: "denied"},
			err:        cause,
		},
	}
	response, err := executor.Execute(context.Background(), messages.ToolCall{ID: "call-1", Name: "show", Arguments: "{}"})
	if err != nil || response.Content != "ok" || inner.call.Name != "show" {
		t.Fatalf("Execute = %+v, %v; call=%+v", response, err, inner.call)
	}
	if !executor.ScreenRecordingPermissionRecheckSupported() {
		t.Fatal("permission recheck was reported unsupported")
	}
	permission, err := executor.RecheckScreenRecordingPermission(context.Background())
	if !errors.Is(err, cause) || permission.State != runtimeTools.DisplayPermissionDenied || permission.Reason != "denied" {
		t.Fatalf("permission recheck = %+v, %v", permission, err)
	}

	var nilExecutor *runtimeDisplayExecutor
	if _, err := nilExecutor.Execute(context.Background(), messages.ToolCall{ID: "nil"}); err == nil {
		t.Fatal("nil display executor executed successfully")
	}
	if nilExecutor.ScreenRecordingPermissionRecheckSupported() {
		t.Fatal("nil display executor reported permission support")
	}
	if permission, err := nilExecutor.RecheckScreenRecordingPermission(context.Background()); err != nil || permission.State != runtimeTools.DisplayPermissionUnavailable {
		t.Fatalf("nil permission recheck = %+v, %v", permission, err)
	}
	if got := adaptRuntimeDisplayExecutor(nil); got != nil {
		t.Fatalf("adaptRuntimeDisplayExecutor(nil) = %v", got)
	}
	plain := &coverageToolExecutor{}
	if got := adaptRuntimeDisplayExecutor(plain); got != plain {
		t.Fatal("plain executor was unexpectedly replaced")
	}
	if got := (&runtimeDisplayExecutor{}).ScreenRecordingPermissionRecheckSupported(); got {
		t.Fatal("executor without rechecker reported permission support")
	}
}

func TestResolveDisplayCapabilityCoversConfigurationAndProbeOutcomes(t *testing.T) {
	disabled := &config.Config{}
	disabled.Tools.List = []config.ToolEntry{{ID: "show", Enabled: false}, {ID: "mouse", Enabled: false}}
	if capability := resolveDisplayCapability(disabled, cliTools.DisplayCapabilityProbeFunc(func(context.Context) (cliTools.DisplayCapability, error) {
		t.Fatal("disabled display tools invoked the probe")
		return cliTools.DisplayCapability{}, nil
	})); capability.Reason == "" || capability.State != cliTools.DisplayCapabilityUnavailable {
		t.Fatalf("disabled display capability = %+v", capability)
	}

	if capability := resolveDisplayCapability(&config.Config{}, nil); capability.Reason == "" || capability.State != cliTools.DisplayCapabilityUnavailable {
		t.Fatalf("missing probe capability = %+v", capability)
	}
	usable := resolveDisplayCapability(&config.Config{}, cliTools.DisplayCapabilityProbeFunc(func(context.Context) (cliTools.DisplayCapability, error) {
		return cliTools.DisplayCapability{Available: true, DisplayCount: 2}, nil
	}))
	if !usable.Usable() || usable.State != cliTools.DisplayCapabilityUsable {
		t.Fatalf("usable display capability = %+v", usable)
	}
	failure := resolveDisplayCapability(&config.Config{}, cliTools.DisplayCapabilityProbeFunc(func(context.Context) (cliTools.DisplayCapability, error) {
		return cliTools.DisplayCapability{}, errors.New("probe failed")
	}))
	if failure.State != cliTools.DisplayCapabilityUnavailable || failure.Reason == "" {
		t.Fatalf("failed display capability = %+v", failure)
	}
	unknown := resolveDisplayCapability(&config.Config{}, cliTools.DisplayCapabilityProbeFunc(func(context.Context) (cliTools.DisplayCapability, error) {
		return cliTools.DisplayCapability{Reason: "not usable"}, nil
	}))
	if unknown.State != cliTools.DisplayCapabilityUnavailable || unknown.Available || unknown.Reason != "not usable" {
		t.Fatalf("unusable display capability = %+v", unknown)
	}
}

func TestServiceResolvesEnabledDisplayCapability(t *testing.T) {
	cfg := &config.Config{FilesystemWorkDir: t.TempDir()}
	cfg.Browser = config.DefaultBrowserConfig()
	cfg.Tools.List = []config.ToolEntry{{ID: "show", Enabled: true}}
	service := New(nil, nil, nil, cliTools.DisplayCapabilityProbeFunc(func(context.Context) (cliTools.DisplayCapability, error) {
		return cliTools.UsableDisplayCapability(1), nil
	}), runtimeToolsWire.NewService())
	capabilities, err := service.Resolve(cfg)
	if err != nil {
		t.Fatalf("Resolve enabled display capability: %v", err)
	}
	if !capabilities.DisplayCapability.Usable() || capabilities.Executor == nil {
		t.Fatalf("resolved capabilities = %+v", capabilities)
	}
}

func TestStableDefinitionsAndBrowserStateHelpers(t *testing.T) {
	stable := []messages.ToolDefinition{{Name: "exec"}, {Name: "show"}}
	refreshed := []messages.ToolDefinition{{Name: "page_b"}, {Name: "show"}, {Name: "page_a"}, {Name: "exec"}}
	ordered := stableDefinitionsFirst(stable, refreshed)
	if len(ordered) != len(refreshed) || ordered[0].Name != "show" || ordered[1].Name != "exec" || ordered[2].Name != "page_b" {
		t.Fatalf("stable definitions = %v", ordered)
	}
	if got := initialBrowserState(serviceTools.BrowserCapability{Status: func() serviceTools.CapabilityStatus {
		return serviceTools.CapabilityStatus{BrowserCapabilityState: webmcp.BrowserCapabilitySelected}
	}}); got != webmcp.BrowserCapabilitySelected {
		t.Fatalf("initial browser state = %q", got)
	}
	if got := initialBrowserState(serviceTools.BrowserCapability{}); got != webmcp.BrowserCapabilityInitializing {
		t.Fatalf("default browser state = %q", got)
	}

	connected := serviceTools.BrowserCapability{Status: func() serviceTools.CapabilityStatus {
		return serviceTools.CapabilityStatus{BrowserCapabilityState: webmcp.BrowserCapabilityConnectedUnselected}
	}}
	stale := webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "stale", map[string]any{
		"reason": "selection_not_connected", "browser_id": "", "target_id": "",
	})
	if !connectedUnselected(connected, stale) {
		t.Fatal("connected-unselected stale selection was not recognized")
	}
	if connectedUnselected(serviceTools.BrowserCapability{}, stale) {
		t.Fatal("browser without status was recognized as connected-unselected")
	}
	deadline := webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "deadline", map[string]any{
		"reason_code": "page_tools_unverified", "reason": "deadline_exceeded",
	})
	deadline.Retryable = true
	if !retryableCatalogDeadline(deadline) {
		t.Fatal("retryable catalog deadline was not recognized")
	}
	deadline.Retryable = false
	if retryableCatalogDeadline(deadline) {
		t.Fatal("non-retryable catalog deadline was recognized")
	}
}
