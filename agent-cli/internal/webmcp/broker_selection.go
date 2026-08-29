package webmcp

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// waitForInitialCatalog gives the browser a bounded opportunity to deliver
// affirmative page-tool evidence triggered by WebMCP.enable. A timeout is a
// diagnostic failure, not a session-lifecycle transition: the caller may
// retry while this connected target continues consuming events.
func (b *StatefulBroker) waitForInitialCatalog(ctx context.Context, selected *brokerSession) error {
	if selected == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wait := initialCatalogWait
	if b != nil && b.catalogWait > 0 {
		wait = b.catalogWait
	}
	timerFactory := TimerFactory(wallTimerFactory{})
	if b != nil && b.timers != nil {
		timerFactory = b.timers
	}
	timer := timerFactory.NewTimer(wait)
	defer timer.Stop()
	for {
		b.mu.Lock()
		if b.selected != selected || !selected.active || !selected.context.Connected {
			failure := sessionLifecycleFailure(selected)
			if failure == nil {
				failure = staleSelectionForSession(selected, "selection_not_connected")
			}
			b.mu.Unlock()
			return failure
		}
		if selected.context.CatalogReady {
			b.mu.Unlock()
			return nil
		}
		if selected.catalogError != nil {
			err := catalogInvalidErrorLocked(selected)
			b.mu.Unlock()
			return err
		}
		signal := selected.catalogSignal
		update := selected.catalogUpdate
		loopDone := selected.loopDone
		b.mu.Unlock()

		select {
		case <-signal:
			// A readiness signal can also be closed while a generation is
			// being fenced. Re-read the state instead of treating every close
			// as proof for the current document.
			continue
		case <-update:
			// Reconcile invalid, removed, or generation-changing catalog
			// observations before deciding whether the wait is complete.
			continue
		case <-loopDone:
			b.flushSession(selected)
			b.mu.Lock()
			if selected.context.CatalogReady && selected.active && selected.context.Connected {
				b.mu.Unlock()
				return nil
			}
			if err := catalogInvalidErrorLocked(selected); err != nil {
				b.mu.Unlock()
				return err
			}
			failure := sessionLifecycleFailure(selected)
			b.mu.Unlock()
			if failure != nil {
				var classifiedErr *ClassifiedError
				if errors.As(failure, &classifiedErr) && classifiedErr != nil {
					switch classifiedErr.Code {
					case ErrorBrowserDisconnected, ErrorTargetDetached:
						return failure
					}
				}
			}
			return b.catalogEvidenceError(selected, "session_ended")
		case <-ctx.Done():
			if failure := b.browserDisconnectObserved(selected, "catalog"); failure != nil {
				return failure
			}
			return ctx.Err()
		case <-timer.C():
			// Events already queued at the deadline win over the timer. This
			// final flush also makes a simultaneous late toolsAdded event
			// deterministic for callers racing the first retry.
			b.flushSession(selected)
			b.mu.Lock()
			if selected.context.CatalogReady && selected.active && selected.context.Connected {
				b.mu.Unlock()
				return nil
			}
			if err := catalogInvalidErrorLocked(selected); err != nil {
				b.mu.Unlock()
				return err
			}
			failure := sessionLifecycleFailure(selected)
			b.mu.Unlock()
			if failure != nil {
				if lifecycle, ok := lifecycleClassifiedError(failure); ok {
					return lifecycle
				}
			}
			if failure := b.browserDisconnectObserved(selected, "catalog"); failure != nil {
				return failure
			}
			return b.catalogEvidenceError(selected, "deadline_exceeded")
		}
	}
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

func (b *StatefulBroker) catalogEvidenceError(selected *brokerSession, reason string) error {
	details := map[string]any{
		"phase":           "catalog",
		"reason_code":     "page_tools_unverified",
		"webmcp_domain":   "supported",
		"page_tools":      "unverified",
		"catalog":         "unverified",
		"deadline_ms":     int(b.catalogWait / time.Millisecond),
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
		if err := selected.session.EnableWebMCP(ctx); err != nil {
			if failure := b.promoteBrowserLoss(selected, TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err); failure != nil {
				return PageContext{}, failure
			}
			return PageContext{}, targetAttachError(TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err)
		}
		b.flushSession(selected)
		b.syncSessionReadiness(selected)
		if err := b.waitForInitialCatalog(ctx, selected); err != nil {
			if failure := b.promoteBrowserLoss(selected, TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err); failure != nil {
				return PageContext{}, failure
			}
			if isCatalogEvidenceError(err) {
				return PageContext{}, err
			}
			return PageContext{}, targetAttachError(TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err)
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
		if err := selected.session.EnableWebMCP(ctx); err != nil {
			if failure := b.promoteBrowserLoss(selected, TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err); failure != nil {
				return ToolCatalogSnapshot{}, failure
			}
			return ToolCatalogSnapshot{}, targetAttachError(TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err)
		}
		b.flushSession(selected)
		b.syncSessionReadiness(selected)
		if err := b.waitForInitialCatalog(ctx, selected); err != nil {
			if failure := b.promoteBrowserLoss(selected, TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err); failure != nil {
				return ToolCatalogSnapshot{}, failure
			}
			if isCatalogEvidenceError(err) {
				return ToolCatalogSnapshot{}, err
			}
			return ToolCatalogSnapshot{}, targetAttachError(TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "refresh", err)
		}
	}
	// A selection may have returned a retryable catalog deadline before the
	// page published its producer. Keep later list calls event-driven and
	// bounded instead of treating the current empty catalog as a success.
	if err := b.waitForInitialCatalog(ctx, selected); err != nil {
		if failure := b.promoteBrowserLoss(selected, TargetSelector{BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID}, "catalog", err); failure != nil {
			return ToolCatalogSnapshot{}, failure
		}
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
	tools := make([]ToolDescriptor, 0, len(selected.catalog))
	for _, descriptor := range selected.catalog {
		if options.NameContains != "" && !strings.Contains(descriptor.Name, options.NameContains) {
			continue
		}
		if options.FrameID != "" && descriptor.FrameID != options.FrameID {
			continue
		}
		tools = append(tools, cloneToolDescriptor(descriptor))
	}
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].FrameID != tools[j].FrameID {
			return tools[i].FrameID < tools[j].FrameID
		}
		return tools[i].Name < tools[j].Name
	})
	return ToolCatalogSnapshot{Context: clonePageContext(selected.context), Generation: selected.context.Generation, Tools: tools}, nil
}
