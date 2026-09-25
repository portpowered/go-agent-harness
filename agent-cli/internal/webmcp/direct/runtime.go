package direct

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production"
)

const (
	// DefaultCommandTimeout is the end-to-end safety deadline for one direct
	// WebMCP command. It covers setup, browser work, output data preparation,
	// and the bounded cleanup handoff. A caller may choose a shorter value;
	// zero uses this safe default.
	DefaultCommandTimeout = 15 * time.Second

	// CleanupTimeout keeps a non-cooperative browser close from extending the
	// command indefinitely after the operation deadline. The close call is
	// started once and is never retried if this allowance expires.
	CleanupTimeout = 2 * time.Second
)

// VersionFunc supplies the browser version/protocol check. It is separate
// from the broker because the broker's stable interface deliberately contains
// only browser operations needed by model-facing tools.
type VersionFunc func(context.Context, webmcp.BrowserCandidate) (webmcp.BrowserVersion, error)

// Runtime is the request-scoped set of seams used by doctor and the direct
// commands. Broker is the real stateful broker in production-capable
// compositions; VersionFunc or Catalog supplies the endpoint/version check.
// Close is optional and owns any runtime resources not owned by Broker.
type Runtime struct {
	Broker      webmcp.Broker
	Discovery   production.DiscoveryService
	VersionFunc VersionFunc
	Catalog     webmcp.DevToolsCatalog
	Close       func() error
	// Navigate and PageState are optional run-scoped browser observations used
	// by probe.scenario.v2. Keeping them outside BrowserRuntime preserves the
	// neutral broker contract for callers that do not need probe evidence.
	Navigate  func(context.Context, string) error
	PageState func(context.Context) (json.RawMessage, error)
}

// Owned reports whether the runtime holds resources that must be closed.
func (r Runtime) Owned() bool {
	return r.Broker != nil || r.Close != nil
}

// Factory constructs one runtime for a resolved browser configuration.
// Construction is lazy and is never called when configuration or endpoint
// policy validation has already failed.
type Factory func(config.BrowserConfig) (Runtime, error)

// Operation is one direct command's browser work.
type Operation func(context.Context, webmcp.Broker, config.BrowserConfig) (any, error)

type factoryResult struct {
	runtime Runtime
	err     error
}

type operationResult struct {
	data any
	err  error
}

// ConstructRuntime calls factory under ctx. The Factory type predates the
// context-aware command contract, so an implementation that ignores the
// deadline is abandoned and its late runtime is closed exactly once.
func ConstructRuntime(ctx context.Context, factory Factory, browser config.BrowserConfig) (Runtime, error) { //nolint:contextcheck // A nil context from legacy callers falls back to Background.
	if factory == nil {
		return Runtime{}, errors.New("WebMCP runtime factory is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := make(chan factoryResult, 1)
	go func() {
		runtime, err := factory(browser)
		result <- factoryResult{runtime: runtime, err: err}
	}()
	select {
	case completed := <-result:
		return completed.runtime, completed.err
	case <-ctx.Done():
		go closeLateRuntime(result)
		return Runtime{}, ctx.Err()
	}
}

// closeLateRuntime releases a runtime whose construction outlived the
// command deadline. No caller remains to receive the cleanup result.
func closeLateRuntime(result <-chan factoryResult) {
	completed := <-result
	if err := CloseRuntimeBounded(completed.runtime); err != nil {
		return
	}
}

// RunOperation runs operation under ctx. After the deadline it still
// prefers a result that completes within CleanupTimeout, so a normal
// browser-loss or interrupt-reconciliation result stays visible without
// waiting indefinitely on an operation that ignores its context.
func RunOperation(ctx context.Context, operation Operation, broker webmcp.Broker, browser config.BrowserConfig) (any, error) {
	if operation == nil {
		return nil, errors.New("WebMCP direct operation is required")
	}
	if ctx == nil {
		ctx = context.Background() //nolint:contextcheck // A nil context from legacy callers falls back to Background.
	}
	result := make(chan operationResult, 1)
	go func() {
		data, err := operation(ctx, broker, browser)
		result <- operationResult{data: data, err: err}
	}()
	select {
	case completed := <-result:
		return completed.data, completed.err
	case <-ctx.Done():
		timer := time.NewTimer(CleanupTimeout)
		defer timer.Stop()
		select {
		case completed := <-result:
			return completed.data, completed.err
		case <-timer.C:
			return nil, ctx.Err()
		}
	}
}

// Run constructs one runtime for browser, runs operation against its
// broker, and always closes the runtime within CleanupTimeout. A
// browser_disconnected cause is preferred over construction, operation, and
// cleanup failures.
func Run(ctx context.Context, factory Factory, browser config.BrowserConfig, operation Operation) (any, error) {
	runtime, err := ConstructRuntime(ctx, factory, browser)
	if err != nil {
		return nil, PreferBrowserDisconnected(errors.Join(RuntimeFactoryFailure(err), CloseRuntimeBounded(runtime)))
	}
	if runtime.Broker == nil {
		return nil, PreferBrowserDisconnected(errors.Join(RuntimeUnavailableError(phaseRuntimeFactory), CloseRuntimeBounded(runtime)))
	}
	data, err := RunOperation(ctx, operation, runtime.Broker, browser)
	return data, PreferBrowserDisconnected(errors.Join(err, CloseRuntimeBounded(runtime)))
}

// CloseRuntimeBounded closes runtime, giving up after CleanupTimeout.
func CloseRuntimeBounded(runtime Runtime) error {
	if !runtime.Owned() {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- CloseRuntime(runtime)
	}()
	timer := time.NewTimer(CleanupTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("WebMCP runtime cleanup exceeded %s: %w", CleanupTimeout, context.DeadlineExceeded)
	}
}

// CloseRuntime closes the broker and the runtime's own Close hook, joining
// their failures. A panicking close is reported as an error.
func CloseRuntime(runtime Runtime) (err error) {
	if runtime.Broker != nil {
		err = errors.Join(err, callClose(runtime.Broker.Close))
	}
	if runtime.Close != nil {
		// The runtime hook owns resources outside the broker (for example a
		// version-endpoint client or an adapter transport), so it is called
		// independently and its failure is joined with broker cleanup.
		err = errors.Join(err, callClose(runtime.Close))
	}
	return err
}

func callClose(closeFunc func() error) (err error) {
	if closeFunc == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = errors.New("doctor cleanup panicked")
		}
	}()
	return closeFunc()
}
