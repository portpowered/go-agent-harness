package service

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/livedelegation"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/providers"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/inference"
)

// errBuildFailed marks a backend the provider service could not build.
var errBuildFailed = fmt.Errorf("%w: the provider could not be built", livedelegation.ErrBackendUnavailable)

// backendBuilder builds the session's backend inferencer once, at the first
// delegation, so a session whose provider never delegates never builds or
// dials a backend. A failed build is not cached: the next delegation retries
// it. The credential reference is resolved once and its value kept for those
// retries, because a host reference may be single-use.
type backendBuilder struct {
	providers   providers.Service
	credentials livedelegation.CredentialResolver
	backend     livedelegation.Backend

	mu         sync.Mutex
	inferencer messages.Inferencer
	apiKey     string
	resolved   bool
}

func (b *backendBuilder) get(ctx context.Context) (messages.Inferencer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inferencer != nil {
		return b.inferencer, nil
	}
	if reason := strings.TrimSpace(b.backend.Unconfigured); reason != "" {
		return nil, fmt.Errorf("%w: %s", livedelegation.ErrBackendUnavailable, reason)
	}
	if strings.TrimSpace(b.backend.Provider) == "" || b.providers == nil {
		return nil, livedelegation.ErrBackendUnavailable
	}
	apiKey, err := b.resolveCredential(ctx)
	if err != nil {
		return nil, err
	}
	provider, err := b.providers.Build(ctx, providers.Config{
		Provider: b.backend.Provider, Model: b.backend.Model, APIKey: apiKey,
		BaseURL: b.backend.BaseURL, ChatGPTAuthPath: b.backend.ChatGPTAuthPath,
	})
	if err != nil {
		return nil, fmt.Errorf("%w (%s): %w", errBuildFailed, b.backend.Provider, err)
	}
	gw, err := gateway.NewGateway(gateway.WithProvider(provider))
	if err != nil {
		return nil, fmt.Errorf("%w: gateway: %w", errBuildFailed, err)
	}
	b.inferencer = inference.NewGatewayInferencer(gw)
	return b.inferencer, nil
}

// resolveCredential resolves the reference on first use and keeps the
// value for later builds of this session's backend.
func (b *backendBuilder) resolveCredential(ctx context.Context) (string, error) {
	if b.resolved {
		return b.apiKey, nil
	}
	reference := strings.TrimSpace(b.backend.CredentialReference)
	if reference == "" {
		b.resolved = true
		return "", nil
	}
	if b.credentials == nil {
		return "", fmt.Errorf("%w: no resolver for the backend credential", livedelegation.ErrBackendUnavailable)
	}
	apiKey, err := b.credentials(ctx, reference)
	if err != nil {
		return "", fmt.Errorf("%w: resolve the backend credential: %w", livedelegation.ErrBackendUnavailable, err)
	}
	b.apiKey, b.resolved = apiKey, true
	return apiKey, nil
}
