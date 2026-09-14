package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// durationServiceLoop is the deliberately small host adapter exposed to the
// sessionduration service. It contains no duration policy; the service owns
// the controller, deadline, terminal admission, and finalization state.
type durationServiceLoop struct {
	inner     *agentloop.AgentLoop
	resources *durationServiceResources
}

type realSessionDurationClock struct{}

func (realSessionDurationClock) NewTimer(duration time.Duration) SessionDurationTimer {
	return platformclock.Real{}.NewTimer(duration)
}

func runAgentLoopSessionWithDurationClock(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock) error {
	return runAgentLoopSessionWithDurationAdmissionClock(ctx, out, sessionInferencer, opts, maxDuration, durationClock, nil)
}

func runAgentLoopSessionWithDurationAdmissionClock(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock, admittedInferencer duration.AdmissionInferencer) (runErr error) {
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
	cleanSIGINT := observerCancellationIsClean(runErr, opts.cancellationIntent, opts.observer)
	runErr = opts.observer.finish(runErr)
	if cleanSIGINT {
		artifacts := durationwire.NewService().ArtifactsFromContext(ctx)
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

func runAgentLoopSessionWithDurationAdmissionClockStream(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock, admittedInferencer duration.AdmissionInferencer) error {
	if maxDuration <= 0 {
		return runAgentLoopSession(ctx, out, sessionInferencer, opts)
	}
	return runAgentLoopSessionWithDurationService(ctx, out, sessionInferencer, opts, maxDuration, durationClock, admittedInferencer)
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

func writeDurationSessionReplayMessage(out io.Writer, msg messages.StreamMessage, artifacts duration.ArtifactLifecycle) error {
	if artifacts != nil {
		if err := artifacts.Accept(msg); err != nil {
			return wrapSessionPhaseError("write duration artifacts", err)
		}
	}
	return writeSessionReplayMessage(out, msg)
}

func (l *durationServiceLoop) Run(ctx context.Context) error {
	l.resources.mu.Lock()
	l.resources.runStarted = true
	l.resources.mu.Unlock()
	err := l.inner.Run(ctx)
	select {
	case l.resources.runResult <- err:
	default:
	}
	return err
}

func (l *durationServiceLoop) Deltas() *messages.TypedBuffer[messages.StreamMessage] {
	return l.inner.Deltas()
}

func (l *durationServiceLoop) Send(ctx context.Context, msg []messages.Message) error {
	return l.inner.Send(ctx, msg)
}

type durationServiceResources struct {
	mu             sync.Mutex
	ctx            context.Context
	opts           sessionLoopOptions
	out            io.Writer
	artifacts      duration.ArtifactLifecycle
	publication    duration.Publication
	clock          SessionDurationClock
	observed       *observedSessionInferencer
	controller     duration.Controller
	loop           *agentloop.AgentLoop
	publisher      *sessionDynamicToolPublisher
	rtcErrors      <-chan error
	externalErrors chan error
	runResult      chan error
	done           chan struct{}
	runStarted     bool
	runDone        bool
	runErr         error
	promptSent     bool
	closeSent      bool
	closeAfterOpen bool
	updatedTimer   SessionDurationTimer
	updatedTimeout <-chan time.Time
	drainPlayback  bool
	drainTimer     SessionDurationTimer
	closeOnce      sync.Once
}

func runAgentLoopSessionWithDurationService(ctx context.Context, out io.Writer, inferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, clock SessionDurationClock, admitted duration.AdmissionInferencer) error {
	const durationExternalErrorCapacity = 4
	service := durationwire.NewService()
	resources := &durationServiceResources{
		opts:           opts,
		out:            out,
		clock:          clock,
		externalErrors: make(chan error, durationExternalErrorCapacity),
		runResult:      make(chan error, 1),
		done:           make(chan struct{}),
	}
	artifacts := durationwire.NewService().ArtifactsFromContext(ctx)
	resources.artifacts = artifacts
	resources.publication = duration.Publication{
		//nolint:contextcheck // the stream observer contract is synchronous and has no context parameter.
		Write: func(msg messages.StreamMessage) error {
			if opts.observer != nil {
				opts.observer.observe(msg)
			}
			return writeDurationSessionReplayMessage(out, msg, artifacts)
		},
	}

	request := duration.RunRequest{
		Context:       ctx,
		Inferencer:    inferencer,
		Admission:     admitted,
		Clock:         clock,
		LivenessClock: durationLivenessClock(opts),
		MaxDuration:   maxDuration,
		Liveness: duration.LivenessOptions{
			Enabled: opts.observer != nil,
			Timeout: sessionProviderLivenessTimeout,
		},
		Retry: duration.RetryPolicy{
			Enabled:    opts.observer != nil,
			MaxRetries: 1,
		},
		Publication: resources.publication,
		LoopFactory: func(runCtx context.Context, admitted duration.AdmissionInferencer, controller duration.Controller) (duration.Loop, error) {
			return resources.buildLoop(runCtx, inferencer, admitted, controller)
		},
		Handle: func(runCtx context.Context, loop duration.Loop, controller duration.Controller, msg messages.StreamMessage) (duration.MessageResult, error) {
			return resources.handle(runCtx, loop, controller, msg)
		},
		Drain: func(drainCtx context.Context, loop duration.Loop, controller duration.Controller) error {
			return resources.drain(drainCtx, loop, controller)
		},
		Close: func() error {
			return resources.close()
		},
		Binding: func() error {
			return closeRTCDeviceBinding(opts.rtcDeviceBinding)
		},
		ExternalErrors: resources.externalErrors,
		Wake:           toolLifecycleEvents(opts.observer),
		OnWake: func(runCtx context.Context, loop duration.Loop, _ duration.Controller) error {
			return resources.handleWake(runCtx, loop)
		},
		Done: mergeDurationDone(opts.Done, resources.done),
		DoneError: func() error {
			if opts.DoneErr == nil {
				return nil
			}
			return opts.DoneErr()
		},
	}
	return service.Run(request)
}

func durationLivenessClock(opts sessionLoopOptions) duration.TimerScheduler {
	if opts.livenessClock != nil {
		return opts.livenessClock
	}
	return platformclock.Real{}
}

func toolLifecycleEvents(observer *sessionProgressObserver) <-chan struct{} {
	if observer == nil {
		return nil
	}
	return observer.toolLifecycleEvents()
}

func mergeDurationDone(first, second <-chan struct{}) <-chan struct{} {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-first:
		case <-second:
		}
		close(done)
	}()
	return done
}

func (r *durationServiceResources) buildLoop(ctx context.Context, inferencer messages.SessionInferencer, admitted duration.AdmissionInferencer, controller duration.Controller) (duration.Loop, error) {
	boundInferencer, rtcErrors := bindRTCDeviceSessionInferencer(admitted, r.opts.rtcDeviceBinding)
	if err := ensureRTCDeviceBindingBuffers(r.opts.rtcDeviceBinding); err != nil {
		return nil, err
	}
	observed := newObservedSessionInferencer(boundInferencer, r.opts.runtime)
	observed.progress = r.opts.observer
	r.opts.observer.setDurationController(controller)
	if r.opts.observer != nil {
		r.opts.observer.setToolResultsEnabled(r.opts.ToolExecutor != nil)
	}
	loop, err := agentloop.New(duplexSessionLoopOptions(observed, r.opts)...)
	if err != nil {
		return nil, fmt.Errorf("create session agent loop: %w", err)
	}
	publisher, publisherErrors := startSessionDynamicToolPublisher(ctx, loop, r.opts)
	r.ctx = ctx
	r.observed = observed
	r.controller = controller
	r.loop = loop
	r.publisher = publisher
	r.rtcErrors = rtcErrors
	go func() {
		select {
		case <-observed.Done():
			close(r.done)
		case <-ctx.Done():
		}
	}()
	forwardDurationServiceErrors(ctx, r.externalErrors, publisherErrors)
	forwardDurationServiceErrors(ctx, r.externalErrors, rtcErrors)
	if r.opts.loopReady != nil {
		select {
		case r.opts.loopReady <- loop:
		case <-ctx.Done():
			if publisher != nil {
				publisher.stop()
			}
			return nil, ctx.Err()
		}
	}
	return &durationServiceLoop{inner: loop, resources: r}, nil
}
