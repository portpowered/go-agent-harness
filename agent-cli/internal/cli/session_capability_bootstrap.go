package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

// sessionSelectionReconnector is intentionally narrower than the production
// discovery service. It keeps older injected discovery fakes source- and
// behavior-compatible while allowing the production session to use the strict
// persisted-identity reconnect path.
type sessionSelectionReconnector interface {
	Reconnect(context.Context, discovery.ConnectionInputs, ...discovery.ReconnectOptions) (discovery.Selection, error)
}

type sessionPersistedSelectionLoader interface {
	LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error)
}

// sessionCapabilityBootstrap creates the one setup operation shared by every
// broker tool in a session. Endpoint verification happens even when there is
// no persisted selection, while a present record always takes the strict
// browser/target/origin/continuity reconnect path.
func sessionCapabilityBootstrap(browser config.BrowserConfig, service WebMCPDiscoveryService, broker webmcp.Broker) func(context.Context) error {
	reconnector, _ := service.(sessionSelectionReconnector)
	loader, _ := service.(sessionPersistedSelectionLoader)

	return func(ctx context.Context) error {
		if ctx == nil {
			ctx = context.Background()
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		selection := browser.Selection
		browserID := strings.TrimSpace(selection.Browser)
		targetID := strings.TrimSpace(selection.Tab)
		origin := strings.TrimSpace(selection.Origin)
		hasExplicitSelection := browserID != "" || targetID != "" || origin != ""

		// An explicitly named target is always reconciled exactly. The
		// reconnect service never substitutes a different target.
		if targetID != "" && reconnector != nil {
			selected, err := reconnector.Reconnect(ctx, productionDiscoveryInputs(browser), discovery.ReconnectOptions{
				BrowserID: browserID,
				TargetID:  targetID,
				Origin:    origin,
				Reason:    "session_bootstrap",
			})
			if err != nil {
				return sessionCapabilityError(err)
			}
			return sessionAdoptSelection(ctx, broker, selected, selection.ActivateTab)
		}

		// A configured browser/origin without a target only admits automatic
		// single-target selection when the operator explicitly requested it.
		if hasExplicitSelection && selection.AutoSelect == config.BrowserAutoSelectSingle && reconnector != nil {
			selected, err := reconnector.Reconnect(ctx, productionDiscoveryInputs(browser), discovery.ReconnectOptions{
				AutoSelect: discovery.AutoSelectSingle,
				BrowserID:  browserID,
				Origin:     origin,
				Reason:     "session_bootstrap",
			})
			if err != nil {
				return sessionCapabilityError(err)
			}
			return sessionAdoptSelection(ctx, broker, selected, selection.ActivateTab)
		}

		if !hasExplicitSelection {
			if loader != nil {
				_, present, err := loader.LoadPersistedSelection(ctx)
				if err != nil {
					return sessionCapabilityError(err)
				}
				if present {
					if reconnector == nil {
						return sessionCapabilityError(errors.New("strict persisted selection restore is unavailable"))
					}
					selected, err := reconnector.Reconnect(ctx, productionDiscoveryInputs(browser), discovery.ReconnectOptions{
						AutoSelect: discovery.AutoSelectPersisted,
						Reason:     "session_bootstrap",
					})
					if err != nil {
						return sessionCapabilityError(err)
					}
					return sessionAdoptSelection(ctx, broker, selected, selection.ActivateTab)
				}
			} else if selection.Persist && reconnector != nil {
				// Compatibility fakes may expose Reconnect without the optional
				// loader. A missing record is ordinary; every other failure is
				// retained as the session's classified failed state.
				selected, err := reconnector.Reconnect(ctx, productionDiscoveryInputs(browser), discovery.ReconnectOptions{
					AutoSelect: discovery.AutoSelectPersisted,
					Reason:     "session_bootstrap",
				})
				if err == nil {
					return sessionAdoptSelection(ctx, broker, selected, selection.ActivateTab)
				}
				if !sessionNoSelectionError(err) {
					return sessionCapabilityError(err)
				}
			}

			if selection.AutoSelect == config.BrowserAutoSelectSingle && reconnector != nil {
				selected, err := reconnector.Reconnect(ctx, productionDiscoveryInputs(browser), discovery.ReconnectOptions{
					AutoSelect: discovery.AutoSelectSingle,
					Reason:     "session_bootstrap",
				})
				if err != nil {
					return sessionCapabilityError(err)
				}
				return sessionAdoptSelection(ctx, broker, selected, selection.ActivateTab)
			}
		}

		return sessionVerifyEndpoint(ctx, browser, broker)
	}
}

func sessionVerifyEndpoint(ctx context.Context, browser config.BrowserConfig, broker webmcp.Broker) error {
	if broker == nil {
		return webmcp.ErrClosed
	}
	connection := browser.Connection
	_, err := broker.Discover(ctx, webmcp.DiscoverOptions{
		BrowserID:        webmcp.BrowserID(strings.TrimSpace(browser.Selection.Browser)),
		ExplicitOnly:     strings.TrimSpace(connection.CDPURL) != "" || strings.TrimSpace(connection.WSEndpoint) != "",
		AllowProcessScan: connection.AllowProcessScan,
		AllowRemoteCDP:   connection.AllowRemoteCDP,
	})
	return sessionCapabilityError(err)
}

func sessionAdoptSelection(ctx context.Context, broker webmcp.Broker, selected discovery.Selection, activate bool) error {
	if broker == nil {
		return webmcp.ErrClosed
	}
	if strings.TrimSpace(selected.BrowserID) == "" || strings.TrimSpace(selected.TargetID) == "" {
		return webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser target is no longer current", map[string]any{
			"browser_id":          strings.TrimSpace(selected.BrowserID),
			"target_id":           strings.TrimSpace(selected.TargetID),
			"selected_generation": selected.Generation,
			"reason":              "persisted_selection_incomplete",
		})
	}
	selector := webmcp.TargetSelector{
		BrowserID: webmcp.BrowserID(selected.BrowserID),
		TargetID:  webmcp.TargetID(selected.TargetID),
	}
	var (
		unused webmcp.PageContext
		err    error
	)
	if selectorWithOptions, ok := broker.(interface {
		SelectWithOptions(context.Context, webmcp.TargetSelector, webmcp.SelectOptions) (webmcp.PageContext, error)
	}); ok {
		unused, err = selectorWithOptions.SelectWithOptions(ctx, selector, webmcp.SelectOptions{Activate: activate})
	} else {
		unused, err = broker.Select(ctx, selector)
	}
	_ = unused
	return sessionCapabilityError(err)
}

func sessionNoSelectionError(err error) bool {
	var discoveryErr *discovery.DiscoveryError
	if errors.As(err, &discoveryErr) && discoveryErr != nil && discoveryErr.Code == discovery.CodeNoEligibleTab {
		return true
	}
	var classified *webmcp.ClassifiedError
	return errors.As(err, &classified) && classified != nil && classified.Code == webmcp.ErrorNoEligibleTab
}

func sessionCapabilityError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil {
		return err
	}
	if converted := productionDiscoveryError(err); converted != err {
		return converted
	}
	wrapped := webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "WebMCP session capability initialization failed", map[string]any{
		"phase": "session_bootstrap",
	})
	wrapped.Cause = err
	return wrapped
}

func (b *sessionBrowserBroker) InitializeSession(ctx context.Context) error {
	return b.ensureInitialized(ctx)
}

func (b *sessionBrowserBroker) SessionCapabilityStatus() SessionCapabilityStatus {
	if b == nil {
		return SessionCapabilityStatus{State: SessionCapabilityFailed, Err: webmcp.ErrClosed}
	}
	b.initMu.Lock()
	defer b.initMu.Unlock()
	if b.initState == "" {
		b.initState = SessionCapabilityInitializing
	}
	return SessionCapabilityStatus{State: b.initState, Err: b.initErr}
}

func (b *sessionBrowserBroker) ensureInitialized(ctx context.Context) error {
	if b == nil || b.Broker == nil {
		return webmcp.ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.initMu.Lock()
	if b.initDone == nil {
		b.initDone = make(chan struct{})
		b.initState = SessionCapabilityInitializing
	}
	done := b.initDone
	b.initMu.Unlock()

	b.initOnce.Do(func() {
		b.initMu.Lock()
		if b.initStarted {
			b.initMu.Unlock()
			return
		}
		b.initStarted = true
		if b.closed {
			b.initErr = webmcp.ErrClosed
			b.initState = SessionCapabilityFailed
			close(done)
			b.initMu.Unlock()
			return
		}
		initContext, cancel := context.WithCancel(ctx)
		b.initCancel = cancel
		bootstrap := b.bootstrap
		b.initMu.Unlock()
		go b.runInitialization(initContext, cancel, bootstrap)
	})

	select {
	case <-done:
		return b.initializationError()
	default:
	}
	select {
	case <-done:
		return b.initializationError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *sessionBrowserBroker) runInitialization(ctx context.Context, cancel context.CancelFunc, bootstrap func(context.Context) error) {
	var err error
	b.initMu.Lock()
	closed := b.closed
	b.initMu.Unlock()
	if !closed && bootstrap != nil {
		err = bootstrap(ctx)
	}
	b.initMu.Lock()
	if b.closed && err == nil {
		err = webmcp.ErrClosed
	}
	b.initErr = err
	if err == nil {
		b.initState = SessionCapabilityReady
	} else {
		b.initState = SessionCapabilityFailed
	}
	b.initCancel = nil
	close(b.initDone)
	b.initMu.Unlock()
	cancel()
}

func (b *sessionBrowserBroker) initializationError() error {
	b.initMu.Lock()
	defer b.initMu.Unlock()
	return b.initErr
}

func (b *sessionBrowserBroker) Discover(ctx context.Context, options webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	return b.Broker.Discover(ctx, options)
}

func (b *sessionBrowserBroker) ListTargets(ctx context.Context, selector webmcp.BrowserSelector) ([]webmcp.Target, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	return b.Broker.ListTargets(ctx, selector)
}

func (b *sessionBrowserBroker) Select(ctx context.Context, selector webmcp.TargetSelector) (webmcp.PageContext, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	return b.Broker.Select(ctx, selector)
}

func (b *sessionBrowserBroker) Selected(ctx context.Context) (webmcp.PageContext, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.PageContext{}, err
	}
	return b.Broker.Selected(ctx)
}

func (b *sessionBrowserBroker) ListTools(ctx context.Context, options webmcp.ListToolsOptions) (webmcp.ToolCatalogSnapshot, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.ToolCatalogSnapshot{}, err
	}
	return b.Broker.ListTools(ctx, options)
}

func (b *sessionBrowserBroker) Invoke(ctx context.Context, request webmcp.InvokeRequest) (webmcp.InvokeResult, error) {
	if err := b.ensureInitialized(ctx); err != nil {
		return webmcp.InvokeResult{}, err
	}
	return b.Broker.Invoke(ctx, request)
}

func (b *sessionBrowserBroker) Cancel(ctx context.Context, request webmcp.CancelRequest) error {
	if err := b.ensureInitialized(ctx); err != nil {
		return err
	}
	return b.Broker.Cancel(ctx, request)
}

func (b *sessionBrowserBroker) Watch(ctx context.Context) <-chan webmcp.BrokerEvent {
	if err := b.ensureInitialized(ctx); err != nil {
		closed := make(chan webmcp.BrokerEvent)
		close(closed)
		return closed
	}
	return b.Broker.Watch(ctx)
}

var _ SessionCapabilityInitializer = (*sessionBrowserBroker)(nil)
