package sessionduration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

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
	var annotations []string
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
	// Execute validates and coordinates host startup effects, one duration
	// invocation, and ordered post-run finalization.
	Execute(ExecutionRequest) error
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
