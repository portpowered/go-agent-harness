package runner

import (
	"errors"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
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
	case <-r.updatedTimeout:
		return runLoopEvent{kind: runLoopSessionUpdatedTimeout, err: r.sessionUpdatedTimeoutError()}
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
	case runLoopSessionUpdatedTimeout:
		return r.finish(false, event.err), true
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
	audioInputErr, callbackErr := r.observeWake()
	if callbackErr != nil {
		return r.finish(false, callbackErr), true
	}
	if err := r.dispatchInterruption(); err != nil {
		return r.finish(false, err), true
	}
	if audioInputErr != nil && !isAudioInputCancellation(audioInputErr) {
		return r.finish(false, audioInputErr), true
	}
	if err := r.dispatchScheduledWake(); err != nil {
		return r.finish(false, err), true
	}
	state, err := r.closePendingSessionIfReady(r.runCtx, r.loop, r.state)
	r.state = state
	if err != nil {
		return r.finish(false, err), true
	}
	return nil, false
}

func (r *runLoop) observeWake() (error, error) {
	inputResult := r.audioInputWakeResult()
	if r.request.Effects.OnWake != nil {
		result, err := r.request.Effects.OnWake(r.runCtx, r.loop)
		return r.mergeWakeResult(result, inputResult), err
	}
	if r.request.OnWake != nil {
		state, err := r.request.OnWake(r.runCtx, r.loop, r.controller, r.state)
		r.state = state
		if err != nil {
			return nil, err
		}
		return r.applyAudioInputResult(inputResult), nil
	}
	return r.applyAudioInputResult(inputResult), nil
}

func (r *runLoop) mergeWakeResult(result sessionduration.WakeResult, input sessionduration.WakeResult) error {
	if input.AudioInputCompleted {
		result.AudioInputCompleted = true
		result.AudioInputError = errors.Join(result.AudioInputError, input.AudioInputError)
	}
	return r.applyAudioInputResult(result)
}

func (r *runLoop) applyAudioInputResult(result sessionduration.WakeResult) error {
	if !result.AudioInputCompleted {
		return nil
	}
	r.state = r.state.WithAwaitingResponse(result.AudioInputError == nil)
	return result.AudioInputError
}

func (r *runLoop) dispatchInterruption() error {
	dispatch := r.request.AudioInterruptions.Dispatch
	if dispatch == nil || r.interruptionPending == nil {
		return nil
	}
	select {
	case input := <-r.interruptionPending:
		return dispatch(r.runCtx, r.loop, input)
	default:
		return nil
	}
}

func (r *runLoop) dispatchScheduledWake() error {
	if r.request.Effects.DispatchScheduledInputs != nil {
		return r.request.Effects.DispatchScheduledInputs(r.runCtx, r.loop)
	}
	return nil
}

func runLoopDoneError(request sessionduration.RunRequest) error {
	if request.DoneError == nil {
		return nil
	}
	return request.DoneError()
}

func (r *runLoop) finishControllerError(err error) error {
	if errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
		return r.finishMaxDuration()
	}
	return r.finish(false, err)
}

func (r *runLoop) finishMaxDuration() error {
	var primary error
	if r.request.MaxDurationExpired != nil {
		primary = r.request.MaxDurationExpired()
	}
	return r.finish(true, primary)
}
