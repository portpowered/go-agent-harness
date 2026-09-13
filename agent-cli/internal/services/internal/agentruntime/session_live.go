package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audiosubsystem "github.com/portpowered/go-agent-harness/go-agent-loop/pkg/subsystems/audio"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive"
	sessionlivewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"io"
	"strings"
	"time"
)

var errSessionMaxDurationExpired = sessionlive.ErrMaxDurationExpired

// Deprecated: use sessionlive.ErrScheduledAudioIncomplete.
var ErrSessionScheduledAudioIncomplete = sessionlive.ErrScheduledAudioIncomplete

// Deprecated: use sessionlive.ScheduledAudioIncompleteError.
type SessionScheduledAudioIncompleteError = sessionlive.ScheduledAudioIncompleteError

// Deprecated: use sessionlive.ErrScheduledAudioConfigTimeout.
var ErrSessionScheduledAudioConfigTimeout = sessionlive.ErrScheduledAudioConfigTimeout

const sessionFirstTurnAckTimeout = 30 * time.Second
const sessionScheduledAudioConfigTimeout = 30 * time.Second

func awaitSessionFirstTurnWithClock(ctx context.Context, ack <-chan error, source platformclock.Source) error {
	return (sessionlive.SessionCapabilities{}).WaitForFirstTurn(ctx, ack, source, sessionFirstTurnAckTimeout)
}

type sessionLoopOptions struct {
	Prompt, ListeningBanner                                                                                                                                                                      string
	PromptProvided, CloseAfterOpen, WaitForClose, BareLive, BrowserToolsInteractive, RequireAssistantResponse, RequireTerminalAssistantResponse, CloseAfterScheduledAudio, RequireSessionUpdated bool
	MaxDuration, ToolExecutionTimeout, SessionUpdatedTimeout                                                                                                                                     time.Duration
	Done, AdmissionClosed, BoundCancellation                                                                                                                                                     <-chan struct{}
	DoneErr                                                                                                                                                                                      func() error
	AudioIn                                                                                                                                                                                      *sessionAudioSource
	awaitFirstTurn                                                                                                                                                                               <-chan error
	observer                                                                                                                                                                                     *sessionProgressObserver
	livenessClock                                                                                                                                                                                SessionLivenessClock
	clockSource                                                                                                                                                                                  platformclock.Source
	toolLifecycleObserver                                                                                                                                                                        sessionToolLifecycleObserver
	ToolExecutor                                                                                                                                                                                 messages.ToolExecutor
	toolDiagnostics                                                                                                                                                                              SessionToolDiagnosticSink
	ToolDefinitions                                                                                                                                                                              []messages.ToolDefinition
	InteractiveToolPolicy                                                                                                                                                                        *InteractiveToolPolicy
	ToolDefinitionBase                                                                                                                                                                           []messages.ToolDefinition
	RefreshToolDefinitions                                                                                                                                                                       func(context.Context) ([]messages.ToolDefinition, error)
	BrowserWatch                                                                                                                                                                                 func(context.Context) <-chan webmcp.BrokerEvent
	PublicationTimerFactory                                                                                                                                                                      webmcp.TimerFactory
	AdvertiseToolDefinitions                                                                                                                                                                     bool
	runtime                                                                                                                                                                                      *sessionRuntimeObservationRecorder
	cancellationIntent                                                                                                                                                                           *SessionCancellationIntent
	terminalSummaryRecorder                                                                                                                                                                      sessionDurationTerminalRecorder
	terminalReporter                                                                                                                                                                             *sessionTerminalReporter
	AudioOutputError                                                                                                                                                                             func() error
	rtcDeviceBinding                                                                                                                                                                             *RTCDeviceBinding
	ScheduledAudioDispatch                                                                                                                                                                       ScheduledAudioDispatchPolicy
	AudioInterruptions                                                                                                                                                                           <-chan ScheduledAudioInput
	InputAudioSampleRate                                                                                                                                                                         int
	loopReady                                                                                                                                                                                    chan<- *agentloop.AgentLoop
	quiesceUpstream                                                                                                                                                                              func() error
}

func sessionLiveLoopOptions(observedInferencer messages.SessionInferencer, opts sessionLoopOptions) sessionlive.LoopOptions {
	var toolPolicy *sessionlive.ToolAcknowledgementPolicy
	if opts.InteractiveToolPolicy != nil {
		policy := opts.InteractiveToolPolicy.Clone()
		toolPolicy = &sessionlive.ToolAcknowledgementPolicy{
			Threshold: policy.AcknowledgementThreshold,
			IsLongRunning: func(name string) bool {
				return policy.ClassForTool(name) == InteractiveToolClassBoundedLongRunning
			},
		}
	}
	return sessionlive.LoopOptions{
		Inferencer:               observedInferencer,
		Audio:                    newSessionAudioSubsystem(opts.rtcDeviceBinding),
		ToolExecutor:             buildSessionToolExecutor(opts),
		ToolDefinitions:          append([]messages.ToolDefinition(nil), opts.ToolDefinitions...),
		AdvertiseToolDefinitions: opts.AdvertiseToolDefinitions,
		ToolAcknowledgement:      toolPolicy,
	}
}
func buildSessionToolExecutor(opts sessionLoopOptions) messages.ToolExecutor {
	if opts.ToolExecutor == nil {
		return nil
	}
	return newSessionToolExecutorWithInteractivePolicyAndObserverAndCancellationIntentAndDiagnostics(opts.ToolExecutor, opts.InteractiveToolPolicy, opts.ToolExecutionTimeout, composeSessionToolLifecycleObserver(opts.toolLifecycleObserver, opts.observer, opts.runtime), opts.cancellationIntent, opts.toolDiagnostics)
}
func sessionToolIsLongRunning(opts sessionLoopOptions, name string) bool {
	return opts.InteractiveToolPolicy != nil && opts.InteractiveToolPolicy.ClassForTool(name) == InteractiveToolClassBoundedLongRunning
}
func newSessionAudioSubsystem(binding *RTCDeviceBinding) *audiosubsystem.Subsystem {
	if binding == nil || (binding.Capture == nil && binding.Sink == nil) {
		return nil
	}
	var capture, playback audiosubsystem.BufferPort
	var commands audiosubsystem.CommandPort
	if binding.Capture != nil {
		capture = binding.Capture.Control()
	}
	if binding.Sink != nil {
		playback, commands = binding.Sink.PlaybackBuffer(), binding.Sink.PlaybackCommands()
	}
	return audiosubsystem.New(audiosubsystem.Ports{Capture: capture, Playback: playback, Commands: commands})
}
func duplexSessionLoopOptions(observedInferencer messages.SessionInferencer, opts sessionLoopOptions) []agentloop.Option {
	loopOpts := []agentloop.Option{agentloop.WithMode(engine.DuplexSession), agentloop.WithSessionInferencer(observedInferencer)}
	if audio := newSessionAudioSubsystem(opts.rtcDeviceBinding); audio != nil {
		loopOpts = append(loopOpts, agentloop.WithAudioSubsystem(audio))
	}
	if opts.ToolExecutor == nil {
		return append(loopOpts, agentloop.WithToolExecutionDisabled())
	}
	if len(opts.ToolDefinitions) > 0 {
		loopOpts = append(loopOpts, agentloop.WithTools(opts.ToolDefinitions))
		if opts.AdvertiseToolDefinitions {
			loopOpts = append(loopOpts, agentloop.WithSessionConfig(messages.SessionUpdateConfig{Tools: append([]messages.ToolDefinition(nil), opts.ToolDefinitions...)}))
		}
	}
	loopOpts = append(loopOpts, agentloop.WithToolExecutor(buildSessionToolExecutor(opts)))
	if policy := opts.InteractiveToolPolicy; policy != nil {
		loopOpts = append(loopOpts, agentloop.WithToolAcknowledgementPolicy(agentloop.ToolAcknowledgementPolicy{Threshold: policy.AcknowledgementThreshold, IsLongRunning: func(name string) bool { return sessionToolIsLongRunning(opts, name) }}))
	}
	return loopOpts
}
func runAgentLoopSession(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) (runErr error) {
	reporter := opts.terminalReporter
	ownsReporter := reporter == nil
	if reporter == nil {
		reporter = newSessionTerminalReporter()
		opts.terminalReporter = reporter
	}
	reporter.markRunStarted()
	renderer := newSessionReplayRenderer(out, reporter)
	runErr = runAgentLoopSessionStream(ctx, renderer, sessionInferencer, opts)
	if !roomChannelClosed(opts.BoundCancellation) {
		runErr = audioResponseCompletionError(runErr, opts)
		runErr = scheduledAudioCompletionError(runErr, opts)
	} else if opts.observer != nil {
		opts.observer.markRoomBoundCancellation()
	}
	cleanSIGINT := sessionSIGINTCleanForObserver(runErr, opts.cancellationIntent, opts.observer)
	runErr = opts.observer.finish(runErr)
	if cleanSIGINT {
		runErr = errors.Join(runErr, publishSessionUserCancellation(renderer, opts, writeSessionReplayMessage))
	}
	if ownsReporter {
		if err := renderer.finishTranscript(); err != nil {
			runErr = errors.Join(runErr, err)
		}
		runErr = errors.Join(runErr, reporter.publish(out, runErr))
	}
	return runErr
}
func publishSessionUserCancellation(out io.Writer, opts sessionLoopOptions, write func(io.Writer, messages.StreamMessage) error) error {
	terminal := sessionUserCancelledTerminalMessage(opts.observer)
	var errs []error
	if opts.terminalSummaryRecorder != nil {
		summary, present, err := recordingTerminalSummaryFromMessage(terminal)
		if err != nil {
			errs = append(errs, fmt.Errorf("record user cancellation terminal summary: %w", err))
		} else if present {
			if err := opts.terminalSummaryRecorder.RecordTerminalSummary(*summary); err != nil {
				errs = append(errs, fmt.Errorf("record user cancellation terminal summary: %w", err))
			}
		}
	}
	if opts.terminalReporter != nil {
		if _, ok := out.(sessionReplayMessageWriter); ok && write != nil {
			if err := write(out, terminal); err != nil {
				errs = append(errs, err)
			}
		} else {
			opts.terminalReporter.observeStreamMessage(terminal, true)
		}
	} else if write != nil {
		if err := write(out, terminal); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func sessionUserCancelledTerminalMessage(observer *sessionProgressObserver) messages.StreamMessage {
	outputState := messages.TerminalOutputNone
	if observer != nil {
		outputState = observer.userCancellationOutputState()
	}
	return messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValueWithTerminal("", SessionUserCancelledClassification, SessionUserCancelledClassification, messages.TerminalReasonCancellation, messages.TerminalProvenanceCLI, outputState)}
}
func audioResponseCompletionError(err error, opts sessionLoopOptions) error {
	if opts.AudioOutputError != nil {
		if outputErr := opts.AudioOutputError(); outputErr != nil {
			err = errors.Join(err, outputErr)
		}
	}
	if opts.RequireTerminalAssistantResponse && (opts.observer == nil || !opts.observer.assistantResponseCompleted()) {
		incomplete := ErrSessionAudioResponseIncomplete
		if err == nil {
			return incomplete
		}
		return errors.Join(err, incomplete)
	}
	if opts.observer == nil || !opts.observer.providerToolCallObserved() || opts.observer.assistantResponseCompleted() {
		return err
	}
	incomplete := ErrSessionAudioResponseIncomplete
	if err == nil {
		return incomplete
	}
	return errors.Join(err, incomplete)
}
func sessionRunTerminationError(ctx context.Context, err error) error {
	err = decorateSessionStreamTerminalError(err)
	if ctx == nil {
		return err
	}
	return errors.Join(err, ctx.Err())
}

type sessionStreamTerminalError struct {
	cause error
	text  string
}

func (e *sessionStreamTerminalError) Error() string { return e.text }
func (e *sessionStreamTerminalError) Unwrap() error { return e.cause }
func decorateSessionStreamTerminalError(err error) error {
	if err == nil {
		return nil
	}
	var deltaErr *engine.StreamDeltaError
	if !errors.As(err, &deltaErr) || deltaErr.Value == nil {
		return err
	}
	fields := sessionErrorFields(deltaErr.Value)
	if fields == "" || strings.Contains(err.Error(), "classification=") {
		return err
	}
	message := strings.TrimSpace(deltaErr.Value.Message)
	if message == "" {
		message = "session error"
	}
	return &sessionStreamTerminalError{cause: err, text: fmt.Sprintf("%s [%s]", message, fields)}
}
func scheduledAudioCompletionError(err error, opts sessionLoopOptions) error {
	err = withUnresolvedToolResults(err, opts.observer)
	if !opts.CloseAfterScheduledAudio || opts.observer == nil || !opts.observer.scheduledAudioIncomplete() {
		return err
	}
	if errors.Is(err, ErrSessionScheduledAudioIncomplete) {
		return err
	}
	completed, dispatched, scheduled := opts.observer.scheduledAudioCounts()
	providerStatus, providerCode, providerDetails := opts.observer.scheduledAudioFailureMetadata()
	incomplete := &SessionScheduledAudioIncompleteError{
		Completed:         completed,
		Dispatched:        dispatched,
		Scheduled:         scheduled,
		ProviderStatus:    providerStatus,
		ProviderErrorCode: providerCode,
		ProviderDetails:   providerDetails,
	}
	if err == nil {
		return incomplete
	}
	return errors.Join(err, incomplete)
}
func sessionScheduledAudioConfigTimeoutError(opts sessionLoopOptions) error {
	if timeout := opts.SessionUpdatedTimeout; timeout > 0 {
		return fmt.Errorf("%w after %s", ErrSessionScheduledAudioConfigTimeout, timeout)
	}
	return fmt.Errorf("%w after %s", ErrSessionScheduledAudioConfigTimeout, sessionScheduledAudioConfigTimeout)
}

type sessionLoopMessageState struct {
	promptSent, closeSent, closeAfterOpenPending, listeningReported bool
}

func handleSessionLoopMessage(ctx context.Context, sessionDone <-chan struct{}, deadline <-chan time.Time, out io.Writer, loop *agentloop.AgentLoop, opts sessionLoopOptions, msg messages.StreamMessage, state sessionLoopMessageState, awaitingResponse bool, startAudio func(), terminate func(error) error) (sessionLoopMessageState, bool, error) {
	promptProvided := opts.PromptProvided || opts.Prompt != ""
	opts.observer.observe(msg)
	if err := writeSessionReplayMessage(out, msg); err != nil {
		return state, false, terminate(err)
	}
	if opts.observer != nil {
		if livenessErr := opts.observer.livenessFailure(); livenessErr != nil {
			return state, false, terminate(livenessErr)
		}
	}
	if msg.Type == messages.StreamTypeSessionCreated && (opts.BareLive || opts.BrowserToolsInteractive) && opts.ListeningBanner != "" && !state.listeningReported {
		if _, err := fmt.Fprintln(out, opts.ListeningBanner); err != nil {
			return state, false, terminate(err)
		}
		state.listeningReported = true
	}
	if err := retryScheduledRateLimitedResponseWithClock(ctx, sessionDone, deadline, loop, opts.observer, msg, opts.clockSource); err != nil {
		if errors.Is(err, errSessionMaxDurationExpired) {
			return state, true, nil
		}
		return state, false, terminate(err)
	}
	closePending := func() error {
		var err error
		state, err = closePendingSessionIfReady(ctx, loop, opts, state)
		return err
	}
	if msg.Type == messages.StreamTypeSessionOpen {
		if promptProvided && !state.promptSent {
			state.promptSent = true
			if err := loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, opts.Prompt)}); err != nil {
				return state, false, terminate(fmt.Errorf("send session message: %w", err))
			}
			opts.observer.noteUserTextInput(opts.Prompt)
			if opts.awaitFirstTurn != nil {
				if err := awaitSessionFirstTurnWithClock(ctx, opts.awaitFirstTurn, opts.clockSource); err != nil {
					return state, false, terminate(fmt.Errorf("send session first turn: %w", err))
				}
			}
		}
		if opts.CloseAfterOpen && !promptProvided && opts.AudioIn == nil && !state.closeSent {
			state.closeAfterOpenPending = true
			if err := closePending(); err != nil {
				return state, false, terminate(err)
			}
		}
		startAudio()
	}
	if shouldDispatchScheduledAudioForMessage(msg, opts.ScheduledAudioDispatch) {
		if err := opts.observer.dispatchScheduledInputs(ctx, loop); err != nil {
			return state, false, terminate(err)
		}
	}
	if (opts.CloseAfterOpen && promptProvided && msg.Type == messages.StreamTypeMessageEnd && !state.closeSent && (opts.observer == nil || opts.observer.lastMessageEndAdmitted())) || (opts.CloseAfterScheduledAudio && msg.Type == messages.StreamTypeMessageEnd && (opts.observer == nil || opts.observer.lastMessageEndAdmitted())) {
		state.closeAfterOpenPending = state.closeAfterOpenPending || opts.CloseAfterOpen && promptProvided
		if err := closePending(); err != nil {
			return state, false, terminate(err)
		}
	}
	if (opts.AudioIn != nil && shouldStopAudioInputSessionLoop(msg, opts, state.closeSent, awaitingResponse)) || (opts.AudioIn == nil && shouldStopSessionLoop(msg, opts)) {
		return state, true, nil
	}
	return state, false, nil
}
func retryScheduledRateLimitedResponseWithClock(ctx context.Context, sessionDone <-chan struct{}, deadline <-chan time.Time, loop *agentloop.AgentLoop, observer *sessionProgressObserver, msg messages.StreamMessage, source platformclock.Source) error {
	if observer == nil || loop == nil || msg.Type != messages.StreamTypeMessageEnd {
		return nil
	}
	terminal, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || terminal == nil {
		return nil
	}
	if sessionDurationTimerReady(deadline) {
		return errSessionMaxDurationExpired
	}
	delay, retry := observer.claimScheduledRateLimitRetry(msg.ResponseID, terminal)
	if !retry {
		return nil
	}
	timer, err := newSessionTimer(source, delay)
	if err != nil {
		return err
	}
	defer timer.Stop()
	select {
	case <-timer.C():
	case <-ctx.Done():
		return ctx.Err()
	case <-sessionDone:
		return context.Canceled
	case <-deadline:
		return errSessionMaxDurationExpired
	}
	if sessionDurationTimerReady(deadline) {
		return errSessionMaxDurationExpired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-sessionDone:
		return context.Canceled
	default:
	}
	if err := loop.SendSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()}); err != nil {
		return fmt.Errorf("send rate-limit retry response: %w", err)
	}
	observer.observeProviderDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	return nil
}
func shouldDispatchScheduledAudioForMessage(msg messages.StreamMessage, policy ScheduledAudioDispatchPolicy) bool {
	switch msg.Type {
	case messages.StreamTypeSessionOpen, messages.StreamTypeMessageEnd, messages.StreamTypeSessionUpdated:
		return true
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		return policy == ScheduledAudioDispatchActiveResponse
	default:
		return false
	}
}
func sessionActionSource[T any](input <-chan T, adapt func(T) sessionlive.Action) sessionlive.ActionSource {
	if input == nil || adapt == nil {
		return nil
	}
	return func(ctx context.Context) <-chan sessionlive.Action {
		actions := make(chan sessionlive.Action)
		go forwardSessionActions(ctx, input, adapt, actions)
		return actions
	}
}

func forwardSessionActions[T any](ctx context.Context, input <-chan T, adapt func(T) sessionlive.Action, actions chan<- sessionlive.Action) {
	defer close(actions)
	for {
		select {
		case item, ok := <-input:
			if !ok {
				return
			}
			select {
			case actions <- adapt(item):
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func sessionLiveInputNeedsEarlyCancellation(source *sessionAudioSource) bool {
	return source != nil && source.send == nil && (source.reader == nil || source.reader.closeOnCancel)
}

func runAgentLoopSessionStream(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) (runErr error) {
	var finishOutput func(error) error
	out, finishOutput = prepareSessionStreamOutput(out, &opts)
	defer func() { runErr = finishOutput(runErr) }()
	if opts.observer != nil {
		defer opts.observer.stopLiveness()
	}
	loop, observedInferencer, rtcPumpErrors, err := newObservedSessionLoop(sessionInferencer, opts)
	if err != nil {
		return err
	}
	publisher, publisherErrors := startSessionDynamicToolPublisher(ctx, loop, opts)
	defer publisher.stop()
	publisherErrors = mergeSessionErrorChannels(ctx, publisherErrors, sessionLivenessErrorChannel(ctx, opts.observer))
	state := sessionLoopMessageState{}
	audioActions := sessionActionSource(opts.AudioInterruptions, func(input ScheduledAudioInput) sessionlive.Action {
		return func(actionCtx context.Context, loop *agentloop.AgentLoop) error {
			return sendEventDrivenAudioInput(actionCtx, loop, opts, input)
		}
	})
	var toolActions sessionlive.ActionSource
	if opts.observer != nil {
		toolActions = sessionActionSource(opts.observer.toolLifecycleEvents(), func(struct{}) sessionlive.Action {
			return func(actionCtx context.Context, loop *agentloop.AgentLoop) error {
				if err := opts.observer.dispatchScheduledInputs(actionCtx, loop); err != nil {
					return err
				}
				var err error
				state, err = closePendingSessionIfReady(actionCtx, loop, opts, state)
				return err
			}
		})
	}
	messageHandler := func(handleCtx context.Context, handleLoop *agentloop.AgentLoop, msg messages.StreamMessage, messageCtx sessionlive.MessageContext) (sessionlive.MessageResult, error) {
		nextState, stopLoop, msgErr := handleSessionLoopMessage(handleCtx, messageCtx.SessionDone, messageCtx.Deadline, out, handleLoop, opts, msg, state, messageCtx.AwaitingResponse, func() {}, func(err error) error { return err })
		state = nextState
		if msgErr != nil {
			return sessionlive.MessageResult{}, msgErr
		}
		if msg.Type == messages.StreamTypeSessionCreated {
			publisher.markSessionReady()
		}
		return sessionlive.MessageResult{Stop: stopLoop, DrainPlayback: stopLoop}, nil
	}
	return sessionlivewire.NewService().Run(ctx, sessionlive.RunOptions{
		Loop: loop,
		Lifecycle: sessionlive.Lifecycle{
			Done:          observedInferencer.Done(),
			ConnectError:  observedInferencer.connectFailure,
			SessionError:  observedInferencer.sessionFailure,
			DrainPlayback: observedInferencer.DrainSessionPlayback,
		},
		Handler:       messageHandler,
		Clock:         opts.clockSource,
		MaxDuration:   opts.MaxDuration,
		DeadlineError: func() error { return nil },
		Bind: func(runCtx, inputCtx context.Context, boundLoop *agentloop.AgentLoop) error {
			return bindSessionLoopInputs(runCtx, inputCtx, boundLoop, opts)
		},
		StartInput: func(inputCtx context.Context, inputLoop *agentloop.AgentLoop) (<-chan error, error) {
			if opts.AudioIn == nil {
				return nil, nil
			}
			errorsCh := make(chan error, 1)
			go func() { errorsCh <- streamSessionAudioInput(inputCtx, inputLoop, opts.AudioIn) }()
			return errorsCh, nil
		},
		InitiallyAwaitingResponse: opts.AudioIn == nil,
		FirstTurnAck:              nil,
		RequireSessionUpdated:     opts.RequireSessionUpdated,
		SessionUpdatedTimeout:     opts.SessionUpdatedTimeout,
		SessionUpdatedReady: func() bool {
			return opts.observer != nil && opts.observer.scheduledAudioReady()
		},
		SessionUpdatedError: func(timeout time.Duration) error {
			return sessionScheduledAudioConfigTimeoutErrorWithTimeout(opts, timeout)
		},
		Done:                           opts.Done,
		DoneErr:                        opts.DoneErr,
		AdmissionClosed:                opts.AdmissionClosed,
		BoundCancellation:              opts.BoundCancellation,
		Actions:                        audioActions,
		ToolLifecycle:                  toolActions,
		Errors:                         mergeSessionErrorChannels(ctx, publisherErrors, rtcPumpErrors),
		OnAdmissionClosed:              func() {},
		QuiesceUpstream:                opts.quiesceUpstream,
		CancelInputBeforeStragglerWait: sessionLiveInputNeedsEarlyCancellation(opts.AudioIn),
		WaitForStragglers: func(waitCtx context.Context) error {
			return waitForSessionLoopStragglersWithContext(waitCtx, out, loop, defaultSessionStragglerDrainPolicy, opts.observer, opts.clockSource)
		},
		StopOwnedResources: func(stopCtx context.Context) error {
			return errors.Join(closeBareSessionIfNeeded(opts.BareLive, observedInferencer), closeRTCDeviceBinding(opts.rtcDeviceBinding))
		},
		JoinProducerErrors: joinSessionTerminationErrors,
		AfterFlush: func() error {
			if opts.observer != nil {
				opts.observer.observeBufferedProviderToolLifecycle(loop.GetConversationDeltas())
			}
			return nil
		},
		TerminationError: sessionRunTerminationError,
	})
}
func sessionScheduledAudioConfigTimeoutErrorWithTimeout(opts sessionLoopOptions, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = sessionScheduledAudioConfigTimeout
	}
	return fmt.Errorf("%w after %s", ErrSessionScheduledAudioConfigTimeout, timeout)
}
func closePendingSessionIfReady(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions, state sessionLoopMessageState) (sessionLoopMessageState, error) {
	if state.closeSent || (opts.observer != nil && opts.observer.hasToolLifecycleObligation()) {
		return state, nil
	}
	if !(opts.CloseAfterOpen && state.closeAfterOpenPending) && !(opts.CloseAfterScheduledAudio && opts.observer != nil && opts.observer.scheduledAudioComplete()) {
		return state, nil
	}
	if err := sendSessionClose(ctx, loop); err != nil {
		return state, err
	}
	state.closeSent = true
	return state, nil
}
func drainPublishedSessionDeltas(read func() (messages.StreamMessage, bool), handle func(messages.StreamMessage) (bool, error)) (bool, error) {
	if read == nil || handle == nil {
		return false, nil
	}
	for {
		msg, ok := read()
		if !ok {
			return false, nil
		}
		stopLoop, err := handle(msg)
		if err != nil || stopLoop {
			return stopLoop, err
		}
	}
}
