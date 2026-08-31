package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
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
	return sessionCapabilityBootstrapWithState(browser, service, broker, nil)
}

func sessionCapabilityBootstrapWithState(browser config.BrowserConfig, service WebMCPDiscoveryService, broker webmcp.Broker, setState func(webmcp.BrowserCapabilityState)) func(context.Context) error {
	reconnector, _ := service.(sessionSelectionReconnector)
	loader, _ := service.(sessionPersistedSelectionLoader)
	mark := func(state webmcp.BrowserCapabilityState) {
		if setState != nil {
			setState(state)
		}
	}
	adoptSelection := func(ctx context.Context, selected discovery.Selection, activate bool) error {
		err := sessionAdoptSelection(ctx, broker, selected, activate)
		if err == nil {
			mark(webmcp.BrowserCapabilitySelected)
		}
		return err
	}

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
		if refBrowserID, refTargetID, composite := splitCompositeTargetRef(targetID); composite {
			// The tabs listing prints "browserID/targetID" references; accept
			// that exact token here the same way the direct commands do.
			if browserID != "" && browserID != refBrowserID {
				return webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the target reference names a different browser than the explicit browser selector", map[string]any{
					"browser_id":          normalizeDirectOpaqueID(browserID),
					"target_id":           normalizeDirectOpaqueID(refTargetID),
					"selected_generation": uint64(0),
					"reason":              "selector_browser_mismatch",
				})
			}
			browserID = refBrowserID
			targetID = refTargetID
		}
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
			return adoptSelection(ctx, selected, selection.ActivateTab)
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
				return sessionRecoverConnectedUnselected(ctx, browser, broker, err, mark)
			}
			return adoptSelection(ctx, selected, selection.ActivateTab)
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
					return adoptSelection(ctx, selected, selection.ActivateTab)
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
					return adoptSelection(ctx, selected, selection.ActivateTab)
				}
				if !sessionNoSelectionError(err) {
					return sessionCapabilityError(err)
				}
			}

			// Endpoint-free managed WebMCP sessions are the default browser
			// experience, so use the existing single-target reconnect semantics after
			// giving an explicitly persisted selection precedence. External browser
			// configurations retain their opt-in auto-select behavior.
			autoSelectSingle := selection.AutoSelect == config.BrowserAutoSelectSingle
			if browser.UsesManagedBrowser() && browser.BrowserBackendEnabled() {
				autoSelectSingle = true
			}
			if autoSelectSingle && reconnector != nil {
				selected, err := reconnector.Reconnect(ctx, productionDiscoveryInputs(browser), discovery.ReconnectOptions{
					AutoSelect: discovery.AutoSelectSingle,
					Reason:     "session_bootstrap",
				})
				if err != nil {
					// #312's recovery already subsumes the endpoint-usability
					// concern: it verifies the endpoint and marks the session
					// connected-but-unselected, so an exact model selector can
					// still resolve the target deterministically, and the model
					// never reads the browser as absent.
					return sessionRecoverConnectedUnselected(ctx, browser, broker, err, mark)
				}
				return adoptSelection(ctx, selected, selection.ActivateTab)
			}
		}

		if err := sessionVerifyEndpoint(ctx, browser, broker); err != nil {
			return err
		}
		mark(webmcp.BrowserCapabilityConnectedUnselected)
		return nil
	}
}

func sessionRecoverConnectedUnselected(ctx context.Context, browser config.BrowserConfig, broker webmcp.Broker, selectionErr error, mark func(webmcp.BrowserCapabilityState)) error {
	if !sessionRecoverableSelectionError(selectionErr) {
		return sessionCapabilityError(selectionErr)
	}
	if err := sessionVerifyEndpoint(ctx, browser, broker); err != nil {
		return err
	}
	if mark != nil {
		mark(webmcp.BrowserCapabilityConnectedUnselected)
	}
	return nil
}

func sessionRecoverableSelectionError(err error) bool {
	var discoveryErr *discovery.DiscoveryError
	if errors.As(err, &discoveryErr) && discoveryErr != nil {
		switch discoveryErr.Code {
		case discovery.CodeNoEligibleTab, discovery.CodeAmbiguousBrowser, discovery.CodeAmbiguousTab:
			return true
		}
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil {
		switch classified.Code {
		case webmcp.ErrorNoEligibleTab, webmcp.ErrorAmbiguousBrowser, webmcp.ErrorAmbiguousTab:
			return true
		}
	}
	return false
}

func sessionInitialBrowserCapabilityState(broker webmcp.Broker) webmcp.BrowserCapabilityState {
	if broker == nil {
		return webmcp.BrowserCapabilityUnavailable
	}
	if _, initializer := broker.(SessionCapabilityInitializer); initializer {
		return webmcp.BrowserCapabilityInitializing
	}
	selected, err := broker.Selected(context.Background())
	if err == nil && selected.Connected && selected.Key.BrowserID != "" && selected.Key.TargetID != "" {
		return webmcp.BrowserCapabilitySelected
	}
	if err != nil {
		return sessionBrowserCapabilityStateForError(err)
	}
	return webmcp.BrowserCapabilityConnectedUnselected
}

func sessionBrowserCapabilityStateForError(err error) webmcp.BrowserCapabilityState {
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil && classified.Code == webmcp.ErrorBrowserDisconnected {
		return webmcp.BrowserCapabilityDisconnected
	}
	var discoveryErr *discovery.DiscoveryError
	if errors.As(err, &discoveryErr) && discoveryErr != nil && discoveryErr.Code == discovery.CodeBrowserDisconnected {
		return webmcp.BrowserCapabilityDisconnected
	}
	return webmcp.BrowserCapabilityUnavailable
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
	// Selection attaches and starts the target event consumer before it waits
	// for affirmative page-tool evidence. A late catalog is therefore an
	// operation-level result: keep the exact connected selection usable for a
	// later model-facing list/retry instead of failing the whole session
	// capability bootstrap. Do not generalize this to every retryable error;
	// discovery, attachment, and lifecycle failures must still fail closed.
	if sessionRetryableCatalogDeadline(err) {
		return nil
	}
	return sessionCapabilityError(err)
}

func sessionRetryableCatalogDeadline(err error) bool {
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified == nil || classified.Code != webmcp.ErrorBrowserProtocol || !classified.Retryable {
		return false
	}
	if classified.Details == nil {
		return false
	}
	return classified.Details["reason_code"] == "page_tools_unverified" && classified.Details["reason"] == "deadline_exceeded"
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
	if converted := sessionManagedBrowserError(err); converted != nil {
		return converted
	}
	wrapped := webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "WebMCP session capability initialization failed", map[string]any{
		"phase": "session_bootstrap",
	})
	wrapped.Cause = err
	return wrapped
}

func sessionManagedBrowserError(err error) error {
	if err == nil {
		return nil
	}
	var launchErr *chrome.ManagedBrowserLaunchError
	if errors.As(err, &launchErr) && launchErr != nil {
		phase := sessionSafeLabel(launchErr.Phase, "startup")
		mode := sessionSafeLabel(launchErr.Mode, "unknown")
		message := launchErr.Error()
		var acquisitionErr *chrome.ManagedChromeAcquisitionError
		if errors.As(err, &acquisitionErr) && acquisitionErr != nil {
			message = fmt.Sprintf("managed WebMCP browser launch failed during %s in %s mode; %s", phase, mode, acquisitionErr.Error())
		}
		classified := webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, message, map[string]any{
			"phase":       phase,
			"mode":        mode,
			"remediation": sessionManagedBrowserRemediation(phase),
		})
		classified.Cause = err
		return classified
	}
	var lifecycleErr *chrome.ManagedBrowserLifecycleError
	if errors.As(err, &lifecycleErr) && lifecycleErr != nil {
		phase := sessionSafeLabel(lifecycleErr.Phase, "lifecycle")
		classified := webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, lifecycleErr.Error(), map[string]any{
			"phase":       phase,
			"remediation": sessionManagedBrowserRemediation(phase),
		})
		classified.Cause = err
		return classified
	}
	return nil
}

func sessionManagedBrowserRemediation(phase string) string {
	switch phase {
	case "configuration":
		return "fix the managed browser startup URL and retry"
	case "profile":
		return "make the agent config directory writable and retry"
	case "acquisition":
		return "install Chrome 151 or newer, or supply an explicit browser endpoint"
	case "port":
		return "retry so the agent can reserve a free loopback DevTools port"
	case "start":
		return "check that the qualified Chrome executable can start with an agent-owned profile"
	case "readiness", "startup":
		return "check that Chrome can publish a loopback DevTools endpoint and retry"
	default:
		return "retry the managed browser operation"
	}
}

func sessionSafeLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 32 {
		return fallback
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return fallback
	}
	return value
}

func (b *sessionBrowserBroker) InitializeSession(ctx context.Context) error {
	return b.ensureInitialized(ctx)
}

func (b *sessionBrowserBroker) setBrowserCapabilityState(state webmcp.BrowserCapabilityState) {
	if b == nil || state == "" {
		return
	}
	b.initMu.Lock()
	b.browserState = state
	b.initMu.Unlock()
}

func (b *sessionBrowserBroker) SessionCapabilityStatus() SessionCapabilityStatus {
	if b == nil {
		return SessionCapabilityStatus{
			State:                  SessionCapabilityFailed,
			Err:                    webmcp.ErrClosed,
			BrowserCapabilityState: webmcp.BrowserCapabilityDisconnected,
		}
	}
	b.initMu.Lock()
	defer b.initMu.Unlock()
	if b.initState == "" {
		b.initState = SessionCapabilityInitializing
	}
	return SessionCapabilityStatus{
		State:                  b.initState,
		Err:                    b.initErr,
		BrowserCapabilityState: b.browserState,
	}
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
		// The bootstrap context is also the parent of any request-scoped browser
		// handles opened while restoring the selection. Keep its values, but do
		// not make those long-lived handles children of the caller's cancellation
		// once initialization succeeds. A small bridge still cancels an in-flight
		// bootstrap when its caller goes away; broker Close owns the successful
		// browser lifetime afterward.
		initContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
		b.initCancel = cancel
		bootstrap := b.bootstrap
		b.initMu.Unlock()
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-done:
			}
		}()
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
	if b.closed {
		b.browserState = webmcp.BrowserCapabilityDisconnected
	} else if b.browserState == "" || b.browserState == webmcp.BrowserCapabilityInitializing {
		if err == nil {
			b.browserState = sessionInitialBrowserCapabilityState(b.Broker)
		} else {
			b.browserState = sessionBrowserCapabilityStateForError(err)
		}
	}
	if err == nil {
		b.initState = SessionCapabilityReady
	} else {
		b.initState = SessionCapabilityFailed
	}
	close(b.initDone)
	b.initMu.Unlock()
	// Do not cancel a successful initialization context here: the production
	// browser handle and selected target session may still be using it. Close
	// cancels this context after the provider/session lifecycle has finished.
	if err != nil {
		cancel()
	}
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

// WatchBrowserEvents forwards the adapter-owned semantic stream without
// making the recording observer a second consumer of TargetSession.Events.
func (b *sessionBrowserBroker) WatchBrowserEvents(ctx context.Context) <-chan webmcp.BrowserEvent {
	if b == nil || b.Broker == nil {
		return nil
	}
	watcher, ok := b.Broker.(webmcp.BrowserEventWatcher)
	if !ok {
		return nil
	}
	return watcher.WatchBrowserEvents(ctx)
}

var _ SessionCapabilityInitializer = (*sessionBrowserBroker)(nil)
