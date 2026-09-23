package sessionduration

import (
	"context"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio"
)

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
	Drain          func(context.Context) error
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

// ExecutionRequest supplies host effects around one duration invocation.
// The duration service validates before Prepare, selects the effective clock,
// invokes Run when present, and finishes Finalization after any returned
// preparation or invocation error. A nil Run means there is no provider
// invocation, while preparation and finalization still occur.
type ExecutionRequest struct {
	Context       context.Context
	Output        io.Writer
	MaxDuration   time.Duration
	Clock         TimerScheduler
	SourceClock   TimerScheduler
	FallbackClock TimerScheduler
	Prepare       func(context.Context, io.Writer) error
	Run           func(context.Context, io.Writer, TimerScheduler) error
	Finalization  FinalizationPorts
}
