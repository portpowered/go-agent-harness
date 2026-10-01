package webmcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type targetCancellation struct {
	session TargetSession
	id      InvocationID
	done    chan struct{}
}

func (b *StatefulBroker) claimTargetCancellationLocked(invocation *brokerInvocation) *targetCancellation {
	if invocation == nil || invocation.cancelSent || !invocation.invocation.CancelRequested || invocation.browserID == "" {
		return nil
	}
	invocation.cancelSent = true
	invocation.cancelDone = make(chan struct{})
	return &targetCancellation{session: invocation.selected.session, id: invocation.browserID, done: invocation.cancelDone}
}

func (b *StatefulBroker) cancellationWaitLocked(invocation *brokerInvocation, action *targetCancellation) <-chan struct{} {
	if action != nil || invocation == nil || !invocation.cancelSent {
		return nil
	}
	return invocation.cancelDone
}

// performTargetCancellation sends the claimed browser cancel with ctx. Callers
// on detached paths (timeouts, lane workers) pass a context that is not tied
// to the canceled caller so the cancel command itself is not aborted.
func performTargetCancellation(ctx context.Context, action *targetCancellation) {
	if action == nil {
		return
	}
	defer close(action.done)
	if action.session == nil || action.id == "" {
		return
	}
	// Cancellation is best effort after the broker has claimed the request.
	// A target that has already detached or replied is still
	// reconciled by the broker's bounded browser-terminal cache.
	if err := action.session.CancelWebMCP(ctx, action.id); err != nil {
		return
	}
}

func cloneInvokeResult(result InvokeResult) InvokeResult {
	result.Output = cloneJSON(result.Output)
	result.ErrorDetails = cloneDetails(result.ErrorDetails)
	return result
}

func cloneInvocation(invocation Invocation) Invocation {
	invocation.Tool = cloneToolDescriptor(invocation.Tool)
	invocation.Arguments = cloneJSON(invocation.Arguments)
	invocation.Result = cloneJSON(invocation.Result)
	return invocation
}

func cloneDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	cloned := make(map[string]any, len(details))
	for key, value := range details {
		switch typed := value.(type) {
		case json.RawMessage:
			cloned[key] = cloneJSON(typed)
		case []byte:
			cloned[key] = append([]byte(nil), typed...)
		default:
			cloned[key] = value
		}
	}
	return cloned
}

// directCancelSelection resolves the connected selection a direct cancel
// must target before the dispatch linearization point is taken.
func (b *StatefulBroker) directCancelSelection(request DirectCancelRequest) (*brokerSession, TargetSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, ErrClosed
	}
	selected := b.selected
	if selected == nil || !selected.active || !selected.context.Connected {
		return nil, nil, staleSelectionForSession(selected, "selection_not_connected")
	}
	if err := directCancelTargetErrorLocked(selected, request); err != nil {
		return nil, nil, err
	}
	return selected, selected.session, nil
}

func directCancelTargetErrorLocked(selected *brokerSession, request DirectCancelRequest) error {
	if selected.context.Key.BrowserID != request.Target.BrowserID || selected.context.Key.TargetID != request.Target.TargetID {
		return staleSelectionError(request.Target.BrowserID, request.Target.TargetID, selected.context.Generation, "exact_target_not_selected")
	}
	return nil
}

func (b *StatefulBroker) revalidateDirectCancelLocked(selected *brokerSession, session TargetSession, request DirectCancelRequest) error {
	if b.closed {
		return ErrClosed
	}
	if b.selected != selected || !selected.active || !selected.context.Connected {
		return staleSelectionForSession(selected, "selection_not_connected")
	}
	if err := directCancelTargetErrorLocked(selected, request); err != nil {
		return err
	}
	if session == nil {
		return targetAttachError(request.Target, "cancel", ErrClosed)
	}
	// The broker selection and the target session are separate state holders.
	// Recheck the session identity at the command boundary so a stale or
	// accidentally substituted session can never receive a direct cancel for a
	// different target.
	sessionContext := session.Context()
	if sessionContext.Key.BrowserID != request.Target.BrowserID || sessionContext.Key.TargetID != request.Target.TargetID {
		return staleSelectionError(request.Target.BrowserID, request.Target.TargetID, selected.context.Generation, "exact_target_session_mismatch")
	}
	return nil
}

// directCancelDispatchFailed finishes a direct cancellation whose CDP command
// returned an error and releases the dispatch linearization point.
func (b *StatefulBroker) directCancelDispatchFailed(selected *brokerSession, operation *directCancellation, err error) error {
	b.mu.Lock()
	observation, observed := takeDirectCancellationObservation(operation)
	if !observed {
		// The CDP command was attempted even when the browser rejected it.
		// Keep the operation in the dispatched phase so an unconfirmed
		// result cannot be mistaken for a pre-dispatch validation failure.
		operation.phase = directCancellationDispatched
	}
	b.finishDirectCancellationLocked(operation)
	b.mu.Unlock()
	selected.dispatchMu.Unlock()
	if observed {
		return directCancellationResult(operation, observation)
	}
	return directCancellationDispatchFailure(operation, err)
}

// callerBinding is what the lane worker needs from the admitting caller: its
// done channel, its cancellation cause, and its deadline. It exposes no
// context, so the worker never derives contexts from, or reads values of, the
// caller's context. The cause closure does keep the caller's context reachable
// for as long as the invocation lease lives; the lease is dropped when the
// invocation terminates.
type callerBinding struct {
	done        <-chan struct{}
	cause       func() error
	deadline    time.Time
	hasDeadline bool
}

func newCallerBinding(ctx context.Context) callerBinding {
	deadline, hasDeadline := ctx.Deadline()
	return callerBinding{
		done:        ctx.Done(),
		cause:       func() error { return context.Cause(ctx) },
		deadline:    deadline,
		hasDeadline: hasDeadline,
	}
}

// callerBoundContext derives the dispatch context from the worker context and
// mirrors the caller: it carries the caller's deadline (so an expired caller
// deadline surfaces as context.DeadlineExceeded) and is canceled, with the
// caller's cancellation cause as its cause, once the caller is canceled.
func callerBoundContext(ctx context.Context, caller callerBinding) (context.Context, func()) {
	dispatchCtx, cancelCause := context.WithCancelCause(ctx)
	stopDeadline := context.CancelFunc(func() {})
	if caller.hasDeadline {
		dispatchCtx, stopDeadline = context.WithDeadline(dispatchCtx, caller.deadline)
	}
	release := func() {
		stopDeadline()
		cancelCause(nil)
	}
	// An expired caller deadline is mirrored by the dispatch deadline itself.
	mirrorCancel := func() {
		cause := caller.cause()
		if caller.hasDeadline && errors.Is(cause, context.DeadlineExceeded) {
			return
		}
		cancelCause(cause)
	}
	select {
	case <-caller.done:
		mirrorCancel()
		return dispatchCtx, release
	default:
	}
	go func() {
		select {
		case <-caller.done:
			mirrorCancel()
		case <-dispatchCtx.Done():
		}
	}()
	return dispatchCtx, release
}

// dispatchQueuedInvocationWithCallerCancellation dispatches invocation with a
// context derived from the worker context that mirrors the admitting caller's
// cancellation and deadline for the browser call.
func (b *StatefulBroker) dispatchQueuedInvocationWithCallerCancellation(ctx context.Context, invocation *brokerInvocation) {
	dispatchCtx, release := callerBoundContext(ctx, invocation.caller)
	defer release()
	b.dispatchQueuedInvocationWithLock(dispatchCtx, invocation)
}
