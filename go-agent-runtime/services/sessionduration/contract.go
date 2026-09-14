// Package sessionduration defines the host-neutral terminal boundary for a
// bounded session. Provider observations, output projection, publication
// ports, and lifecycle error facts are explicit; host resources stay outside
// the contract.
package sessionduration

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type sessionDurationError string

func (e sessionDurationError) Error() string { return string(e) }

const (
	ErrInvalidDuration         sessionDurationError = "invalid session max duration"
	ErrMaxDurationExceeded     sessionDurationError = "session exceeded maximum duration"
	ErrProviderEmptyResponse   sessionDurationError = "silent provider returned an empty response"
	ErrProviderLivenessTimeout sessionDurationError = "silent provider response timed out"
	ErrSchedulerUnavailable    sessionDurationError = "session duration scheduler is required"
	ErrFinalizationPanic       sessionDurationError = "session finalization panicked"
)

// Timer is the timer contract shared by duration and liveness controllers.
// It is an alias so host clocks can be passed without an adapter.
type Timer = platformclock.Timer

// TimerScheduler is the minimal clock contract required by the controller.
// Context scheduling remains a host concern; the controller only owns timers.
type TimerScheduler interface {
	NewTimer(time.Duration) Timer
}

// InvalidDurationError preserves the stable validation identity and the
// offending value without importing a CLI validation package.
type InvalidDurationError struct{ Duration time.Duration }

func (e *InvalidDurationError) Error() string {
	if e == nil {
		return ErrInvalidDuration.Error()
	}
	return fmt.Sprintf("--max-duration must be non-negative, got %s", e.Duration)
}

func (e *InvalidDurationError) Unwrap() error { return ErrInvalidDuration }

// TerminalSource supplies the provider observation facts needed to distinguish
// a provider-authored close from a loop shutdown request.
type TerminalSource struct {
	Message func() (messages.StreamMessage, bool)
	Matches func(messages.StreamMessage) bool
}

// MessageWriter is an injected publication port. The service never opens or
// owns the underlying stream or artifact resource.
type MessageWriter func(messages.StreamMessage) error

// ArtifactWriter is the host-neutral artifact admission port.
type ArtifactWriter interface {
	Accept(messages.StreamMessage) error
}

// Publication describes the ordered artifact and output effects for one
// terminal message.
type Publication struct {
	Artifacts ArtifactWriter
	Write     MessageWriter
}

// ArtifactLifecycle is the bounded lifecycle needed during ordered
// finalization. Implementations remain host-owned; the controller only calls
// the explicit port methods.
type ArtifactLifecycle interface {
	ArtifactWriter
	Flush() error
	Close() error
}

// EventAdmission is the opaque admission boundary assembled by the service.
// Hosts pass it back to NewAdmissionInferencer without owning its state.
type EventAdmission interface{}

// AdmissionInferencer is the provider bridge that applies the event
// admission boundary before the session loop sees provider messages.
type AdmissionInferencer interface {
	messages.SessionInferencer
	CloseError() error
	RuntimeError() error
	WaitForClose()
	ProviderTerminalMessage() (messages.StreamMessage, bool)
	IsProviderTerminalMessage(messages.StreamMessage) bool
	CloseAdmission()
}

// AdmissionSession is the wrapped provider session exposed for host seams
// that need optional complete-message capabilities.
type AdmissionSession interface {
	messages.Session
	SendMessage(context.Context, messages.Message) bool
	SendMessageWithoutResponse(context.Context, messages.Message) bool
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}

// LivenessOptions describes one response-progress watchdog.
type LivenessOptions struct {
	Enabled bool
	Timeout time.Duration
}

// LivenessError carries bounded, credential-free provider facts while
// retaining a stable errors.Is identity for the failure class.
type LivenessError struct {
	Classification     string
	ResponseID         string
	TerminalReason     messages.TerminalReason
	TerminalProvenance messages.TerminalProvenance
	OutputState        messages.TerminalOutputState
	Usage              messages.TokenUsage
	Cause              error
}

func (e *LivenessError) Error() string {
	if e == nil {
		return "session liveness failure"
	}
	if e.Classification == "" {
		return "session liveness failure"
	}
	return fmt.Sprintf("%s: provider response produced no observable output", e.Classification)
}

func (e *LivenessError) Unwrap() error {
	if e == nil {
		return ErrProviderEmptyResponse
	}
	if e.Cause != nil {
		return e.Cause
	}
	if e.Classification == "silent_provider_timeout" {
		return ErrProviderLivenessTimeout
	}
	return ErrProviderEmptyResponse
}

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
	Context        context.Context
	Inferencer     messages.SessionInferencer
	Admission      AdmissionInferencer
	Clock          TimerScheduler
	LivenessClock  TimerScheduler
	MaxDuration    time.Duration
	Liveness       LivenessOptions
	Retry          RetryPolicy
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
}

// Result is the controller's terminal snapshot after cleanup.
type Result struct {
	OutputState     messages.TerminalOutputState
	TerminalWritten bool
	Expired         bool
}

// State owns one bounded-session observation and terminal-admission lifecycle.
type State interface {
	Observe(messages.StreamMessage)
	OutputState() messages.TerminalOutputState
	Admit(bool, messages.StreamMessage) (messages.StreamMessage, bool)
	PublishProviderTerminal(Publication) error
	PublishMaxDuration(Publication, messages.TerminalOutputState) error
	Written() bool
}

// Controller owns one session's mutable shutdown, admission, liveness, retry,
// and finalization state.
type Controller interface {
	// Start arms the configured max-duration timer. Begin starts controllers by
	// default; Run uses deferred start so loop construction remains observable
	// before an injected scheduler can block or fail.
	Start() error
	Observe(messages.StreamMessage) Admission
	// ObserveDrain admits output already published by the loop while the
	// bounded finalizer is draining. It is distinct from hot-path admission,
	// which rejects newly arriving output after expiry.
	ObserveDrain(messages.StreamMessage) Admission
	SetToolObligation(bool)
	BeginLocalToolExecution()
	EndLocalToolExecution()
	Errors() <-chan error
	LivenessFailure() error
	Expire() error
	Retry(RetryRequest) RetryDecision
	OutputState() messages.TerminalOutputState
	TerminalWritten() bool
	Finalize(context.Context, FinalizeRequest) (Result, error)
}

// ControllerService is the narrow dependency needed by a host session that
// delegates bounded-session policy to this service. The broader Service
// contract remains available to composition and artifact callers.
type ControllerService interface {
	Begin(Options) (Controller, error)
}

// LifecycleFailures contains independent shutdown causes. The service joins
// each non-nil cause while retaining errors.Is/As identity.
type LifecycleFailures struct {
	Runtime error
	Close   error
	Binding error
}

// Service owns terminal precedence, output projection, publication ordering,
// terminal synthesis, and normalized error composition.
type Service interface {
	Begin(Options) (Controller, error)
	Run(RunRequest) error
	NewFinalizer(FinalizationPorts) Finalizer
	NewState(TerminalSource) State
	PublishMaxDuration(Publication, messages.TerminalOutputState) error
	LifecycleError(LifecycleFailures) error
	TransportError(error) error
	ValidateDuration(time.Duration) error
	NewEventAdmission() EventAdmission
	NewAdmissionInferencer(messages.SessionInferencer, EventAdmission, chan struct{}) AdmissionInferencer
	NewAdmissionSession(context.Context, messages.Session, EventAdmission, func(error)) AdmissionSession
	WithArtifacts(context.Context, ArtifactLifecycle) context.Context
	ArtifactsFromContext(context.Context) ArtifactLifecycle
	WithTerminalRecorder(context.Context, TerminalRecorder) context.Context
	WithArtifactPaths(context.Context, SessionDurationArtifactPaths) context.Context
	PrepareArtifacts(context.Context) (context.Context, error)
	FinalizeArtifacts(ArtifactLifecycle) error
	EvaluateRetry(RetryPolicy, *messages.MessageEndValue) RetryDecision
	IsDurationShutdownMessage(messages.StreamMessage) bool
	IsDurationForwardMessage(messages.StreamMessage) bool
	RecordingTerminalSummaryFromMessage(messages.StreamMessage) (*transcript.RecordingTerminalSummary, bool, error)
}

// TerminalRecorder receives the normalized terminal summary emitted by a
// duration run. It is a host-owned persistence port.
type TerminalRecorder interface {
	RecordTerminalSummary(transcript.RecordingTerminalSummary) error
}

// SessionDurationArtifactPaths identifies the host paths for service-owned
// duration artifacts.
type SessionDurationArtifactPaths struct {
	AudioPath      string
	TranscriptPath string
}
