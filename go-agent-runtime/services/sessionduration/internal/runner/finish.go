package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

func (r *runLoop) retry(msg messages.StreamMessage) (bool, error) {
	if msg.Type != messages.StreamTypeMessageEnd {
		return false, nil
	}
	terminal, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || terminal == nil {
		return false, nil
	}
	decision := r.controller.Retry(sessionduration.RetryRequest{Terminal: terminal})
	if decision.Exhausted {
		return false, sessionduration.ErrRateLimitRetryExhausted
	}
	if !decision.Eligible {
		return false, nil
	}
	sender, ok := r.loop.(sessionduration.SessionEventSender)
	if !ok {
		return false, errors.New("session duration loop does not support provider session events")
	}
	if err := r.waitForRetry(decision.Delay); err != nil {
		return false, err
	}
	control := messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()}
	if err := sender.SendSessionEvent(r.runCtx, control); err != nil {
		return false, fmt.Errorf("send rate-limit retry response: %w", err)
	}
	r.controller.ExpectProviderProgress()
	if r.request.RetryDispatched != nil {
		r.request.RetryDispatched(control)
	}
	return true, nil
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
		r.finalize(planned, r.finalizationCause(primary))
	})
	return r.finishErr
}

func (r *runLoop) finalizationCause(primary error) error {
	if errors.Is(primary, context.Canceled) && r.ctx.Err() != nil && r.state.isAwaitingResponse() {
		return fmt.Errorf("session cancelled while awaiting model response after end-of-turn: %w", r.ctx.Err())
	}
	return primary
}

func (r *runLoop) finalize(planned bool, primary error) {
	r.stopSessionUpdatedTimer()
	r.admitted.CloseAdmission()
	drainPolicy := r.finalizationDrainPolicy()
	result, err := r.controller.Finalize(r.ctx, r.finalizationRequest(planned, primary, drainPolicy))
	r.result = result
	r.finishErr = errors.Join(err, r.service.LifecycleError(sessionduration.LifecycleFailures{
		Runtime: r.admitted.RuntimeError(),
		Close:   r.admitted.CloseError(),
	}))
	r.finished = true
}

func (r *runLoop) finalizationDrainPolicy() sessionduration.DrainPolicy {
	policy := r.request.DrainPolicy
	if policy.Clock == nil {
		policy.Clock = r.request.Clock
	}
	pending := policy.Pending
	policy.Pending = func() bool { return r.needsDrainWait(pending) }
	return policy
}

func (r *runLoop) needsDrainWait(pending func() bool) bool {
	terminalToolFailure := fact(r.request.Facts.HasTerminalToolContinuationFailure)
	terminalScheduledFailure := fact(r.request.Facts.HasTerminalScheduledResponseFailure)
	if terminalToolFailure || terminalScheduledFailure {
		return fact(pending)
	}
	if !r.loopDone {
		select {
		case r.loopErr = <-r.runErrs:
			r.loopDone = true
		default:
			return true
		}
	}
	return fact(r.request.Facts.HasToolLifecycleObligation) || fact(pending)
}

func (r *runLoop) finalizationRequest(planned bool, primary error, drainPolicy sessionduration.DrainPolicy) sessionduration.FinalizeRequest {
	return sessionduration.FinalizeRequest{
		Primary:     primary,
		Quiesce:     r.quiesce,
		Drain:       func(ctx context.Context) error { return r.drain(ctx, planned) },
		DrainLoop:   r.loop,
		DrainPolicy: drainPolicy,
		Close:       func() error { return r.closeResources(primary, drainPolicy) },
		Binding:     r.request.Binding,
		Artifacts:   r.request.Artifacts,
	}
}

func (r *runLoop) quiesce() error {
	r.quiesceAudioInput()
	if r.request.Quiesce != nil {
		return r.request.Quiesce()
	}
	return nil
}

func (r *runLoop) drain(ctx context.Context, planned bool) error {
	drainErr := r.drainPending()
	if drainErr == nil && r.request.Drain != nil {
		drainErr = r.request.Drain(ctx)
	}
	if planned {
		drainErr = errors.Join(drainErr, sendLoopClose(ctx, r.loop))
	}
	r.cancelRun()
	return drainErr
}

func (r *runLoop) closeResources(primary error, policy sessionduration.DrainPolicy) error {
	closeErr := r.closeAudioInput()
	if r.interruptionDone != nil {
		<-r.interruptionDone
	}
	if r.request.Close != nil {
		closeErr = errors.Join(closeErr, r.request.Close())
	}
	joinCtx, cancel := context.WithTimeout(context.Background(), loopJoinTimeout(policy))
	defer cancel()
	loopErr := r.waitForLoop(joinCtx)
	if errors.Is(primary, loopErr) {
		loopErr = nil
	}
	return errors.Join(closeErr, loopErr)
}

func (r *runLoop) startAudioInput(ctx context.Context, loop sessionduration.Loop) error {
	port := r.request.AudioInput
	if port.Run == nil || r.audioInputDone != nil {
		return nil
	}
	audioCtx, stop := context.WithCancel(ctx)
	r.audioInputStop = stop
	r.audioInputDone = make(chan struct{})
	r.audioInputErrs = make(chan error, 1)
	if port.BindContext != nil {
		port.BindContext(audioCtx)
	}
	go func() {
		err := port.Run(audioCtx, loop)
		r.audioInputErrs <- err
		close(r.audioInputDone)
		select {
		case r.audioInputWake <- struct{}{}:
		default:
		}
	}()
	return nil
}

func (r *runLoop) audioInputWakeResult() sessionduration.WakeResult {
	if r.audioInputErrs == nil || r.audioInputReadHandled {
		return sessionduration.WakeResult{}
	}
	select {
	case err := <-r.audioInputErrs:
		r.audioInputErr = err
		r.audioInputReadHandled = true
		return sessionduration.WakeResult{AudioInputCompleted: true, AudioInputError: err}
	default:
		return sessionduration.WakeResult{}
	}
}

func (r *runLoop) quiesceAudioInput() {
	if r.audioInputStop != nil {
		r.audioInputStop()
		r.audioInputStop = nil
	}
}

func (r *runLoop) closeAudioInput() error {
	r.quiesceAudioInput()
	if r.audioInputDone != nil {
		<-r.audioInputDone
	}
	if r.audioInputErrs != nil && !r.audioInputReadHandled {
		r.audioInputErr = <-r.audioInputErrs
		r.audioInputReadHandled = true
	}
	if isAudioInputCancellation(r.audioInputErr) {
		return nil
	}
	return r.audioInputErr
}

func isAudioInputCancellation(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (r *runLoop) startSessionUpdatedTimer(msg messages.StreamMessage) error {
	wait := r.request.SessionUpdated
	if msg.Type != messages.StreamTypeSessionOpen || wait.Pending == nil || !wait.Pending() || r.updatedTimer != nil {
		return nil
	}
	if r.request.Clock == nil {
		return sessionduration.ErrSchedulerUnavailable
	}
	timeout := wait.Timeout
	if timeout <= 0 {
		timeout = defaultSessionUpdatedTimeout
	}
	timer := r.request.Clock.NewTimer(timeout)
	if timer == nil {
		return errors.New("session duration clock returned a nil session-updated timer")
	}
	r.updatedTimer = timer
	r.updatedTimeout = timer.C()
	return nil
}

func (r *runLoop) stopSessionUpdatedTimer() {
	if r.updatedTimer == nil {
		return
	}
	r.updatedTimer.Stop()
	r.updatedTimer = nil
	r.updatedTimeout = nil
}

func (r *runLoop) sessionUpdatedTimeoutError() error {
	if err := r.request.SessionUpdated.TimeoutError; err != nil {
		return err
	}
	return errors.New("session updated acknowledgement timed out")
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
			return errors.New("session loop join context is required")
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
			if err := r.publish(r.request.Publication, admission.Message); err != nil {
				return err
			}
		}
	}
	r.pending = nil
	return nil
}
