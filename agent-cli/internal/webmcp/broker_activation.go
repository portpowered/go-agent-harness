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
