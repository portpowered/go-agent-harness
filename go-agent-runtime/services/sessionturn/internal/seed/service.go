package seed

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

type Service struct{ allocator sessionturn.Allocator }

func New(allocator sessionturn.Allocator) *Service {
	if allocator == nil {
		allocator = newDefaultAllocator()
	}
	return &Service{allocator: allocator}
}

func (s *Service) Allocate() string {
	if s == nil || s.allocator == nil {
		return ""
	}
	return s.allocator.Allocate()
}

func (s *Service) WrapInferencer(inner messages.SessionInferencer, wirePrompt string, seed sessionturn.Seed) messages.SessionInferencer {
	return &inferencer{service: s, inner: inner, wirePrompt: wirePrompt, seed: seed}
}

func (s *Service) WrapSession(ctx context.Context, inner messages.Session, wirePrompt string, seed sessionturn.Seed) sessionturn.Session {
	return newSession(ctx, inner, wirePrompt, seed)
}

func (s *Service) NewOutput(writer io.Writer) sessionturn.Output { return &output{writer: writer} }
