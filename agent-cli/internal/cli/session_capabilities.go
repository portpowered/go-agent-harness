package cli

import (
	"context"
	"errors"
	"fmt"
	"sync"

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
	return func(cfg *config.Config) (SessionToolCapabilities, error) {
		if cfg != nil {
			if err := cfg.ValidateBrowser(); err != nil {
				return SessionToolCapabilities{}, fmt.Errorf("resolve browser config: %w", err)
			}
		}
		_, isRegistryExecutor := staticExecutor.(*cliTools.RegistryExecutor)
		var (
			resolvedStaticExecutor messages.ToolExecutor
			staticDefinitions      []messages.ToolDefinition
		)
		if isRegistryExecutor || staticExecutor == nil {
			registry := cliTools.NewToolRegistryFromConfig(cfg)
			resolvedStaticExecutor = cliTools.NewRegistryExecutor(registry)
			staticDefinitions = registry.ToAgentLoopDefs()
		} else {
			resolvedStaticExecutor = staticExecutor
		}

		if cfg == nil || !cfg.Browser.BrowserBackendEnabled() {
			return SessionToolCapabilities{
				Executor:    resolvedStaticExecutor,
				Definitions: staticDefinitions,
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
		capabilityCoordinator := services.NewSessionCapabilityCoordinator(broker.Close)
		capabilities := SessionToolCapabilities{
			Executor:     surface.Executor,
			Definitions:  surface.Definitions,
			BrowserWatch: broker.Watch,
			Close:        capabilityCoordinator.Close,
		}
		if initializer, ok := broker.(SessionCapabilityInitializer); ok {
			capabilities.Initialize = initializer.InitializeSession
			capabilities.Status = initializer.SessionCapabilityStatus
		}
		return capabilities, nil
	}
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
	}
	broker.bootstrap = sessionCapabilityBootstrap(browser, runtime.Discovery, runtime.Broker)
	return broker, nil
}

type sessionBrowserBroker struct {
	webmcp.Broker
	closeRuntime func() error
	closeOnce    sync.Once
	closeErr     error

	bootstrap   func(context.Context) error
	initOnce    sync.Once
	initDone    chan struct{}
	initMu      sync.Mutex
	initStarted bool
	initState   SessionCapabilityState
	initErr     error
	initCancel  context.CancelFunc
	closed      bool
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
