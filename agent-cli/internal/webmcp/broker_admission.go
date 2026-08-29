package webmcp

import "context"

// waitForAdmissionDispatch gives a result that has already crossed the
// dispatch handoff priority over context cancellation. The browser call and
// the caller's signal watcher run independently, so both channels can be
// ready at the same time. A plain select would randomly discard the browser
// invocation ID in that case, leaving the CLI unable to reconcile the call.
func (b *StatefulBroker) waitForAdmissionDispatch(ctx context.Context, invocation *brokerInvocation) (InvokeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Check the handoff before entering the blocking select. This is the
	// deterministic fast path for a dispatch that was reported just before a
	// signal was delivered.
	select {
	case outcome := <-invocation.dispatchDone:
		return cloneInvokeResult(outcome.result), outcome.err
	default:
	}

	select {
	case outcome := <-invocation.dispatchDone:
		return cloneInvokeResult(outcome.result), outcome.err
	case <-ctx.Done():
		return b.resolveAdmissionCancellation(invocation, ctx.Err())
	case <-b.closedCh:
		return b.resolveAdmissionCancellation(invocation, ErrClosed)
	}
}

// resolveAdmissionCancellation linearizes caller cancellation against the
// dispatch handoff while holding the broker mutex. If reportDispatchLocked
// has already published a browser ID, that report wins even when ctx.Done is
// also ready. If dispatch is still in flight, cancellation wins at this
// point; a later browser response cannot retroactively become the caller's
// handoff.
func (b *StatefulBroker) resolveAdmissionCancellation(invocation *brokerInvocation, fallback error) (InvokeResult, error) {
	if invocation == nil {
		return InvokeResult{}, fallback
	}

	b.mu.Lock()
	reported := invocation.reported
	if !reported && !invocation.terminalized && invocation.invocation.State == InvocationQueued {
		removeQueuedInvocationLocked(invocation.selected, invocation)
		result := invocationFailureResult(invocation, InvocationCanceled, ErrorInvocationCanceled, map[string]any{
			"invocation_id": string(invocation.invocation.ID),
			"cancel_source": "context",
		})
		b.finishInvocationLocked(invocation, result)
		reported = invocation.reported
	}
	dispatchDone := invocation.dispatchDone
	b.mu.Unlock()

	if !reported {
		return InvokeResult{}, fallback
	}
	outcome := <-dispatchDone
	return admissionDispatchOutcome(outcome, fallback)
}

func admissionDispatchOutcome(outcome invocationDispatch, fallback error) (InvokeResult, error) {
	// A queued cancellation also reports through dispatchDone, but it has no
	// browser ID and must remain a context/closed error to the caller. Only a
	// result carrying the target's ID crossed the handoff this method exists to
	// preserve.
	if outcome.result.BrowserInvocationID == "" {
		return InvokeResult{}, fallback
	}
	return cloneInvokeResult(outcome.result), outcome.err
}
