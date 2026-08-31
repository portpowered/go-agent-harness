package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type realSessionDurationClock struct{}

func (realSessionDurationClock) NewTimer(duration time.Duration) SessionDurationTimer {
	return realSessionDurationTimer{timer: time.NewTimer(duration)}
}

type realSessionDurationTimer struct {
	timer *time.Timer
}

func (t realSessionDurationTimer) C() <-chan time.Time {
	return t.timer.C
}

func (t realSessionDurationTimer) Stop() bool {
	return t.timer.Stop()
}

func runAgentLoopSessionWithDurationClock(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock) error {
	return runAgentLoopSessionWithDurationAdmissionClock(ctx, out, sessionInferencer, opts, maxDuration, durationClock, nil)
}

func runAgentLoopSessionWithDurationAdmissionClock(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock, admittedInferencer *sessionDurationAdmissionInferencer) (runErr error) {
	reporter := opts.terminalReporter
	ownsReporter := reporter == nil
	if reporter == nil {
		reporter = newSessionTerminalReporter()
		opts.terminalReporter = reporter
	}
	reporter.markRunStarted()
	renderer := newSessionReplayRenderer(out, reporter)
	runErr = runAgentLoopSessionWithDurationAdmissionClockStream(ctx, renderer, sessionInferencer, opts, maxDuration, durationClock, admittedInferencer)
	runErr = scheduledAudioCompletionError(runErr, opts)
	cleanSIGINT := sessionSIGINTCleanForObserver(runErr, opts.cancellationIntent, opts.observer)
	runErr = opts.observer.finish(runErr)
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
		runErr = errors.Join(runErr, reporter.publish(out, runErr))
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
	var rtcPumpErrors <-chan error
	boundInferencer, rtcErrors := bindRTCDeviceSessionInferencer(admittedInferencer, opts.rtcDeviceBinding)
	rtcPumpErrors = rtcErrors
	observedInferencer := newObservedSessionInferencer(boundInferencer)
	observedInferencer.progress = opts.observer
	if opts.observer != nil {
		opts.observer.setLivenessClock(opts.livenessClock)
		opts.observer.setToolResultsEnabled(opts.ToolExecutor != nil)
	}
	if opts.observer != nil {
		defer opts.observer.stopLiveness()
	}
	loop, err := agentloop.New(duplexSessionLoopOptions(observedInferencer, opts)...)
	if err != nil {
		return fmt.Errorf("create session agent loop: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	publisher, publisherErrors := startSessionDynamicToolPublisher(runCtx, loop, opts)
	publisherErrors = mergeSessionErrorChannels(runCtx, publisherErrors, sessionLivenessErrorChannel(runCtx, opts.observer))
	defer publisher.stop()
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- loop.Run(runCtx)
	}()

	timer := durationClock.NewTimer(maxDuration)
	if timer == nil {
		admittedInferencer.closeAdmission()
		cancel()
		<-runErrCh
		admittedInferencer.waitForClose()
		return errors.New("session duration clock returned a nil timer")
	}
	defer timer.Stop()
	timerCh := timer.C()

	var sessionUpdatedTimer *time.Timer
	var sessionUpdatedTimeout <-chan time.Time
	startSessionUpdatedTimer := func() {
		if !opts.RequireSessionUpdated || opts.observer == nil || !opts.observer.scheduledAudioAwaitingConfiguration() || sessionUpdatedTimer != nil {
			return
		}
		timeout := opts.SessionUpdatedTimeout
		if timeout <= 0 {
			timeout = sessionScheduledAudioConfigTimeout
		}
		sessionUpdatedTimer = time.NewTimer(timeout)
		sessionUpdatedTimeout = sessionUpdatedTimer.C
	}
	stopSessionUpdatedTimer := func() {
		if sessionUpdatedTimer == nil {
			return
		}
		sessionUpdatedTimer.Stop()
		sessionUpdatedTimer = nil
		sessionUpdatedTimeout = nil
	}
	defer stopSessionUpdatedTimer()

	promptSent := false
	closeSent := false
	closeAfterOpenPending := false
	durationExpired := false
	durationTerminalWritten := false
	artifacts := sessionDurationArtifactsFromContext(ctx)
	terminalState := newSessionDurationTerminalState(admittedInferencer)
	toolLifecycleEvents := opts.observer.toolLifecycleEvents()

	finish := func(planned bool, preferredErr error) error {
		// A terminal signal must never skip the straggler drain. Provider
		// messages travel through the session receive buffer and the loop's
		// delta buffer, while termination travels out of band, so a terminal
		// signal routinely overtakes output the session has already accepted.
		// Once cancel() runs, everything still upstream of loop.Deltas() is
		// discarded, and the unconditional drain below only collects what is
		// already buffered. Draining first regardless of preferredErr keeps an
		// errored run from losing the output it did receive; preferredErr is
		// still reported as the primary cause below.
		preCancelDrainErr := drainDurationSessionLoopMessagesUntilQuiet(out, loop, planned, &durationTerminalWritten, artifacts, opts.observer, terminalState)
		cancel()
		providerErr := closeBareSessionIfNeeded(opts.BareLive, observedInferencer)
		bindingErr := closeRTCDeviceBinding(opts.rtcDeviceBinding)
		runErr := <-runErrCh
		admittedInferencer.waitForClose()
		sessionErr := observedInferencer.sessionFailure()
		if drainErr := drainDurationSessionLoopMessages(out, loop, planned, &durationTerminalWritten, artifacts, opts.observer, terminalState); drainErr != nil {
			return drainErr
		}
		markSessionDurationExpiry(opts.terminalReporter, planned, terminalState.outputState())
		if planned && !terminalState.terminalWritten {
			if err := terminalState.writeObservedProviderTerminal(out, artifacts); err != nil {
				return err
			}
		}
		runtimeErr := admittedInferencer.runtimeError()
		closeErr := admittedInferencer.closeError()
		if preferredErr != nil {
			lifecycleErr := errors.Join(providerErr, sessionDurationLifecycleError(runtimeErr, closeErr, bindingErr))
			transportErr := sessionTransportError(sessionErr)
			if lifecycleErr != nil || transportErr != nil {
				return errors.Join(preferredErr, lifecycleErr, transportErr)
			}
			return preferredErr
		}
		if preCancelDrainErr != nil {
			return preCancelDrainErr
		}
		if lifecycleErr := errors.Join(providerErr, sessionDurationLifecycleError(runtimeErr, closeErr, bindingErr)); lifecycleErr != nil {
			return lifecycleErr
		}
		if sessionErr != nil {
			return sessionTransportError(sessionErr)
		}
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			return fmt.Errorf("session error: %w", runErr)
		}
		if planned && !terminalState.terminalWritten {
			if err := writeMaxDurationTerminal(out, artifacts, terminalState.outputState()); err != nil {
				return err
			}
			terminalState.terminalWritten = true
			durationTerminalWritten = true
		}
		return nil
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
			if err := opts.observer.dispatchScheduledInputs(runCtx, loop); err != nil {
				return finish(false, err)
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
			stopSessionUpdatedTimer()
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
			return finish(durationExpired && doneErr == nil, doneErr)
		case pumpErr := <-rtcPumpErrors:
			return finish(false, pumpErr)
		case err := <-runErrCh:
			admittedInferencer.waitForClose()
			if drainErr := drainDurationSessionLoopMessages(out, loop, durationExpired, &durationTerminalWritten, artifacts, opts.observer, terminalState); drainErr != nil {
				return sessionRunTerminationError(ctx, drainErr)
			}
			if runtimeErr := admittedInferencer.runtimeError(); runtimeErr != nil {
				return sessionRunTerminationError(ctx, wrapSessionPhaseError("session runtime", runtimeErr))
			}
			if closeErr := admittedInferencer.closeError(); closeErr != nil {
				return sessionRunTerminationError(ctx, wrapSessionPhaseError("close session", closeErr))
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return sessionRunTerminationError(ctx, fmt.Errorf("session error: %w", err))
			}
			markSessionDurationExpiry(opts.terminalReporter, durationExpired, terminalState.outputState())
			if durationExpired && !terminalState.terminalWritten {
				if err := terminalState.writeObservedProviderTerminal(out, artifacts); err != nil {
					return sessionRunTerminationError(ctx, err)
				}
			}
			if durationExpired && !terminalState.terminalWritten {
				if err := writeMaxDurationTerminal(out, artifacts, terminalState.outputState()); err != nil {
					return sessionRunTerminationError(ctx, err)
				}
				terminalState.terminalWritten = true
				durationTerminalWritten = true
			}
			return sessionRunTerminationError(ctx, nil)
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return finish(durationExpired, nil)
			}
			result, msgErr := processDurationLoopMessage(runCtx, observedInferencer.Done(), timerCh, loop, out, msg, opts, durationExpired, promptSent, closeSent, closeAfterOpenPending, durationTerminalWritten, artifacts, terminalState)
			promptSent = result.promptSent
			closeSent = result.closeSent
			closeAfterOpenPending = result.closeAfterOpenPending
			durationTerminalWritten = result.durationTerminalWritten
			if msgErr != nil {
				return finishDurationLoopMessageError(msgErr, expire, finish)
			}
			if msg.Type == messages.StreamTypeSessionCreated {
				// ModelRunner sends the initial SESSION.UPDATE while handling
				// SESSION.CREATED. Release dynamic publication only after that
				// provider bootstrap boundary has been processed, so a page
				// update cannot overtake the initial configuration.
				publisher.markSessionReady()
			}
			if msg.Type == messages.StreamTypeSessionOpen {
				startSessionUpdatedTimer()
			}
			if opts.observer != nil && opts.observer.scheduledAudioReady() {
				stopSessionUpdatedTimer()
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
			result.durationTerminalWritten = terminalState.terminalWritten
			result.planned = durationExpired
			result.stop = false
			return result, nil
		}
		result.durationTerminalWritten = terminalState.terminalWritten
	}
	opts.observer.observe(msg)
	if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
		return result, err
	}
	if err := retryScheduledRateLimitedResponse(ctx, sessionDone, deadline, loop, opts.observer, msg); err != nil {
		return result, err
	}
	promptProvided := opts.PromptProvided || opts.Prompt != ""
	if msg.Type == messages.StreamTypeSessionOpen && !durationExpired {
		if promptProvided && !result.promptSent {
			result.promptSent = true
			userMsg := messages.NewTextMessage(messages.RoleUser, opts.Prompt)
			if err := loop.Send(ctx, []messages.Message{userMsg}); err != nil {
				return result, fmt.Errorf("send session message: %w", err)
			}
			opts.observer.noteUserTextInput(opts.Prompt)
			if opts.awaitFirstTurn != nil {
				if err := awaitSessionFirstTurn(ctx, opts.awaitFirstTurn); err != nil {
					return result, fmt.Errorf("send session first turn: %w", err)
				}
			}
		}
		if opts.CloseAfterOpen && !promptProvided && !result.closeSent {
			result.closeAfterOpenPending = true
		}
	}
	var err error
	result.closeSent, err = processDurationScheduledMessage(ctx, loop, msg, opts, result.closeSent)
	if err != nil {
		return result, err
	}
	if !durationExpired && opts.CloseAfterOpen && promptProvided && msg.Type == messages.StreamTypeMessageEnd && !result.closeSent && (opts.observer == nil || opts.observer.lastMessageEndAdmitted()) {
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
	result.stop = shouldStopSessionLoop(msg, opts, result.closeSent) && (!durationExpired || msg.Type == messages.StreamTypeSessionClose)
	result.planned = durationExpired
	return result, nil
}

func processDurationScheduledMessage(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage, opts sessionLoopOptions, closeSent bool) (bool, error) {
	if !shouldDispatchScheduledAudioForMessage(msg, opts.ScheduledAudioDispatch) {
		return closeSent, nil
	}
	if err := opts.observer.dispatchScheduledInputs(ctx, loop); err != nil {
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

func drainDurationSessionLoopMessages(out io.Writer, loop *agentloop.AgentLoop, planned bool, terminalWritten *bool, artifacts SessionDurationArtifactLifecycle, obs *sessionProgressObserver, terminalState *sessionDurationTerminalState) error {
	for {
		msg, ok := loop.Deltas().Read()
		if !ok {
			return nil
		}
		if terminalState != nil {
			terminalState.observe(msg)
			var shouldWrite bool
			msg, shouldWrite = terminalState.admitTerminal(planned, msg)
			*terminalWritten = terminalState.terminalWritten
			if !shouldWrite {
				continue
			}
		}
		obs.observe(msg)
		if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
			return err
		}
	}
}

func drainDurationSessionLoopMessagesUntilQuiet(out io.Writer, loop *agentloop.AgentLoop, planned bool, terminalWritten *bool, artifacts SessionDurationArtifactLifecycle, obs *sessionProgressObserver, terminalState *sessionDurationTerminalState) error {
	timer := time.NewTimer(sessionReplayDoneDrainIdleDelay)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return nil
			}
			if terminalState != nil {
				terminalState.observe(msg)
				var shouldWrite bool
				msg, shouldWrite = terminalState.admitTerminal(planned, msg)
				*terminalWritten = terminalState.terminalWritten
				if !shouldWrite {
					continue
				}
			}
			obs.observe(msg)
			if err := writeDurationSessionReplayMessage(out, msg, artifacts); err != nil {
				return err
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(sessionReplayDoneDrainIdleDelay)
		case <-timer.C:
			return nil
		}
	}
}
