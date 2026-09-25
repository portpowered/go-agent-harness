package operations

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/direct"
)

const issuePathInvocationID = "/invocation_id"

// CancelRequest cancels one invocation by its exact receipt ID, supplied by
// InvocationID or as the single positional argument, never both.
type CancelRequest struct {
	Selector     Selector
	InvocationID string
	Args         []string
	Reason       string
	Timeout      time.Duration
}

func (r CancelRequest) invocationID() (string, error) {
	invocationID := r.InvocationID
	if invocationID == "" && len(r.Args) == 1 {
		invocationID = r.Args[0]
	}
	if invocationID == "" {
		return "", direct.InvalidInputError("an invocation ID is required (use --invocation or a positional ID)", issuePathInvocationID)
	}
	if r.InvocationID != "" && len(r.Args) == 1 {
		return "", direct.InvalidInputError("invocation ID must be supplied by --invocation or positionally, not both", issuePathInvocationID)
	}
	return invocationID, nil
}

// Cancel rehydrates only the exact persisted or explicitly selected target
// and asks it to cancel the invocation. It never searches for or falls back
// to another target, and it succeeds only after the target reports the
// terminal cancellation.
func Cancel(ctx context.Context, broker webmcp.Broker, request CancelRequest) (CancelData, error) {
	invocationID, err := request.invocationID()
	if err != nil {
		return CancelData{}, err
	}
	resolution, err := ResolveTarget(ctx, broker, request.Selector)
	if err != nil {
		return CancelData{}, err
	}
	if !hasExactTarget(request.Selector, resolution) {
		browser := request.Selector.Browser
		return CancelData{}, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "direct cancellation requires an exact persisted or explicitly selected browser target", map[string]any{
			detailBrowserID: browser.Selection.Browser,
			detailTargetID:  browser.Selection.Tab,
			detailReason:    "exact_selection_required",
		})
	}
	selector := resolution.selector()
	if _, err := SelectTarget(ctx, broker, selector, false); err != nil {
		return CancelData{}, err
	}
	cancelCtx := ctx
	if request.Timeout > 0 {
		var cancel context.CancelFunc
		cancelCtx, cancel = context.WithTimeout(ctx, request.Timeout)
		defer cancel()
	}
	if err := requestCancel(cancelCtx, broker, selector, webmcp.InvocationID(invocationID), request.Reason); err != nil {
		return CancelData{}, err
	}
	return CancelData{
		InvocationID: invocationID,
		Status:       "canceled",
		Phase:        "terminal",
		Outcome:      "confirmed_canceled",
	}, nil
}

func hasExactTarget(selector Selector, resolution Resolution) bool {
	return resolution.Stored != nil || selector.Browser.Selection.Tab != "" || selector.TabFlagChanged
}

func requestCancel(ctx context.Context, broker webmcp.Broker, selector webmcp.TargetSelector, invocationID webmcp.InvocationID, reason string) error {
	if directCanceller, ok := broker.(webmcp.DirectCanceller); ok {
		return directCanceller.CancelDirect(ctx, webmcp.DirectCancelRequest{
			Target:       selector,
			InvocationID: invocationID,
			Reason:       reason,
		})
	}
	return broker.Cancel(ctx, webmcp.CancelRequest{InvocationID: invocationID, Reason: reason})
}
