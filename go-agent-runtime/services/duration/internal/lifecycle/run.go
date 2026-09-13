package lifecycle

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/duration"
)

func (s *service) Run(ctx context.Context, req RunRequest) error {
	ctx = nonNilContext(ctx)
	clock, err := validateRequest(s, req)
	if err != nil {
		return err
	}
	req.Clock = clock
	req.Handle = defaultHandler(req.Handle)
	artifactLifecycle, err := prepareArtifacts(req)
	if err != nil {
		return err
	}
	req.Artifacts = artifactLifecycle
	admission := newAdmission(req.Inferencer)
	handle, err := req.Runner.Start(ctx, admission)
	if err != nil {
		return err
	}
	if handle == nil || handle.Deltas() == nil {
		if handle != nil {
			return errors.Join(errors.New("duration runner returned an invalid handle"), handle.Stop(ctx))
		}
		return errors.New("duration runner returned an invalid handle")
	}
	loop := &runLoop{service: s, ctx: ctx, req: req, admission: admission, handle: handle, handler: req.Handle}
	if req.MaxDuration == 0 {
		return loop.unbounded()
	}
	return loop.bounded(ctx)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func validateRequest(s *service, req RunRequest) (Clock, error) {
	if req.MaxDuration < 0 {
		return nil, &duration.InvalidDurationError{Duration: req.MaxDuration}
	}
	if req.Runner == nil {
		return nil, errors.New("duration runner is required")
	}
	if req.Inferencer == nil {
		return nil, errors.New("duration inferencer is required")
	}
	clock := req.Clock
	if clock == nil {
		clock = s.clock
	}
	if req.MaxDuration > 0 && clock == nil {
		return nil, duration.ErrClockRequired
	}
	return clock, nil
}

func defaultHandler(handler MessageHandler) MessageHandler {
	if handler != nil {
		return handler
	}
	return func(context.Context, messages.StreamMessage, MessageState) (MessageResult, error) {
		return MessageResult{}, nil
	}
}

type runLoop struct {
	service   *service
	ctx       context.Context
	req       RunRequest
	admission *admission
	handle    Handle
	handler   MessageHandler
	state     terminalState
	planned   bool
	closeSent bool
	finished  bool
}

func (r *runLoop) process(msg messages.StreamMessage) (MessageResult, error) {
	r.state.observe(msg)
	if r.skipTerminal(msg) {
		return MessageResult{}, nil
	}
	result, err := r.service.acceptAndHandle(r.ctx, r.req, r.handler, &r.state, msg)
	if err != nil {
		return MessageResult{}, err
	}
	if msg.Type == messages.StreamTypeSessionClose {
		r.state.terminalWritten = true
	}
	return result, nil
}

func (r *runLoop) skipTerminal(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeSessionClose {
		return false
	}
	return (r.planned && !r.admission.IsProviderTerminal(msg)) || r.state.terminalWritten
}

func (r *runLoop) finish(planned bool, preferred error) error {
	if r.finished {
		return nil
	}
	r.finished = true
	r.planned = planned
	r.state.durationExpired = planned
	r.state.deadline = nil
	return r.service.finish(r.ctx, r.req, r.admission, r.handle, &r.state, planned, preferred)
}

func (r *runLoop) unbounded() error {
	for {
		select {
		case err := <-r.handle.Result():
			return errors.Join(r.finish(false, err), normalizeResultError(r.ctx, err))
		case msg, ok := <-r.handle.Deltas().Chan():
			if !ok {
				return r.finish(false, nil)
			}
			if err := r.processMessage(msg); err != nil {
				return err
			}
		case <-r.ctx.Done():
			return errors.Join(r.finish(false, nil), r.ctx.Err())
		}
	}
}

func (r *runLoop) bounded(ctx context.Context) error {
	timer := r.req.Clock.NewTimer(r.req.MaxDuration)
	if timer == nil {
		return r.finish(false, errors.New("session duration clock returned a nil timer"))
	}
	defer timer.Stop()
	r.state.deadline = timer.C()
	for {
		if timerReady(timer.C()) && !r.planned {
			return r.deadlineFinish(ctx)
		}
		select {
		case <-timer.C():
			r.state.deadline = nil
			return r.deadlineFinish(ctx)
		case err := <-r.handle.Result():
			return errors.Join(r.finish(r.planned, err), normalizeResultError(ctx, err))
		case msg, ok := <-r.handle.Deltas().Chan():
			if !ok {
				return r.finish(r.planned, nil)
			}
			if err := r.processMessage(msg); err != nil {
				return err
			}
		case <-ctx.Done():
			return errors.Join(r.finish(r.planned, nil), ctx.Err())
		}
	}
}

func (r *runLoop) processMessage(msg messages.StreamMessage) error {
	result, err := r.process(msg)
	if err != nil {
		return r.finish(false, err)
	}
	if result.Stop {
		return r.finish(result.Planned, nil)
	}
	return nil
}

func (r *runLoop) deadlineFinish(ctx context.Context) error {
	r.admission.Close(ctx)
	if !r.closeSent {
		r.closeSent = true
		if err := r.handle.SendClose(ctx); err != nil {
			return r.finish(false, err)
		}
	}
	return r.finish(true, nil)
}
