package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeDuration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
	"io"
	"sync"
	"time"
)

type realSessionDurationClock = platformclock.Real

type durationRunner struct {
	opts                                         sessionLoopOptions
	loop                                         *agentloop.AgentLoop
	observed                                     *observedSessionInferencer
	handleObserver                               *sessionProgressObserver
	publisher                                    *sessionDynamicToolPublisher
	cancel                                       context.CancelFunc
	loopDone, result                             <-chan error
	rtcErrors                                    <-chan error
	asyncErrors                                  chan error
	runCtx                                       context.Context
	sessionUpdatedTimer                          platformclock.Timer
	timerMu                                      sync.Mutex
	stopOnce                                     sync.Once
	stopErr                                      error
	closeSent, promptSent, closeAfterOpenPending bool
}

func (r *durationRunner) Start(parent context.Context, inferencer messages.SessionInferencer) (runtimeDuration.Handle, error) {
	loop, observed, rtcErrors, err := newObservedSessionLoop(inferencer, r.opts)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(parent)
	r.asyncErrors = make(chan error, 1)
	r.runCtx = runCtx
	r.handleObserver = r.opts.observer
	if r.handleObserver == nil {
		r.handleObserver = newSessionProgressObserver(nil, nil, "", "")
	}
	publisher, publisherErrors := startSessionDynamicToolPublisher(runCtx, loop, r.opts)
	publisherErrors = mergeSessionErrorChannels(runCtx, publisherErrors, sessionLivenessErrorChannel(runCtx, r.opts.observer))
	if r.opts.loopReady != nil {
		select {
		case r.opts.loopReady <- loop:
		case <-runCtx.Done():
			cancel()
			if publisher != nil {
				publisher.stop()
			}
			return nil, runCtx.Err()
		}
	}
	loopResult := make(chan error, 1)
	loopDone := make(chan error, 1)
	go func() { err := loop.Run(runCtx); loopResult <- err; loopDone <- err }()
	r.observed = observed
	firstResult := (runtimeDuration.ResultInputs{Context: runCtx, Loop: loopResult, Publisher: publisherErrors, RTC: rtcErrors, Async: r.asyncErrors, SessionDone: observed.Done(), SessionError: r.sessionError, Done: r.opts.Done, DoneError: r.opts.DoneErr}).FirstResult()
	r.loop, r.publisher, r.cancel, r.loopDone, r.result, r.rtcErrors = loop, publisher, cancel, loopDone, firstResult, rtcErrors
	return r, nil
}
func (r *durationRunner) sessionError() error {
	if err := r.observed.connectFailure(); err != nil {
		return fmt.Errorf("session connect: %w", err)
	}
	if err := r.observed.sessionFailure(); err != nil {
		return fmt.Errorf("session transport: %w", err)
	}
	return nil
}
func (r *durationRunner) Deltas() *messages.TypedBuffer[messages.StreamMessage] {
	return r.loop.Deltas()
}
func (r *durationRunner) Result() <-chan error { return r.result }
func (r *durationRunner) SendClose(ctx context.Context) error {
	if r.closeSent {
		return nil
	}
	r.closeSent = true
	if r.loop == nil {
		return errors.New("duration session loop is not started")
	}
	return sendSessionClose(ctx, r.loop)
}
func (r *durationRunner) Stop(ctx context.Context) error {
	r.stopOnce.Do(func() {
		r.stopErr = (runtimeDuration.CleanupHooks{StopTimer: r.stopSessionUpdatedTimer, StopPublisher: r.publisher.stop, CloseSession: r.observed.CloseSession, RTCError: r.rtcErrors, CloseBare: func() error { return closeBareSessionIfNeeded(r.opts.BareLive, r.observed) }, CloseBinding: func() error { return closeRTCDeviceBinding(r.opts.rtcDeviceBinding) }, Cancel: r.cancel, LoopDone: r.loopDone, StopLiveness: r.opts.observer.stopLiveness}).StopResources(ctx)
	})
	return r.stopErr
}
func (r *durationRunner) handle(ctx context.Context, out io.Writer, msg messages.StreamMessage, state runtimeDuration.MessageState) (runtimeDuration.MessageResult, error) {
	opts := r.opts
	opts.observer = r.handleObserver
	if err := r.prepareSessionMessage(msg, state); err != nil {
		return runtimeDuration.MessageResult{}, err
	}
	loopState := sessionLoopMessageState{promptSent: r.promptSent, closeSent: r.closeSent, closeAfterOpenPending: r.closeAfterOpenPending}
	next, stop, err := handleSessionLoopMessage(ctx, r.observedDone(), state.Deadline, out, r.loop, opts, msg, loopState, false, func() {}, func(err error) error { return err })
	r.promptSent, r.closeSent, r.closeAfterOpenPending = next.promptSent, next.closeSent, next.closeAfterOpenPending
	if err != nil {
		return runtimeDuration.MessageResult{}, err
	}
	if r.handleObserver.scheduledAudioReady() {
		r.stopSessionUpdatedTimer()
	}
	if r.opts.observer == nil && shouldStopSessionLoop(msg, r.opts) {
		stop = true
	}
	return runtimeDuration.MessageResult{Stop: stop, Planned: state.DurationExpired || stop && sessionDurationTimerReady(state.Deadline)}, nil
}
func (r *durationRunner) prepareSessionMessage(msg messages.StreamMessage, state runtimeDuration.MessageState) error {
	if msg.Type == messages.StreamTypeSessionCreated && r.publisher != nil {
		r.publisher.markSessionReady()
	}
	if !r.needsSessionUpdatedTimer(msg, state) {
		return nil
	}
	timeout := r.opts.SessionUpdatedTimeout
	if timeout <= 0 {
		timeout = sessionScheduledAudioConfigTimeout
	}
	timer, err := newSessionTimer(r.opts.clockSource, timeout)
	if err != nil {
		return err
	}
	r.timerMu.Lock()
	defer r.timerMu.Unlock()
	if r.sessionUpdatedTimer == nil {
		r.sessionUpdatedTimer = timer
		go r.awaitSessionUpdated(timer)
		return nil
	}
	timer.Stop()
	return nil
}
func (r *durationRunner) needsSessionUpdatedTimer(msg messages.StreamMessage, state runtimeDuration.MessageState) bool {
	return msg.Type == messages.StreamTypeSessionOpen && !state.DurationExpired && r.opts.RequireSessionUpdated && r.opts.observer != nil && r.opts.observer.scheduledAudioAwaitingConfiguration()
}
func (r *durationRunner) awaitSessionUpdated(timer platformclock.Timer) {
	select {
	case <-timer.C():
		err := sessionScheduledAudioConfigTimeoutError(r.opts)
		select {
		case r.asyncErrors <- err:
		default:
		}
	case <-r.runCtx.Done():
	}
}
func (r *durationRunner) stopSessionUpdatedTimer() {
	r.timerMu.Lock()
	defer r.timerMu.Unlock()
	if r.sessionUpdatedTimer != nil {
		r.sessionUpdatedTimer.Stop()
		r.sessionUpdatedTimer = nil
	}
}
func (r *durationRunner) observedDone() <-chan struct{} { return r.observed.Done() }
func runAgentLoopSessionWithDurationClock(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock SessionDurationClock) error {
	reporter := opts.terminalReporter
	ownsReporter := reporter == nil
	if reporter == nil {
		reporter = newSessionTerminalReporter()
		opts.terminalReporter = reporter
	}
	reporter.markRunStarted()
	renderer := newSessionReplayRenderer(out, reporter)
	runErr := runAgentLoopSessionWithDurationClockStream(ctx, renderer, inferencer, opts, maxDuration, clock)
	runErr = scheduledAudioCompletionError(runErr, opts)
	cleanSIGINT := sessionSIGINTCleanForObserver(runErr, opts.cancellationIntent, opts.observer)
	runErr = opts.observer.finish(runErr)
	if cleanSIGINT {
		runErr = errors.Join(runErr, publishSessionUserCancellation(renderer, opts, nil))
	}
	if ownsReporter {
		if err := renderer.finishTranscript(); err != nil {
			runErr = errors.Join(runErr, err)
		}
		runErr = errors.Join(runErr, reporter.publish(out, runErr))
	}
	return runErr
}
func runAgentLoopSessionWithDurationClockStream(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock SessionDurationClock) error {
	ctx = sessionDurationContext(ctx)
	if clock == nil {
		clock = realSessionDurationClock{}
	}
	runner := &durationRunner{opts: opts}
	service := durationwire.NewServiceWithClock(clock)
	return service.Run(ctx, runtimeDuration.RunRequest{
		MaxDuration:      maxDuration,
		Inferencer:       inferencer,
		Runner:           runner,
		Clock:            clock,
		Artifacts:        sessionDurationArtifactsFromContext(ctx),
		ArtifactPaths:    sessionDurationArtifactPathsForRequest(ctx),
		TerminalRecorder: sessionDurationTerminalRecorderFromContext(ctx),
		OnArtifactsFinalized: func(err error) {
			if opts.terminalReporter != nil {
				opts.terminalReporter.recordArtifactFinalization(sessionDurationArtifactsFromContext(ctx) != nil, err)
			}
		},
		Handle: func(messageCtx context.Context, msg messages.StreamMessage, state runtimeDuration.MessageState) (runtimeDuration.MessageResult, error) {
			return runner.handle(messageCtx, out, msg, state)
		},
		Quiesce: opts.quiesceUpstream,
		OnFinish: func(planned bool, outputState messages.TerminalOutputState) {
			markSessionDurationExpiry(opts.terminalReporter, planned, outputState)
		},
	})
}
func sessionDurationTimerReady(ch <-chan time.Time) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
