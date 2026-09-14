package tools

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// DefaultToolExecutionTimeout bounds a non-interactive tool call without
	// changing the lifetime of its enclosing session.
	DefaultToolExecutionTimeout = 60 * time.Second
	// ToolExecutionPermissionRecheckTimeout bounds the optional display
	// permission probe after an interactive display deadline.
	ToolExecutionPermissionRecheckTimeout = 100 * time.Millisecond
	// ToolExecutionTimeoutClassification is the stable provider-visible marker
	// for a tool-local interactive deadline.
	ToolExecutionTimeoutClassification = "interactive_tool_timeout"
	// PageSightUnavailableErrorCode classifies a page-sight failure produced
	// when a page executor returns a Go error instead of its normal envelope.
	PageSightUnavailableErrorCode = "page_sight_unavailable"
)

// ToolExecutionError is a stable, immutable error identity for controller
// policy outcomes. String constants avoid package-level mutable state while
// remaining compatible with errors.Is and errors.Join.
type ToolExecutionError string

func (e ToolExecutionError) Error() string { return string(e) }

const (
	// ErrToolExecutionTimeout identifies a deadline applied by this controller.
	ErrToolExecutionTimeout ToolExecutionError = "tool execution timed out"
	// ErrToolExecutionPermissionDenied identifies a denied display re-check
	// when the host did not provide a richer typed error.
	ErrToolExecutionPermissionDenied ToolExecutionError = "tool execution permission denied"
)

// ToolExecutionFailureKind identifies the host-facing presentation route for
// a failed call. The controller owns route selection; hosts own only the
// narrow error-code and typed-error adapters in ToolExecutionRequest.
type ToolExecutionFailureKind string

const (
	ToolExecutionFailureGeneric         ToolExecutionFailureKind = "generic"
	ToolExecutionFailurePageSight       ToolExecutionFailureKind = "page_sight"
	ToolExecutionFailurePhysicalDisplay ToolExecutionFailureKind = "physical_display"
)

// ToolExecutionObserver receives one call event and, unless the call follows
// the correlated cancellation path, at most one result event.
type ToolExecutionObserver interface {
	ObserveToolCall(messages.ToolCall)
	ObserveToolResult(messages.ToolCall, messages.ToolCallResponse, bool)
}

// ToolExecutionDiagnostic is the raw failure fact emitted before the result
// is projected. The host may add source-specific classification at its edge.
type ToolExecutionDiagnostic struct {
	ToolCallID string
	ToolName   string
	Kind       ToolExecutionFailureKind
	Error      error
}

// ToolExecutionDiagnosticSink receives one diagnostic for each projected
// execution failure, including a generic failure with no source classification.
type ToolExecutionDiagnosticSink interface {
	RecordToolExecutionDiagnostic(ToolExecutionDiagnostic)
}

// ToolExecutionCancellationIntent is the optional operator-cancellation
// marker. Ordinary parent cancellation remains a correlated failed result.
type ToolExecutionCancellationIntent interface {
	SIGINTReceived() bool
}

// PhysicalDisplayToolMatcher keeps host tool names out of the reusable
// controller while allowing display-specific failure projection and re-checks.
type PhysicalDisplayToolMatcher func(string) bool

// ScreenToolErrorCode projects a host display error into the stable screen
// result envelope. A nil function uses the generic capture-failed code.
type ScreenToolErrorCode func(error) string

// PermissionDeniedErrorFactory preserves host-specific operator diagnostics
// without importing a host error type into the reusable runtime.
type PermissionDeniedErrorFactory func(string) error

// ToolExecutionTimeoutPolicy supplies an immutable per-name deadline when a
// host has already resolved its policy snapshot.
type ToolExecutionTimeoutPolicy func(string) time.Duration

// ToolExecutionRequest contains the immutable dependencies for one controller.
// Executor, policy, and observers are request-scoped; no package-global
// registry or mutable execution state is consulted.
type ToolExecutionRequest struct {
	Executor                    messages.ToolExecutor
	TimeoutOverride             time.Duration
	InteractivePolicy           InteractiveToolPolicy
	TimeoutForTool              ToolExecutionTimeoutPolicy
	UseDefaultInteractivePolicy bool
	Observer                    ToolExecutionObserver
	CancellationIntent          ToolExecutionCancellationIntent
	Diagnostics                 ToolExecutionDiagnosticSink
	PermissionRechecker         ScreenRecordingPermissionRechecker
	IsPhysicalDisplayTool       PhysicalDisplayToolMatcher
	ScreenErrorCode             ScreenToolErrorCode
	PermissionDeniedError       PermissionDeniedErrorFactory
}

// ToolExecutionController applies one bounded, correlated invocation policy.
type ToolExecutionController interface {
	Execute(context.Context, messages.ToolCall) (messages.ToolCallResponse, error)
}

// ExecutionService is the optional execution capability exposed by the
// registered tools service. Keeping it separate lets existing host adapters
// retain the older capability-resolution contract while new embedders opt in.
type ExecutionService interface {
	NewToolExecutionController(ToolExecutionRequest) ToolExecutionController
}

// ToolExecutionService is the descriptive alias used by embedders.
type ToolExecutionService = ExecutionService
