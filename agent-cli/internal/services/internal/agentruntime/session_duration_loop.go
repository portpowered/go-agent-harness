package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal"
	sessionterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
)

//lint:ignore U1000 package tests exercise the context-free admission seam.
func runAgentLoopSessionWithDurationClock(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock) error {
	return runAgentLoopSessionWithDurationAdmissionClock(ctx, out, sessionInferencer, opts, maxDuration, durationClock, nil)
}
func runAgentLoopSessionWithDurationAdmissionClock(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock, admittedInferencer *sessionDurationAdmissionInferencer) (runErr error) {
	reporter := opts.terminalReporter
	ownsReporter := reporter == nil
	if reporter == nil {
		reporter = sessionterminalwire.NewReporter()
		opts.terminalReporter = reporter
	}
	reporter.MarkRunStarted()
	renderer := newSessionReplayRenderer(out, reporter)
	runErr = runAgentLoopSessionWithDurationAdmissionClockStream(ctx, renderer, sessionInferencer, opts, maxDuration, durationClock, admittedInferencer)
	runErr = scheduledAudioCompletionError(runErr, opts)
	cleanSIGINT := sessionSIGINTCleanForObserver(runErr, opts.cancellationIntent, opts.observer)
	if opts.observer != nil {
		runErr = opts.observer.Finish(runErr)
	}
	if cleanSIGINT {
		artifacts := sessionDurationArtifactsFromContext(ctx)
		runErr = errors.Join(runErr, publishSessionUserCancellation(renderer, opts, func(out io.Writer, msg messages.StreamMessage) error {
			return writeDurationSessionReplayMessage(out, msg, artifacts)
		}))
	}
	if ownsReporter {
		if err := renderer.finishTranscript(); err != nil {
			runErr = errors.Join(runErr, err)
		}
		runErr = errors.Join(runErr, reporter.Publish(out, runErr))
	}
	return runErr
}

func runAgentLoopSessionWithDurationAdmissionClockStream(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock, admittedInferencer *sessionDurationAdmissionInferencer) error {
	if maxDuration <= 0 {
		return runAgentLoopSession(ctx, out, sessionInferencer, opts)
	}

	if admittedInferencer == nil {
		admission := newSessionDurationAdmission()
		admittedInferencer = &sessionDurationAdmissionInferencer{
			inner:     sessionInferencer,
			admission: admission,
			closeDone: make(chan struct{}),
		}
	}
	rtcPumpErrors := sessionDurationRTCPumpErrors(opts)
	observedInferencer := newObservedSessionInferencer(admittedInferencer)
	observedInferencer.progress = opts.observer
	defer startSessionDurationObserver(opts)()
	loop, err := agentloop.New(duplexSessionLoopOptions(observedInferencer, opts)...)
	if err != nil {
		return fmt.Errorf("create session agent loop: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	publisher, publisherErrors := startSessionDynamicToolPublisher(runCtx, loop, opts)
	var livenessErrors <-chan error
	if opts.observer != nil {
		livenessErrors = opts.observer.LivenessErrors(runCtx)
	}
	publisherErrors = sessiontracewire.MergeErrorChannels(runCtx, publisherErrors, livenessErrors)
	defer publisher.stop()
	if opts.loopReady != nil {
		select {
		case opts.loopReady <- loop:
		case <-runCtx.Done():
			return runCtx.Err()
		}
	}
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- loop.Run(runCtx)
	}()

	var durationExpired bool
	durationTerminalWritten := false
	artifacts := sessionDurationArtifactsFromContext(ctx)
	terminalState := newSessionDurationTerminalState(admittedInferencer)
	var runErr error
	runDone := false
	waitRun := func() error {
		if !runDone {
			runErr = <-runErrCh
			runDone = true
		}
		return runErr
	}

	var terminationPlanned bool
	var drainDevicePlayback bool
	termination := sessionterminalwire.NewTerminationBoundary(sessionterminal.TerminationOptions{
		Context:         ctx,
		QuiesceUpstream: opts.quiesceUpstream,
		WaitForStragglers: func() error {
			return waitForDurationSessionLoopStragglers(out, loop, sessionStragglerDrainPolicy{quietPeriod: sessionterminal.StragglerDrainQuietPeriod}, durationClock, terminationPlanned, &durationTerminalWritten, artifacts, opts.observer, terminalState)
		},
		StopOwnedResources: func() error {
			var drainErr error
			if drainDevicePlayback {
				drainErr = observedInferencer.DrainSessionPlayback(ctx)
			}
			cancel()
			providerErr := closeBareSessionIfNeeded(opts.BareLive, observedInferencer)
			var runTerminationErr error
			if runErr := waitRun(); runErr != nil && !sessionErrorIsCancellation(runErr) {
				runTerminationErr = fmt.Errorf("session error: %w", runErr)
			}
			admittedInferencer.waitForClose()
			return errors.Join(drainErr, providerErr, runTerminationErr)
		},
		FlushBuffered: func() error {
			flushErr := flushBufferedDurationSessionLoopMessages(out, loop, terminationPlanned, &durationTerminalWritten, artifacts, opts.observer, terminalState)
			if terminationPlanned && !terminalState.written() {
				flushErr = errors.Join(flushErr, terminalState.writeObservedProviderTerminal(out, artifacts))
			}
			return flushErr
		},
	})

	finish := func(planned bool, preferredErr error) error {
		terminationPlanned = planned
		drainDevicePlayback = !planned && preferredErr == nil && ctx.Err() == nil
		terminationErr := termination.Terminate(preferredErr)
		durationTerminalWritten = terminalState.written()
		if opts.terminalReporter != nil {
			opts.terminalReporter.MarkDurationExpiry(planned, terminalState.outputState())
		}
		sessionErr := observedInferencer.sessionFailure()
		runtimeErr := admittedInferencer.runtimeError()
		closeErr := admittedInferencer.closeError()
		lifecycleErr := sessionDurationLifecycleError(runtimeErr, closeErr, nil)
		transportErr := sessionTransportError(sessionErr)
		return resolveSessionDurationFinishError(terminationErr, lifecycleErr, sessionErr, transportErr, runErr, planned, out, artifacts, terminalState, &durationTerminalWritten)
	}

	timer := durationClock.NewTimer(maxDuration)
	if timer == nil {
		admittedInferencer.closeAdmission()
		return finish(false, errors.New("session duration clock returned a nil timer"))
	}
	defer timer.Stop()
	timerCh := timer.C()

	var sessionUpdatedTimer SessionDurationTimer
	var sessionUpdatedTimeout <-chan time.Time
	defer func() { stopDurationSessionUpdatedTimer(&sessionUpdatedTimer, &sessionUpdatedTimeout) }()

	promptSent := false
	closeSent := false
	closeAfterOpenPending := false
	var toolLifecycleEvents <-chan struct{}
	if opts.observer != nil {
		toolLifecycleEvents = opts.observer.ToolLifecycleEvents()
	}

	expire := func() error {
		if durationExpired {
			return nil
		}
		durationExpired = true
		timerCh = nil
		admittedInferencer.closeAdmission()
		if closeSent {
			return nil
		}
		closeSent = true
		return sendSessionClose(runCtx, loop)
	}
	processDelta := func(msg messages.StreamMessage) (sessionDurationMessageResult, error) {
		result, msgErr := processDurationLoopMessage(runCtx, observedInferencer.Done(), timerCh, loop, out, msg, opts, durationExpired, promptSent, closeSent, closeAfterOpenPending, durationTerminalWritten, artifacts, terminalState)
		promptSent = result.promptSent
		closeSent = result.closeSent
		closeAfterOpenPending = result.closeAfterOpenPending
		durationTerminalWritten = result.durationTerminalWritten
		if msgErr != nil {
			return result, msgErr
		}
		if msg.Type == messages.StreamTypeSessionCreated {
			// ModelRunner sends the initial SESSION.UPDATE while handling
			// SESSION.CREATED. Release dynamic publication only after that
			// provider bootstrap boundary has been processed, so a page
			// update cannot overtake the initial configuration.
			publisher.markSessionReady()
		}
		if msg.Type == messages.StreamTypeSessionOpen {
			var err error
			sessionUpdatedTimer, sessionUpdatedTimeout, err = startDurationSessionUpdatedTimer(durationClock, opts, sessionUpdatedTimer, sessionUpdatedTimeout)
			if err != nil {
				return result, err
			}
		}
		if opts.observer != nil && opts.observer.ScheduledAudioReady() {
			stopDurationSessionUpdatedTimer(&sessionUpdatedTimer, &sessionUpdatedTimeout)
		}
		return result, nil
	}

	for {
		// Prefer a deadline that is already ready over a simultaneously ready
		// provider-close signal. Once this branch wins, the planned reason is
		// retained and the close is still drained normally.
		if !durationExpired && sessionDurationTimerReady(timerCh) {
			if err := expire(); err != nil {
				return finish(false, err)
			}
			return finish(true, nil)
		}

		select {
		case publicationErr := <-publisherErrors:
			return finish(false, publicationErr)
		case <-toolLifecycleEvents:
			// Tool lifecycle completion is an asynchronous scheduler wake. It
			// must re-check pending audio before checking whether the session
			// can close; otherwise a completed continuation can leave the next
			// scheduled turn waiting for an unrelated provider delta.
			if opts.observer != nil {
				if err := opts.observer.DispatchScheduledInputs(runCtx, loop); err != nil {
					return finish(false, err)
				}
			}
			state, closeErr := closePendingSessionIfReady(runCtx, loop, opts, sessionLoopMessageState{
				closeSent:             closeSent,
				closeAfterOpenPending: closeAfterOpenPending,
			})
			if closeErr != nil {
				return finish(false, closeErr)
			}
			closeSent = state.closeSent
		case <-timerCh:
			if err := expire(); err != nil {
				return finish(false, err)
			}
			return finish(true, nil)
		case <-sessionUpdatedTimeout:
			stopDurationSessionUpdatedTimer(&sessionUpdatedTimer, &sessionUpdatedTimeout)
			return finish(false, sessionScheduledAudioConfigTimeoutError(opts))
		case <-ctx.Done():
			if err := finish(durationExpired, nil); err != nil {
				return err
			}
			return ctx.Err()
		case <-opts.Done:
			doneErr := error(nil)
			if opts.DoneErr != nil {
				doneErr = opts.DoneErr()
			}
			return finish(durationExpired && doneErr == nil, doneErr)
		case <-observedInferencer.Done():
			doneErr := error(nil)
			if opts.DoneErr != nil {
				doneErr = opts.DoneErr()
			}
			finishErr := finish(durationExpired && doneErr == nil, doneErr)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return sessionRunTerminationError(ctx, finishErr)
			}
			return finishErr
		case pumpErr := <-rtcPumpErrors:
			return finish(false, pumpErr)
		case err := <-runErrCh:
			runErr = err
			runDone = true
			return sessionRunTerminationError(ctx, finish(durationExpired, nil))
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return finish(durationExpired, nil)
			}
			result, msgErr := processDelta(msg)
			if msgErr != nil {
				return finishDurationLoopMessageError(msgErr, expire, finish)
			}
			if result.stop {
				return finish(result.planned, nil)
			}
		}
	}
}

func finishDurationLoopMessageError(msgErr error, expire func() error, finish func(bool, error) error) error {
	if !errors.Is(msgErr, errSessionMaxDurationExpired) {
		return finish(false, msgErr)
	}
	if err := expire(); err != nil {
		return finish(false, err)
	}
	return finish(true, nil)
}

type sessionDurationMessageResult struct {
	promptSent              bool
	closeSent               bool
	closeAfterOpenPending   bool
	durationTerminalWritten bool
	stop                    bool
	planned                 bool
}

func processDurationLoopMessage(ctx context.Context, sessionDone <-chan struct{}, deadline <-chan time.Time, loop *agentloop.AgentLoop, out io.Writer, msg messages.StreamMessage, opts sessionLoopOptions, durationExpired, promptSent, closeSent, closeAfterOpenPending, durationTerminalWritten bool, artifacts SessionDurationArtifactLifecycle, terminalState *sessionDurationTerminalState) (sessionDurationMessageResult, error) {
	result := sessionDurationMessageResult{
		promptSent:              promptSent,
		closeSent:               closeSent,
		closeAfterOpenPending:   closeAfterOpenPending,
		durationTerminalWritten: durationTerminalWritten,
	}
	if terminalState != nil {
		terminalState.observe(msg)
		var shouldWrite bool
		msg, shouldWrite = terminalState.admitTerminal(durationExpired, msg)
		if !shouldWrite {
			result.durationTerminalWritten = terminalState.written()
			result.planned = durationExpired
			result.stop = false
			return result, nil
		}
		result.durationTerminalWritten = terminalState.written()
	}
	if opts.observer != nil {
		opts.observer.Observe(msg)
	}
	if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
		return result, err
	}
	if err := retryScheduledRateLimitedResponseWithClock(ctx, sessionDone, deadline, loop, opts.observer, msg, opts.clockSource); err != nil {
		return result, err
	}
	if msg.Type == messages.StreamTypeSessionOpen && !durationExpired {
		var err error
		result, err = processDurationSessionOpen(ctx, loop, opts, result)
		if err != nil {
			return result, err
		}
	}
	var err error
	result.closeSent, err = processDurationScheduledMessage(ctx, loop, msg, opts, result.closeSent)
	if err != nil {
		return result, err
	}
	if !durationExpired && opts.CloseAfterOpen && (opts.PromptProvided || opts.Prompt != "") && msg.Type == messages.StreamTypeMessageEnd && !result.closeSent && (opts.observer == nil || opts.observer.LastMessageEndAdmitted()) {
		result.closeAfterOpenPending = true
	}
	state, err := closePendingSessionIfReady(ctx, loop, opts, sessionLoopMessageState{
		closeSent:             result.closeSent,
		closeAfterOpenPending: result.closeAfterOpenPending,
	})
	if err != nil {
		return result, err
	}
	result.closeSent = state.closeSent
	result.stop = shouldStopSessionLoop(msg, opts) && (!durationExpired || msg.Type == messages.StreamTypeSessionClose)
	result.planned = durationExpired
	return result, nil
}

func processDurationSessionOpen(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions, result sessionDurationMessageResult) (sessionDurationMessageResult, error) {
	promptProvided := opts.PromptProvided || opts.Prompt != ""
	if promptProvided && !result.promptSent {
		result.promptSent = true
		userMsg := messages.NewTextMessage(messages.RoleUser, opts.Prompt)
		if err := loop.Send(ctx, []messages.Message{userMsg}); err != nil {
			return result, fmt.Errorf("send session message: %w", err)
		}
		noteSessionUserTextInput(opts.observer, opts.Prompt)
		if opts.awaitFirstTurn != nil {
			if err := awaitSessionFirstTurnWithClock(opts.audioService, ctx, opts.awaitFirstTurn, opts.clockSource); err != nil {
				return result, fmt.Errorf("send session first turn: %w", err)
			}
		}
	}
	if opts.CloseAfterOpen && !promptProvided && !result.closeSent {
		result.closeAfterOpenPending = true
	}
	return result, nil
}

func handleLiveSessionOpen(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions, state sessionLoopMessageState) (sessionLoopMessageState, error) {
	if (opts.PromptProvided || opts.Prompt != "") && !state.promptSent {
		state.promptSent = true
		userMsg := messages.NewTextMessage(messages.RoleUser, opts.Prompt)
		if err := loop.Send(ctx, []messages.Message{userMsg}); err != nil {
			return state, fmt.Errorf("send session message: %w", err)
		}
		noteSessionUserTextInput(opts.observer, opts.Prompt)
		if opts.awaitFirstTurn != nil {
			if err := awaitSessionFirstTurnWithClock(opts.audioService, ctx, opts.awaitFirstTurn, opts.clockSource); err != nil {
				return state, fmt.Errorf("send session first turn: %w", err)
			}
		}
	}
	if opts.CloseAfterOpen && !opts.PromptProvided && opts.Prompt == "" && !state.closeSent {
		state.closeAfterOpenPending = true
		var err error
		state, err = closePendingSessionIfReady(ctx, loop, opts, state)
		if err != nil {
			return state, err
		}
	}
	return state, nil
}

func noteSessionUserTextInput(observer sessiontrace.Observer, prompt string) {
	if observer != nil {
		observer.NoteUserTextInput(prompt)
	}
}

func processDurationScheduledMessage(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage, opts sessionLoopOptions, closeSent bool) (bool, error) {
	if !shouldDispatchScheduledAudioForMessage(msg, opts.ScheduledAudioDispatch) {
		return closeSent, nil
	}
	if opts.observer == nil {
		return closeSent, nil
	}
	if err := opts.observer.DispatchScheduledInputs(ctx, loop); err != nil {
		return closeSent, err
	}
	return closeSent, nil
}

func sessionDurationTimerReady(timerCh <-chan time.Time) bool {
	if timerCh == nil {
		return false
	}
	select {
	case <-timerCh:
		return true
	default:
		return false
	}
}

func writeDurationSessionReplayMessage(out io.Writer, msg messages.StreamMessage, artifacts SessionDurationArtifactLifecycle) error {
	if artifacts != nil {
		if err := artifacts.Accept(msg); err != nil {
			return wrapSessionPhaseError("write duration artifacts", err)
		}
	}
	return writeSessionReplayMessage(out, msg)
}

// flushBufferedDurationSessionLoopMessages renders only messages already
// buffered after the loop's owned resources have stopped. Duration-specific
// terminal and artifact state remains in the runtime service state rather than
// in the shared termination boundary.
func flushBufferedDurationSessionLoopMessages(out io.Writer, loop *agentloop.AgentLoop, planned bool, terminalWritten *bool, artifacts SessionDurationArtifactLifecycle, obs sessiontrace.Observer, terminalState *sessionDurationTerminalState) error {
	for {
		msg, ok := loop.Deltas().Read()
		if !ok {
			return nil
		}
		if terminalState != nil {
			terminalState.observe(msg)
			var shouldWrite bool
			msg, shouldWrite = terminalState.admitTerminal(planned, msg)
			*terminalWritten = terminalState.written()
			if !shouldWrite {
				continue
			}
		}
		if obs != nil {
			obs.Observe(msg)
		}
		if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
			return err
		}
	}
}
