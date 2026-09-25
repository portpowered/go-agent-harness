package operations

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// InterruptReconciliationTimeout is both the cleanup deadline and the
// user-visible bound documented by the invoke command. The final result
// remains classified as canceled even when the browser cannot acknowledge
// the best-effort cancellation before this deadline.
const InterruptReconciliationTimeout = 2 * time.Second

const (
	cancelSourceInterrupt = "interrupt"

	cancelStatusNotRequested  = "not_requested"
	cancelStatusRequested     = "requested"
	cancelStatusTimedOut      = "timed_out"
	cancelStatusRejected      = "rejected"
	cancelStatusNoBroker      = "broker_unavailable"
	cancelStatusNoInvocation  = "broker_invocation_id_unavailable"
	phaseBeforeDispatch       = "before_dispatch"
	phaseInterruptReconciling = "interrupt_reconciliation"
)

// NewInterruptContext keeps SIGINT separate from the command context. The
// returned context is canceled to stop the normal wait; interrupted reports
// whether SIGINT caused that; stop releases the signal handler. The
// cancellation request itself uses a fresh bounded context.
func NewInterruptContext(parent context.Context) (ctx context.Context, interrupted func() bool, stop func()) {
	if parent == nil {
		parent = context.Background()
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	interruptReceived := make(chan struct{})
	watcherStop := make(chan struct{})
	watcherDone := make(chan struct{})
	ctx, cancel := context.WithCancel(parent)

	go func() {
		defer close(watcherDone)
		select {
		case <-signals:
			close(interruptReceived)
			cancel()
		case <-parent.Done():
			cancel()
		case <-watcherStop:
		}
	}()

	var stopOnce sync.Once
	stop = func() {
		stopOnce.Do(func() {
			signal.Stop(signals)
			close(watcherStop)
			cancel()
			<-watcherDone
		})
	}
	interrupted = func() bool {
		select {
		case <-interruptReceived:
			return true
		default:
			return false
		}
	}
	return ctx, interrupted, stop
}

// CanceledBeforeDispatch classifies an interrupt that arrived before the
// browser accepted the call. It never fabricates an invocation ID.
func CanceledBeforeDispatch(toolRef webmcp.ToolRef) error {
	details := map[string]any{
		detailPhase:        phaseBeforeDispatch,
		detailCancelSource: cancelSourceInterrupt,
	}
	if toolRef != "" {
		details[detailToolRef] = string(toolRef)
	}
	return webmcp.NewClassifiedError(webmcp.ErrorInvocationCanceled, webmcp.DefaultErrorMessage(webmcp.ErrorInvocationCanceled), details)
}

// ReconcileInterrupt asks the broker that admitted the call to cancel it
// using the broker-owned ID. A fallback DirectCanceller path is available
// for small compositions that only return the browser protocol ID. Both
// calls are bounded and run independently of the already-canceled wait
// context; the side effect of the call remains unknown.
func ReconcileInterrupt(ctx context.Context, broker webmcp.Broker, result webmcp.InvokeResult, selected webmcp.PageKey, receiptID webmcp.InvocationID, toolRef webmcp.ToolRef) error {
	details := map[string]any{
		detailCancelSource:      cancelSourceInterrupt,
		detailSideEffectUnknown: true,
		detailPhase:             phaseInterruptReconciling,
	}
	if receiptID != "" {
		details[detailInvocationID] = string(receiptID)
	}
	if toolRef != "" {
		details[detailToolRef] = string(toolRef)
	}
	details["cancel_status"] = interruptCancelStatus(ctx, broker, result, selected)
	return webmcp.NewClassifiedError(webmcp.ErrorInvocationCanceled, webmcp.DefaultErrorMessage(webmcp.ErrorInvocationCanceled), details)
}

func interruptCancelStatus(ctx context.Context, broker webmcp.Broker, result webmcp.InvokeResult, selected webmcp.PageKey) string {
	switch {
	case broker == nil:
		return cancelStatusNoBroker
	case result.InvocationID != "":
		return boundedCancellationStatus(ctx, func(cleanup context.Context) error {
			return broker.Cancel(cleanup, webmcp.CancelRequest{InvocationID: result.InvocationID, Reason: cancelSourceInterrupt})
		})
	case result.BrowserInvocationID == "":
		return cancelStatusNotRequested
	}
	directCanceller, ok := broker.(webmcp.DirectCanceller)
	if !ok || selected.BrowserID == "" || selected.TargetID == "" {
		return cancelStatusNoInvocation
	}
	return boundedCancellationStatus(ctx, func(cleanup context.Context) error {
		return directCanceller.CancelDirect(cleanup, webmcp.DirectCancelRequest{
			Target:       webmcp.TargetSelector(selected),
			InvocationID: result.BrowserInvocationID,
			Reason:       cancelSourceInterrupt,
		})
	})
}

// boundedCancellationStatus runs request under a context that keeps ctx's
// values but not its cancellation, bounded by InterruptReconciliationTimeout,
// and reports requested, rejected, or timed_out. The interrupted wait's
// cancellation therefore never cancels the reconciliation itself.
func boundedCancellationStatus(ctx context.Context, request func(context.Context) error) string {
	if request == nil {
		return cancelStatusNotRequested
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), InterruptReconciliationTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- request(cleanupCtx) }()
	select {
	case err := <-done:
		if err == nil {
			return cancelStatusRequested
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return cancelStatusTimedOut
		}
		return cancelStatusRejected
	case <-cleanupCtx.Done():
		return cancelStatusTimedOut
	}
}
