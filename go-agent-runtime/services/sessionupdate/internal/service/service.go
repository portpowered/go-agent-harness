package service

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionupdate"
)

// Service is the private implementation of the public sessionupdate
// contract. It carries no provider or host dependencies.
type Service struct{}

var _ sessionupdate.Service = (*Service)(nil)

// New constructs the stateless session-update service.
func New() *Service { return &Service{} }

// Decorate snapshots config before returning the inferencer adapter so callers
// cannot mutate the update after construction.
func (s *Service) Decorate(inner messages.SessionInferencer, config sessionupdate.Config) messages.SessionInferencer {
	if inner == nil {
		return nil
	}
	return &inferencer{
		inner:   inner,
		service: s,
		config:  snapshotConfig(config),
	}
}

// DecorateSession snapshots config before starting the session relay.
func (s *Service) DecorateSession(ctx context.Context, inner messages.Session, config sessionupdate.Config) sessionupdate.DecoratedSession {
	if inner == nil {
		return nil
	}
	return newSession(inner, ctx, snapshotConfig(config))
}

func snapshotConfig(config sessionupdate.Config) sessionupdate.Config {
	config.ToolDefinitions = messages.CanonicalToolDefinitions(config.ToolDefinitions)
	return config
}
