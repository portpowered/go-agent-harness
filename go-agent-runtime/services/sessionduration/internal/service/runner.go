package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/internal/stream"
)

const defaultLoopJoinTimeout = 5 * time.Second

// Run is the single bounded-session loop boundary. Host-specific prompt,
// scheduled-input, and rendering behavior is supplied as a handler; deadline,
// admission, terminal publication, and cleanup remain service-owned.
//
// ctx bounds the run: the loop, its handlers, and the signal fan-in run under
// a context derived from it, and finalization detaches from its cancellation
// so ordered cleanup still completes.
func (s *Service) Run(ctx context.Context, request sessionduration.RunRequest) error {
	if ctx == nil {
		return sessionduration.ErrContextRequired
	}
	if err := validateRunRequest(s, request); err != nil {
		return err
	}
	admitted := runAdmission(request)
	durationController, err := s.Begin(ctx, sessionduration.Options{
		Clock:         request.Clock,
		LivenessClock: request.LivenessClock,
		MaxDuration:   request.MaxDuration,
		DeferStart:    true,
		Liveness:      request.Liveness,
		Retry:         request.Retry,
		Terminal:      runTerminalSource(request.Terminal, admitted),
		Publication:   request.Publication,
		Artifacts:     request.Artifacts,
	})
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	loop, err := buildRunLoop(request, admitted, durationController, runCtx)
	if err != nil {
		admitted.CloseAdmission()
		cancel()
		_, finalizeErr := durationController.Finalize(ctx, sessionduration.FinalizeRequest{
			Primary:   err,
			Close:     request.Close,
			Binding:   request.Binding,
			Artifacts: request.Artifacts,
		})
		return finalizeErr
	}
	runner := &runLoop{
		cancel:     cancel,
		controller: durationController,
		admitted:   admitted,
		loop:       loop,
		request:    request,
		runErrs:    make(chan error, 1),
		service:    s,
	}
	runner.bindSources(runCtx)
	runner.start(runCtx)
	if err := durationController.Start(); err != nil {
		return runner.finish(runCtx, false, err)
	}
	return runner.run(runCtx)
}

func validateRunRequest(s *Service, request sessionduration.RunRequest) error {
	if err := s.ValidateDuration(request.MaxDuration); err != nil {
		return err
	}
	if request.Inferencer == nil && request.Admission == nil {
		return errors.New("session duration inferencer is required")
	}
	if request.LoopFactory == nil {
		return errors.New("session duration loop factory is required")
	}
	return nil
}

func runAdmission(request sessionduration.RunRequest) sessionduration.AdmissionInferencer {
	if request.Admission != nil {
		return request.Admission
	}
	return NewAdmissionInferencer(request.Inferencer, NewEventAdmission(), nil)
}

func runTerminalSource(source sessionduration.TerminalSource, admitted sessionduration.AdmissionInferencer) sessionduration.TerminalSource {
	if source.Message == nil {
		source.Message = admitted.ProviderTerminalMessage
	}
	if source.Matches == nil {
		source.Matches = admitted.IsProviderTerminalMessage
	}
	return source
}

func buildRunLoop(request sessionduration.RunRequest, admitted sessionduration.AdmissionInferencer, controller sessionduration.Controller, ctx context.Context) (sessionduration.Loop, error) {
	loop, err := request.LoopFactory(ctx, admitted, controller)
	if err != nil {
		return nil, err
	}
	if loop == nil || loop.Deltas() == nil {
		return nil, errors.New("session duration loop factory returned an invalid loop")
	}
	return loop, nil
}

// runLoop owns one run's event state. The run context is passed to each
// method rather than stored; the loop keeps only its cancel function.
type runLoop struct {
	cancel     context.CancelFunc
	controller sessionduration.Controller
	admitted   sessionduration.AdmissionInferencer
	loop       sessionduration.Loop
	request    sessionduration.RunRequest
	runErrs    chan error
	loopErr    error
	loopDone   bool
	external   <-chan error
	done       <-chan struct{}
	updated    sessionTimer
	pending    []messages.StreamMessage
	finishOnce sync.Once
	startOnce  sync.Once
	finished   bool
	finishErr  error
	service    *Service
}

type runLoopEvent struct {
	kind  runLoopEventKind
	err   error
	msg   messages.StreamMessage
	valid bool
}

type runLoopEventKind uint8

const (
	runLoopControllerError runLoopEventKind = iota
	runLoopError
	runLoopExternalError
	runLoopWake
	runLoopDone
	runLoopMessage
	runLoopContext
	runLoopWakeClosed
	runLoopSessionUpdated
)

func (r *runLoop) run(ctx context.Context) error {
	defer r.cancelRun()
	defer r.stopSessionUpdated()
	r.start(ctx)
	for {
		if result, done := r.handleEvent(ctx, r.nextEvent(ctx)); done {
			return result
		}
	}
}

func (r *runLoop) start(ctx context.Context) {
	r.startOnce.Do(func() {
		go func(ctx context.Context) { r.runErrs <- r.loop.Run(ctx) }(ctx)
	})
}

func (r *runLoop) nextEvent(ctx context.Context) runLoopEvent {
	select {
	case err := <-r.controller.Errors():
		return runLoopEvent{kind: runLoopControllerError, err: err}
	case err := <-r.runErrs:
		return runLoopEvent{kind: runLoopError, err: err}
	case err := <-r.external:
		return runLoopEvent{kind: runLoopExternalError, err: err}
	case _, ok := <-r.request.Wake:
		if !ok {
			r.request.Wake = nil
			return runLoopEvent{kind: runLoopWakeClosed}
		}
		return runLoopEvent{kind: runLoopWake}
	case <-r.done:
		return runLoopEvent{kind: runLoopDone}
	case <-r.sessionUpdatedC():
		return runLoopEvent{kind: runLoopSessionUpdated}
	case msg, ok := <-r.loop.Deltas().Chan():
		return runLoopEvent{kind: runLoopMessage, msg: msg, valid: ok}
	case <-ctx.Done():
		return runLoopEvent{kind: runLoopContext, err: ctx.Err()}
	}
}

func (r *runLoop) handleEvent(ctx context.Context, event runLoopEvent) (error, bool) {
	switch event.kind {
	case runLoopControllerError:
		return r.finishControllerError(ctx, event.err), true
	case runLoopError:
		r.loopErr = event.err
		r.loopDone = true
		return r.finish(ctx, false, runLoopFailure(ctx, event.err)), true
	case runLoopExternalError:
		return r.finish(ctx, false, event.err), true
	case runLoopWake:
		return r.handleWake(ctx)
	case runLoopWakeClosed:
		return nil, false
	case runLoopDone:
		return r.finish(ctx, false, runLoopDoneError(r.request)), true
	case runLoopMessage:
		if !event.valid {
			return r.finish(ctx, false, nil), true
		}
		if err := r.process(ctx, event.msg); err != nil {
			return r.finish(ctx, false, err), true
		}
		if r.finished {
			return r.finishErr, true
		}
		return nil, false
	case runLoopContext:
		return r.finish(ctx, false, event.err), true
	case runLoopSessionUpdated:
		r.stopSessionUpdated()
		return r.finish(ctx, false, r.request.SessionUpdated.TimeoutError), true
	default:
		return nil, false
	}
}

func (r *runLoop) handleWake(ctx context.Context) (error, bool) {
	if r.request.OnWake == nil {
		return nil, false
	}
	if err := r.request.OnWake(ctx, r.loop, r.controller); err != nil {
		return r.finish(ctx, false, err), true
	}
	return nil, false
}

func runLoopDoneError(request sessionduration.RunRequest) error {
	if request.DoneError == nil {
		return nil
	}
	return request.DoneError()
}

func (r *runLoop) finishControllerError(ctx context.Context, err error) error {
	if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
		return r.finish(ctx, true, nil)
	}
	return r.finish(ctx, false, err)
}

func (r *runLoop) process(ctx context.Context, msg messages.StreamMessage) error {
	admission := r.controller.Observe(msg)
	if !admission.Accepted {
		r.pending = append(r.pending, msg)
		return nil
	}
	if err := publish(r.request.Publication, admission.Message); err != nil {
		return err
	}
	if admission.LivenessErr != nil {
		return admission.LivenessErr
	}
	if err := r.retry(ctx, admission.Message); err != nil {
		if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
			return r.finish(ctx, true, nil)
		}
		return err
	}
	if r.finished {
		return nil
	}
	if r.request.Handle == nil {
		return r.observeSessionUpdated(admission.Message)
	}
	result, err := r.request.Handle(ctx, r.loop, r.controller, admission.Message)
	if err != nil {
		if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
			return r.finish(ctx, true, nil)
		}
		return err
	}
	if !result.Stop {
		return r.observeSessionUpdated(admission.Message)
	}
	return r.finish(ctx, result.Planned, nil)
}

// finish runs ordered finalization once. ctx is the run context; Finalize
// detaches from its cancellation, and the loop join below is bounded by its
// own timeout on a detached copy.
func (r *runLoop) finish(ctx context.Context, planned bool, primary error) error {
	r.finishOnce.Do(func() {
		r.admitted.CloseAdmission()
		if planned {
			primary = errors.Join(primary, sendLoopClose(ctx, r.loop))
		}
		drainPolicy := r.request.DrainPolicy
		if drainPolicy.Clock == nil {
			drainPolicy.Clock = r.request.Clock
		}
		r.stopSessionUpdated()
		result, finalizeErr := r.controller.Finalize(ctx, sessionduration.FinalizeRequest{
			Primary: primary,
			Drain: func(ctx context.Context) error {
				drainErr := r.drainPending()
				if drainErr == nil && r.request.Drain != nil {
					drainErr = r.request.Drain(ctx, r.loop, r.controller)
				}
				r.cancelRun()
				return drainErr
			},
			DrainLoop:   r.loop,
			DrainPolicy: drainPolicy,
			Close: func() error {
				var closeErr error
				if r.request.Close != nil {
					closeErr = r.request.Close()
				}
				joinCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loopJoinTimeout(drainPolicy))
				defer cancel()
				loopErr := r.waitForLoop(joinCtx)
				if errors.Is(primary, loopErr) {
					loopErr = nil
				}
				r.awaitAdmissionClose()
				return errors.Join(closeErr, loopErr)
			},
			Binding:   r.request.Binding,
			Artifacts: r.request.Artifacts,
		})
		r.finishErr = errors.Join(finalizeErr, r.service.LifecycleError(sessionduration.LifecycleFailures{
			Runtime: r.admitted.RuntimeError(),
			Close:   r.admitted.CloseError(),
		}))
		r.finishErr = r.complete(result, r.finishErr)
		r.finished = true
	})
	return r.finishErr
}

// bindSources evaluates host signal sources once the loop exists. Forwarding
// workers stop with the run context, so no worker outlives the run.
func (r *runLoop) bindSources(ctx context.Context) {
	errorSources := []<-chan error{r.request.ExternalErrors}
	if r.request.ExternalErrorSources != nil {
		errorSources = append(errorSources, r.request.ExternalErrorSources()...)
	}
	r.external = stream.FanInErrors(ctx, errorSources)
	doneSources := []<-chan struct{}{r.request.Done}
	if r.request.DoneSources != nil {
		doneSources = append(doneSources, r.request.DoneSources()...)
	}
	r.done = stream.FanInDone(ctx, doneSources)
}
