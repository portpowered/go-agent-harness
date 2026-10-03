// Package service implements the live delegation executor: a bounded worker
// pool that runs each client delegation as a nested turn-based agent loop
// and answers it with CONTEXT.APPEND messages.
package service

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

// Service opens executors that build their backend through the provider
// service.
type Service struct {
	providers   providers.Service
	credentials livedelegation.CredentialResolver
	scheduler   platformclock.Scheduler
}

var _ livedelegation.Service = (*Service)(nil)

// New returns a service. scheduler is the default time domain for bindings
// that carry none; nil selects the host clock.
func New(providerService providers.Service, credentials livedelegation.CredentialResolver, scheduler platformclock.Scheduler) *Service {
	return &Service{providers: providerService, credentials: credentials, scheduler: scheduler}
}

// Open starts an executor for one session. Nothing is built or dialed until
// the first delegation runs.
func (s *Service) Open(ctx context.Context, binding livedelegation.Binding) (livedelegation.Executor, error) {
	if ctx == nil {
		return nil, errors.New("live delegation context is required")
	}
	if binding.Append == nil {
		return nil, livedelegation.ErrAppendUnavailable
	}
	if binding.Scheduler == nil {
		binding.Scheduler = s.scheduler
	}
	if binding.Scheduler == nil {
		binding.Scheduler = platformclock.Real{}
	}
	backend := &backendBuilder{providers: s.providers, credentials: s.credentials, backend: binding.Policy.Backend}
	return newExecutor(ctx, binding, backend), nil
}
