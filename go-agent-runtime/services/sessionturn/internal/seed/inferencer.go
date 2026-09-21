package seed

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

type inferencer struct {
	service    *Service
	inner      messages.SessionInferencer
	wirePrompt string
	seed       sessionturn.Seed
}

var _ messages.SessionInferencer = (*inferencer)(nil)

func (i *inferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return i.service.WrapSession(ctx, session, i.wirePrompt, i.seed), nil
}

func (i *inferencer) Request() inference.SessionRequest {
	if i == nil || i.inner == nil {
		return inference.SessionRequest{}
	}
	if source, ok := i.inner.(interface {
		Request() inference.SessionRequest
	}); ok {
		return source.Request()
	}
	return inference.SessionRequest{}
}
