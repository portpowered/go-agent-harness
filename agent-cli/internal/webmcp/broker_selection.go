package webmcp

import (
	"context"
	"errors"
	"time"
)

// waitForInitialCatalog gives the browser a bounded opportunity to deliver
// affirmative page-tool evidence triggered by WebMCP.enable. A loading
// document receives the separate first-attach allowance; a timeout is a
// diagnostic failure, not a session-lifecycle transition.
func (b *StatefulBroker) waitForInitialCatalog(ctx context.Context, selected *brokerSession) error {
	return b.waitForCatalog(ctx, selected, true)
}

func (b *StatefulBroker) waitForCatalog(ctx context.Context, selected *brokerSession, initial bool) error {
	if selected == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wait := b.catalogWaitDuration(selected, initial)
	timerFactory := TimerFactory(wallTimerFactory{})
	if b != nil && b.timers != nil {
		timerFactory = b.timers
	}
	timer := timerFactory.NewTimer(wait)
	defer timer.Stop()
	for {
		channels, done, err := b.catalogWaitStep(selected)
		if done {
			return err
		}
		select {
		case <-channels.signal:
			// A readiness signal can also be closed while a generation is
			// being fenced. Re-read the state instead of treating every close
			// as proof for the current document.
			continue
		case <-channels.update:
			// Reconcile invalid, removed, or generation-changing catalog
			// observations before deciding whether the wait is complete.
			continue
		case <-channels.loopDone:
			return b.catalogAfterSessionEnded(selected, wait)
		case <-ctx.Done():
			if failure := b.browserDisconnectObserved(selected, "catalog"); failure != nil {
				return failure
			}
			return ctx.Err()
		case <-timer.C():
			// Events already queued at the deadline win over the timer. This
			// final flush also makes a simultaneous late toolsAdded event
			// deterministic for callers racing the first retry.
			return b.catalogAfterDeadline(selected, wait)
		}
	}
}

func (b *StatefulBroker) catalogWaitDuration(selected *brokerSession, initial bool) time.Duration {
	wait := initialCatalogWait
	if b != nil && b.catalogWait > 0 {
		wait = b.catalogWait
	}
	loading := false
	if b != nil {
		b.mu.Lock()
		loading = selected.context.DocumentLoadingKnown && selected.context.DocumentLoading
		b.mu.Unlock()
	}
	if initial && b != nil && b.loadingCatalogWait > wait && loading {
		wait = b.loadingCatalogWait
	}
	return wait
}

// catalogWaitChannels are the wake-up sources for one catalog wait round.
type catalogWaitChannels struct {
	signal   chan struct{}
	update   chan struct{}
	loopDone chan struct{}
}

// catalogWaitStep reports whether the wait is already decided; otherwise it
// returns the channels that can change the decision.
func (b *StatefulBroker) catalogWaitStep(selected *brokerSession) (catalogWaitChannels, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected != selected || !selected.active || !selected.context.Connected {
		failure := sessionLifecycleFailure(selected)
		if failure == nil {
			failure = staleSelectionForSession(selected, "selection_not_connected")
		}
		return catalogWaitChannels{}, true, failure
	}
	if selected.context.CatalogReady {
		return catalogWaitChannels{}, true, nil
	}
	if selected.catalogError != nil {
		return catalogWaitChannels{}, true, catalogInvalidErrorLocked(selected)
	}
	return catalogWaitChannels{signal: selected.catalogSignal, update: selected.catalogUpdate, loopDone: selected.loopDone}, false, nil
}

// catalogSettlement is the state observed after a final catalog flush. When
// decided is false, lifecycleFailure carries the session failure, if any.
type catalogSettlement struct {
	decided          bool
	result           error
	lifecycleFailure error
}

// settleFlushedCatalog flushes queued events and reports whether they decide
// the wait.
func (b *StatefulBroker) settleFlushedCatalog(selected *brokerSession) catalogSettlement {
	b.flushSession(selected)
	b.mu.Lock()
	defer b.mu.Unlock()
	if selected.context.CatalogReady && selected.active && selected.context.Connected {
		return catalogSettlement{decided: true}
	}
	if err := catalogInvalidErrorLocked(selected); err != nil {
		return catalogSettlement{decided: true, result: err}
	}
	return catalogSettlement{lifecycleFailure: sessionLifecycleFailure(selected)}
}

func (b *StatefulBroker) catalogAfterSessionEnded(selected *brokerSession, wait time.Duration) error {
	settled := b.settleFlushedCatalog(selected)
	if settled.decided {
		return settled.result
	}
	failure := settled.lifecycleFailure
	if failure != nil {
		var classifiedErr *ClassifiedError
		if errors.As(failure, &classifiedErr) && classifiedErr != nil {
			if code := classifiedErr.Code; code == ErrorBrowserDisconnected || code == ErrorTargetDetached {
				return failure
			}
		}
	}
	return b.catalogEvidenceError(selected, "session_ended", wait)
}

func (b *StatefulBroker) catalogAfterDeadline(selected *brokerSession, wait time.Duration) error {
	settled := b.settleFlushedCatalog(selected)
	if settled.decided {
		return settled.result
	}
	failure := settled.lifecycleFailure
	if failure != nil {
		if lifecycle, ok := lifecycleClassifiedError(failure); ok {
			return lifecycle
		}
	}
	if failure := b.browserDisconnectObserved(selected, "catalog"); failure != nil {
		return failure
	}
	return b.catalogEvidenceError(selected, "deadline_exceeded", wait)
}

// browserDisconnectObserved closes the timeout/disconnect race at the
// catalog boundary. The session can know that its transport ended before the
// broker event loop has published the corresponding lifecycle event.
func (b *StatefulBroker) browserDisconnectObserved(selected *brokerSession, phase string) error {
	if b == nil || selected == nil || selected.session == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if selected.invalidatedCode != ErrorBrowserDisconnected {
		failure := sessionLifecycleFailure(selected)
		if classified, ok := lifecycleClassifiedError(failure); !ok || classified.Code != ErrorBrowserDisconnected {
			return nil
		}
	}
	if b.selected == selected && selected.active {
		b.invalidateSessionWithCodeLocked(selected, ErrorBrowserDisconnected, phase)
	}
	return browserDisconnectedErrorForSession(selected, phase, sessionLifecycleFailure(selected))
}

func (b *StatefulBroker) syncSessionReadiness(selected *brokerSession) {
	if selected == nil {
		return
	}
	page := selected.session.Context()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected != selected || !selected.active {
		return
	}
	// EnableWebMCP returned successfully, so this is domain support evidence.
	// It is deliberately recorded independently from page-tool/catalog
	// readiness, which still requires an affirmative page observation.
	selected.context.WebMCPDomainSupported = true
	selected.context.DocumentReadyState = page.DocumentReadyState
	selected.context.DocumentLoading = page.DocumentLoading
	selected.context.DocumentLoadingKnown = page.DocumentLoadingKnown
	if page.CatalogReady {
		b.markCatalogReadyLocked(selected, page.CatalogEvidence)
	}
	b.updateReadinessLocked(selected)
}

func (b *StatefulBroker) updateReadinessLocked(selected *brokerSession) {
	if selected == nil {
		return
	}
	selected.context.Ready = selected.active && selected.context.Connected &&
		selected.context.WebMCPDomainSupported && selected.context.CatalogReady
}

func (b *StatefulBroker) markCatalogReadyLocked(selected *brokerSession, evidence string) {
	if selected == nil {
		return
	}
	selected.context.WebMCPDomainSupported = true
	if !selected.context.CatalogReady {
		selected.context.CatalogReady = true
		selected.catalogEvidencePending = false
		if selected.catalogSignal != nil {
			close(selected.catalogSignal)
			selected.catalogSignal = nil
		}
	}
	if selected.context.CatalogEvidence == "" {
		selected.context.CatalogEvidence = evidence
	}
	signalCatalogUpdateLocked(selected)
	b.updateReadinessLocked(selected)
}

func signalCatalogUpdateLocked(selected *brokerSession) {
	if selected == nil {
		return
	}
	if selected.catalogUpdate == nil {
		selected.catalogUpdate = make(chan struct{})
		return
	}
	close(selected.catalogUpdate)
	selected.catalogUpdate = make(chan struct{})
}

func catalogInvalidErrorLocked(selected *brokerSession) error {
	if selected == nil || selected.catalogError == nil {
		return nil
	}
	return classified(ErrorBrowserProtocol, "the page catalog is invalid", map[string]any{
		"phase":       "catalog",
		"protocol":    "webmcp",
		"reason_code": "invalid_descriptor",
	}, selected.catalogError)
}

func (b *StatefulBroker) catalogEvidenceError(selected *brokerSession, reason string, wait time.Duration) error {
	if wait <= 0 {
		wait = initialCatalogWait
	}
	details := map[string]any{
		"phase":           "catalog",
		"reason_code":     "page_tools_unverified",
		"webmcp_domain":   "supported",
		"page_tools":      "unverified",
		"catalog":         "unverified",
		"deadline_ms":     int(wait / time.Millisecond),
		"evidence_needed": "affirmative page producer/catalog-ready observation",
		"reason":          reason,
	}
	if selected != nil {
		b.mu.Lock()
		details["browser_id"] = string(selected.context.Key.BrowserID)
		details["target_id"] = string(selected.context.Key.TargetID)
		details["generation"] = selected.context.Generation
		b.mu.Unlock()
	}
	err := classified(ErrorBrowserProtocol, "the WebMCP domain is supported, but the selected page did not provide affirmative page-tool catalog evidence before the diagnostic deadline", details, nil)
	if reason == "deadline_exceeded" {
		var classifiedErr *ClassifiedError
		if errors.As(err, &classifiedErr) {
			classifiedErr.Retryable = true
		}
	}
	return err
}

func isCatalogEvidenceError(err error) bool {
	var classifiedErr *ClassifiedError
	if !errors.As(err, &classifiedErr) || classifiedErr == nil || classifiedErr.Code != ErrorBrowserProtocol {
		return false
	}
	return classifiedErr.Details != nil && classifiedErr.Details["reason_code"] == "page_tools_unverified"
}

// Selected returns the current selection after reconciling any already
// queued lifecycle/catalog events.
func (b *StatefulBroker) Selected(ctx context.Context) (PageContext, error) {
	return b.SelectedWithRefresh(ctx, false)
}

// SelectedWithRefresh is an optional extension for webmcp_get_context.
func (b *StatefulBroker) SelectedWithRefresh(ctx context.Context, refresh bool) (PageContext, error) {
	if err := contextError(ctx); err != nil {
		return PageContext{}, err
	}
	if b == nil {
		return PageContext{}, ErrClosed
	}
	b.flushSelected()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return PageContext{}, ErrClosed
	}
	selected := b.selected
	b.mu.Unlock()
	if err := b.selectedStateError(selected, "lifecycle", "selection_not_connected"); err != nil {
		return PageContext{}, err
	}
	if refresh {
		if err := b.refreshSelectedCatalog(ctx, selected); err != nil {
			return PageContext{}, err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected != selected || !selected.active || !selected.context.Connected {
		return PageContext{}, selectionStateErrorLocked(selected, "lifecycle", "selection_changed")
	}
	return clonePageContext(selected.context), nil
}

// ListTools returns the current selected page catalog. Refresh re-enables the
// semantic catalog stream; the stable broker definitions remain elsewhere.
func (b *StatefulBroker) ListTools(ctx context.Context, options ListToolsOptions) (ToolCatalogSnapshot, error) {
	if err := contextError(ctx); err != nil {
		return ToolCatalogSnapshot{}, err
	}
	if b == nil {
		return ToolCatalogSnapshot{}, ErrClosed
	}
	b.flushSelected()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ToolCatalogSnapshot{}, ErrClosed
	}
	selected := b.selected
	b.mu.Unlock()
	if err := b.selectedStateError(selected, "lifecycle", "selection_not_connected"); err != nil {
		return ToolCatalogSnapshot{}, err
	}
	if options.Refresh {
		if err := b.refreshSelectedCatalog(ctx, selected); err != nil {
			return ToolCatalogSnapshot{}, err
		}
	}
	if err := b.waitForPendingCatalogEvidence(ctx, selected); err != nil {
		return ToolCatalogSnapshot{}, err
	}
	b.flushSession(selected)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.selected != selected || !selected.active || !selected.context.Connected {
		return ToolCatalogSnapshot{}, selectionStateErrorLocked(selected, "lifecycle", "selection_changed")
	}
	if selected.catalogError != nil {
		return ToolCatalogSnapshot{}, catalogInvalidErrorLocked(selected)
	}
	tools := filteredCatalogToolsLocked(selected, options)
	return ToolCatalogSnapshot{Context: clonePageContext(selected.context), Generation: selected.context.Generation, Tools: tools}, nil
}
