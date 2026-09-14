package service

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/textseed"
)

// Service is the private implementation behind the public textseed contract.
type Service struct {
	allocator textseed.Allocator
}

func New(allocator textseed.Allocator) *Service {
	if allocator == nil {
		allocator = newDefaultAllocator()
	}
	return &Service{allocator: allocator}
}

func NewDefault() *Service { return New(nil) }

var _ textseed.Service = (*Service)(nil)

func (s *Service) Allocate() string {
	if s == nil || s.allocator == nil {
		return ""
	}
	return s.allocator.Allocate()
}

func (s *Service) WrapInferencer(inner messages.SessionInferencer, wirePrompt string, seed textseed.Seed) messages.SessionInferencer {
	return &inferencer{service: s, inner: inner, wirePrompt: wirePrompt, seed: seed}
}

func (s *Service) WrapSession(ctx context.Context, inner messages.Session, wirePrompt string, seed textseed.Seed) textseed.Session {
	return newSession(ctx, inner, wirePrompt, seed)
}

func (s *Service) NewOutput(writer io.Writer) textseed.Output {
	return &output{writer: writer}
}
