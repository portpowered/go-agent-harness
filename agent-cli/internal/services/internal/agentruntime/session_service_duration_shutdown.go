package agentruntime

import (
	"context"
	"errors"

	durationwire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration/wire"
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

func (r *durationServiceResources) drainPlaybackOnly(ctx context.Context) error {
	if !r.drainPlayback || r.observed == nil {
		return nil
	}
	return r.observed.DrainSessionPlayback(ctx)
}

func (r *durationServiceResources) close() error {
	var result error
	r.closeOnce.Do(func() { result = r.closeResources() })
	return result
}

func (r *durationServiceResources) closeResources() error {
	r.stopSessionUpdatedTimer()
	if r.publisher != nil {
		r.publisher.stop()
	}
	result := r.closeSessionResources()
	if r.observed != nil {
		result = errors.Join(result, durationTransportError(r.observed.sessionFailure()))
	}
	return result
}

func (r *durationServiceResources) closeSessionResources() error {
	var result error
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
