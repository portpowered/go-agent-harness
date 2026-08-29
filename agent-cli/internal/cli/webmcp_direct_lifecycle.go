package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/spf13/cobra"
)

const (
	// DefaultWebMCPDirectCommandTimeout is the end-to-end safety deadline for
	// one direct WebMCP command. It covers setup, browser work, output data
	// preparation, and the bounded cleanup handoff. A caller may choose a
	// shorter value with --command-timeout; zero uses this safe default.
	DefaultWebMCPDirectCommandTimeout = 15 * time.Second

	// webmcpDirectCleanupTimeout keeps a non-cooperative browser close from
	// extending the command indefinitely after the operation deadline. The
	// close call is started once and is never retried if this allowance expires.
	webmcpDirectCleanupTimeout = 2 * time.Second
)

type directRuntimeFactoryResult struct {
	runtime WebMCPDoctorRuntime
	err     error
}

type directOperationResult struct {
	data any
	err  error
}

func registerWebMCPDirectCommandTimeoutFlag(cmd *cobra.Command, values *webmcpDirectFlags) {
	if cmd == nil || values == nil {
		return
	}
	registerWebMCPCommandTimeoutFlag(cmd, &values.commandTimeout)
}

func registerWebMCPCommandTimeoutFlag(cmd *cobra.Command, target *time.Duration) {
	if cmd == nil || target == nil {
		return
	}
	cmd.Flags().DurationVar(target, "command-timeout", DefaultWebMCPDirectCommandTimeout, "End-to-end WebMCP command bound (Go duration; zero uses the safe default)")
}

func directCommandTimeout(values *webmcpDirectFlags) time.Duration {
	if values == nil || values.commandTimeout == 0 {
		return DefaultWebMCPDirectCommandTimeout
	}
	return values.commandTimeout
}

func directBrowserFlagChanged(cmd *cobra.Command) bool {
	return directFlagChanged(cmd, "browser", "browser-browser")
}

func directFlagChanged(cmd *cobra.Command, names ...string) bool {
	if cmd == nil {
		return false
	}
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func constructWebMCPDoctorRuntime(ctx context.Context, factory WebMCPDoctorFactory, browser config.BrowserConfig) (WebMCPDoctorRuntime, error) {
	if factory == nil {
		return WebMCPDoctorRuntime{}, errors.New("WebMCP runtime factory is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// A caller may enter the command with an already-canceled context (for
	// example, a watch that is being shut down). Preserve the pre-existing
	// command behavior of constructing and closing its request-scoped runtime;
	// the operation itself still observes the canceled context below. A
	// deadline, however, must never force a potentially blocking legacy
	// factory call onto the command's critical path.
	if errors.Is(ctx.Err(), context.Canceled) {
		return factory(browser)
	}

	result := make(chan directRuntimeFactoryResult, 1)
	go func() {
		runtime, err := factory(browser)
		result <- directRuntimeFactoryResult{runtime: runtime, err: err}
	}()
	select {
	case completed := <-result:
		return completed.runtime, completed.err
	case <-ctx.Done():
		// WebMCPDoctorFactory predates the context-aware command contract. Keep
		// the legacy function type source-compatible, but close a late runtime
		// exactly once if an implementation ignores the command deadline.
		go func() {
			completed := <-result
			_ = closeWebMCPDoctorRuntimeBounded(completed.runtime)
		}()
		return WebMCPDoctorRuntime{}, ctx.Err()
	}
}

func runWebMCPDirectOperation(ctx context.Context, operation webmcpDirectOperation, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
	if operation == nil {
		return nil, errors.New("WebMCP direct operation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := make(chan directOperationResult, 1)
	go func() {
		data, err := operation(ctx, broker, browser)
		result <- directOperationResult{data: data, err: err}
	}()
	select {
	case completed := <-result:
		return completed.data, completed.err
	case <-ctx.Done():
		// Prefer a result that completed concurrently with the deadline. This
		// keeps the normal browser-loss or interrupt-reconciliation result
		// visible without waiting indefinitely on an operation that failed to
		// honor its context.
		timer := time.NewTimer(webmcpDirectCleanupTimeout)
		defer timer.Stop()
		select {
		case completed := <-result:
			return completed.data, completed.err
		case <-timer.C:
			return nil, ctx.Err()
		}
	}
}

func closeWebMCPDoctorRuntimeBounded(runtime WebMCPDoctorRuntime) error {
	if runtime.Broker == nil && runtime.Close == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- closeWebMCPDoctorRuntime(runtime)
	}()
	timer := time.NewTimer(webmcpDirectCleanupTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("WebMCP runtime cleanup exceeded %s: %w", webmcpDirectCleanupTimeout, context.DeadlineExceeded)
	}
}

// directBrowserDisconnectedError walks classified and joined errors instead
// of relying on errors.As alone. A generic target_attach_failed wrapper may
// contain the more important C0 browser_disconnected cause.
func directBrowserDisconnectedError(err error) error {
	if err == nil {
		return nil
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil && classified.Code == webmcp.ErrorBrowserDisconnected {
		return classified
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if failure := directBrowserDisconnectedError(cause); failure != nil {
				return failure
			}
		}
		return nil
	}
	return directBrowserDisconnectedError(errors.Unwrap(err))
}

func preferDirectBrowserDisconnected(err error) error {
	if failure := directBrowserDisconnectedError(err); failure != nil {
		return failure
	}
	return err
}

func directRuntimeFactoryFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return webmcpRuntimeFactoryError(err)
}
