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
