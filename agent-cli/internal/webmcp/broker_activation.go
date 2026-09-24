package webmcp

import "context"

// Activate performs only the explicit foreground operation for one exact
// browser/target pair. It is separate from SelectWithOptions because a
// direct activate command must report an activation failure, while selection
// treats foreground activation as best effort after the WebMCP session is
// attached and ready.
func (b *StatefulBroker) Activate(ctx context.Context, selector TargetSelector) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if b == nil {
		return ErrClosed
	}
	if selector.BrowserID == "" || selector.TargetID == "" {
		return staleSelectionError(selector.BrowserID, selector.TargetID, 0, "exact_browser_and_target_required")
	}

	b.flushSelected()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrClosed
	}
	selected := b.selected
	b.mu.Unlock()
	if selected != nil && selected.context.Key.BrowserID == selector.BrowserID && selected.context.Key.TargetID == selector.TargetID {
		if err := b.selectedStateError(selected, "activate", "selection_not_connected"); err != nil {
			return err
		}
		return b.activateExactTarget(ctx, selected.handle, selected, selector)
	}
	if selected != nil && selected.context.Key.BrowserID == selector.BrowserID {
		if err := b.selectedStateError(selected, "activate", "selection_not_connected"); err != nil {
			return err
		}
	}

	candidate, err := b.candidateFor(ctx, selector.BrowserID)
	if err != nil {
		if failure := b.promoteBrowserLoss(selected, selector, "discover", err); failure != nil {
			return failure
		}
		return err
	}
	handle, err := b.handleFor(ctx, candidate)
	if err != nil {
		if failure := b.promoteBrowserLoss(b.selectedForBrowser(candidate.ID), selector, "open", err); failure != nil {
			return failure
		}
		return err
	}
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		if failure := b.promoteBrowserLoss(b.selectedForBrowser(candidate.ID), selector, "list_targets", err); failure != nil {
			return failure
		}
		return targetAttachError(selector, "list_targets", err)
	}
	if _, ok := findTarget(targets, selector.TargetID); !ok {
		return staleSelectionError(selector.BrowserID, selector.TargetID, 0, "target_not_present")
	}
	return b.activateExactTarget(ctx, handle, b.selectedForBrowser(candidate.ID), selector)
}

func (b *StatefulBroker) activateExactTarget(ctx context.Context, handle BrowserHandle, selected *brokerSession, selector TargetSelector) error {
	if handle == nil {
		return targetAttachError(selector, "activate", ErrClosed)
	}
	if err := handle.Activate(ctx, selector.TargetID); err != nil {
		if failure := b.promoteActivationLoss(selected, selector, "activate", err); failure != nil {
			return failure
		}
		return targetAttachError(selector, "activate", err)
	}
	return nil
}

// AcquirePageFocus leases media readiness without changing the selected target.
func (b *StatefulBroker) AcquirePageFocus(ctx context.Context) (func(context.Context) error, error) {
	var release func(context.Context) error
	err := b.withSelectedSession(ctx, "acquire_page_focus", func(session TargetSession) error {
		controller, ok := session.(PageFocusLeaser)
		if !ok {
			return classified(ErrorBrowserProtocol, "selected page does not support bounded focus", map[string]any{"reason_code": "unsupported_operation"}, nil)
		}
		var err error
		release, err = controller.AcquirePageFocus(ctx)
		return err
	})
	// If selection changed after acquisition, the caller still owns cleanup.
	return release, err
}

var _ PageFocusLeaser = (*StatefulBroker)(nil)

// reuseSelection reports reused=true when selection is already decided: the
// exact selection is active (or failed its lifecycle checks), or the broker
// is closed. Otherwise it returns the current selection for loss promotion.
func (b *StatefulBroker) reuseSelection(ctx context.Context, selector TargetSelector, options SelectOptions) (*brokerSession, PageContext, bool, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, PageContext{}, true, ErrClosed
	}
	current := b.selected
	if current != nil && current.active &&
		current.context.Key.BrowserID == selector.BrowserID && current.context.Key.TargetID == selector.TargetID {
		handle := current.handle
		contextValue := clonePageContext(current.context)
		b.mu.Unlock()
		page, err := b.confirmActiveSelection(ctx, current, handle, contextValue, selector, options)
		return current, page, true, err
	}
	b.mu.Unlock()
	if current != nil && current.context.Key.BrowserID == selector.BrowserID {
		if err := b.selectedStateError(current, "selection", "selection_not_connected"); err != nil {
			return current, PageContext{}, true, err
		}
	}
	return current, PageContext{}, false, nil
}

func (b *StatefulBroker) confirmActiveSelection(ctx context.Context, current *brokerSession, handle BrowserHandle, contextValue PageContext, selector TargetSelector, options SelectOptions) (PageContext, error) {
	if err := b.selectedStateError(current, "lifecycle", "selection_not_connected"); err != nil {
		return PageContext{}, err
	}
	if !options.Activate {
		return contextValue, nil
	}
	if err := handle.Activate(ctx, selector.TargetID); err != nil {
		if classified, lifecycle := lifecycleClassifiedError(err); lifecycle && classified != nil {
			return PageContext{}, err
		}
		if failure := b.promoteActivationLoss(current, selector, "activate", err); failure != nil {
			return PageContext{}, failure
		}
		// Foreground activation is ancillary to the already-ready target
		// session. A live browser may reject the operation (notably in
		// headless mode) without making the exact WebMCP selection unusable.
	}
	if failure := b.selectedStateError(current, "activate", "selection_not_connected"); failure != nil {
		return PageContext{}, failure
	}
	return contextValue, nil
}

// selectionLoss prefers a promoted browser-loss classification over fallback.
func (b *StatefulBroker) selectionLoss(current *brokerSession, selector TargetSelector, phase string, err, fallback error) error {
	if failure := b.promoteBrowserLoss(current, selector, phase, err); failure != nil {
		return failure
	}
	return fallback
}

// attachSelection resolves and attaches the exact target, returning a session
// that has not yet been installed as the broker selection.
func (b *StatefulBroker) attachSelection(ctx context.Context, current *brokerSession, selector TargetSelector) (*brokerSession, error) {
	candidate, err := b.candidateFor(ctx, selector.BrowserID)
	if err != nil {
		return nil, b.selectionLoss(current, selector, "discover", err, err)
	}
	handle, err := b.handleFor(ctx, candidate)
	if err != nil {
		return nil, b.selectionLoss(current, selector, "open", err, err)
	}
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		return nil, b.selectionLoss(current, TargetSelector{BrowserID: selector.BrowserID}, "list_targets", err, targetAttachError(selector, "list_targets", err))
	}
	target, ok := findTarget(targets, selector.TargetID)
	if !ok {
		return nil, staleSelectionError(selector.BrowserID, selector.TargetID, 0, "target_not_present")
	}
	if target.BrowserID == "" {
		target.BrowserID = candidate.ID
	}
	session, err := handle.Attach(ctx, selector.TargetID, b.ownershipValue())
	if err != nil {
		return nil, b.selectionLoss(current, selector, "attach", err, targetAttachError(selector, "attach", err))
	}
	page := session.Context()
	page = normalizePageContext(page, candidate.ID, target)
	if page.SelectedAt.IsZero() {
		page.SelectedAt = b.clock.Now()
	}
	return newBrokerSession(handle, session, target, page), nil
}

func newBrokerSession(handle BrowserHandle, session TargetSession, target Target, page PageContext) *brokerSession {
	newSession := &brokerSession{
		handle:              handle,
		session:             session,
		target:              cloneTarget(target),
		context:             page,
		active:              true,
		catalog:             make(map[catalogKey]ToolDescriptor),
		flush:               make(chan chan struct{}),
		loopDone:            make(chan struct{}),
		queueWake:           make(chan struct{}, 1),
		queueStop:           make(chan struct{}),
		queueWorkerDone:     make(chan struct{}),
		observedInvocations: make(map[InvocationID]observedInvocation),
		directCancellations: make(map[InvocationID]*directCancellation),
		catalogSignal:       make(chan struct{}),
		catalogUpdate:       make(chan struct{}),
	}
	if page.CatalogReady {
		close(newSession.catalogSignal)
		newSession.catalogSignal = nil
	}
	return newSession
}

// installSelectionLocked retires the previous selection and installs the
// new session. The caller starts the new session's event loop and invocation
// queue before releasing the broker mutex, then closes the returned session.
func (b *StatefulBroker) installSelectionLocked(newSession *brokerSession) (*brokerSession, error) {
	if b.closed {
		return nil, ErrClosed
	}
	page := newSession.context
	old := b.selected
	if old != nil {
		b.retireSessionLocked(old, "target_switch")
	}
	b.selected = newSession
	b.emitLocked(BrokerEvent{Type: BrokerEventSelected, BrowserID: page.Key.BrowserID, TargetID: page.Key.TargetID, Generation: page.Generation, Reason: "selected"})
	return old, nil
}

// enableSelection enables WebMCP on the new session and waits for its
// initial catalog evidence.
func (b *StatefulBroker) enableSelection(ctx context.Context, newSession *brokerSession, selector TargetSelector) error {
	session := newSession.session
	if err := session.EnableWebMCP(ctx); err != nil {
		if failure := b.promoteBrowserLoss(newSession, selector, "enable_webmcp", err); failure != nil {
			discardCloseError(session.Close)
			return failure
		}
		b.invalidateSession(newSession, "enable_failed")
		discardCloseError(session.Close)
		return targetAttachError(selector, "enable_webmcp", err)
	}
	b.flushSession(newSession)
	b.syncSessionReadiness(newSession)
	if err := b.waitForInitialCatalog(ctx, newSession); err != nil {
		return b.initialCatalogFailure(newSession, selector, err)
	}
	return nil
}

func (b *StatefulBroker) initialCatalogFailure(newSession *brokerSession, selector TargetSelector, err error) error {
	session := newSession.session
	if failure := b.promoteBrowserLoss(newSession, selector, "catalog", err); failure != nil {
		discardCloseError(session.Close)
		return failure
	}
	// A catalog deadline is an operation result, not a lifecycle
	// transition. Keep the connected selection and its event consumer
	// alive so a later toolsAdded/catalog-ready event can recover it.
	if isCatalogEvidenceError(err) {
		b.mu.Lock()
		if b.selected == newSession && newSession.active {
			newSession.catalogEvidencePending = true
		}
		b.mu.Unlock()
		b.flushSession(newSession)
		return err
	}
	b.invalidateSession(newSession, "catalog_wait_canceled")
	discardCloseError(session.Close)
	return targetAttachError(selector, "catalog", err)
}

// confirmNewSelection verifies the new session survived initialization and
// applies an optional best-effort foreground activation.
func (b *StatefulBroker) confirmNewSelection(ctx context.Context, newSession *brokerSession, selector TargetSelector, options SelectOptions) (PageContext, error) {
	session := newSession.session
	b.flushSession(newSession)
	b.mu.Lock()
	if b.selected != newSession || !newSession.active || !newSession.context.Connected {
		failure := sessionLifecycleFailure(newSession)
		if failure == nil {
			failure = staleSelectionForSession(newSession, "selection_not_connected")
		}
		b.mu.Unlock()
		discardCloseError(session.Close)
		return PageContext{}, failure
	}
	b.updateReadinessLocked(newSession)
	page := clonePageContext(newSession.context)
	b.mu.Unlock()
	if !options.Activate {
		return page, nil
	}
	if err := newSession.handle.Activate(ctx, selector.TargetID); err != nil {
		if failure := b.promoteActivationLoss(newSession, selector, "activate", err); failure != nil {
			discardCloseError(session.Close)
			return PageContext{}, failure
		}
		// Foreground activation is best effort once exact attachment and
		// catalog readiness have succeeded. Check the session after the
		// operation so a concurrent target/browser loss still wins.
	}
	if failure := b.selectedStateError(newSession, "activate", "selection_not_connected"); failure != nil {
		discardCloseError(session.Close)
		return PageContext{}, failure
	}
	return page, nil
}
