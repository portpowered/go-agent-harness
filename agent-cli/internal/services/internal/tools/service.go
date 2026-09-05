// Package tools contains the private session capability composition.
package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	sessioncapability "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/sessioncapability"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
}

var _ serviceTools.Service = (*Service)(nil)

func New(staticExecutor messages.ToolExecutor, brokerFactory serviceTools.BrowserFactory, displaySurface cliTools.DisplaySurface, displayProbe cliTools.DisplayCapabilityProbe) *Service {
	return &Service{staticExecutor: staticExecutor, brokerFactory: brokerFactory, displaySurface: displaySurface, displayProbe: displayProbe}
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
	if s.brokerFactory == nil {
		return serviceTools.Capabilities{}, errors.New("construct WebMCP broker: browser factory is nil")
	}
	stable := webmcpTools.NewBrokerToolSet(nil, cfg.Browser.Tools.WebCast).Definitions()
	if err := cliTools.ValidateToolDefinitionNamespaces(definitions, stable); err != nil {
		return serviceTools.Capabilities{}, fmt.Errorf("compose session tools: %w", err)
	}
	browser, err := s.brokerFactory(cfg.Browser, cfg.ConfigDir)
	if err != nil {
		return serviceTools.Capabilities{}, closeFailedBrowser(browser, fmt.Errorf("construct WebMCP broker: %w", err))
	}
	if browser.Broker == nil {
		return serviceTools.Capabilities{}, closeFailedBrowser(browser, errors.New("construct WebMCP broker: factory returned nil"))
	}
	brokerSet := webmcpTools.NewBrokerToolSet(browser.Broker, cfg.Browser.Tools.WebCast)
	surface, err := cliTools.ComposeToolSurface(resolvedStatic, definitions, brokerSet.Executor(), brokerSet.Definitions())
	if err != nil {
		return serviceTools.Capabilities{}, closeFailedBrowser(browser, fmt.Errorf("compose session tools: %w", err))
	}
	reserved := make([]string, 0, len(surface.Definitions)+2)
	for _, definition := range surface.Definitions {
		reserved = append(reserved, definition.Name)
	}
	reserved = append(reserved, cliTools.ScreenToolID, cliTools.HostDisplayToolID)
	brokerSet.SetReservedToolNames(reserved)
	refresh := func(ctx context.Context) ([]messages.ToolDefinition, error) {
		page, refreshErr := brokerSet.PageToolDefinitionsWithError(ctx)
		if refreshErr != nil {
			if connectedUnselected(browser, refreshErr) || retryableCatalogDeadline(refreshErr) {
				return append([]messages.ToolDefinition(nil), surface.Definitions...), nil
			}
			return nil, fmt.Errorf("refresh WebMCP page tools: %w", refreshErr)
		}
		result := append([]messages.ToolDefinition(nil), surface.Definitions...)
		return append(result, page...), nil
	}
	capabilities := serviceTools.Capabilities{
		Executor: surface.Executor, Definitions: surface.Definitions,
		BrowserCapabilityState: initialBrowserState(browser), DisplayCapability: displayCapability,
		RefreshDefinitions: func(ctx context.Context) []messages.ToolDefinition {
			result, err := refresh(ctx)
			if err != nil {
				return append([]messages.ToolDefinition(nil), surface.Definitions...)
			}
			return result
		},
		RefreshDefinitionsWithError: refresh,
		Initialize:                  browser.Initialize, Status: browser.Status,
		BrowserWatch: browser.BrowserWatch, BrowserEventWatch: browser.BrowserEventWatch,
	}
	if browser.Close != nil {
		coordinator := sessioncapability.NewSessionCapabilityCoordinator(browser.Close)
		capabilities.Close = coordinator.Close
	}
	return capabilities, nil
}

func closeFailedBrowser(browser serviceTools.BrowserCapability, primary error) error {
	if browser.Close == nil {
		return primary
	}
	if closeErr := sessioncapability.NewSessionCapabilityCoordinator(browser.Close).Close(); closeErr != nil {
		return errors.Join(primary, fmt.Errorf("close WebMCP broker: %w", closeErr))
	}
	return primary
}

func (s *Service) resolveStatic(cfg *config.Config, display cliTools.DisplayCapability) (messages.ToolExecutor, []messages.ToolDefinition, error) {
	if s != nil && s.staticExecutor != nil {
		if _, registry := s.staticExecutor.(*cliTools.RegistryExecutor); !registry {
			return s.staticExecutor, nil, nil
		}
	}
	var workdir string
	var allowPaths []string
	if cfg != nil {
		workdir, allowPaths = cfg.FilesystemWorkDir, cfg.FilesystemAllowPaths
	}
	policy, err := cliTools.ResolveFilesystemPolicy(workdir, allowPaths...)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve filesystem scope: %w", err)
	}
	registry := cliTools.NewToolRegistryFromConfigWithDisplayCapabilityAndPolicy(cfg, display, s.displaySurface, policy)
	return cliTools.NewRegistryExecutor(registry), registry.ToAgentLoopDefs(), nil
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
