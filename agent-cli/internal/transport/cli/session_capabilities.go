package cli

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	servicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	cliTools "github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// SessionBrowserBrokerFactory is retained as the transport injection seam;
// capability composition itself lives in services/internal/tools.
type SessionBrowserBrokerFactory func(config.BrowserConfig) (webmcp.Broker, error)

type SessionDisplayCapability = cliTools.DisplayCapability

// Retained for deterministic display-admission timeout tests; resolution is
// owned by services/internal/tools.
const sessionDisplayCapabilityProbeTimeout = 3 * time.Second

func NewSessionToolCapabilitiesFactory(staticExecutor messages.ToolExecutor, brokerFactory SessionBrowserBrokerFactory) SessionToolCapabilitiesFactory {
	return NewSessionToolCapabilitiesFactoryWithDisplaySurface(staticExecutor, brokerFactory, nil)
}

// NewSessionToolCapabilitiesFactoryFromService adapts the injected service
// contract to the command's existing lifecycle value without constructing a
// registry or browser in the transport.
func NewSessionToolCapabilitiesFactoryFromService(resolver serviceTools.Service) SessionToolCapabilitiesFactory {
	return func(cfg *config.Config) (SessionToolCapabilities, error) {
		if resolver == nil {
			return SessionToolCapabilities{}, errors.New("session tool capability service is not configured")
		}
		capabilities, err := resolver.Resolve(cfg)
		if err != nil {
			return SessionToolCapabilities{}, err
		}
		return fromServiceToolCapabilities(capabilities), nil
	}
}

func NewSessionToolCapabilitiesFactoryWithDisplaySurface(staticExecutor messages.ToolExecutor, brokerFactory SessionBrowserBrokerFactory, displaySurface cliTools.DisplaySurface) SessionToolCapabilitiesFactory {
	if displaySurface == nil {
		displaySurface = cliTools.NewHostDisplaySurface()
	}
	return newSessionToolCapabilitiesFactory(staticExecutor, brokerFactory, displaySurface, displaySurface)
}

func NewSessionToolCapabilitiesFactoryWithDisplayProbe(staticExecutor messages.ToolExecutor, brokerFactory SessionBrowserBrokerFactory, displayProbe cliTools.DisplayCapabilityProbe) SessionToolCapabilitiesFactory {
	surface := cliTools.NewHostDisplaySurface()
	if displayProbe == nil {
		displayProbe = surface
	}
	return newSessionToolCapabilitiesFactory(staticExecutor, brokerFactory, surface, displayProbe)
}

func newSessionToolCapabilitiesFactory(staticExecutor messages.ToolExecutor, brokerFactory SessionBrowserBrokerFactory, displaySurface cliTools.DisplaySurface, displayProbe cliTools.DisplayCapabilityProbe) SessionToolCapabilitiesFactory {
	browserFactory := func(browser config.BrowserConfig, configDir string) (serviceTools.BrowserCapability, error) {
		var broker webmcp.Broker
		var err error
		if brokerFactory != nil {
			broker, err = brokerFactory(browser)
		} else {
			broker, err = newSessionBrowserBrokerWithConfigDir(browser, configDir)
		}
		return serviceBrowserCapability(broker), err
	}
	resolver := servicewire.NewToolCapabilitiesService(staticExecutor, browserFactory, displaySurface, displayProbe, runtimeToolsWire.NewService())
	return NewSessionToolCapabilitiesFactoryFromService(resolver)
}

func serviceBrowserCapability(broker webmcp.Broker) serviceTools.BrowserCapability {
	result := serviceTools.BrowserCapability{Broker: broker}
	if broker == nil {
		return result
	}
	if initializer, ok := broker.(SessionCapabilityInitializer); ok {
		result.Initialize = initializer.InitializeSession
		result.Status = func() serviceTools.CapabilityStatus {
			status := initializer.SessionCapabilityStatus()
			return serviceTools.CapabilityStatus{State: serviceTools.CapabilityState(status.State), Err: status.Err, BrowserCapabilityState: status.BrowserCapabilityState}
		}
	} else {
		result.Status = func() serviceTools.CapabilityStatus {
			return serviceTools.CapabilityStatus{BrowserCapabilityState: webmcp.BrowserCapabilityInitializing}
		}
	}
	result.BrowserWatch = broker.Watch
	if watcher, ok := broker.(webmcp.BrowserEventWatcher); ok {
		result.BrowserEventWatch = watcher.WatchBrowserEvents
	}
	result.Close = broker.Close
	return result
}

// NewSessionBrowserCapability adapts the low-level browser runtime to the
// injected services/tools capability seam. The composition root owns the
// runtime; the private service owns capability assembly and lifecycle.
func NewSessionBrowserCapability(broker webmcp.Broker) serviceTools.BrowserCapability {
	return serviceBrowserCapability(broker)
}

func fromServiceToolCapabilities(capabilities serviceTools.Capabilities) SessionToolCapabilities {
	var status func() SessionCapabilityStatus
	if capabilities.Status != nil {
		status = func() SessionCapabilityStatus {
			value := capabilities.Status()
			return SessionCapabilityStatus{State: SessionCapabilityState(value.State), Err: value.Err, BrowserCapabilityState: value.BrowserCapabilityState}
		}
	}
	return SessionToolCapabilities{
		Executor: capabilities.Executor, Definitions: capabilities.Definitions,
		BrowserCapabilityState:      capabilities.BrowserCapabilityState,
		DisplayCapability:           capabilities.DisplayCapability,
		RefreshDefinitions:          capabilities.RefreshDefinitions,
		RefreshDefinitionsWithError: capabilities.RefreshDefinitionsWithError,
		Initialize:                  capabilities.Initialize, Status: status,
		BrowserWatch: capabilities.BrowserWatch, BrowserEventWatch: capabilities.BrowserEventWatch,
		Close: capabilities.Close,
	}
}

// Resolve adapts the browser transport's capability factory to the injected
// tool service contract. Production composition injects its private service.
func (factory SessionToolCapabilitiesFactory) Resolve(cfg *config.Config) (serviceTools.Capabilities, error) {
	if factory == nil {
		return serviceTools.Capabilities{}, errors.New("session capability factory is required")
	}

	capabilities, err := factory(cfg)
	if err != nil {
		return serviceTools.Capabilities{}, err
	}
	return serviceTools.Capabilities{
		Executor: capabilities.Executor, Definitions: capabilities.Definitions,
		BrowserCapabilityState:      capabilities.BrowserCapabilityState,
		DisplayCapability:           capabilities.DisplayCapability,
		RefreshDefinitions:          capabilities.RefreshDefinitions,
		RefreshDefinitionsWithError: capabilities.RefreshDefinitionsWithError,
		Initialize:                  capabilities.Initialize,
		BrowserWatch:                capabilities.BrowserWatch, BrowserEventWatch: capabilities.BrowserEventWatch,
		Close: capabilities.Close,
		Status: func() serviceTools.CapabilityStatus {
			if capabilities.Status == nil {
				return serviceTools.CapabilityStatus{}
			}
			status := capabilities.Status()
			return serviceTools.CapabilityStatus{State: serviceTools.CapabilityState(status.State), Err: status.Err, BrowserCapabilityState: status.BrowserCapabilityState}
		},
	}, nil
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

// OpenTab preserves model-facing tab creation through the session lifecycle
// wrapper. Embedding the base Broker interface alone hides this optional
// capability even when the production broker and Chrome adapter support it.
func (b *sessionBrowserBroker) OpenTab(ctx context.Context, request webmcp.OpenTabRequest) (webmcp.PageContext, error) {
	if b == nil || b.Broker == nil {
		return webmcp.PageContext{}, errors.New("WebMCP broker is unavailable")
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	opener, ok := b.Broker.(webmcp.BrokerTabOpener)
	if !ok {
		return webmcp.PageContext{}, webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "The connected browser cannot open a new tab.", map[string]any{
			"phase":  "open_tab",
			"reason": "unsupported_operation",
		})
	}
	return opener.OpenTab(ctx, request)
}

// NavigateSelectedTab preserves target-scoped navigation through the session
// lifecycle wrapper. This keeps model requests on the same target, including
// when Chrome is actively mirroring that target to a Cast receiver.
func (b *sessionBrowserBroker) NavigateSelectedTab(ctx context.Context, targetURL string) (webmcp.PageContext, error) {
	if b == nil || b.Broker == nil {
		return webmcp.PageContext{}, errors.New("WebMCP broker is unavailable")
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	navigator, ok := b.Broker.(webmcp.BrokerTabNavigator)
	if !ok {
		return webmcp.PageContext{}, webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "The connected browser cannot navigate the selected tab.", map[string]any{
			"phase":  "navigate_tab",
			"reason": "unsupported_operation",
		})
	}
	return navigator.NavigateSelectedTab(ctx, targetURL)
}

// CreateTab preserves the unselected creation seam used by managed browser
// bootstrap to make an ordinary about:blank window visible.
func (b *sessionBrowserBroker) CreateTab(ctx context.Context, request webmcp.OpenTabRequest) (webmcp.Target, error) {
	if b == nil || b.Broker == nil {
		return webmcp.Target{}, errors.New("WebMCP broker is unavailable")
	}
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.Target{}, err
	}
	creator, ok := b.Broker.(webmcp.BrokerTabCreator)
	if !ok {
		return webmcp.Target{}, webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "The connected browser cannot open a new tab.", map[string]any{
			"phase":  "open_tab",
			"reason": "unsupported_operation",
		})
	}
	return creator.CreateTab(ctx, request)
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
	_ webmcp.BrokerTabOpener  = (*sessionBrowserBroker)(nil)
	_ webmcp.BrokerTabCreator = (*sessionBrowserBroker)(nil)
)
