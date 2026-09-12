package service

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed"
)

type inferencer struct {
	service    *Service
	inner      messages.SessionInferencer
	wirePrompt string
	seed       textseed.Seed
}

var _ messages.SessionInferencer = (*inferencer)(nil)

func (i *inferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return i.service.WrapSession(ctx, session, i.wirePrompt, i.seed), nil
}
