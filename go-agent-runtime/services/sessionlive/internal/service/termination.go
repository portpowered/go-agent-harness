package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive"
)

func (r *runState) terminate(primary error, drainPlayback bool) error {
	r.terminateOnce.Do(func() {
		var quiesceErr, waitErr, stopErr, flushErr error
		if r.opts.QuiesceUpstream != nil {
			quiesceErr = r.opts.QuiesceUpstream()
		}
		if r.opts.WaitForStragglers == nil {
			waitErr = errors.New("session live termination requires a straggler waiter")
		} else {
			waitErr = r.opts.WaitForStragglers(r.ctx)
		}
		// Keep caller-owned finite producers alive through the bounded provider
		// drain. An admitted audio frame or end-of-turn signal may still be
		// completing while the provider's terminal signal is being observed.
		// Process-owned producers that cannot safely remain active can be
		// quiesced by the host callback above.
		r.cancelInput()
		var playbackErr error
		if drainPlayback && r.opts.Lifecycle.DrainPlayback != nil {
			playbackErr = r.opts.Lifecycle.DrainPlayback(r.ctx)
		}
		r.cancelRun()
		if r.opts.StopOwnedResources != nil {
			stopErr = r.opts.StopOwnedResources(r.ctx)
		}
		joinedRunErr, joinedInputErr := r.waitRun(), r.waitInput()
		if r.opts.JoinProducerErrors != nil {
			stopErr = errors.Join(stopErr, r.opts.JoinProducerErrors(joinedRunErr, joinedInputErr))
		} else {
			stopErr = errors.Join(stopErr, expectedCancellation(joinedRunErr), expectedCancellation(joinedInputErr))
		}
		flushErr = flushPublished(r.ctx, r.opts, r.opts.Loop)
		r.terminalErr = errors.Join(primary, quiesceErr, waitErr, playbackErr, stopErr, flushErr)
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			r.terminalErr = errors.Join(r.terminalErr, ctxErr)
		}
	})
	return r.terminalErr
}

func flushPublished(ctx context.Context, opts sessionlive.RunOptions, loop *sessionlive.Loop) error {
	for {
		message, ok := loop.Deltas().Read()
		if !ok {
			break
		}
		if _, err := opts.Handler(ctx, loop, message, sessionlive.MessageContext{AwaitingResponse: opts.InitiallyAwaitingResponse}); err != nil {
			return err
		}
	}
	var err error
	if opts.Lifecycle.SessionError != nil {
		if sessionErr := opts.Lifecycle.SessionError(); sessionErr != nil {
			err = errors.Join(err, fmt.Errorf("session transport: %w", sessionErr))
		}
	}
	if opts.AfterFlush != nil {
		err = errors.Join(err, opts.AfterFlush())
	}
	return err
}

func terminateWithTerminationError(ctx context.Context, err error, decorate func(context.Context, error) error) error {
	if decorate == nil {
		return err
	}
	return decorate(ctx, err)
}

func isCancellation(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func expectedCancellation(err error) error {
	if isCancellation(err) {
		return nil
	}
	return err
}
