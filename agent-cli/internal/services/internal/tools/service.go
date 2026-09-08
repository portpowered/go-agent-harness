// Package tools contains the private session capability composition.
package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const displayCapabilityProbeTimeout = 3 * time.Second

// Service owns static tool registry creation and browser surface composition.
// The browser factory is injected so transport code only adapts its existing
// browser runtime to this neutral request-scoped seam.
type Service struct {
	staticExecutor messages.ToolExecutor
	brokerFactory  serviceTools.BrowserFactory
	displaySurface cliTools.DisplaySurface
	displayProbe   cliTools.DisplayCapabilityProbe
	runtimeService runtimeTools.Service
}

// runtimeDisplayExecutor adapts the CLI's display permission value contract
// to the reusable tools service. It is intentionally a one-purpose host
// adapter: runtime composition owns route selection while the CLI retains the
// platform-specific permission implementation.
type runtimeDisplayExecutor struct {
	inner     messages.ToolExecutor
	rechecker cliTools.ScreenRecordingPermissionRechecker
}

var _ messages.ToolExecutor = (*runtimeDisplayExecutor)(nil)
var _ runtimeTools.ScreenRecordingPermissionRechecker = (*runtimeDisplayExecutor)(nil)

func (e *runtimeDisplayExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if e == nil || e.inner == nil {
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name}, errors.New("display executor is unavailable")
	}
	return e.inner.Execute(ctx, call)
}

func (e *runtimeDisplayExecutor) ScreenRecordingPermissionRecheckSupported() bool {
	return e != nil && e.rechecker != nil && e.rechecker.ScreenRecordingPermissionRecheckSupported()
}

func (e *runtimeDisplayExecutor) RecheckScreenRecordingPermission(ctx context.Context) (runtimeTools.DisplayPermission, error) {
	if e == nil || e.rechecker == nil {
		return runtimeTools.DisplayPermission{State: runtimeTools.DisplayPermissionUnavailable, Reason: "screen recording permission re-check is unavailable"}, nil
	}
	permission, err := e.rechecker.RecheckScreenRecordingPermission(ctx)
	return runtimeTools.DisplayPermission{State: runtimeTools.DisplayPermissionState(permission.State), Reason: permission.Reason}, err
}

func adaptRuntimeDisplayExecutor(executor messages.ToolExecutor) messages.ToolExecutor {
	if executor == nil {
		return nil
	}
	if _, ok := executor.(runtimeTools.ScreenRecordingPermissionRechecker); ok {
		return executor
	}
	rechecker, ok := executor.(cliTools.ScreenRecordingPermissionRechecker)
	if !ok {
		return executor
	}
	return &runtimeDisplayExecutor{inner: executor, rechecker: rechecker}
}

var _ serviceTools.Service = (*Service)(nil)

func New(staticExecutor messages.ToolExecutor, brokerFactory serviceTools.BrowserFactory, displaySurface cliTools.DisplaySurface, displayProbe cliTools.DisplayCapabilityProbe, runtimeService runtimeTools.Service) *Service {
	return &Service{staticExecutor: staticExecutor, brokerFactory: brokerFactory, displaySurface: displaySurface, displayProbe: displayProbe, runtimeService: runtimeService}
}

func (s *Service) Resolve(cfg *config.Config) (serviceTools.Capabilities, error) {
	if cfg != nil {
		if err := cfg.ValidateBrowser(); err != nil {
			return serviceTools.Capabilities{}, fmt.Errorf("resolve browser config: %w", err)
		}
	}
	displayCapability := resolveDisplayCapability(cfg, s.displayProbe)
	resolvedStatic, definitions, err := s.resolveStatic(cfg, displayCapability)
	if err != nil {
		return serviceTools.Capabilities{}, err
	}
	if cfg == nil || !cfg.Browser.BrowserBackendEnabled() {
		return serviceTools.Capabilities{Executor: resolvedStatic, Definitions: definitions, BrowserCapabilityState: webmcp.BrowserCapabilityDisabled, DisplayCapability: displayCapability}, nil
	}
	return s.resolveBrowser(cfg, displayCapability, resolvedStatic, definitions)
}

func (s *Service) resolveBrowser(cfg *config.Config, displayCapability cliTools.DisplayCapability, resolvedStatic messages.ToolExecutor, definitions []messages.ToolDefinition) (serviceTools.Capabilities, error) {
	if s.brokerFactory == nil {
		return serviceTools.Capabilities{}, errors.New("construct WebMCP broker: browser factory is nil")
	}
	browser, err := s.brokerFactory(cfg.Browser, cfg.ConfigDir)
	if err != nil {
		return serviceTools.Capabilities{}, closeFailedBrowser(browser, fmt.Errorf("construct WebMCP broker: %w", err))
	}
	if browser.Broker == nil {
		return serviceTools.Capabilities{}, closeFailedBrowser(browser, errors.New("construct WebMCP broker: factory returned nil"))
	}
	brokerSet := webmcpTools.NewBrokerToolSet(browser.Broker, cfg.Browser.Tools.WebCast)
	browserSurface := runtimeTools.BrowserSurface{
		Executor:    brokerSet.Executor(),
		Definitions: brokerSet.Definitions(),
		RefreshDefinitions: func(ctx context.Context) ([]messages.ToolDefinition, error) {
			return s.refreshBrowserDefinitions(ctx, browser, brokerSet)
		},
		Initialize: browser.Initialize,
		Close:      browser.Close,
	}
	if s.runtimeService == nil {
		return serviceTools.Capabilities{}, closeFailedBrowser(browser, errors.New("resolve tools: runtime service is nil"))
	}
	capability, err := s.runtimeService.Resolve(context.Background(), runtimeTools.Request{
		Executor:                adaptRuntimeDisplayExecutor(resolvedStatic),
		Definitions:             definitions,
		FilesystemPolicyApplied: true,
		Browser:                 &browserSurface,
	})
	if err != nil {
		return serviceTools.Capabilities{}, closeFailedBrowser(browser, fmt.Errorf("compose session tools: %w", err))
	}
	reserved := make([]string, 0, len(capability.Definitions)+2)
	for _, definition := range capability.Definitions {
		reserved = append(reserved, definition.Name)
	}
	reserved = append(reserved, runtimeTools.ScreenToolID, runtimeTools.HostDisplayToolID)
	brokerSet.SetReservedToolNames(reserved)
	refresh := func(ctx context.Context) ([]messages.ToolDefinition, error) {
		if capability.Handle == nil {
			return append([]messages.ToolDefinition(nil), capability.Definitions...), nil
		}
		refreshed, err := capability.Handle.RefreshDefinitions(ctx)
		if err != nil {
			return nil, err
		}
		return stableDefinitionsFirst(capability.Definitions, refreshed), nil
	}
	capabilities := serviceTools.Capabilities{
		Executor: capability.Executor, Definitions: capability.Definitions,
		BrowserCapabilityState: initialBrowserState(browser), DisplayCapability: displayCapability,
		RefreshDefinitions: func(ctx context.Context) []messages.ToolDefinition {
			result, err := refresh(ctx)
			if err != nil {
				return append([]messages.ToolDefinition(nil), capability.Definitions...)
			}
			return result
		},
		RefreshDefinitionsWithError: refresh,
		Initialize:                  capability.Handle.Initialize, Status: browser.Status,
		BrowserWatch: browser.BrowserWatch, BrowserEventWatch: browser.BrowserEventWatch,
	}
	if capability.Handle != nil {
		capabilities.Close = capability.Handle.Close
	}
	return capabilities, nil
}

func closeFailedBrowser(browser serviceTools.BrowserCapability, primary error) error {
	if browser.Close == nil {
		return primary
	}
	if closeErr := browser.Close(); closeErr != nil {
		return errors.Join(primary, fmt.Errorf("close WebMCP broker: %w", closeErr))
	}
	return primary
}

func (s *Service) refreshBrowserDefinitions(ctx context.Context, browser serviceTools.BrowserCapability, brokerSet *webmcpTools.BrokerToolSet) ([]messages.ToolDefinition, error) {
	page, refreshErr := brokerSet.PageToolDefinitionsWithError(ctx)
	if refreshErr != nil {
		if connectedUnselected(browser, refreshErr) || retryableCatalogDeadline(refreshErr) {
			return brokerSet.Definitions(), nil
		}
		return nil, fmt.Errorf("refresh WebMCP page tools: %w", refreshErr)
	}
	definitions := brokerSet.Definitions()
	return append(definitions, page...), nil
}

func stableDefinitionsFirst(stable, refreshed []messages.ToolDefinition) []messages.ToolDefinition {
	stableNames := make(map[string]struct{}, len(stable))
	for _, definition := range stable {
		stableNames[definition.Name] = struct{}{}
	}
	result := make([]messages.ToolDefinition, 0, len(refreshed))
	page := make([]messages.ToolDefinition, 0)
	for _, definition := range refreshed {
		if _, ok := stableNames[definition.Name]; ok {
			result = append(result, definition)
			continue
		}
		page = append(page, definition)
	}
	return append(result, page...)
}

func (s *Service) resolveStatic(cfg *config.Config, display cliTools.DisplayCapability) (messages.ToolExecutor, []messages.ToolDefinition, error) {
	if s != nil && s.staticExecutor != nil {
		return s.staticExecutor, nil, nil
	}
	var workdir string
	var allowPaths []string
	if cfg != nil {
		workdir, allowPaths = cfg.FilesystemWorkDir, cfg.FilesystemAllowPaths
	}
	if strings.TrimSpace(workdir) == "" {
		return nil, nil, errors.New("resolve filesystem scope: workdir must be supplied by the CLI host")
	}
	if s == nil || s.runtimeService == nil {
		return nil, nil, errors.New("resolve tools: runtime service is nil")
	}
	selections := make([]runtimeTools.ToolSelection, 0)
	execPolicy := runtimeTools.ExecPolicy{}
	if cfg != nil {
		selections = make([]runtimeTools.ToolSelection, 0, len(cfg.Tools.List))
		for _, entry := range cfg.Tools.List {
			selections = append(selections, runtimeTools.ToolSelection{ID: entry.ID, Enabled: entry.Enabled})
		}
		execPolicy = runtimeTools.ExecPolicy{
			EnableDenyPatterns: cfg.Tools.Exec.EnableDenyPatterns,
			CustomDenyPatterns: append([]string(nil), cfg.Tools.Exec.CustomDenyPatterns...),
			Configured:         true,
		}
	}
	capability, err := s.runtimeService.Resolve(context.Background(), runtimeTools.Request{
		WorkDir:              workdir,
		AllowPaths:           append([]string(nil), allowPaths...),
		Selections:           selections,
		Exec:                 execPolicy,
		DisplaySurface:       s.displaySurface,
		DisplayCapability:    display,
		DisplayCapabilitySet: true,
		UseDefaultTool:       true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("resolve tools: %w", err)
	}
	return capability.Executor, capability.Definitions, nil
}

func resolveDisplayCapability(cfg *config.Config, probe cliTools.DisplayCapabilityProbe) cliTools.DisplayCapability {
	if cfg != nil && !cfg.Tools.ToolEnabled("show") && !cfg.Tools.ToolEnabled("mouse") {
		return cliTools.UnavailableDisplayCapability("display-dependent tools are disabled by configuration")
	}
	if probe == nil {
		return cliTools.UnavailableDisplayCapability("display capability probe is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), displayCapabilityProbeTimeout)
	defer cancel()
	type result struct {
		capability cliTools.DisplayCapability
		err        error
	}
	ch := make(chan result, 1)
	go func() { capability, err := probe.Probe(ctx); ch <- result{capability, err} }()
	select {
	case value := <-ch:
		capability := value.capability
		if value.err != nil && capability.State == "" {
			return cliTools.UnavailableDisplayCapability("display capability probe failed")
		}
		if !capability.Usable() {
			if capability.Reason == "" {
				capability.Reason = "no usable display or capture surface was proven"
			}
			if capability.State == "" {
				capability.State = cliTools.DisplayCapabilityUnavailable
			}
			capability.Available = false
			return capability
		}
		capability.State, capability.Available = cliTools.DisplayCapabilityUsable, true
		return capability
	case <-ctx.Done():
		return cliTools.UnavailableDisplayCapability("display capability probe timed out")
	}
}

func initialBrowserState(browser serviceTools.BrowserCapability) webmcp.BrowserCapabilityState {
	if browser.Status != nil {
		if status := browser.Status(); status.BrowserCapabilityState != "" {
			return status.BrowserCapabilityState
		}
	}
	return webmcp.BrowserCapabilityInitializing
}

func connectedUnselected(browser serviceTools.BrowserCapability, err error) bool {
	if browser.Status == nil || err == nil {
		return false
	}
	status := browser.Status()
	if status.BrowserCapabilityState != webmcp.BrowserCapabilityConnectedUnselected {
		return false
	}
	var classified *webmcp.ClassifiedError
	return errors.As(err, &classified) && classified != nil && classified.Code == webmcp.ErrorStaleSelection && classified.Details != nil && classified.Details["reason"] == "selection_not_connected" && classified.Details["browser_id"] == "" && classified.Details["target_id"] == ""
}

func retryableCatalogDeadline(err error) bool {
	var classified *webmcp.ClassifiedError
	return errors.As(err, &classified) && classified != nil && classified.Code == webmcp.ErrorBrowserProtocol && classified.Retryable && classified.Details != nil && classified.Details["reason_code"] == "page_tools_unverified" && classified.Details["reason"] == "deadline_exceeded"
}
