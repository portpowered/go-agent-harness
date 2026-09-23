package sessiontrace

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// RuntimeRecorder owns ordered runtime observations for one invocation. The
// recorder is intentionally host-neutral; callers provide provider/device
// boundary values and never construct its mutable state themselves.
type RuntimeRecorder interface {
	EnableProviderBoundaryObservations()
	ObservesProviderBoundaries() bool
	Observe(SessionRuntimeObservationKind, []byte, int, bool, error)
	ObserveWithInputCommit(SessionRuntimeObservationKind, []byte, int, int, bool, error)
	ObserveFinal(SessionRuntimeObservationKind, []byte, int, int, bool, error, *SessionFinalAccounting)
	AudioOutputMessage([]byte, messages.StreamMessage)
	AudioPlaybackReceipt(audio.PlaybackReceipt)
	AudioInput([]byte)
	ProviderAudioSent([]byte)
	InputCommit()
	ProviderInputCommit()
	ResponseCreate(messages.StreamMessage)
	TurnCompleted(int)
	TerminalWithAccounting(int, error, *SessionFinalAccounting)
	ObserveToolCall(messages.ToolCall)
	ObserveToolResult(messages.ToolCall, messages.ToolCallResponse, bool)
}

// ObserverState is a detached view of the session facts needed by terminal
// and room callers. It prevents callers from reaching mutable service state.
type ObserverState struct {
	Provider                 string
	Model                    string
	SawSessionOpen           bool
	TurnsCompleted           int
	ScheduledInputs          int
	DispatchedInputs         int
	CompletedScheduled       int
	ActiveResponse           bool
	ActiveResponseID         string
	AssistantResponseDone    bool
	AssistantOutputObserved  bool
	ResponseOutputTextBytes  uint64
	ResponseOutputAudioBytes uint64
	OutputAudioBytes         uint64
	OutputTextBytes          uint64
	OutputToolBytes          uint64
	InputAudioBytes          uint64
	InputTextBytes           uint64
	RoomInputAudioBytes      uint64
	UserCancelled            bool
	RoomBoundCancellation    bool
	Failure                  *FailureState
}

// TerminalService is the narrow terminal projection needed by the observer.
// Full terminal formatting and reporting remain available through the public
// sessionterminal.Service contract without making observer tests or hosts
// implement unrelated methods.
type TerminalService interface {
	Finalize(sessionterminal.Request) sessionterminal.Result
	CancellationOutputState(sessionterminal.OutputSnapshot) messages.TerminalOutputState
}

type FailureState struct {
	Classification string
	TerminalReason string
	Provenance     string
	OutputState    string
	ErrorType      string
	Code           string
	FailingEvent   string
}

// Observer is the service-owned session stream observer. Mutable lifecycle,
// accounting, liveness, and failure state live behind the sessiontrace Wire.
type Observer interface {
	Observe(messages.StreamMessage)
	ScheduleAudioInputs([]ScheduledAudioInput)
	DispatchScheduledInputs(context.Context, ScheduledInputSender) error
	Finish(error) error
	CancellationClean(error) bool
	State() ObserverState
	SetRuntimeRecorder(RuntimeRecorder)
	SetStreamObserver(StreamObserver)
	SetAdmittedTurnObserver(StreamObserver)
	SetTurnAdmission(func(messages.StreamMessage) bool)
	SetCancellationIntent(CancellationIntent)
	SetRequireSessionUpdated(bool)
	SetScheduledAudioDispatch(ScheduledAudioDispatchPolicy)
	SetLivenessClock(LivenessClock)
	SetLivenessObserver(func(error))
	SetToolResultsEnabled(bool)
	SetTerminalObserver(func(TerminalObservation) bool)
	SetFailureObserver(func(TerminalObservation))
	AccountRoomAudioInput(int)
	LockProviderBoundary() func()
	MarkRoomBoundCancellation()
	NotifyTerminalObservation(TerminalObservation) bool
	NotifyFailureObservation(TerminalObservation) bool
	LivenessFailure() error
	LivenessEvents() <-chan struct{}
	LivenessErrors(context.Context) <-chan error
	StopLiveness()
	ArmProviderProgress()
	ResetProviderProgress()
	ObserveProviderEvent(messages.StreamMessage)
	ObserveProviderDispatch(messages.StreamMessage)
	ScheduledAudioReady() bool
	ScheduledAudioAwaitingConfiguration() bool
	ScheduledAudioComplete() bool
	ScheduledAudioIncomplete() bool
	ScheduledAudioCounts() (completed, dispatched, scheduled int)
	ScheduledAudioFailureMetadata() (status, code, details string)
	HasTerminalToolContinuationFailure() bool
	HasTerminalScheduledResponseFailure() bool
	LastMessageEndAdmitted() bool
	HasToolLifecycleObligation() bool
	NextScheduledResponse() (ScheduledAudioInput, int, bool)
	ClaimScheduledRateLimitRetry(string, *messages.MessageEndValue) (time.Duration, bool)
	NoteUserTextInput(string)
	NoteProviderUsage(messages.TokenUsage)
	CompleteTurn()
	SetToolResultsEnabledForObservation(bool)
	Account(metrics.Direction, metrics.Modality, int)
	ObserveProviderToolCall(*messages.ToolCallEndValue)
	ObserveProviderToolCallWithID(string, string)
	NoteToolResultAccepted(string)
	NoteToolResultRejected(string, messages.SessionSendOutcome)
	NoteToolContinuationRequested()
	NoteToolContinuationRequestedFor(string)
	ToolLifecycleEvents() <-chan struct{}
	ObserveBufferedProviderToolLifecycle([]messages.StreamMessage)
	HasUnresolvedToolCalls() bool
	UnresolvedToolCallIDs() []string
	UnresolvedToolResultSendStatuses() map[string]messages.SessionSendStatus
	PendingToolContinuationCallIDs() []string
	PendingNonImageToolContinuationSnapshot() ([]string, map[string]string, map[string]string, map[string]string)
	PendingImageContinuationCallIDs() []string
	PendingImageContinuationSnapshot() ([]string, map[string]string, map[string]string, map[string]string)
	PendingContinuationMetadata() (map[string]string, map[string]string, map[string]string)
	ContinuationResultAccepted(string) bool
	HasPendingToolContinuations() bool
	HasPendingImageContinuations() bool
	BeginLocalToolExecution()
	EndLocalToolExecution()
	ProviderToolCallObserved() bool
	AssistantResponseCompleted() bool
	UserCancellationOutputState() messages.TerminalOutputState
	ObserveSilentProviderEmptyResponse(messages.StreamMessage, *messages.MessageEndValue, bool, bool)
}

// NewObserverOptions contains boundary callbacks and copied request values.
type NewObserverOptions struct {
	Sink                   DiagnosticSink
	Recorder               metrics.Recorder
	Provider               string
	Model                  string
	StreamObserver         StreamObserver
	AdmittedTurnObserver   StreamObserver
	TurnAdmission          func(messages.StreamMessage) bool
	RuntimeRecorder        RuntimeRecorder
	TerminalService        TerminalService
	CancellationIntent     CancellationIntent
	LivenessClock          LivenessClock
	RequireSessionUpdated  bool
	ScheduledAudioDispatch ScheduledAudioDispatchPolicy
}
