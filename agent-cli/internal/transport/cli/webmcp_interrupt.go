package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// webmcpDirectInterruptReconciliationTimeout is both the cleanup deadline and
// the user-visible bound documented by the invoke command. The final result
// remains classified as canceled even when the browser cannot acknowledge the
// best-effort cancellation before this deadline.
const webmcpDirectInterruptReconciliationTimeout = 2 * time.Second

// newWebMCPDirectInterruptContext keeps SIGINT separate from the command
// context. The command context is canceled to stop the normal wait; the
// cancellation request uses a fresh bounded context below.
func newWebMCPDirectInterruptContext(parent context.Context) (context.Context, func() bool, func()) {
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
	stop := func() {
		stopOnce.Do(func() {
			signal.Stop(signals)
			close(watcherStop)
			cancel()
			<-watcherDone
		})
	}
	wasInterrupted := func() bool {
		select {
		case <-interruptReceived:
			return true
		default:
			return false
		}
	}
	return ctx, wasInterrupted, stop
}

func directInvocationCanceledBeforeDispatch(toolRef webmcp.ToolRef) error {
	details := map[string]any{
		"phase":         "before_dispatch",
		"cancel_source": "interrupt",
	}
	if toolRef != "" {
		details["tool_ref"] = string(toolRef)
	}
	return webmcp.NewClassifiedError(webmcp.ErrorInvocationCanceled, webmcp.DefaultErrorMessage(webmcp.ErrorInvocationCanceled), details)
}

// reconcileDirectInvocationInterrupt asks the broker that admitted the call
// to cancel it using the broker-owned ID. A fallback DirectCanceller path is
// available for small compositions that only return the browser protocol ID.
// Both calls are bounded and run independently of the already-canceled wait
// context.
func reconcileDirectInvocationInterrupt(broker webmcp.Broker, result webmcp.InvokeResult, selected webmcp.PageKey, receiptID webmcp.InvocationID, toolRef webmcp.ToolRef) error {
	details := map[string]any{
		"cancel_source":       "interrupt",
		"side_effect_unknown": true,
		"phase":               "interrupt_reconciliation",
	}
	if receiptID != "" {
		details["invocation_id"] = string(receiptID)
	}
	if toolRef != "" {
		details["tool_ref"] = string(toolRef)
	}

	status := "not_requested"
	if broker == nil {
		status = "broker_unavailable"
	} else if result.InvocationID != "" {
		status = boundedInterruptCancellationStatus(func(ctx context.Context) error {
			return broker.Cancel(ctx, webmcp.CancelRequest{
				InvocationID: result.InvocationID,
				Reason:       "interrupt",
			})
		})
	} else if result.BrowserInvocationID != "" {
		if directCanceller, ok := broker.(webmcp.DirectCanceller); ok && selected.BrowserID != "" && selected.TargetID != "" {
			status = boundedInterruptCancellationStatus(func(ctx context.Context) error {
				return directCanceller.CancelDirect(ctx, webmcp.DirectCancelRequest{
					Target:       webmcp.TargetSelector(selected),
					InvocationID: result.BrowserInvocationID,
					Reason:       "interrupt",
				})
			})
		} else {
			status = "broker_invocation_id_unavailable"
		}
	}
	details["cancel_status"] = status

	return webmcp.NewClassifiedError(webmcp.ErrorInvocationCanceled, webmcp.DefaultErrorMessage(webmcp.ErrorInvocationCanceled), details)
}

func boundedInterruptCancellationStatus(request func(context.Context) error) string {
	if request == nil {
		return "not_requested"
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), webmcpDirectInterruptReconciliationTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- request(cleanupCtx) }()
	select {
	case err := <-done:
		if err == nil {
			return "requested"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "timed_out"
		}
		return "rejected"
	case <-cleanupCtx.Done():
		return "timed_out"
	}
}
