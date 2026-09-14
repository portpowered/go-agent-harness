package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessionterminalwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionterminal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace"
	sessiontracewire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type realSessionDurationClock struct{}

func (realSessionDurationClock) NewTimer(duration time.Duration) SessionDurationTimer {
	return platformclock.Real{}.NewTimer(duration)
}

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
	cleanSIGINT := opts.observer != nil && opts.observer.CancellationClean(runErr)
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
	var rtcPumpErrors <-chan error
	boundInferencer, rtcErrors := bindRTCDeviceSessionInferencer(admittedInferencer, opts.rtcDeviceBinding)
	rtcPumpErrors = rtcErrors
	if err := ensureRTCDeviceBindingBuffers(opts.rtcDeviceBinding); err != nil {
		return err
	}
	observedInferencer := newObservedSessionInferencer(boundInferencer)
	observedInferencer.progress = opts.observer
	if opts.observer != nil {
		opts.observer.SetLivenessClock(opts.livenessClock)
		opts.observer.SetToolResultsEnabled(opts.ToolExecutor != nil)
	}
	if opts.observer != nil {
		defer opts.observer.StopLiveness()
	}
	loop, err := agentloop.New(duplexSessionLoopOptions(observedInferencer, opts)...)
	if err != nil {
		return fmt.Errorf("create session agent loop: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	publisher, publisherErrors := startSessionDynamicToolPublisher(runCtx, loop, opts)
	if opts.observer != nil {
		publisherErrors = sessiontracewire.MergeErrorChannels(runCtx, publisherErrors, opts.observer.LivenessErrors(runCtx))
	}
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
	termination := newSessionDurationTerminationBoundary(ctx, out, loop, opts, durationClock, &terminationPlanned, &durationTerminalWritten, artifacts, terminalState, cancel, observedInferencer, admittedInferencer, &drainDevicePlayback, waitRun)

	finish := func(planned bool, preferredErr error) error {
		terminationPlanned = planned
		drainDevicePlayback = !planned && preferredErr == nil && ctx.Err() == nil
		terminationErr := termination.Terminate(preferredErr)
		durationTerminalWritten = terminalState.written()
		opts.terminalReporter.MarkDurationExpiry(planned, terminalState.outputState())
		sessionErr := observedInferencer.sessionFailure()
		runtimeErr := admittedInferencer.runtimeError()
		closeErr := admittedInferencer.closeError()
		lifecycleErr := sessionDurationLifecycleError(runtimeErr, closeErr, nil)
		transportErr := sessionTransportError(sessionErr)
		if terminationErr != nil {
			return errors.Join(terminationErr, lifecycleErr, transportErr)
		}
		if lifecycleErr != nil {
			return lifecycleErr
		}
		if sessionErr != nil {
			return sessionTransportError(sessionErr)
		}
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			return fmt.Errorf("session error: %w", runErr)
		}
		if planned && !terminalState.written() {
			if err := terminalState.writeMaxDurationTerminal(out, artifacts, terminalState.outputState()); err != nil {
				return err
			}
			durationTerminalWritten = terminalState.written()
		}
		return nil
	}

	timer := durationClock.NewTimer(maxDuration)
	if timer == nil {
		admittedInferencer.closeAdmission()
		return finish(false, errors.New("session duration clock returned a nil timer"))
	}
	defer func() { timer.Stop() }()
	timerCh := timer.C()

	var sessionUpdatedTimer SessionDurationTimer
	var sessionUpdatedTimeout <-chan time.Time
	startSessionUpdatedTimer := func() error {
		if !opts.RequireSessionUpdated || opts.observer == nil || !opts.observer.ScheduledAudioAwaitingConfiguration() || sessionUpdatedTimer != nil {
			return nil
		}
		timeout := opts.SessionUpdatedTimeout
		if timeout <= 0 {
			timeout = sessionScheduledAudioConfigTimeout
		}
		sessionUpdatedTimer = durationClock.NewTimer(timeout)
		if sessionUpdatedTimer == nil {
			return errors.New("session duration clock returned a nil session-updated timer")
		}
		sessionUpdatedTimeout = sessionUpdatedTimer.C()
		return nil
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
			if err := startSessionUpdatedTimer(); err != nil {
				return result, err
			}
		}
		if opts.observer != nil && opts.observer.ScheduledAudioReady() {
			stopSessionUpdatedTimer()
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
			if err := opts.observer.DispatchScheduledInputs(runCtx, loop); err != nil {
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
	promptProvided := opts.PromptProvided || opts.Prompt != ""
	if msg.Type == messages.StreamTypeSessionOpen && !durationExpired {
		if promptProvided && !result.promptSent {
			result.promptSent = true
			if err := sendSessionOpenPrompt(ctx, loop, opts); err != nil {
				return result, err
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
	if !durationExpired && opts.CloseAfterOpen && promptProvided && msg.Type == messages.StreamTypeMessageEnd && !result.closeSent && (opts.observer == nil || opts.observer.LastMessageEndAdmitted()) {
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

// waitForDurationSessionLoopStragglers waits for provider deltas during the
// required positive policy quiet period. Only the shared termination boundary
// selects this waiting operation for terminal cleanup.
func waitForDurationSessionLoopStragglers(out io.Writer, loop *agentloop.AgentLoop, quiet time.Duration, durationClock SessionDurationClock, planned bool, terminalWritten *bool, artifacts SessionDurationArtifactLifecycle, obs sessiontrace.Observer, terminalState *sessionDurationTerminalState) error {
	if quiet <= 0 {
		return errors.New("session straggler drain requires a positive quiet period")
	}
	if durationClock == nil {
		return errors.New("session duration clock is required for straggler drain")
	}
	timer := durationClock.NewTimer(quiet)
	if timer == nil {
		return errors.New("session duration clock returned a nil straggler timer")
	}
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
			nextTimer, resetErr := resetDurationStragglerTimer(timer, durationClock, quiet)
			if resetErr != nil {
				return resetErr
			}
			timer = nextTimer
		case <-timer.C():
			return nil
		}
	}
}

func resetDurationStragglerTimer(timer SessionDurationTimer, durationClock SessionDurationClock, quiet time.Duration) (SessionDurationTimer, error) {
	if !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
	next := durationClock.NewTimer(quiet)
	if next == nil {
		return nil, errors.New("session duration clock returned a nil straggler timer")
	}
	return next, nil
}
