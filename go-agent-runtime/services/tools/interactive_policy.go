package tools

import (
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// InteractiveToolClass determines which interactive budget applies to a
// resolved tool call.
type InteractiveToolClass string

const (
	// InteractiveToolClassFastRead is the safe default for read-shaped and
	// unknown calls.
	InteractiveToolClassFastRead InteractiveToolClass = "fast/read"
	// InteractiveToolClassBoundedLongRunning is reserved for operations that
	// are intentionally allowed to outlive the fast/read budget.
	InteractiveToolClassBoundedLongRunning InteractiveToolClass = "bounded-long-running"
)

const (
	// DefaultInteractiveFastReadTimeout is the default deadline for a
	// read-shaped tool in a voice or realtime session.
	DefaultInteractiveFastReadTimeout = 5 * time.Second
	// DefaultInteractiveLongRunningTimeout bounds an admitted long-running
	// operation while leaving time for a grounded continuation before the
	// session's 30-second silence budget is exhausted.
	DefaultInteractiveLongRunningTimeout = 20 * time.Second
	// DefaultInteractiveAcknowledgementThreshold is when a pending
	// long-running operation becomes eligible for a spoken acknowledgement.
	DefaultInteractiveAcknowledgementThreshold = 2 * time.Second
	// InteractiveFastReadTimeoutLimit is exclusive: fast/read values must be
	// positive and strictly below ten seconds.
	InteractiveFastReadTimeoutLimit = 10 * time.Second
	// InteractiveLongRunningTimeoutLimit is exclusive so a bounded operation
	// cannot consume the complete 30-second no-acknowledgement budget.
	InteractiveLongRunningTimeoutLimit = 30 * time.Second
	// InteractiveAcknowledgementThresholdLimit is inclusive. Keeping this
	// threshold at or below two seconds preserves the voice patience contract.
	InteractiveAcknowledgementThresholdLimit = 2 * time.Second
)

// InteractiveToolPolicySettings contains the latency policy for
// voice/realtime tool calls. It is a runtime value contract and deliberately
// does not depend on a host configuration package.
type InteractiveToolPolicySettings struct {
	FastReadTimeout          time.Duration
	LongRunningTimeout       time.Duration
	AcknowledgementThreshold time.Duration
}

// InteractiveToolPolicyRequest is the host-neutral input to policy
// resolution. Hosts normalize configuration and supply explicit names for
// any remote or otherwise known long-running tools; the runtime does not
// import those hosts' transport or browser packages.
type InteractiveToolPolicyRequest struct {
	Settings                 InteractiveToolPolicySettings
	Definitions              []messages.ToolDefinition
	BaseDefinitions          []messages.ToolDefinition
	ExplicitLongRunningNames []string
	DynamicLongRunning       bool
}

// InteractiveToolPolicy is an immutable, request-scoped snapshot of
// interactive tool budgets and classifications.
type InteractiveToolPolicy interface {
	Settings() InteractiveToolPolicySettings
	ClassForTool(name string) InteractiveToolClass
	TimeoutForTool(name string) time.Duration
	Clone() InteractiveToolPolicy
	Validate() error
}

// InteractiveToolPolicyFactory resolves policies without exposing the
// classification map or its implementation package to callers.
type InteractiveToolPolicyFactory interface {
	Resolve(request InteractiveToolPolicyRequest) (InteractiveToolPolicy, error)
	ValidateSettings(settings InteractiveToolPolicySettings) error
}
