package sessionturn

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

const (
	// DefaultToolExecutionTimeout bounds one invocation for callers that do
	// not supply an interactive policy or explicit timeout.
	DefaultToolExecutionTimeout = 60 * time.Second
	// ScreenPermissionRecheckTimeout bounds the optional permission re-check
	// after a physical-display call times out.
	ScreenPermissionRecheckTimeout = 100 * time.Millisecond
	// ToolTimeoutClassification is the stable provider-visible marker for an
	// interactive tool deadline, distinct from a transport or session timeout.
	ToolTimeoutClassification = "interactive_tool_timeout"
	// PageSightUnavailableErrorCode classifies a page-sight failure returned as
	// a Go error instead of the page executor's normal result envelope.
	PageSightUnavailableErrorCode = "page_sight_unavailable"
)

// ToolLifecycleObserver records the exact local call and result boundary.
type ToolLifecycleObserver interface {
	ObserveToolCall(messages.ToolCall)
	ObserveToolResult(call messages.ToolCall, response messages.ToolCallResponse, failed bool)
}

// ToolProgress is the participant-liveness boundary. The provider call
// obligation is recorded before local execution starts so a cancellation
// cannot outrun stream publication.
type ToolProgress interface {
	ObserveProviderToolCallWithID(callID, name string)
	BeginLocalToolExecution()
	EndLocalToolExecution()
}

// ToolLifecycle composes the optional observers of one executor. Calls reach
// Runtime, Progress, then Recording; results reach Runtime, Recording, then
// end Progress.
type ToolLifecycle struct {
	Recording ToolLifecycleObserver
	Progress  ToolProgress
	Runtime   ToolLifecycleObserver
}

// CancellationIntent reports whether an operator SIGINT ended the run.
type CancellationIntent interface {
	SIGINTReceived() bool
}

// ToolPresentation is the host projection of provider-visible tool failures.
// Nil functions select the generic runtime presentation.
type ToolPresentation struct {
	// DisplayTool identifies calls that may reach a physical host display.
	DisplayTool func(name string) bool
	// DisplayFailure renders the customer-safe failure for a display call.
	DisplayFailure func(error) string
	// DisplayErrorCode classifies a display failure for operator diagnostics.
	DisplayErrorCode func(error) string
	// DisplayPermissionDenied converts a denied post-timeout permission
	// re-check into the host's typed display error.
	DisplayPermissionDenied func(tools.DisplayPermission) error
	// FailedContent recognizes host-structured failure content.
	FailedContent func(content string) bool
	// PageSightFailure renders the customer-safe page-sight failure.
	PageSightFailure func() string
	// DisplaySource and PageSightSource label operator diagnostics.
	DisplaySource   string
	PageSightSource string
}

// ToolExecutorRequest configures one session-owned executor. Timeout takes
// precedence; otherwise Policy selects a per-tool deadline; otherwise
// DefaultToolExecutionTimeout applies.
type ToolExecutorRequest struct {
	Inner        messages.ToolExecutor
	Timeout      time.Duration
	Policy       tools.InteractiveToolPolicy
	Lifecycle    ToolLifecycle
	Cancellation CancellationIntent
	Diagnostics  sessiontrace.ToolDiagnosticSink
	Presentation ToolPresentation
}

// InteractivePolicyRequest resolves one session policy. Nil Settings selects
// the runtime defaults. Browser broker operations are always admitted to the
// bounded long-running budget. Definitions beyond BaseDefinitions are dynamic
// page tools; with DynamicLongRunning, names outside the snapshot (page tools
// registered mid-session) inherit the same budget instead of fast/read.
type InteractivePolicyRequest struct {
	Settings           *tools.InteractiveToolPolicySettings
	Definitions        []messages.ToolDefinition
	BaseDefinitions    []messages.ToolDefinition
	DynamicLongRunning bool
}

// ToolService owns session tool execution and its latency policy.
type ToolService interface {
	// NewToolExecutor returns an executor that converts errors, panics, and
	// deadlines into correlated tool results with a nil Go error. Only an
	// operator SIGINT cancellation is returned as an error.
	NewToolExecutor(ToolExecutorRequest) messages.ToolExecutor
	ResolveInteractivePolicy(InteractivePolicyRequest) (tools.InteractiveToolPolicy, error)
}
