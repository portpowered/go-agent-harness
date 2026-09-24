package session

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// DurationSessionLifecycle exposes the provider resource operations owned by a
// single bounded session invocation. The session runtime controls when these
// operations run and joins Done before returning.
type DurationSessionLifecycle interface {
	Done() <-chan struct{}
	Close() error
	DrainPlayback(context.Context) error
	Error() error
}

// DurationPublication is the invocation-local publication worker owned by the
// session runtime. The host supplies construction and transport translation.
type DurationPublication interface {
	MarkReady()
	Errors() <-chan error
	Stop()
}

// DurationCompletionOptions contains host observations needed to classify the
// completed duration invocation. The duration service combines them with the
// run result and observer facts after cleanup.
type DurationCompletionOptions struct {
	AudioOutputError              func() error
	AssistantResponseIncomplete   error
	ScheduledAudioIncompleteCause error
	BoundCancellation             <-chan struct{}
}

// DurationRunRequest contains normalized host facts and effect ports for one
// bounded session. It deliberately leaves policy assembly, completion
// classification, and all mutable invocation state to service internals.
type DurationRunRequest struct {
	Context                    context.Context
	Inferencer                 messages.SessionInferencer
	Admission                  sessionduration.AdmissionInferencer
	Clock                      sessionduration.TimerScheduler
	LivenessClock              sessionduration.TimerScheduler
	MaxDuration                time.Duration
	Prompt                     string
	PromptProvided             bool
	CloseAfterOpen             bool
	WaitForClose               bool
	RequireAssistantResponse   bool
	RequireTerminalReply       bool
	CloseAfterScheduledAudio   bool
	ScheduledAudioDispatch     sessionduration.ScheduledAudioDispatch
	AudioInput                 sessionduration.AudioInputPort
	AudioInterruptions         sessionduration.AudioInterruptionPort
	Observer                   sessionduration.RunObserver
	CompletionObserver         sessionduration.CompletionObserver
	SessionUpdatedTimeout      time.Duration
	SessionUpdatedTimeoutError error
	Effects                    sessionduration.RunEffects
	Publication                sessionduration.Publication
	Done                       <-chan struct{}
	DoneError                  func() error
	Completion                 DurationCompletionOptions
	ToolExecutor               messages.ToolExecutor
	ToolDefinitions            []messages.ToolDefinition
	AdvertiseToolDefinitions   bool
	InteractiveToolPolicy      tools.InteractiveToolPolicy
	Binding                    devices.RTCBinding
	StartPublication           func(context.Context, sessionduration.Loop) (DurationPublication, error)
	WrapInferencer             func(messages.SessionInferencer) (messages.SessionInferencer, DurationSessionLifecycle, error)
	ControllerReady            func(sessionduration.Controller)
	CloseProviderOnShutdown    bool
	QuiesceUpstream            func() error
	LoopReady                  func(sessionduration.Loop) error
	FinishObserver             func(error, bool) (error, bool)
	PublishUserCancellation    func() error
}

// DurationRunner runs one bounded session with invocation resource ownership
// kept behind the session service contract.
type DurationRunner interface {
	RunDuration(DurationRunRequest) (sessionduration.Result, error)
	// Execute runs a duration lifecycle whose effects are supplied by the host.
	Execute(sessionduration.ExecutionRequest) error
}
