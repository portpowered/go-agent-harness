package webmcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Lifecycle reasons recorded on retired sessions and invocations mirror the
// browser event names that caused the retirement.
const (
	lifecycleReasonSessionClosed  = string(EventSessionClosed)
	lifecycleReasonTargetDetached = string(EventTargetDetached)
)

func classifyOperation(descriptor ToolDescriptor) OperationClass {
	if descriptor.Annotations.ReadOnly == nil {
		return OperationUnknown
	}
	if *descriptor.Annotations.ReadOnly {
		return OperationReadOnly
	}
	return OperationMutating
}

func lifecycleInvocationErrorCode(reason string, fallback ErrorCode) ErrorCode {
	switch strings.ToLower(reason) {
	case "disconnect", "disconnected", "browser_disconnected":
		return ErrorBrowserDisconnected
	case "detach", "detached", lifecycleReasonTargetDetached:
		return ErrorTargetDetached
	default:
		return fallback
	}
}

func errorCodeFor(err error, fallback ErrorCode) ErrorCode {
	var classifiedError *ClassifiedError
	if errors.As(err, &classifiedError) && classifiedError != nil && IsKnownErrorCode(classifiedError.Code) {
		return classifiedError.Code
	}
	return fallback
}

func safePageErrorCode(code string) string {
	if code == "" {
		return ""
	}
	if len(code) > 64 {
		return code[:64]
	}
	for _, character := range code {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' && character != '.' {
			return "unknown"
		}
	}
	return code
}

func (b *StatefulBroker) dispatchQueuedInvocationWithLock(invocation *brokerInvocation) {
	selected := invocation.selected
	b.mu.Lock()
	if b.dispatchPreconditionFailedLocked(invocation) {
		b.mu.Unlock()
		return
	}
	handle := selected.handle
	session := selected.session
	descriptor := cloneToolDescriptor(invocation.invocation.Tool)
	ctx := invocation.ctx
	b.mu.Unlock()

	// The target check is repeated for every dequeued call. This prevents a
	// target disappearing while an earlier invocation occupied the lane.
	if b.dispatchTargetMissing(ctx, invocation, handle, descriptor) {
		return
	}

	b.mu.Lock()
	if b.dispatchPreconditionFailedLocked(invocation) {
		b.mu.Unlock()
		return
	}
	provisionalID := b.bindProvisionalBrowserIDLocked(invocation, session)
	b.mu.Unlock()

	id, invokeErr := invokeWebMCP(ctx, session, invocation.invocation.ID, descriptor.FrameID, descriptor.Name, cloneJSON(invocation.invocation.Arguments))

	b.mu.Lock()
	b.completeDispatchLocked(invocation, session, provisionalID, id, invokeErr)
	b.mu.Unlock()
}

// dispatchPreconditionFailedLocked reports and terminalizes an invocation
// which can no longer be dispatched. It returns true when dispatch must stop.
func (b *StatefulBroker) dispatchPreconditionFailedLocked(invocation *brokerInvocation) bool {
	selected := invocation.selected
	if invocation.terminalized {
		var dispatchErr error
		if ErrorCode(invocation.finalResult.ErrorCode) == ErrorBrowserDisconnected {
			dispatchErr = browserDisconnectedErrorForSession(selected, "list_targets", sessionLifecycleFailure(selected))
		}
		b.reportDispatchLocked(invocation, invocation.finalResult, dispatchErr)
		return true
	}
	if b.closed || b.selected != selected || !selected.active || !selected.context.Connected {
		err := selectionStateErrorLocked(selected, "lifecycle", "selection_changed_before_dispatch")
		result := invocationFailureResultForError(invocation, err, ErrorStaleSelection)
		b.reportDispatchLocked(invocation, result, err)
		b.finishInvocationLocked(invocation, result)
		return true
	}
	record, ok := b.refs[invocation.invocation.Tool.Ref]
	if !ok || !refCurrentLocked(selected, record) {
		err := staleToolRefError(invocation.invocation.Tool.Ref, selected.context.Generation)
		result := invocationFailureResult(invocation, InvocationError, ErrorStaleToolRef, nil)
		b.reportDispatchLocked(invocation, result, err)
		b.finishInvocationLocked(invocation, result)
		return true
	}
	return false
}

// dispatchTargetMissing verifies the target is still listed by its browser.
// It terminalizes the invocation and returns true when it is not.
func (b *StatefulBroker) dispatchTargetMissing(ctx context.Context, invocation *brokerInvocation, handle BrowserHandle, descriptor ToolDescriptor) bool {
	targets, err := handle.ListTargets(ctx)
	if err == nil && targetPresent(targets, descriptor.TargetID) {
		return false
	}
	selected := invocation.selected
	failure := err
	if failure == nil {
		failure = staleSelectionError(descriptor.BrowserID, descriptor.TargetID, descriptor.Generation, "target_not_current")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	failure = reconcileTargetLossLocked(invocation, failure)
	if b.selected == selected && (isBrowserEndpointLossError(failure) || isBrowserDisconnectedTransportError(failure)) {
		if promoted := b.browserDisconnectedLocked(selected, "list_targets", failure); promoted != nil {
			failure = promoted
		}
	}
	result := invocationFailureResultForError(invocation, failure, ErrorStaleSelection)
	b.reportDispatchLocked(invocation, result, failure)
	b.finishInvocationLocked(invocation, result)
	return true
}

// bindProvisionalBrowserIDLocked binds broker-owned replay IDs before the
// target can synchronously publish terminal events. Opaque production IDs
// retain the post-return path in completeDispatchLocked.
func (b *StatefulBroker) bindProvisionalBrowserIDLocked(invocation *brokerInvocation, session TargetSession) InvocationID {
	if _, deterministic := session.(targetSessionInvokerWithID); !deterministic {
		return ""
	}
	candidateID := invocation.invocation.ID
	if _, externallyObserved := invocation.selected.observedInvocations[candidateID]; externallyObserved {
		return ""
	}
	invocation.browserID = candidateID
	b.browserInvocations[candidateID] = invocation
	return candidateID
}

func (b *StatefulBroker) completeDispatchLocked(invocation *brokerInvocation, session TargetSession, provisionalID, id InvocationID, invokeErr error) {
	invokeErr = reconcileTargetLossLocked(invocation, invokeErr)
	if id == "" {
		b.failUnassignedDispatchLocked(invocation, session, provisionalID, invokeErr)
		return
	}
	if invocation.terminalized {
		if provisionalID != "" && provisionalID != id {
			delete(b.browserInvocations, provisionalID)
		}
		invocation.browserID = id
		invocation.finalResult.BrowserInvocationID = id
		b.recordBrowserTerminalIDLocked(id)
		b.takeEarlyTerminalLocked(id, 0)
		b.rebindTerminalInvocationLocked(invocation)
		b.reportDispatchLocked(invocation, invocation.finalResult, nil)
		return
	}
	if provisionalID != "" && provisionalID != id {
		delete(b.browserInvocations, provisionalID)
		invocation.browserID = ""
	}
	if err := b.dispatchCorrelationConflictLocked(invocation, id); err != nil {
		result := invocationFailureResult(invocation, InvocationError, ErrorInvocationFailed, map[string]any{
			"invocation_id":       string(id),
			"phase":               "correlation",
			"side_effect_unknown": true,
		})
		b.reportDispatchLocked(invocation, result, err)
		b.finishInvocationLocked(invocation, result)
		return
	}
	b.markDispatchedLocked(invocation, id, invokeErr)
}

func (b *StatefulBroker) failUnassignedDispatchLocked(invocation *brokerInvocation, session TargetSession, provisionalID InvocationID, invokeErr error) {
	selected := invocation.selected
	if provisionalID != "" {
		delete(b.browserInvocations, provisionalID)
		invocation.browserID = ""
	}
	if b.selected == selected && (isBrowserEndpointLossError(invokeErr) || isBrowserDisconnectedTransportError(session.Err())) {
		if failure := b.browserDisconnectedLocked(selected, "invoke", invokeErr); failure != nil {
			invokeErr = failure
		}
	}
	result := invocationFailureResultForError(invocation, invokeErr, ErrorInvocationFailed)
	b.reportDispatchLocked(invocation, result, invokeErr)
	b.finishInvocationLocked(invocation, result)
}

func (b *StatefulBroker) dispatchCorrelationConflictLocked(invocation *brokerInvocation, id InvocationID) error {
	if existing, ok := b.browserInvocations[id]; ok && existing != invocation {
		return fmt.Errorf("webmcp: duplicate target invocation ID %q", id)
	}
	if _, observed := invocation.selected.observedInvocations[id]; observed {
		return fmt.Errorf("webmcp: target invocation ID %q is still observed by another client", id)
	}
	if _, terminal := b.browserTerminalSeen[id]; terminal {
		return fmt.Errorf("webmcp: reused terminal target invocation ID %q", id)
	}
	return nil
}

func (b *StatefulBroker) markDispatchedLocked(invocation *brokerInvocation, id InvocationID, invokeErr error) {
	invocation.browserID = id

	invocation.invocation.State = InvocationDispatched
	invocation.invocation.DispatchedAt = b.clock.Now()
	b.browserInvocations[id] = invocation
	// The queued admission event identifies the broker invocation before the
	// browser call starts. Publish the same identity again at the authoritative
	// dispatch transition so session-scoped consumers can act inside the real
	// browser invocation window rather than polling broker state or waiting for
	// a terminal response.
	b.emitLocked(BrokerEvent{
		Type:         BrokerEventInvocationCreated,
		At:           invocation.invocation.DispatchedAt,
		BrowserID:    invocation.invocation.Tool.BrowserID,
		TargetID:     invocation.invocation.Tool.TargetID,
		Generation:   invocation.invocation.Tool.Generation,
		InvocationID: invocation.invocation.ID,
		ToolRef:      invocation.invocation.Tool.Ref,
		ToolName:     invocation.invocation.Tool.Name,
		State:        InvocationDispatched,
		Reason:       "dispatched",
	})
	result := InvokeResult{InvocationID: invocation.invocation.ID, BrowserInvocationID: id, State: InvocationDispatched}
	if _, ok := b.takeEarlyTerminalLocked(id, 0); ok {
		// An early response has no invocation provenance. It may be a stale
		// response from a previous call which happened to reuse this protocol
		// ID, so it must fail closed instead of becoming a successful result.
		b.finishInvocationLocked(invocation, freshnessFailureResult(invocation, "terminal_provenance", "terminal_before_invocation", true))
		return
	}
	if invokeErr != nil && !invocation.terminalized {
		result = invocationFailureResultForError(invocation, invokeErr, ErrorInvocationFailed)
		b.reportDispatchLocked(invocation, result, invokeErr)
		b.finishInvocationLocked(invocation, result)
		return
	}
	b.reportDispatchLocked(invocation, result, nil)
}

func closeInvocationQueueLocked(selected *brokerSession) {
	if selected == nil || selected.queueClosed {
		return
	}
	selected.queueClosed = true
	selected.queue = nil // queued entries were terminalized before lane close.
	close(selected.queueStop)
	signalInvocationQueueLocked(selected)
}

func removeQueuedInvocationLocked(selected *brokerSession, target *brokerInvocation) bool {
	if selected == nil || target == nil {
		return false
	}
	for i, invocation := range selected.queue {
		if invocation != target {
			continue
		}
		copy(selected.queue[i:], selected.queue[i+1:])
		selected.queue[len(selected.queue)-1] = nil
		selected.queue = selected.queue[:len(selected.queue)-1]
		signalInvocationQueueLocked(selected)
		return true
	}
	return false
}

func signalInvocationQueueLocked(selected *brokerSession) {
	if selected == nil {
		return
	}
	select {
	case selected.queueWake <- struct{}{}:
	default:
	}
}

// Invocation returns a defensive snapshot of an active or recently terminal
// call. The terminal cache is bounded and exists only to close the race
// between browser completion and a consumer asking for the result.
func (b *StatefulBroker) Invocation(id InvocationID) (Invocation, bool) {
	if b == nil {
		return Invocation{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if invocation, ok := b.invocations[id]; ok {
		return cloneInvocation(invocation.invocation), true
	}
	if terminal, ok := b.terminalResults[id]; ok {
		return cloneInvocation(terminal.invocation), true
	}
	return Invocation{}, false
}

// PendingInvocations returns active registry entries in admission order. It
// is an observation seam for tests and diagnostics, not a provider API.
func (b *StatefulBroker) PendingInvocations() []Invocation {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entries := make([]*brokerInvocation, 0, len(b.invocations))
	for _, invocation := range b.invocations {
		entries = append(entries, invocation)
	}
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].admissionSeq < entries[j-1].admissionSeq; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	result := make([]Invocation, 0, len(entries))
	for _, invocation := range entries {
		result = append(result, cloneInvocation(invocation.invocation))
	}
	return result
}
