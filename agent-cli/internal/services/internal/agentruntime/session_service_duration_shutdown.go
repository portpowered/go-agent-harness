package agentruntime

import (
	"context"
	"errors"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	duration "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func observerFailureErrors(observer *sessionProgressObserver) <-chan error {
	if observer == nil {
		return nil
	}
	if observer.durationController != nil {
		return observer.durationController.Errors()
	}
	return observer.livenessErrors
}

func mergeSessionErrorChannels(ctx context.Context, first, second <-chan error) <-chan error {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	merged := make(chan error, 1)
	go func() {
		select {
		case err := <-first:
			merged <- err
		case err := <-second:
			merged <- err
		case <-ctx.Done():
		}
	}()
	return merged
}

func (r *durationServiceResources) startSessionUpdatedTimer() error {
	if !r.opts.RequireSessionUpdated || r.opts.observer == nil || !r.opts.observer.scheduledAudioAwaitingConfiguration() || r.updatedTimer != nil {
		return nil
	}
	timeout := r.opts.SessionUpdatedTimeout
	if timeout <= 0 {
		timeout = sessionScheduledAudioConfigTimeout
	}
	r.updatedTimer = r.clock.NewTimer(timeout)
	if r.updatedTimer == nil {
		return errors.New("session duration clock returned a nil session-updated timer")
	}
	r.updatedTimeout = r.updatedTimer.C()
	go func(timer SessionDurationTimer) {
		select {
		case <-timer.C():
			forwardDurationServiceError(r.ctx, r.externalErrors, sessionScheduledAudioConfigTimeoutError(r.opts))
		case <-r.ctx.Done():
		}
	}(r.updatedTimer)
	return nil
}

func (r *durationServiceResources) stopSessionUpdatedTimer() {
	if r.updatedTimer == nil {
		return
	}
	r.updatedTimer.Stop()
	r.updatedTimer = nil
	r.updatedTimeout = nil
}

func (r *durationServiceResources) drain(ctx context.Context, loop duration.Loop, controller duration.Controller) error {
	concrete, err := durationAgentLoop(loop)
	if err != nil {
		return err
	}
	if r.clock == nil {
		return errors.New("session duration clock is required for straggler drain")
	}
	timer := r.clock.NewTimer(sessionStragglerDrainQuietPeriod)
	if timer == nil {
		return r.drainBuffered(concrete, controller)
	}
	return r.drainWithTimer(concrete, controller, timer)
}

func (r *durationServiceResources) drainWithTimer(loop *agentloop.AgentLoop, controller duration.Controller, timer SessionDurationTimer) error {
	if err := r.drainBuffered(loop, controller); err != nil {
		return err
	}
	r.mu.Lock()
	r.drainTimer = timer
	r.mu.Unlock()
	defer func() {
		timer.Stop()
		r.mu.Lock()
		r.drainTimer = nil
		r.mu.Unlock()
	}()
	var wallSafety platformclock.Timer
	if r.opts.clockSource != nil {
		wallSafety = platformclock.Real{}.NewTimer(sessionStragglerDrainWallSafety)
		defer wallSafety.Stop()
	}
	for {
		msg, ok := nextDurationDrainMessage(loop, timer, wallSafety)
		if !ok {
			return nil
		}
		admission := controller.ObserveDrain(msg)
		if admission.Accepted {
			if err := r.publication.Write(admission.Message); err != nil {
				return err
			}
		}
		next, err := r.resetDrainTimer(timer)
		if err != nil {
			return err
		}
		timer = next
	}
}

func nextDurationDrainMessage(loop *agentloop.AgentLoop, timer, wallSafety platformclock.Timer) (messages.StreamMessage, bool) {
	select {
	case msg, ok := <-loop.Deltas().Chan():
		return msg, ok
	default:
	}
	select {
	case <-timer.C():
		return messages.StreamMessage{}, false
	case <-wallTimerChannel(wallSafety):
		return messages.StreamMessage{}, false
	case msg, ok := <-loop.Deltas().Chan():
		return msg, ok
	}
}

func (r *durationServiceResources) resetDrainTimer(timer SessionDurationTimer) (SessionDurationTimer, error) {
	if !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
	next := r.clock.NewTimer(sessionStragglerDrainQuietPeriod)
	if next == nil {
		return nil, errors.New("session duration clock returned a nil straggler timer")
	}
	r.mu.Lock()
	r.drainTimer = next
	r.mu.Unlock()
	return next, nil
}

func (r *durationServiceResources) drainBuffered(loop *agentloop.AgentLoop, controller duration.Controller) error {
	for {
		msg, ok := loop.Deltas().Read()
		if !ok {
			return nil
		}
		admission := controller.ObserveDrain(msg)
		if admission.Accepted {
			if err := r.publication.Write(admission.Message); err != nil {
				return err
			}
		}
	}
}

func wallTimerChannel(timer platformclock.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C()
}

func (r *durationServiceResources) close() error {
	var result error
	r.closeOnce.Do(func() { result = r.closeResources() })
	return result
}

func (r *durationServiceResources) closeResources() error {
	r.stopSessionUpdatedTimer()
	r.mu.Lock()
	drainTimer, runStarted := r.drainTimer, r.runStarted
	r.mu.Unlock()
	if drainTimer != nil {
		drainTimer.Stop()
	}
	if r.publisher != nil {
		r.publisher.stop()
	}
	result := r.closeSessionResources()
	if runStarted && !r.runDone {
		r.runErr = <-r.runResult
		r.runDone = true
	}
	if r.loop != nil && r.controller != nil {
		result = errors.Join(result, r.drainBuffered(r.loop, r.controller))
	}
	result = errors.Join(result, joinSessionTerminationErrors(r.runErr, nil))
	if r.observed != nil {
		result = errors.Join(result, durationTransportError(r.observed.sessionFailure()))
	}
	return result
}

func (r *durationServiceResources) closeSessionResources() error {
	var result error
	if r.drainPlayback && r.observed != nil {
		result = errors.Join(result, r.observed.DrainSessionPlayback(r.ctx))
	}
	if r.opts.BareLive && r.observed != nil {
		result = errors.Join(result, r.observed.CloseSession())
	}
	return result
}

func durationTransportError(err error) error {
	if err == nil {
		return nil
	}
	return durationwire.NewService().TransportError(err)
}

func forwardDurationServiceErrors(ctx context.Context, target chan<- error, source <-chan error) {
	if source == nil {
		return
	}
	go func() {
		for {
			select {
			case err, ok := <-source:
				if !ok {
					return
				}
				if err != nil {
					forwardDurationServiceError(ctx, target, err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

func forwardDurationServiceError(ctx context.Context, target chan<- error, err error) {
	if err == nil {
		return
	}
	select {
	case target <- err:
	case <-ctx.Done():
	}
}
