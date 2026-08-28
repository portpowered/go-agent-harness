package webmcp

func (b *StatefulBroker) applyGenerationChangeLocked(selected *brokerSession, event BrowserEvent, reason string) {
	previous := selected.context.Generation
	next, applied := nextLifecycleGeneration(previous, event.PreviousGeneration, event.Generation)
	if !applied {
		return
	}
	b.advanceGenerationLocked(selected, next, reason)
	contextValue := selected.session.Context()
	if contextValue.Key.BrowserID == "" {
		contextValue.Key.BrowserID = selected.context.Key.BrowserID
	}
	if contextValue.Key.TargetID == "" {
		contextValue.Key.TargetID = selected.context.Key.TargetID
	}
	contextValue.Generation = selected.context.Generation
	contextValue.Connected = true
	contextValue.Ready = false
	if contextValue.SelectedAt.IsZero() {
		contextValue.SelectedAt = selected.context.SelectedAt
	}
	selected.context = contextValue
}

func (b *StatefulBroker) advanceGenerationLocked(selected *brokerSession, generation uint64, reason string) {
	if generation <= selected.context.Generation {
		return
	}
	previous := selected.context.Generation
	selected.context.Generation = generation
	b.terminalizeSessionInvocationsLocked(selected, ErrorPageNavigated, reason, previous)
	b.retireCatalogLocked(selected)
	// A descriptor validation failure belongs to the retired document. A fresh
	// generation gets an independent catalog validation result.
	selected.catalogError = nil
	selected.context.Ready = false
	b.emitLocked(BrokerEvent{Type: BrokerEventGenerationChanged, BrowserID: selected.context.Key.BrowserID, TargetID: selected.context.Key.TargetID, Generation: generation, Reason: reason})
}

// nextLifecycleGeneration validates one generation-bearing lifecycle event.
// An explicit generation must be strictly newer than the selected document;
// an optional previous-generation claim must match the broker's last applied
// generation. This makes duplicate, delayed, and out-of-order observations
// idempotent without ever reusing or decreasing a published generation.
func nextLifecycleGeneration(previous, expectedPrevious, requested uint64) (uint64, bool) {
	if expectedPrevious != 0 && expectedPrevious != previous {
		return previous, false
	}
	if requested != 0 {
		if requested <= previous {
			return previous, false
		}
		return requested, true
	}
	if previous == ^uint64(0) {
		return previous, false
	}
	return previous + 1, true
}
