package service

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionupdate"
)

type inferencer struct {
	inner   messages.SessionInferencer
	service *Service
	config  sessionupdate.Config
}

var _ messages.SessionInferencer = (*inferencer)(nil)

func (i *inferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return i.service.DecorateSession(ctx, inner, i.config), nil
}
