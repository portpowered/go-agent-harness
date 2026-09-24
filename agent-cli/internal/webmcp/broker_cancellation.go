package webmcp

import (
	"context"
	"encoding/json"
)

type targetCancellation struct {
	session TargetSession
	id      InvocationID
	ctx     context.Context
	done    chan struct{}
}

func (b *StatefulBroker) claimTargetCancellationLocked(invocation *brokerInvocation, cancelCtx context.Context) *targetCancellation {
	if invocation == nil || invocation.cancelSent || !invocation.invocation.CancelRequested || invocation.browserID == "" {
		return nil
	}
	if cancelCtx == nil {
		cancelCtx = context.Background()
	}
	invocation.cancelSent = true
	invocation.cancelDone = make(chan struct{})
	return &targetCancellation{session: invocation.selected.session, id: invocation.browserID, ctx: cancelCtx, done: invocation.cancelDone}
}

func (b *StatefulBroker) cancellationWaitLocked(invocation *brokerInvocation, action *targetCancellation) <-chan struct{} {
	if action != nil || invocation == nil || !invocation.cancelSent {
		return nil
	}
	return invocation.cancelDone
}

func performTargetCancellation(action *targetCancellation) {
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
	_ = action.session.CancelWebMCP(action.ctx, action.id)
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
