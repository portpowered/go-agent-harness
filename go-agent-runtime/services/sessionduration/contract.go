// Package sessionduration defines the host-neutral terminal boundary for a
// bounded session. Provider observations, output projection, publication
// ports, and lifecycle error facts are explicit; host resources stay outside
// the contract.
package sessionduration

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audiosubsystem "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type sessionDurationError string

func (e sessionDurationError) Error() string { return string(e) }

const (
	ErrInvalidDuration                  sessionDurationError = "invalid session max duration"
	ErrMaxDurationExceeded              sessionDurationError = "session exceeded maximum duration"
	ErrProviderEmptyResponse            sessionDurationError = "silent provider returned an empty response"
	ErrProviderLivenessTimeout          sessionDurationError = "silent provider response timed out"
	ErrAssistantResponseIncomplete      sessionDurationError = "audio session ended before the final assistant response"
	ErrScheduledAudioIncomplete         sessionDurationError = "scheduled audio session ended before all turns completed"
	ErrSchedulerUnavailable             sessionDurationError = "session duration scheduler is required"
	ErrFinalizationPanic                sessionDurationError = "session finalization panicked"
	LivenessClassificationEmptyResponse                      = "silent_provider_empty_response"
	LivenessClassificationTimeout                            = "silent_provider_timeout"
	// MaxDurationReason is the stable terminal reason published when the
	// duration controller ends a run at its configured bound.
	MaxDurationReason messages.TerminalReason = "max_duration"
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
	// SessionAdmissionClosed and SessionAdmissionAllows preserve an optional
	// outer transport admission boundary through duration event wrapping.
	SessionAdmissionClosed() bool
	SessionAdmissionAllows(messages.StreamMessage) bool
	SessionAdmissionAllowsCompleteMessage(messages.Message) bool
	SendMessage(context.Context, messages.Message) bool
	SendMessageWithoutResponse(context.Context, messages.Message) bool
	SupportsCompleteMessages() bool
	SupportsCompleteMessagesWithoutResponse() bool
}

// PlaybackDrainer is an optional session capability used during bounded
// finalization. Admission wrappers preserve it so accepted device playback
// can drain before the binding closes.
type PlaybackDrainer interface {
	DrainPlayback(context.Context) error
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
	FailingEvent       messages.StreamMessageType
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
	if e.Classification == LivenessClassificationTimeout {
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

// RunState contains the mutable session-loop decisions held by the duration
// runner while one invocation is active. Handlers receive a copy and return
// any updated value through MessageResult; they must not retain the value.
type RunState struct {
	promptSent            bool
	closeSent             bool
	closeAfterOpenPending bool
	drainPlayback         bool
	awaitingResponse      bool
}

// PromptSent reports whether the opening prompt was sent.
func (s RunState) PromptSent() bool { return s.promptSent }

// CloseSent reports whether a session close control was sent.
func (s RunState) CloseSent() bool { return s.closeSent }

// CloseAfterOpenPending reports whether a session close is waiting for readiness.
func (s RunState) CloseAfterOpenPending() bool { return s.closeAfterOpenPending }

// DrainPlayback reports whether finalization should drain session playback.
func (s RunState) DrainPlayback() bool { return s.drainPlayback }

// AwaitingResponse reports whether the last accepted end-of-turn dispatch is
// still waiting for provider output.
func (s RunState) AwaitingResponse() bool { return s.awaitingResponse }

// WithPromptSent returns a state recording the opening prompt send.
func (s RunState) WithPromptSent() RunState {
	s.promptSent = true
	return s
}

// WithCloseSent returns a state with the close-control result.
func (s RunState) WithCloseSent(value bool) RunState {
	s.closeSent = value
	return s
}

// WithCloseAfterOpenPending returns a state with the pending-close decision.
func (s RunState) WithCloseAfterOpenPending(value bool) RunState {
	s.closeAfterOpenPending = value
	return s
}

// WithDrainPlayback returns a state requesting bounded playback drain.
func (s RunState) WithDrainPlayback() RunState {
	s.drainPlayback = true
	return s
}

// WithAwaitingResponse returns a state recording provider response wait.
func (s RunState) WithAwaitingResponse(value bool) RunState {
	s.awaitingResponse = value
	return s
}

// MessageResult tells the duration service whether the host's ordinary
// session completion rules selected a terminal boundary for the message.
type MessageResult struct {
	Stop    bool
	Planned bool
	State   *RunState
}

// ScheduledAudioDispatch selects when an already-admitted scheduled input may
// be dispatched. The duration service owns the boundary decision; a host only
// supplies the effect that sends the selected input.
type ScheduledAudioDispatch string

const (
	ScheduledAudioCompletionGated ScheduledAudioDispatch = "completion-gated"
	ScheduledAudioActiveResponse  ScheduledAudioDispatch = "active-response"
)

// RunPolicy contains the immutable, normalized session completion settings
// used by the duration runner. It contains values, not decisions or mutable
// invocation state.
type RunPolicy struct {
	Prompt                           string
	PromptProvided                   bool
	CloseAfterOpen                   bool
	WaitForClose                     bool
	HasAudioInput                    bool
	RequireAssistantResponse         bool
	RequireTerminalAssistantResponse bool
	CloseAfterScheduledAudio         bool
	ScheduledAudioDispatch           ScheduledAudioDispatch
}

// RunFacts are host observations used by service-owned run policy. These
// callbacks must report facts only; they must not decide whether the run
// continues or dispatch another input.
type RunFacts struct {
	LastMessageEndAdmitted              func() bool
	HasToolLifecycleObligation          func() bool
	HasTerminalToolContinuationFailure  func() bool
	HasTerminalScheduledResponseFailure func() bool
	AssistantResponseCompleted          func() bool
	ProviderToolCallObserved            func() bool
	ScheduledAudioComplete              func() bool
	ScheduledAudioAwaitingConfiguration func() bool
	ScheduledAudioReady                 func() bool
}

// RunObserver exposes session-owned observations to the bounded execution
// service. Implementations report facts only; duration policy and the mutable
// controller state remain service-owned.
type RunObserver interface {
	Active() bool
	RunFacts() RunFacts
	ToolLifecycleEvents() <-chan struct{}
	SessionUpdatedPending() bool
	SessionUpdatedReady() bool
	EnrichLifecycleError(error) error
	RetryDispatched(messages.StreamMessage)
	ObserveStreamMessage(messages.StreamMessage)
	NoteUserTextInput(string)
	SetToolResultsEnabled(bool)
	SetDurationController(Controller)
	DispatchScheduledInputs(context.Context, ScheduledInputSender) error
}

// WakeResult carries facts observed while the host handles invocation-only
// wake sources such as an audio reader or an event-driven input queue.
type WakeResult struct {
	AudioInputCompleted bool
	AudioInputError     error
}

// RunEffects expose resource operations around service-owned message and
// shutdown policy. They are called only after the service selects the
// corresponding boundary.
type RunEffects struct {
	SessionCreated          func(context.Context, Loop) error
	SessionOpened           func(context.Context, Loop) error
	AwaitFirstTurn          func(context.Context) error
	NoteUserTextInput       func(string)
	DispatchScheduledInputs func(context.Context, Loop) error
	OnWake                  func(context.Context, Loop) (WakeResult, error)
}

// MessageHandler is the narrow host callback for session-specific prompt,
// tool, and scheduled-input behavior. It cannot bypass controller admission:
// the service invokes it only for an admitted message.
type MessageHandler func(context.Context, Loop, Controller, messages.StreamMessage, RunState) (MessageResult, error)

// DrainHandler receives the runner-owned terminal state after the final
// message and before loop cancellation.
type DrainHandler func(context.Context, Loop, Controller, RunState) error

// WakeHandler handles service wakeups using a runner-owned state snapshot.
type WakeHandler func(context.Context, Loop, Controller, RunState) (RunState, error)

// SessionUpdatedWait asks the runner to bound the acknowledgement after an
// admitted session-open event. Pending and Ready expose host-observed facts;
// timer ownership and timeout delivery remain with the service.
type SessionUpdatedWait struct {
	Timeout      time.Duration
	Pending      func() bool
	Ready        func() bool
	TimeoutError error
}

// LoopFactory constructs one loop around the service-owned admission bridge.
// The controller is supplied so host tool adapters can report local execution
// boundaries to the same service-owned liveness state.
type LoopFactory func(context.Context, AdmissionInferencer, Controller) (Loop, error)

// AudioInputPort supplies one session's local audio reader. The implementation
// owns only the source-specific binding and read operation; the duration
// service owns the worker, cancellation, wake signal, result collection, and
// shutdown join.
type AudioInputPort struct {
	BindContext func(context.Context)
	Run         func(context.Context, Loop) error
}

// AudioInterruptionPort admits one finite audio turn after an external wake.
// The source pump, pending-item bound, wake signal, and worker join belong to
// the duration service; Dispatch is the host's ordered audio-send effect.
type AudioInterruptionPort struct {
	Source   <-chan audioio.ScheduledAudioInput
	Dispatch func(context.Context, Loop, audioio.ScheduledAudioInput) error
}

// RunRequest describes one bounded session execution. Resource callbacks are
// explicit ports so construction remains inert and the service retains the
// shutdown order without importing a host or transport package.
type RunRequest struct {
	Context            context.Context
	Inferencer         messages.SessionInferencer
	Admission          AdmissionInferencer
	Clock              TimerScheduler
	LivenessClock      TimerScheduler
	MaxDuration        time.Duration
	AudioInput         AudioInputPort
	AudioInterruptions AudioInterruptionPort
	// AwaitingResponseOnCancel seeds response wait for sessions whose opening
	// dispatch is initiated outside the duration loop handler.
	AwaitingResponseOnCancel bool
	// MaxDurationExpired returns any lifecycle failure that must survive the
	// service's bounded terminal cause.
	MaxDurationExpired func() error
	Liveness           LivenessOptions
	Retry              RetryPolicy
	// RetryDispatched observes a successfully sent retry control for tracing.
	// It must not make policy decisions or perform another transport write.
	RetryDispatched func(messages.StreamMessage)
	// Quiesce stops host-owned producers before draining accepted loop and
	// playback work. The service invokes it once at the start of shutdown.
	Quiesce        func() error
	Terminal       TerminalSource
	Publication    Publication
	Artifacts      ArtifactLifecycle
	LoopFactory    LoopFactory
	Handle         MessageHandler
	Drain          DrainHandler
	DrainPolicy    DrainPolicy
	Close          func() error
	Binding        func() error
	ExternalErrors <-chan error
	// ExternalErrorSources is evaluated after LoopFactory returns, when host
	// resource error channels become available. The duration service owns the
	// bounded fan-in and stops its forwarding workers with Context.
	ExternalErrorSources func() []<-chan error
	Wake                 <-chan struct{}
	// WakeSources are independent host observations that wake one invocation.
	// The duration service coalesces them into its bounded runner signal.
	WakeSources []<-chan struct{}
	OnWake      WakeHandler
	Done        <-chan struct{}
	// DoneSources close the run when any host-owned completion signal closes.
	DoneSources    []<-chan struct{}
	DoneError      func() error
	SessionUpdated SessionUpdatedWait
	Policy         RunPolicy
	Facts          RunFacts
	Effects        RunEffects
	// Observer supplies diagnostic facts used by the duration policy. The
	// service derives Facts, observer wakeups, readiness observations, liveness
	// and retry enablement from its presence.
	Observer RunObserver
}

// Result is the controller's terminal snapshot after cleanup.
type Result struct {
	OutputState     messages.TerminalOutputState
	TerminalWritten bool
	Expired         bool
}

// CompletionRequest contains host observations for the final response and
// scheduled-input boundaries. The duration service decides which failures
// apply and preserves their causes while composing the final result.
type CompletionRequest struct {
	RunError                      error
	AudioOutputError              func() error
	AssistantResponseIncomplete   error
	ScheduledAudioIncompleteCause error
	HasAudioInput                 bool
	RequireAssistantResponse      bool
	RequireTerminalAssistantReply bool
	ProviderToolCallObserved      bool
	AssistantResponseCompleted    bool
	RoomBoundCancellation         bool
	DurationExpired               bool
	CloseAfterScheduledAudio      bool
	ScheduledAudioIncomplete      bool
	ScheduledAudioCompleted       int
	ScheduledAudioDispatched      int
	ScheduledAudioCount           int
	ProviderScheduledStatus       string
	ProviderScheduledErrorCode    string
	ProviderScheduledErrorDetails string
	// Observer supplies completion facts from the active session. Completion
	// policy reads these facts inside the service instead of being assembled by
	// a transport caller.
	Observer CompletionObserver
	// BoundCancellation is checked by the service at completion time so the
	// cancellation classification remains part of session-duration policy.
	BoundCancellation <-chan struct{}
}

// CompletionFacts are observations needed to classify a bounded session's
// final response and scheduled-audio status.
type CompletionFacts struct {
	ProviderToolCallObserved     bool
	AssistantResponseCompleted   bool
	ScheduledAudioIncomplete     bool
	ScheduledAudioCompleted      int
	ScheduledAudioDispatched     int
	ScheduledAudioCount          int
	ProviderScheduledStatus      string
	ProviderScheduledErrorCode   string
	ProviderScheduledErrorDetail string
}

// CompletionObserver supplies final response and scheduled-audio facts to
// the duration service without transferring ownership of observation state.
type CompletionObserver interface {
	Active() bool
	CompletionFacts() CompletionFacts
}

// ScheduledAudioIncompleteError carries the deterministic schedule counters
// and bounded provider metadata observed at a terminal boundary.
type ScheduledAudioIncompleteError struct {
	Completed         int
	Dispatched        int
	Scheduled         int
	ProviderStatus    string
	ProviderErrorCode string
	ProviderDetails   string
	Cause             error
}

func (e *ScheduledAudioIncompleteError) Error() string {
	if e == nil {
		return ErrScheduledAudioIncomplete.Error()
	}
	message := fmt.Sprintf("%s: completed=%d dispatched=%d scheduled=%d", ErrScheduledAudioIncomplete, e.Completed, e.Dispatched, e.Scheduled)
	annotations := make([]string, 0, 3)
	if status := strings.TrimSpace(e.ProviderStatus); status != "" {
		annotations = append(annotations, "status="+status)
	}
	if code := strings.TrimSpace(e.ProviderErrorCode); code != "" {
		annotations = append(annotations, "code="+code)
	}
	if detail := strings.TrimSpace(e.ProviderDetails); detail != "" {
		annotations = append(annotations, "detail="+detail)
	}
	if len(annotations) > 0 {
		message += " (" + strings.Join(annotations, "; ") + ")"
	}
	return message
}

func (e *ScheduledAudioIncompleteError) Unwrap() error {
	if e != nil && e.Cause != nil {
		return e.Cause
	}
	return ErrScheduledAudioIncomplete
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
	// ExpectProviderProgress arms the response watchdog after the host admits a
	// response-producing provider dispatch such as a prompt or audio commit.
	ExpectProviderProgress()
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
	// RunWithResult returns the immutable terminal snapshot produced by the
	// service-owned controller along with the run error.
	RunWithResult(RunRequest) (Result, error)
	Complete(CompletionRequest) error
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
