package sessionduration

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audiosubsystem "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems/audio"
)

// Loop is the host-neutral portion of a running session loop needed by the
// bounded execution service. The host constructs the loop through LoopFactory;
// the service owns admission, deadline, terminal, drain, and finalization
// ordering around it.
type Loop interface {
	Run(context.Context) error
	Deltas() *messages.TypedBuffer[messages.StreamMessage]
	Send(context.Context, []messages.Message) error
}

// DuplexToolAcknowledgementPolicy is the immutable tool-class snapshot used
// to configure an agent loop. The execution service turns the names into its
// native lookup without making a policy decision.
type DuplexToolAcknowledgementPolicy struct {
	Threshold            time.Duration
	LongRunningToolNames []string
}

// DuplexLoopOptions contains host-neutral values needed to construct the
// session agent loop. It carries already-admitted capabilities and audio
// buffer ports; it does not accept agentloop options or CLI runtime types.
type DuplexLoopOptions struct {
	AudioPorts                *audiosubsystem.Ports
	ToolExecutor              messages.ToolExecutor
	ToolDefinitions           []messages.ToolDefinition
	AdvertiseToolDefinitions  bool
	ToolAcknowledgementPolicy *DuplexToolAcknowledgementPolicy
}

// DuplexLoopFactory constructs a session loop under the session execution
// service. Duration policy remains owned by the sessionduration service.
type DuplexLoopFactory interface {
	Build(context.Context, messages.SessionInferencer, DuplexLoopOptions) (Loop, error)
}

// SessionEventSender is the transport edge used by service-owned retry
// scheduling. Implementations translate one provider session event into their
// loop's native control path; the duration service owns eligibility, timing,
// cancellation, and dispatch ordering.
type SessionEventSender interface {
	SendSessionEvent(context.Context, messages.StreamMessage) error
}

// MessageResult tells the duration service whether the host's ordinary
// session completion rules selected a terminal boundary for the message.
type MessageResult struct {
	Stop    bool
	Planned bool
}

// MessageHandler is the narrow host callback for session-specific prompt,
// tool, and scheduled-input behavior. It cannot bypass controller admission:
// the service invokes it only for an admitted message.
type MessageHandler func(context.Context, Loop, Controller, messages.StreamMessage) (MessageResult, error)

// LoopFactory constructs one loop around the service-owned admission bridge.
// The controller is supplied so host tool adapters can report local execution
// boundaries to the same service-owned liveness state.
type LoopFactory func(context.Context, AdmissionInferencer, Controller) (Loop, error)

// RunRequest describes one bounded session execution. Resource callbacks are
// explicit ports so construction remains inert and the service retains the
// shutdown order without importing a host or transport package.
type RunRequest struct {
	Context       context.Context
	Inferencer    messages.SessionInferencer
	Admission     AdmissionInferencer
	Clock         TimerScheduler
	LivenessClock TimerScheduler
	MaxDuration   time.Duration
	Liveness      LivenessOptions
	Retry         RetryPolicy
	// RetryDispatched observes a successfully sent retry control for tracing.
	// It must not make policy decisions or perform another transport write.
	RetryDispatched func(messages.StreamMessage)
	Terminal        TerminalSource
	Publication     Publication
	Artifacts       ArtifactLifecycle
	LoopFactory     LoopFactory
	Handle          MessageHandler
	Drain           func(context.Context, Loop, Controller) error
	DrainPolicy     DrainPolicy
	Close           func() error
	Binding         func() error
	ExternalErrors  <-chan error
	Wake            <-chan struct{}
	OnWake          func(context.Context, Loop, Controller) error
	Done            <-chan struct{}
	DoneError       func() error
}

// Result is the controller's terminal snapshot after cleanup.
type Result struct {
	OutputState     messages.TerminalOutputState
	TerminalWritten bool
	Expired         bool
}
