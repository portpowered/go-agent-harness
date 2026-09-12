package agentruntime

import (
	"context"
	"errors"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome"
	terminaloutcomewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/terminaloutcome/wire"
)

// Deprecated: sessionTerminalReporter is the source-compatible CLI adapter
// for the host-neutral terminaloutcome service. It owns no terminal policy.
type sessionTerminalReporter struct {
	service  terminaloutcome.Service
	reporter terminaloutcome.Reporter
}

// Deprecated: this alias preserves the sentinel identity for existing CLI
// callers while the runtime service owns publication errors. Keep the alias
// immutable so the compatibility layer does not reintroduce mutable policy.
const ErrSessionTerminalAlreadyPublished = terminaloutcome.ErrSessionTerminalAlreadyPublished

type sessionTerminalReporterContextKey struct{}

// Deprecated: use the terminaloutcome service's reporter operations at a host
// boundary. The context value remains only for unchanged replay callbacks.
func withSessionTerminalReporter(ctx context.Context, reporter *sessionTerminalReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, sessionTerminalReporterContextKey{}, reporter)
}

// Deprecated: retained as the unchanged replay finalizer lookup seam.
func sessionTerminalReporterFromContext(ctx context.Context) *sessionTerminalReporter {
	if ctx == nil {
		return nil
	}
	reporter, _ := ctx.Value(sessionTerminalReporterContextKey{}).(*sessionTerminalReporter)
	return reporter
}

// Deprecated: construct a fresh runtime reporter through the dedicated Wire
// graph; no CLI state is initialized here.
func newSessionTerminalReporter() *sessionTerminalReporter {
	service := terminaloutcomewire.NewService()
	return &sessionTerminalReporter{service: service, reporter: service.NewReporter()}
}

func (r *sessionTerminalReporter) markRunStarted() {
	if r != nil && r.reporter != nil {
		r.reporter.MarkRunStarted()
	}
}

func (r *sessionTerminalReporter) observeStreamMessage(msg messages.StreamMessage, leadingNewline bool) {
	if r != nil && r.reporter != nil {
		r.reporter.ObserveStreamMessage(msg, leadingNewline)
	}
}

func (r *sessionTerminalReporter) markDurationExpiryWithOutput(outputState messages.TerminalOutputState) {
	if r != nil && r.reporter != nil {
		r.reporter.MarkDurationExpiry(outputState)
	}
}

func markSessionDurationExpiry(reporter *sessionTerminalReporter, planned bool, outputState messages.TerminalOutputState) {
	if planned && reporter != nil {
		reporter.markDurationExpiryWithOutput(outputState)
	}
}

func (r *sessionTerminalReporter) markReplayComplete() {
	if r != nil && r.reporter != nil {
		r.reporter.MarkReplayComplete()
	}
}

func (r *sessionTerminalReporter) recordArtifactFinalization(requested bool, err error) {
	if r != nil && r.reporter != nil {
		r.reporter.RecordArtifactFinalization(requested, err)
	}
}

// Deprecated: this forwarding helper supports the two unchanged runtime
// planners that gate replay completion on the terminal error classification.
func sessionErrorHasIndependentFailure(err error) bool {
	if err == nil {
		return false
	}
	return terminaloutcomewire.NewService().HasIndependentFailure(err, errSessionMaxDurationExpired)
}

func (r *sessionTerminalReporter) publish(out io.Writer, runErr error) error {
	if r == nil || r.reporter == nil {
		return nil
	}
	return r.reporter.Publish(out, r.adaptLegacyDurationError(runErr))
}

// adaptLegacyDurationError translates only a duration-only legacy error. A
// mixed joined error remains independent unless its only other leaves are
// context cancellation/deadline, whose standard identities are retained.
func (r *sessionTerminalReporter) adaptLegacyDurationError(runErr error) error {
	if runErr == nil || !errors.Is(runErr, errSessionMaxDurationExpired) {
		return runErr
	}
	service := r.service
	if service == nil {
		service = terminaloutcomewire.NewService()
	}
	if service.HasIndependentFailure(runErr, errSessionMaxDurationExpired) {
		return runErr
	}
	var contextErrors []error
	if errors.Is(runErr, context.Canceled) {
		contextErrors = append(contextErrors, context.Canceled)
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		contextErrors = append(contextErrors, context.DeadlineExceeded)
	}
	return errors.Join(append([]error{terminaloutcome.ErrSessionMaxDurationExpired}, contextErrors...)...)
}
