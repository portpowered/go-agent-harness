package seed

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sessiondiagnostics "github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessiontrace/lifecycle"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

type Service struct {
	allocator sessionturn.Allocator
	lifecycle sessiondiagnostics.Service
	observer  sessionturn.ToolLifecycleObserver
}

type Options struct {
	Lifecycle sessiondiagnostics.Service
	Observer  sessionturn.ToolLifecycleObserver
}

func New(allocator sessionturn.Allocator, options ...Options) *Service {
	if allocator == nil {
		allocator = newDefaultAllocator()
	}
	var lifecycle sessiondiagnostics.Service
	var observer sessionturn.ToolLifecycleObserver
	if len(options) > 0 {
		lifecycle, observer = options[0].Lifecycle, options[0].Observer
	}
	return &Service{allocator: allocator, lifecycle: lifecycle, observer: observer}
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
	return newSession(ctx, inner, wirePrompt, seed, s.lifecycle, s.observer)
}

func (s *Service) NewOutput(writer io.Writer) sessionturn.Output { return &output{writer: writer} }
