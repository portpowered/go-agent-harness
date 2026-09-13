package service

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive"
)

func actionChannel(source sessionlive.ActionSource, ctx context.Context) <-chan sessionlive.Action {
	if source == nil {
		return nil
	}
	return source(ctx)
}

func (r *runState) waitRun() error {
	if r.runDone {
		return r.runErr
	}
	select {
	case r.runErr = <-r.runErrCh:
		r.runDone = true
		return r.runErr
	case <-r.ctx.Done():
		return r.ctx.Err()
	}
}

func (r *runState) waitInput() error {
	if r.inputErrCh == nil {
		return r.inputErr
	}
	r.inputErr = <-r.inputErrCh
	r.inputErrCh = nil
	return r.inputErr
}
