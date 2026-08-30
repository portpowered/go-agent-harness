package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	webmcpTools "github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SessionBrowserBrokerFactory constructs the broker for one resolved browser
// configuration. It is called only after browser capability activation has
// been resolved, which keeps disabled sessions free of browser construction
// side effects and gives tests a neutral fake-broker seam.
type SessionBrowserBrokerFactory func(config.BrowserConfig) (webmcp.Broker, error)

// system_profiler can take roughly a second even on an otherwise healthy
// macOS desktop. Keep admission bounded, but allow the single metadata query
// to finish so a real display is not mistaken for a headless environment.
const sessionDisplayCapabilityProbeTimeout = 3 * time.Second

// SessionDisplayCapability is the CLI-facing alias for the display admission
// snapshot carried with a session capability set.
type SessionDisplayCapability = cliTools.DisplayCapability

// NewSessionToolCapabilitiesFactory returns the production session capability
// resolver. Static tools remain filtered by the resolved config. When browser
// tools are enabled, the resolver constructs the real broker tool set and
// preflights its namespace against the static surface before returning either
// definitions or an executor.
//
// A non-registry static executor is retained for disabled sessions for
// compatibility with injected callers. Such an executor has no inspectable
// definition namespace, so enabled browser composition advertises the broker
// surface and does not guess at hidden static names.
func NewSessionToolCapabilitiesFactory(
	staticExecutor messages.ToolExecutor,
	brokerFactory SessionBrowserBrokerFactory,
) SessionToolCapabilitiesFactory {
	return NewSessionToolCapabilitiesFactoryWithDisplaySurface(staticExecutor, brokerFactory, nil)
}

// NewSessionToolCapabilitiesFactoryWithDisplaySurface is the hermetic
// composition seam for display admission. The supplied surface owns both the
// side-effect-free probe and the later show capture, so tests can model a
// headless host, capability loss, and cancellation without the real desktop.
func NewSessionToolCapabilitiesFactoryWithDisplaySurface(
	staticExecutor messages.ToolExecutor,
	brokerFactory SessionBrowserBrokerFactory,
	displaySurface cliTools.DisplaySurface,
) SessionToolCapabilitiesFactory {
	if displaySurface == nil {
		displaySurface = cliTools.NewHostDisplaySurface()
	}
	return newSessionToolCapabilitiesFactory(staticExecutor, brokerFactory, displaySurface, displaySurface)
}

// NewSessionToolCapabilitiesFactoryWithDisplayProbe is useful when admission
// is supplied by an environment-specific capability service while capture is
// still owned by the host surface. A failed probe fails closed and simply
// removes display-dependent tools from this session's snapshot.
func NewSessionToolCapabilitiesFactoryWithDisplayProbe(
	staticExecutor messages.ToolExecutor,
	brokerFactory SessionBrowserBrokerFactory,
	displayProbe cliTools.DisplayCapabilityProbe,
) SessionToolCapabilitiesFactory {
	surface := cliTools.NewHostDisplaySurface()
	if displayProbe == nil {
		displayProbe = surface
	}
	return newSessionToolCapabilitiesFactory(staticExecutor, brokerFactory, surface, displayProbe)
}

func newSessionToolCapabilitiesFactory(
	staticExecutor messages.ToolExecutor,
	brokerFactory SessionBrowserBrokerFactory,
	displaySurface cliTools.DisplaySurface,
	displayProbe cliTools.DisplayCapabilityProbe,
) SessionToolCapabilitiesFactory {
	return func(cfg *config.Config) (SessionToolCapabilities, error) {
		if cfg != nil {
			if err := cfg.ValidateBrowser(); err != nil {
				return SessionToolCapabilities{}, fmt.Errorf("resolve browser config: %w", err)
			}
		}
		displayCapability := resolveSessionDisplayCapability(cfg, displayProbe)
		_, isRegistryExecutor := staticExecutor.(*cliTools.RegistryExecutor)
		var (
			resolvedStaticExecutor messages.ToolExecutor
			staticDefinitions      []messages.ToolDefinition
		)
		if isRegistryExecutor || staticExecutor == nil {
			registry := cliTools.NewToolRegistryFromConfigWithDisplayCapability(cfg, displayCapability, displaySurface)
			resolvedStaticExecutor = cliTools.NewRegistryExecutor(registry)
			staticDefinitions = registry.ToAgentLoopDefs()
		} else {
			resolvedStaticExecutor = staticExecutor
		}

		if cfg == nil || !cfg.Browser.BrowserBackendEnabled() {
			return SessionToolCapabilities{
				Executor:               resolvedStaticExecutor,
				Definitions:            staticDefinitions,
				BrowserCapabilityState: webmcp.BrowserCapabilityDisabled,
				DisplayCapability:      displayCapability,
			}, nil
		}

		// The broker definitions are stable and side-effect free. Check their
		// namespace before asking the injected factory to allocate or dial any
		// browser resource.
		stableBrokerDefinitions := webmcpTools.NewBrokerToolSet(nil).Definitions()
		if err := cliTools.ValidateToolDefinitionNamespaces(staticDefinitions, stableBrokerDefinitions); err != nil {
			return SessionToolCapabilities{}, fmt.Errorf("compose session tools: %w", err)
		}

		resolvedBrokerFactory := brokerFactory
		if resolvedBrokerFactory == nil {
			resolvedBrokerFactory = func(browser config.BrowserConfig) (webmcp.Broker, error) {
				return newSessionBrowserBrokerWithConfigDir(browser, cfg.ConfigDir)
			}
		}
		broker, err := resolvedBrokerFactory(cfg.Browser)
		if err != nil {
			return closeFailedBroker(broker, fmt.Errorf("construct WebMCP broker: %w", err))
		}
		if broker == nil {
			return SessionToolCapabilities{}, errors.New("construct WebMCP broker: broker factory returned nil")
		}

		brokerSet := webmcpTools.NewBrokerToolSet(broker)
		surface, err := cliTools.ComposeToolSurface(
			resolvedStaticExecutor,
			staticDefinitions,
			brokerSet.Executor(),
			brokerSet.Definitions(),
		)
		if err != nil {
			return closeFailedBroker(broker, fmt.Errorf("compose session tools: %w", err))
		}
		reservedNames := make([]string, 0, len(surface.Definitions))
		for _, definition := range surface.Definitions {
			reservedNames = append(reservedNames, definition.Name)
		}
		brokerSet.SetReservedToolNames(reservedNames)
		capabilityCoordinator := services.NewSessionCapabilityCoordinator(broker.Close)
		refreshDefinitions := func(ctx context.Context) ([]messages.ToolDefinition, error) {
			pageDefinitions, refreshErr := brokerSet.PageToolDefinitionsWithError(ctx)
			if refreshErr != nil {
				return nil, fmt.Errorf("refresh WebMCP page tools: %w", refreshErr)
			}
			refreshed := append([]messages.ToolDefinition(nil), surface.Definitions...)
			return append(refreshed, pageDefinitions...), nil
		}
		capabilities := SessionToolCapabilities{
			Executor:               surface.Executor,
			Definitions:            surface.Definitions,
			BrowserCapabilityState: sessionInitialBrowserCapabilityState(broker),
			DisplayCapability:      displayCapability,
			// After the capability bootstrap has connected the broker, the
			// connected page catalog is advertised as first-class session
			// tools alongside the composed surface. Page-tool calls resolve
			// against the live catalog at execution time, so the definition
			// snapshot taken here is a steering surface, not a routing table.
			RefreshDefinitions: func(ctx context.Context) []messages.ToolDefinition {
				refreshed, refreshErr := refreshDefinitions(ctx)
				if refreshErr != nil {
					// Preserve the historical best-effort helper contract for
					// callers that cannot return an error. The live session path
					// uses RefreshDefinitionsWithError instead.
					return append([]messages.ToolDefinition(nil), surface.Definitions...)
				}
				return refreshed
			},
			RefreshDefinitionsWithError: refreshDefinitions,
			BrowserWatch:                broker.Watch,
			Close:                       capabilityCoordinator.Close,
		}
		if initializer, ok := broker.(SessionCapabilityInitializer); ok {
			capabilities.Initialize = initializer.InitializeSession
			capabilities.Status = initializer.SessionCapabilityStatus
		}
		return capabilities, nil
	}
}

func resolveSessionDisplayCapability(cfg *config.Config, probe cliTools.DisplayCapabilityProbe) cliTools.DisplayCapability {
	if !sessionDisplayToolsEnabled(cfg) {
		return cliTools.UnavailableDisplayCapability("display-dependent tools are disabled by configuration")
	}
	if probe == nil {
		return cliTools.UnavailableDisplayCapability("display capability probe is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), sessionDisplayCapabilityProbeTimeout)
	defer cancel()
	type probeResult struct {
		capability cliTools.DisplayCapability
		err        error
	}
	resultCh := make(chan probeResult, 1)
	go func() {
		capability, err := probe.Probe(ctx)
		resultCh <- probeResult{capability: capability, err: err}
	}()

	select {
	case result := <-resultCh:
		capability := result.capability
		if result.err != nil && capability.State == "" {
			// The probe boundary failed without producing any typed
			// admission signal at all (for example, an injected probe
			// returning a bare error). There is nothing structural to
			// advertise, so fail closed exactly as before.
			return cliTools.UnavailableDisplayCapability("display capability probe failed")
		}
		if !capability.Usable() {
			// A non-usable result still carries a State that distinguishes
			// "structurally absent" (headless: State == Unavailable or
			// unset) from "structurally present but not currently
			// capturable" (most commonly macOS Screen Recording permission
			// denied: State == Denied). That distinction must survive this
			// normalization so registry gating (DisplayCapability.
			// Advertisable) can keep show/mouse advertised in the latter
			// case -- clobbering State to Unavailable here is exactly what
			// used to de-advertise show whenever permission was denied,
			// leaving the model with no tool to invoke and no way to relay
			// the invocation-time grant instructions.
			if capability.Reason == "" {
				capability.Reason = "no usable display or capture surface was proven"
			}
			if capability.State == "" {
				capability.State = cliTools.DisplayCapabilityUnavailable
			}
			capability.Available = false
			return capability
		}
		capability.State = cliTools.DisplayCapabilityUsable
		capability.Available = true
		return capability
	case <-ctx.Done():
		return cliTools.UnavailableDisplayCapability("display capability probe timed out")
	}
}

func sessionDisplayToolsEnabled(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	return cfg.Tools.ToolEnabled("show") || cfg.Tools.ToolEnabled("mouse")
}

// NewSessionBrowserBroker creates the production browser broker used by
// browser-enabled sessions. The runtime is request-scoped so session cleanup
// can retire both broker state and discovery resources through one idempotent
// close hook.
func NewSessionBrowserBroker(browser config.BrowserConfig) (webmcp.Broker, error) {
	return newSessionBrowserBrokerWithConfigDir(browser, "")
}

func newSessionBrowserBrokerWithConfigDir(browser config.BrowserConfig, configDir string) (webmcp.Broker, error) {
	return newSessionBrowserBrokerWithDoctorFactory(browser, NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionConfigDir(configDir),
		WithWebMCPProductionSelectionStoreFactory(func() any {
			return NewFileWebMCPSelectionStore(configDir)
		}),
	))
}

func newSessionBrowserBrokerWithDoctorFactory(browser config.BrowserConfig, factory WebMCPDoctorFactory) (webmcp.Broker, error) {
	if factory == nil {
		return nil, errors.New("construct WebMCP broker: doctor factory is nil")
	}
	runtime, err := factory(browser)
	if err != nil {
		return nil, err
	}
	if runtime.Broker == nil {
		if closeErr := closeWebMCPDoctorRuntime(runtime); closeErr != nil {
			return nil, errors.Join(webmcpRuntimeUnavailableError("session_runtime"), closeErr)
		}
		return nil, webmcpRuntimeUnavailableError("session_runtime")
	}
	broker := &sessionBrowserBroker{
		Broker:       runtime.Broker,
		closeRuntime: runtime.Close,
		initDone:     make(chan struct{}),
		initState:    SessionCapabilityInitializing,
		browserState: webmcp.BrowserCapabilityInitializing,
	}
	broker.bootstrap = sessionCapabilityBootstrapWithState(browser, runtime.Discovery, runtime.Broker, broker.setBrowserCapabilityState)
	return broker, nil
}

type sessionBrowserBroker struct {
	webmcp.Broker
	closeRuntime func() error
	closeOnce    sync.Once
	closeErr     error

	bootstrap    func(context.Context) error
	initOnce     sync.Once
	initDone     chan struct{}
	initMu       sync.Mutex
	initStarted  bool
	initState    SessionCapabilityState
	initErr      error
	initCancel   context.CancelFunc
	browserState webmcp.BrowserCapabilityState
	closed       bool
}

func (b *sessionBrowserBroker) Close() error {
	if b == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		b.initMu.Lock()
		if b.initDone == nil {
			b.initDone = make(chan struct{})
			b.initState = SessionCapabilityInitializing
		}
		b.closed = true
		cancel := b.initCancel
		started := b.initStarted
		done := b.initDone
		if !started {
			b.initStarted = true
			b.initErr = webmcp.ErrClosed
			b.initState = SessionCapabilityFailed
			b.browserState = webmcp.BrowserCapabilityDisconnected
			close(done)
		}
		b.initMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if started {
			<-done
		}
		b.closeErr = b.Broker.Close()
		if b.closeRuntime != nil {
			b.closeErr = errors.Join(b.closeErr, b.closeRuntime())
		}
	})
	return b.closeErr
}

// WaitInvocation forwards the terminal-result capability of the stateful
// production broker. Embedding webmcp.Broker alone would expose only the
// frozen base interface and make model-facing invoke calls return dispatch
// acknowledgements instead of completed page results.
func (b *sessionBrowserBroker) WaitInvocation(ctx context.Context, id webmcp.InvocationID) (webmcp.InvokeResult, error) {
	if b == nil || b.Broker == nil {
		return webmcp.InvokeResult{}, errors.New("WebMCP invocation waiter is unavailable")
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.InvokeResult{}, err
	}
	waiter, ok := b.Broker.(webmcp.InvocationWaiter)
	if !ok {
		return webmcp.InvokeResult{}, errors.New("WebMCP broker does not support terminal invocation results")
	}
	return waiter.WaitInvocation(ctx, id)
}

// CapturePageScreenshot forwards the optional page-capture capability through
// the session coordinator so initialization and cleanup remain owned by the
// same browser capability lifetime as the other broker tools.
func (b *sessionBrowserBroker) CapturePageScreenshot(ctx context.Context) (webmcp.PageScreenshot, error) {
	if b == nil || b.Broker == nil {
		return webmcp.PageScreenshot{}, errors.New("WebMCP broker is unavailable")
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.PageScreenshot{}, err
	}
	capturer, ok := b.Broker.(webmcp.PageScreenshotter)
	if !ok {
		return webmcp.PageScreenshot{}, webmcp.NewClassifiedError(
			webmcp.ErrorUnsupportedWebMCP,
			"the selected browser page does not support screenshot capture",
			map[string]any{"capability": webmcp.PageCaptureScreenshotMethod},
		)
	}
	return capturer.CapturePageScreenshot(ctx)
}

// SelectedWithRefresh preserves the production broker's refresh extension;
// older injected brokers retain the frozen Selected behavior.
func (b *sessionBrowserBroker) SelectedWithRefresh(ctx context.Context, refresh bool) (webmcp.PageContext, error) {
	if b == nil || b.Broker == nil {
		return webmcp.PageContext{}, errors.New("WebMCP broker is unavailable")
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	if refresher, ok := b.Broker.(interface {
		SelectedWithRefresh(context.Context, bool) (webmcp.PageContext, error)
	}); ok {
		return refresher.SelectedWithRefresh(ctx, refresh)
	}
	return b.Broker.Selected(ctx)
}

// SelectWithOptions preserves explicit target activation in the production
// broker while retaining compatibility with a base-interface-only delegate.
func (b *sessionBrowserBroker) SelectWithOptions(ctx context.Context, selector webmcp.TargetSelector, options webmcp.SelectOptions) (webmcp.PageContext, error) {
	if b == nil || b.Broker == nil {
		return webmcp.PageContext{}, errors.New("WebMCP broker is unavailable")
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	if selectorWithOptions, ok := b.Broker.(interface {
		SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
	}); ok {
		return selectorWithOptions.SelectWithOptions(ctx, selector, options)
	}
	return b.Broker.Select(ctx, selector)
}

// CancelDirect preserves the cross-process cancellation extension for the
// direct CLI callers that receive the session's broker value.
func (b *sessionBrowserBroker) CancelDirect(ctx context.Context, request webmcp.DirectCancelRequest) error {
	if b == nil || b.Broker == nil {
		return errors.New("WebMCP broker is unavailable")
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return err
	}
	if canceller, ok := b.Broker.(webmcp.DirectCanceller); ok {
		return canceller.CancelDirect(ctx, request)
	}
	return errors.New("WebMCP broker does not support direct cancellation")
}

var (
	_ webmcp.InvocationWaiter = (*sessionBrowserBroker)(nil)
	_ webmcp.DirectCanceller  = (*sessionBrowserBroker)(nil)
)

func closeFailedBroker(broker webmcp.Broker, primary error) (SessionToolCapabilities, error) {
	if broker == nil {
		return SessionToolCapabilities{}, primary
	}
	if closeErr := services.NewSessionCapabilityCoordinator(broker.Close).Close(); closeErr != nil {
		return SessionToolCapabilities{}, errors.Join(primary, fmt.Errorf("close WebMCP broker: %w", closeErr))
	}
	return SessionToolCapabilities{}, primary
}
