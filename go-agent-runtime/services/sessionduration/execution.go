package sessionduration

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
	// RetryClaim, when present, replaces the Retry policy with a host-observed
	// eligibility fact for one admitted provider terminal. The service still
	// owns the bounded wait, its interruption, and the dispatch.
	RetryClaim     func(responseID string, terminal *messages.MessageEndValue) (time.Duration, bool)
	Terminal       TerminalSource
	Publication    Publication
	Artifacts      ArtifactLifecycle
	LoopFactory    LoopFactory
	Handle         MessageHandler
	Drain          func(context.Context, Loop, Controller) error
	DrainPolicy    DrainPolicy
	Close          func() error
	Binding        func() error
	ExternalErrors <-chan error
	Wake           <-chan struct{}
	OnWake         func(context.Context, Loop, Controller) error
	Done           <-chan struct{}
	DoneError      func() error
	// ExternalErrorSources and DoneSources are evaluated once after
	// LoopFactory returns, when host resource signals exist. The service owns
	// their bounded fan-in and stops forwarding when the run ends.
	ExternalErrorSources func() []<-chan error
	DoneSources          func() []<-chan struct{}
	// SessionUpdated bounds a configuration acknowledgement after an admitted
	// SESSION.OPEN on the Clock scheduler.
	SessionUpdated SessionUpdatedWait
	// AwaitAdmissionClose waits for the admitted provider session to finish
	// closing after the loop joins, so a provider terminal published while
	// closing is retained. Hosts set it when their loop closes its session.
	AwaitAdmissionClose bool
	// Completion observes the finalized snapshot and the joined run result.
	// Its error is joined with that result.
	Completion func(Result, error) error
}

// Result is the controller's terminal snapshot after cleanup.
type Result struct {
	OutputState     messages.TerminalOutputState
	TerminalWritten bool
	Expired         bool
}
