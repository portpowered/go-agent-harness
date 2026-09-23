package observer

import (
	"context"
	"sort"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
)

// NewObserver constructs the one invocation-scoped observer owned by the
// sessiontrace service. Callers can configure callbacks and immutable request
// values through the public options, but cannot construct or mutate the
// recorder, lifecycle reducer, or failure state directly.
func NewObserver(options sessiontrace.NewObserverOptions) sessiontrace.Observer {
	observer := newObserverState(options.Sink, options.Recorder, options.Provider, options.Model)
	observer.streamObserver = options.StreamObserver
	observer.admittedTurnObserver = options.AdmittedTurnObserver
	observer.turnAdmission = options.TurnAdmission
	observer.runtime = options.RuntimeRecorder
	observer.terminal = options.TerminalService
	observer.cancellationIntent = options.CancellationIntent
	observer.requireSessionUpdated = options.RequireSessionUpdated
	observer.scheduledAudioDispatch = ScheduledAudioDispatchPolicy(options.ScheduledAudioDispatch)
	if options.LivenessClock != nil {
		observer.setLivenessClock(options.LivenessClock)
	}
	return observer
}

func (o *observerState) Observe(msg messages.StreamMessage) { o.observe(msg) }
func (o *observerState) ScheduleAudioInputs(inputs []sessiontrace.ScheduledAudioInput) {
	o.scheduleAudioInputs(inputs)
}
func (o *observerState) DispatchScheduledInputs(ctx context.Context, sender sessiontrace.ScheduledInputSender) error {
	return o.dispatchScheduledInputs(ctx, sender)
}
func (o *observerState) Finish(err error) error { return o.finish(err) }
func (o *observerState) CancellationClean(err error) bool {
	return sessionSIGINTCleanForObserver(err, o.cancellationIntent, o)
}

func (o *observerState) State() sessiontrace.ObserverState {
	if o == nil {
		return sessiontrace.ObserverState{}
	}
	o.lifecycleProjectionMu.Lock()
	state := sessiontrace.ObserverState{
		Provider:           o.provider,
		Model:              o.model,
		SawSessionOpen:     o.sawSessionOpen,
		TurnsCompleted:     o.turnsCompleted,
		ScheduledInputs:    o.scheduledInputs,
		DispatchedInputs:   o.dispatchedInputs,
		CompletedScheduled: o.completedScheduled,
		ActiveResponse:     o.activeResponse,
		ActiveResponseID:   o.activeResponseID,
	}
	o.lifecycleProjectionMu.Unlock()

	o.toolStateMu.Lock()
	state.AssistantResponseDone = o.assistantResponseDone
	state.AssistantOutputObserved = o.assistantOutputObserved
	state.OutputTextBytes = o.totals.outText
	state.OutputAudioBytes = o.totals.outAudio
	state.OutputToolBytes = o.totals.outTool
	state.InputAudioBytes = o.totals.inputAudio
	state.InputTextBytes = o.totals.inputText
	o.toolStateMu.Unlock()

	o.roomInputMu.Lock()
	state.RoomInputAudioBytes = o.roomInputTotalBytes
	o.roomInputMu.Unlock()

	if failure := o.failureSnapshot(); failure != nil {
		state.Failure = &sessiontrace.FailureState{
			Classification: failure.classification,
			TerminalReason: failure.terminalReason,
			Provenance:     failure.provenance,
			OutputState:    failure.outputState,
			ErrorType:      failure.errorType,
			Code:           failure.code,
			FailingEvent:   failure.failingEvent,
		}
	}
	return state
}

func (o *observerState) SetRuntimeRecorder(recorder sessiontrace.RuntimeRecorder) {
	if o != nil {
		o.runtime = recorder
	}
}
func (o *observerState) SetStreamObserver(observer sessiontrace.StreamObserver) {
	if o != nil {
		o.streamObserver = observer
	}
}
func (o *observerState) SetAdmittedTurnObserver(observer sessiontrace.StreamObserver) {
	if o != nil {
		o.admittedTurnObserver = observer
	}
}
func (o *observerState) SetTurnAdmission(admission func(messages.StreamMessage) bool) {
	if o != nil {
		o.turnAdmission = admission
	}
}
func (o *observerState) SetCancellationIntent(intent sessiontrace.CancellationIntent) {
	if o != nil {
		o.cancellationIntent = intent
	}
}
func (o *observerState) SetRequireSessionUpdated(required bool) {
	if o != nil {
		o.requireSessionUpdated = required
	}
}
func (o *observerState) SetScheduledAudioDispatch(policy sessiontrace.ScheduledAudioDispatchPolicy) {
	if o != nil {
		o.scheduledAudioDispatch = ScheduledAudioDispatchPolicy(policy)
	}
}
func (o *observerState) SetLivenessClock(clock sessiontrace.LivenessClock) {
	o.setLivenessClock(clock)
}
func (o *observerState) LockProviderBoundary() func() { return o.lockProviderBoundary() }
func (o *observerState) SetLivenessObserver(observer func(error)) {
	if o != nil {
		o.livenessObserver = observer
	}
}
func (o *observerState) SetToolResultsEnabled(enabled bool) {
	o.setToolResultsEnabled(enabled)
}
func (o *observerState) SetToolResultsEnabledForObservation(enabled bool) {
	o.setToolResultsEnabled(enabled)
}
func (o *observerState) Account(direction metrics.Direction, modality metrics.Modality, n int) {
	if o != nil {
		o.account(direction, modality, n)
	}
}
func (o *observerState) SetTerminalObserver(observer func(sessiontrace.TerminalObservation) bool) {
	if o != nil {
		o.terminalObserver = observer
	}
}
func (o *observerState) SetFailureObserver(observer func(sessiontrace.TerminalObservation)) {
	if o != nil {
		o.failureObserver = observer
	}
}
func (o *observerState) AccountRoomAudioInput(n int) { o.accountRoomAudioInput(n) }
func (o *observerState) MarkRoomBoundCancellation()  { o.markRoomBoundCancellation() }
func (o *observerState) NotifyTerminalObservation(observation sessiontrace.TerminalObservation) bool {
	return o.notifyTerminalObservation(observation)
}
func (o *observerState) NotifyFailureObservation(observation sessiontrace.TerminalObservation) bool {
	return o.notifyFailureObservation(observation)
}
func (o *observerState) LivenessFailure() error          { return o.livenessFailure() }
func (o *observerState) LivenessEvents() <-chan struct{} { return o.livenessEvents() }
func (o *observerState) LivenessErrors(ctx context.Context) <-chan error {
	return sessionLivenessErrorChannel(ctx, o)
}
func (o *observerState) StopLiveness()          { o.stopLiveness() }
func (o *observerState) ArmProviderProgress()   { o.armProviderProgress() }
func (o *observerState) ResetProviderProgress() { o.resetProviderProgress() }
func (o *observerState) ObserveProviderEvent(msg messages.StreamMessage) {
	o.observeProviderEvent(msg)
}
func (o *observerState) ObserveProviderDispatch(msg messages.StreamMessage) {
	o.observeProviderDispatch(msg)
}
func (o *observerState) ScheduledAudioReady() bool { return o.scheduledAudioReady() }
func (o *observerState) ScheduledAudioAwaitingConfiguration() bool {
	return o.scheduledAudioAwaitingConfiguration()
}
func (o *observerState) ScheduledAudioComplete() bool { return o.scheduledAudioComplete() }
func (o *observerState) ScheduledAudioIncomplete() bool {
	return o.scheduledAudioIncomplete()
}
func (o *observerState) ScheduledAudioCounts() (int, int, int) {
	return o.scheduledAudioCounts()
}
func (o *observerState) ScheduledAudioFailureMetadata() (string, string, string) {
	return o.scheduledAudioFailureMetadata()
}
func (o *observerState) HasTerminalToolContinuationFailure() bool {
	return o.hasTerminalToolContinuationFailure()
}
func (o *observerState) HasTerminalScheduledResponseFailure() bool {
	return o.hasTerminalScheduledResponseFailure()
}
func (o *observerState) LastMessageEndAdmitted() bool { return o.lastMessageEndAdmitted() }
func (o *observerState) HasToolLifecycleObligation() bool {
	return o.hasToolLifecycleObligation()
}
func (o *observerState) NextScheduledResponse() (sessiontrace.ScheduledAudioInput, int, bool) {
	return o.nextScheduledAudioInput()
}
func (o *observerState) ClaimScheduledRateLimitRetry(id string, terminal *messages.MessageEndValue) (time.Duration, bool) {
	return o.claimScheduledRateLimitRetry(id, terminal)
}
func (o *observerState) NoteUserTextInput(text string) { o.noteUserTextInput(text) }
func (o *observerState) NoteProviderUsage(usage messages.TokenUsage) {
	o.noteProviderUsage(usage)
}
func (o *observerState) CompleteTurn() { o.completeTurn() }
func (o *observerState) ObserveProviderToolCall(value *messages.ToolCallEndValue) {
	o.observeProviderToolCall(value)
}
func (o *observerState) ObserveProviderToolCallWithID(id, name string) {
	o.observeProviderToolCallWithID(id, name)
}
func (o *observerState) NoteToolResultAccepted(id string) { o.noteToolResultAccepted(id) }
func (o *observerState) NoteToolResultRejected(id string, outcome messages.SessionSendOutcome) {
	o.noteToolResultRejected(id, outcome)
}
func (o *observerState) NoteToolContinuationRequested() { o.noteToolContinuationRequested() }
func (o *observerState) NoteToolContinuationRequestedFor(id string) {
	o.noteToolContinuationRequestedFor(id)
}
func (o *observerState) ToolLifecycleEvents() <-chan struct{} {
	return o.toolLifecycleEvents()
}
func (o *observerState) ObserveBufferedProviderToolLifecycle(deltas []messages.StreamMessage) {
	o.observeBufferedProviderToolLifecycle(deltas)
}
func (o *observerState) HasUnresolvedToolCalls() bool    { return o.hasUnresolvedToolCalls() }
func (o *observerState) UnresolvedToolCallIDs() []string { return o.unresolvedToolCallIDs() }
func (o *observerState) UnresolvedToolResultSendStatuses() map[string]messages.SessionSendStatus {
	return o.unresolvedToolResultSendStatuses()
}
func (o *observerState) PendingToolContinuationCallIDs() []string {
	return o.pendingToolContinuationCallIDs()
}
func (o *observerState) PendingNonImageToolContinuationSnapshot() ([]string, map[string]string, map[string]string, map[string]string) {
	return o.pendingNonImageToolContinuationSnapshot()
}
func (o *observerState) PendingImageContinuationCallIDs() []string {
	return o.pendingImageContinuationCallIDs()
}
func (o *observerState) PendingImageContinuationSnapshot() ([]string, map[string]string, map[string]string, map[string]string) {
	return o.pendingImageContinuationSnapshot()
}
func (o *observerState) PendingContinuationMetadata() (map[string]string, map[string]string, map[string]string) {
	return o.pendingContinuationMetadata()
}
func (o *observerState) ContinuationResultAccepted(id string) bool {
	return o.continuationResultAccepted(id)
}
func (o *observerState) HasPendingToolContinuations() bool {
	return o.hasPendingToolContinuations()
}
func (o *observerState) HasPendingImageContinuations() bool {
	return o.hasPendingImageContinuations()
}
func (o *observerState) BeginLocalToolExecution() { o.beginLocalToolExecution() }
func (o *observerState) EndLocalToolExecution()   { o.endLocalToolExecution() }
func (o *observerState) ProviderToolCallObserved() bool {
	return o.providerToolCallObserved()
}
func (o *observerState) AssistantResponseCompleted() bool {
	return o.assistantResponseCompleted()
}
func (o *observerState) UserCancellationOutputState() messages.TerminalOutputState {
	return o.userCancellationOutputState()
}
func (o *observerState) ObserveSilentProviderEmptyResponse(msg messages.StreamMessage, value *messages.MessageEndValue, outputPresent, toolObligation bool) {
	o.observeSilentProviderEmptyResponse(msg, value, outputPresent, toolObligation)
}

var _ sessiontrace.Observer = (*observerState)(nil)
var _ metrics.Recorder = (*metrics.InMemorySink)(nil)

// Keep sort imported for the generated API's deterministic helper boundary;
// the import also documents that all observer snapshots are sorted by the
// underlying lifecycle service before exposure.
var _ = sort.Strings
