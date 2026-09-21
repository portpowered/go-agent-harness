package sessionduration

import (
	"context"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audiosubsystem "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems/audio"
)

// RetryPolicy describes a bounded provider retry budget. Retry never sleeps;
// it returns a decision for the loop's injected scheduler.
type RetryPolicy struct {
	Enabled      bool
	MaxRetries   int
	DefaultDelay time.Duration
	MaxDelay     time.Duration
}

// Options creates one isolated controller. Context cancellation stops timing
// workers; no provider, filesystem, or device effect occurs here.
type Options struct {
	Context context.Context
	Clock   TimerScheduler
	// DeferStart leaves max-duration timer creation to Controller.Start. It is
	// used by Run to construct the host loop before scheduler effects occur.
	DeferStart bool
	// LivenessClock may use a different time domain from the max-duration
	// scheduler. When omitted, Clock remains the liveness scheduler.
	LivenessClock TimerScheduler
	MaxDuration   time.Duration
	Liveness      LivenessOptions
	Retry         RetryPolicy
	Terminal      TerminalSource
	Publication   Publication
	Artifacts     ArtifactLifecycle
	FirstCause    func(error)
}

// Admission is the controller's response-boundary decision. Rejected
// nonterminal messages are never forwarded to the loop or artifacts.
type Admission struct {
	Message      messages.StreamMessage
	Accepted     bool
	OutputState  messages.TerminalOutputState
	LivenessErr  error
	TerminalSeen bool
}

// RetryRequest contains a provider terminal candidate.
type RetryRequest struct{ Terminal *messages.MessageEndValue }

// RetryDecision contains only bounded eligibility data. The caller owns the
// actual wait and response.create dispatch.
type RetryDecision struct {
	Delay     time.Duration
	Eligible  bool
	Exhausted bool
}

// FinalizeRequest supplies ordered cleanup ports owned by the host.
type FinalizeRequest struct {
	Primary     error
	Quiesce     func() error
	DrainLoop   Loop
	DrainPolicy DrainPolicy
	Drain       func(context.Context) error
	Close       func() error
	Binding     func() error
	Artifacts   ArtifactLifecycle
}

// DrainPolicy bounds the quiet-period observation window used while a loop is
// shutting down. Clock is injected for deterministic callers; the service
// supplies bounded defaults for omitted durations.
type DrainPolicy struct {
	Clock       TimerScheduler
	QuietPeriod time.Duration
	WallSafety  time.Duration
	// Pending reports service work that still needs loop output to settle, such
	// as an accepted provider tool result awaiting its continuation response.
	// WallSafety remains the final bound even while this callback returns true.
	Pending func() bool
	// LoopJoinTimeout bounds joining the loop after close and cancellation.
	// Zero selects the service default.
	LoopJoinTimeout time.Duration
}

// FinalizationPorts are already-admitted host cleanup operations. The service
// owns their order, panic recovery, and once-only execution; hosts only expose
// individual resource effects.
type FinalizationPorts struct {
	CloseCapabilities func() error
	CloseSession      func() error
	CloseBinding      func() error
	CloseRuntime      func() error
	FlushCapture      func() error
	Finalize          func(context.Context, io.Writer) error
	ReleaseCapture    func() error
}

// Finalizer is the standalone ordered cleanup boundary used by session modes
// that do not need a running duration controller.
type Finalizer interface {
	SetDeviceBinding(func() error)
	Finish(context.Context, io.Writer, error) error
}

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

// ScheduledInputSender is the transport effect needed to deliver one
// admitted scheduled audio input. The duration service owns dispatch timing;
// hosts implement only the ordered send operations.
type ScheduledInputSender interface {
	SendAudioInput(context.Context, []byte) error
	SessionEventSender
}
