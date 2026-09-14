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
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// newSessionLiveTerminationBoundary keeps live-loop exit paths on the shared
// terminal service while leaving provider/device callbacks with the host.
func newSessionLiveTerminationBoundary(ctx context.Context, quiesceUpstream, stopOwnedResources func() error, out io.Writer, loop *agentloop.AgentLoop, opts sessionLoopOptions, observedInferencer *observedSessionInferencer) sessionterminal.TerminationBoundary {
	return sessionterminalwire.NewTerminationBoundary(sessionterminal.TerminationOptions{
		Context: ctx, QuiesceUpstream: quiesceUpstream,
		WaitForStragglers: func() error {
			// Keep the injected clock as the canonical quiet-period source so
			// runtime timestamps and scheduling remain in one domain. The drain
			// itself also has a wall-time safety bound for deterministic clocks.
			return waitForSessionLoopStragglersWithContext(ctx, out, loop, sessionterminal.StragglerDrainQuietPeriod, opts.observer, opts.clockSource)
		},
		StopOwnedResources: stopOwnedResources,
		FlushBuffered: func() error {
			flushErr := flushBufferedSessionLoopMessages(out, loop, opts.observer)
			if opts.observer != nil {
				// Recover provider tool lifecycle identity after the hot loop stops;
				// this avoids duplicate output accounting.
				opts.observer.ObserveBufferedProviderToolLifecycle(loop.GetConversationDeltas())
			}
			if sessionErr := observedInferencer.sessionFailure(); sessionErr != nil {
				flushErr = errors.Join(flushErr, fmt.Errorf("session transport: %w", sessionErr))
			}
			return flushErr
		},
	})
}

func newSessionDurationTerminationBoundary(ctx context.Context, out io.Writer, loop *agentloop.AgentLoop, opts sessionLoopOptions, durationClock SessionDurationClock, terminationPlanned *bool, durationTerminalWritten *bool, artifacts SessionDurationArtifactLifecycle, terminalState *sessionDurationTerminalState, cancel context.CancelFunc, observedInferencer *observedSessionInferencer, admittedInferencer *sessionDurationAdmissionInferencer, drainDevicePlayback *bool, waitRun func() error) sessionterminal.TerminationBoundary {
	return sessionterminalwire.NewTerminationBoundary(sessionterminal.TerminationOptions{
		Context: ctx, QuiesceUpstream: opts.quiesceUpstream,
		WaitForStragglers: func() error {
			return waitForDurationSessionLoopStragglers(out, loop, sessionterminal.StragglerDrainQuietPeriod, durationClock, *terminationPlanned, durationTerminalWritten, artifacts, opts.observer, terminalState)
		},
		StopOwnedResources: func() error {
			var drainErr error
			if *drainDevicePlayback {
				drainErr = observedInferencer.DrainSessionPlayback(ctx)
			}
			cancel()
			providerErr := closeBareSessionIfNeeded(opts.BareLive, observedInferencer)
			bindingErr := closeRTCDeviceBinding(opts.rtcDeviceBinding)
			runTerminationErr := joinSessionTerminationErrors(waitRun(), nil)
			admittedInferencer.waitForClose()
			return errors.Join(drainErr, providerErr, runTerminationErr, bindingErr)
		},
		FlushBuffered: func() error {
			flushErr := flushBufferedDurationSessionLoopMessages(out, loop, *terminationPlanned, durationTerminalWritten, artifacts, opts.observer, terminalState)
			if *terminationPlanned && !terminalState.written() {
				flushErr = errors.Join(flushErr, terminalState.writeObservedProviderTerminal(out, artifacts))
			}
			return flushErr
		},
	})
}

// prepareSessionStreamOutput gives an unowned stream its terminal renderer.
// The returned finalizer preserves transcript errors before publishing status.
func prepareSessionStreamOutput(out io.Writer, opts *sessionLoopOptions) (io.Writer, func(error) error) {
	if opts.terminalReporter != nil {
		return out, func(err error) error { return err }
	}
	reporter := sessionterminalwire.NewReporter()
	opts.terminalReporter = reporter
	reporter.MarkRunStarted()
	renderer := newSessionReplayRenderer(out, reporter)
	return renderer, func(runErr error) error {
		runErr = errors.Join(runErr, renderer.finishTranscript())
		return errors.Join(runErr, reporter.Publish(out, runErr))
	}
}

func newObservedSessionLoop(inferencer messages.SessionInferencer, opts sessionLoopOptions) (*agentloop.AgentLoop, *observedSessionInferencer, <-chan error, error) {
	inferencer, pumpErrors := bindRTCDeviceSessionInferencer(inferencer, opts.rtcDeviceBinding)
	if err := ensureRTCDeviceBindingBuffers(opts.rtcDeviceBinding); err != nil {
		return nil, nil, nil, err
	}
	observed := newObservedSessionInferencer(inferencer, opts.runtime)
	observed.progress = opts.observer
	if opts.observer != nil {
		opts.observer.SetLivenessClock(opts.livenessClock)
		opts.observer.SetToolResultsEnabled(opts.ToolExecutor != nil)
	}
	loop, err := agentloop.New(duplexSessionLoopOptions(observed, opts)...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create session agent loop: %w", err)
	}
	return loop, observed, pumpErrors, nil
}

func sessionStreamDeadline(opts sessionLoopOptions) (<-chan time.Time, func(), error) {
	if opts.MaxDuration <= 0 {
		return nil, func() {}, nil
	}
	timer, err := newSessionTimer(opts.clockSource, opts.MaxDuration)
	if err != nil {
		return nil, nil, err
	}
	return timer.C(), func() { timer.Stop() }, nil
}

func bindSessionLoopInputs(runCtx, audioCtx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions) error {
	if opts.loopReady != nil {
		select {
		case opts.loopReady <- loop:
		case <-runCtx.Done():
			return runCtx.Err()
		}
	}
	if opts.AudioIn != nil {
		opts.AudioIn.bindContext(audioCtx)
	}

	return nil
}

func startSessionUpdatedTimer(opts sessionLoopOptions, timer *platformclock.Timer, timeout *<-chan time.Time) error {
	if timer == nil || *timer != nil || !opts.RequireSessionUpdated || opts.observer == nil || !opts.observer.ScheduledAudioAwaitingConfiguration() {
		return nil
	}
	duration := opts.SessionUpdatedTimeout
	if duration <= 0 {
		duration = sessionScheduledAudioConfigTimeout
	}
	next, err := newSessionTimer(opts.clockSource, duration)
	if err != nil {
		return err
	}
	*timer, *timeout = next, next.C()
	return nil
}

func stopSessionUpdatedTimer(timer *platformclock.Timer, timeout *<-chan time.Time) {
	if timer == nil || *timer == nil {
		return
	}
	(*timer).Stop()
	*timer, *timeout = nil, nil
}

func (s *observedSession) lockProviderBoundary() func() {
	if s == nil || s.progress == nil {
		return func() {}
	}
	return s.progress.LockProviderBoundary()
}

func (s *observedSession) markDone() {
	s.once.Do(s.closeDone)
}
