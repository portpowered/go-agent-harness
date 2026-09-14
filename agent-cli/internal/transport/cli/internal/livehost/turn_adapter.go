package livehost

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

// TurnAdapter is the transport-facing seam for callers that need to pass a
// session-turn request through the CLI host. It carries no session state: the
// service owns the prepared runtime and all mutable turn lifecycle.
type TurnAdapter struct {
	service sessionturn.Service
}

func NewTurnAdapter(service sessionturn.Service) TurnAdapter {
	return TurnAdapter{service: service}
}

func (a TurnAdapter) Prepare(ctx context.Context, request sessionturn.Request) (sessionturn.Runtime, error) {
	if a.service == nil {
		return nil, sessionturn.ErrMissingTurnInferencer
	}
	return a.service.Prepare(ctx, request)
}

func (TurnAdapter) Run(ctx context.Context, runtime sessionturn.Runtime, request sessionturn.TurnRequest) (sessionturn.TurnResult, error) {
	if runtime == nil {
		return sessionturn.TurnResult{}, sessionturn.ErrSessionClosed
	}
	return runtime.RunTurn(ctx, request)
}

func (TurnAdapter) Output(runtime sessionturn.Runtime, writer io.Writer) sessionturn.Output {
	if runtime == nil {
		return nil
	}
	return runtime.NewOutput(writer)
}
