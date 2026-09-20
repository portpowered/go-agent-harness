package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

const defaultLoopJoinTimeout = 5 * time.Second

// Run is the single bounded-session loop boundary. Host-specific prompt,
// scheduled-input, and rendering behavior is supplied as a handler; deadline,
// admission, terminal publication, and cleanup remain service-owned.
func (s *Service) Run(request sessionduration.RunRequest) error {
	ctx := nonNilRunContext(request.Context)
	if err := validateRunRequest(s, request); err != nil {
		return err
	}
	admitted, err := runAdmission(request)
	if err != nil {
		return err
	}
	durationController, err := s.Begin(sessionduration.Options{
		Context:       ctx,
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
	runCtx, cancel := newRunContext(ctx)
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
		ctx:        ctx,
		runCtx:     runCtx,
		cancel:     cancel,
		controller: durationController,
		admitted:   admitted,
		loop:       loop,
		request:    request,
		runErrs:    make(chan error, 1),
		service:    s,
	}
	runner.start()
	if err := durationController.Start(); err != nil {
		return runner.finish(false, err)
	}
	return runner.run()
}

func nonNilRunContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
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

func runAdmission(request sessionduration.RunRequest) (sessionduration.AdmissionInferencer, error) {
	if request.Admission != nil {
		return request.Admission, nil
	}
	return NewAdmissionInferencer(request.Inferencer, NewEventAdmission(), nil), nil
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

func newRunContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(ctx)
}

type runLoop struct {
	ctx        context.Context
	runCtx     context.Context
	cancel     context.CancelFunc
	controller sessionduration.Controller
	admitted   sessionduration.AdmissionInferencer
	loop       sessionduration.Loop
	request    sessionduration.RunRequest
	runErrs    chan error
	loopErr    error
	loopDone   bool
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
)

func (r *runLoop) run() error {
	defer r.cancelRun()
	r.start()
	for {
		if result, done := r.handleEvent(r.nextEvent()); done {
			return result
		}
	}
}

func (r *runLoop) start() {
	r.startOnce.Do(func() {
		go func() { r.runErrs <- r.loop.Run(r.runCtx) }()
	})
}

func (r *runLoop) nextEvent() runLoopEvent {
	select {
	case err := <-r.controller.Errors():
		return runLoopEvent{kind: runLoopControllerError, err: err}
	case err := <-r.runErrs:
		return runLoopEvent{kind: runLoopError, err: err}
	case err := <-r.request.ExternalErrors:
		return runLoopEvent{kind: runLoopExternalError, err: err}
	case <-r.request.Wake:
		return runLoopEvent{kind: runLoopWake}
	case <-r.request.Done:
		return runLoopEvent{kind: runLoopDone}
	case msg, ok := <-r.loop.Deltas().Chan():
		return runLoopEvent{kind: runLoopMessage, msg: msg, valid: ok}
	case <-r.ctx.Done():
		return runLoopEvent{kind: runLoopContext, err: r.ctx.Err()}
	}
}

func (r *runLoop) handleEvent(event runLoopEvent) (error, bool) {
	switch event.kind {
	case runLoopControllerError:
		return r.finishControllerError(event.err), true
	case runLoopError:
		r.loopErr = event.err
		r.loopDone = true
		return r.finish(false, runLoopFailure(r.ctx, event.err)), true
	case runLoopExternalError:
		return r.finish(false, event.err), true
	case runLoopWake:
		return r.handleWake()
	case runLoopDone:
		return r.finish(false, runLoopDoneError(r.request)), true
	case runLoopMessage:
		if !event.valid {
			return r.finish(false, nil), true
		}
		if err := r.process(event.msg); err != nil {
			return r.finish(false, err), true
		}
		if r.finished {
			return r.finishErr, true
		}
		return nil, false
	case runLoopContext:
		return r.finish(false, event.err), true
	default:
		return nil, false
	}
}

func (r *runLoop) handleWake() (error, bool) {
	if r.request.OnWake == nil {
		return nil, false
	}
	if err := r.request.OnWake(r.runCtx, r.loop, r.controller); err != nil {
		return r.finish(false, err), true
	}
	return nil, false
}

func runLoopDoneError(request sessionduration.RunRequest) error {
	if request.DoneError == nil {
		return nil
	}
	return request.DoneError()
}

func (r *runLoop) finishControllerError(err error) error {
	if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
		return r.finish(true, nil)
	}
	return r.finish(false, err)
}

func (r *runLoop) process(msg messages.StreamMessage) error {
	admission := r.controller.Observe(msg)
	if !admission.Accepted {
		r.pending = append(r.pending, msg)
		return nil
	}
	if err := publish(r.request.Publication, admission.Message); err != nil {
		return err
	}
	if err := r.retry(admission.Message); err != nil {
		if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
			return r.finish(true, nil)
		}
		return err
	}
	if r.finished {
		return nil
	}
	if r.request.Handle == nil {
		return nil
	}
	result, err := r.request.Handle(r.runCtx, r.loop, r.controller, admission.Message)
	if err != nil {
		if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
			return r.finish(true, nil)
		}
		return err
	}
	if !result.Stop {
		return nil
	}
	return r.finish(result.Planned, nil)
}

func (r *runLoop) retry(msg messages.StreamMessage) error {
	if msg.Type != messages.StreamTypeMessageEnd {
		return nil
	}
	terminal, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || terminal == nil {
		return nil
	}
	decision := r.controller.Retry(sessionduration.RetryRequest{Terminal: terminal})
	if !decision.Eligible {
		return nil
	}
	sender, ok := r.loop.(sessionduration.SessionEventSender)
	if !ok {
		return errors.New("session duration loop does not support provider session events")
	}
	if err := r.waitForRetry(decision.Delay); err != nil {
		return err
	}
	control := messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()}
	if err := sender.SendSessionEvent(r.runCtx, control); err != nil {
		return fmt.Errorf("send rate-limit retry response: %w", err)
	}
	if r.request.RetryDispatched != nil {
		r.request.RetryDispatched(control)
	}
	return nil
}

func (r *runLoop) waitForRetry(delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if r.request.Clock == nil {
		return sessionduration.ErrSchedulerUnavailable
	}
	timer := r.request.Clock.NewTimer(delay)
	if timer == nil {
		return errors.New("session duration clock returned a nil retry timer")
	}
	defer timer.Stop()
	select {
	case <-timer.C():
		return nil
	case err := <-r.controller.Errors():
		return err
	case <-r.request.Done:
		if err := runLoopDoneError(r.request); err != nil {
			return err
		}
		return context.Canceled
	case <-r.ctx.Done():
		return r.ctx.Err()
	case <-r.runCtx.Done():
		return r.runCtx.Err()
	}
}

func (r *runLoop) finish(planned bool, primary error) error {
	r.finishOnce.Do(func() {
		r.admitted.CloseAdmission()
		if planned {
			primary = errors.Join(primary, sendLoopClose(r.runCtx, r.loop))
		}
		drainPolicy := r.request.DrainPolicy
		if drainPolicy.Clock == nil {
			drainPolicy.Clock = r.request.Clock
		}
		_, finalizeErr := r.controller.Finalize(r.ctx, sessionduration.FinalizeRequest{
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
				joinCtx, cancel := context.WithTimeout(context.Background(), loopJoinTimeout(drainPolicy))
				defer cancel()
				loopErr := r.waitForLoop(joinCtx)
				if errors.Is(primary, loopErr) {
					loopErr = nil
				}
				return errors.Join(closeErr, loopErr)
			},
			Binding:   r.request.Binding,
			Artifacts: r.request.Artifacts,
		})
		r.finishErr = errors.Join(finalizeErr, r.service.LifecycleError(sessionduration.LifecycleFailures{
			Runtime: r.admitted.RuntimeError(),
			Close:   r.admitted.CloseError(),
		}))
		r.finished = true
	})
	return r.finishErr
}

func loopJoinTimeout(policy sessionduration.DrainPolicy) time.Duration {
	if policy.LoopJoinTimeout <= 0 {
		return defaultLoopJoinTimeout
	}
	return policy.LoopJoinTimeout
}

func (r *runLoop) waitForLoop(ctx context.Context) error {
	if !r.loopDone {
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case r.loopErr = <-r.runErrs:
			r.loopDone = true
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if errors.Is(r.loopErr, context.Canceled) {
		return nil
	}
	return r.loopErr
}

func waitForLoop(results <-chan error) error {
	err := <-results
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (r *runLoop) cancelRun() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

func sendLoopClose(ctx context.Context, loop sessionduration.Loop) error {
	if loop == nil {
		return nil
	}
	if err := loop.Send(ctx, []messages.Message{{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: messages.ControlPlaneMessageTypeSessionClose},
		},
	}}); err != nil {
		return fmt.Errorf("close session loop: %w", err)
	}
	return nil
}

func normalizeLoopError(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil //nolint:nilerr // caller cancellation intentionally normalizes loop cancellation.
	}
	return err
}

func runLoopFailure(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return ctx.Err()
	}
	return normalizeLoopError(ctx, err)
}

func (r *runLoop) drainPending() error {
	for _, msg := range r.pending {
		admission := r.controller.ObserveDrain(msg)
		if admission.Accepted {
			if err := publish(r.request.Publication, admission.Message); err != nil {
				return err
			}
		}
	}
	r.pending = nil
	return nil
}
