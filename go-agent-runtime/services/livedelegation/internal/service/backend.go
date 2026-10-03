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

// backendBuilder builds the session's backend inferencer once, at the first
// delegation, so a session whose provider never delegates never builds or
// dials a backend. A failed build is not cached: the next delegation retries
// it, and every failure is answered as a spoken failure.
type backendBuilder struct {
	providers   providers.Service
	credentials livedelegation.CredentialResolver
	backend     livedelegation.Backend

	mu         sync.Mutex
	inferencer messages.Inferencer
}

func (b *backendBuilder) get(ctx context.Context) (messages.Inferencer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inferencer != nil {
		return b.inferencer, nil
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
		return nil, fmt.Errorf("build delegation backend %s: %w", b.backend.Provider, err)
	}
	gw, err := gateway.NewGateway(gateway.WithProvider(provider))
	if err != nil {
		return nil, fmt.Errorf("create delegation gateway: %w", err)
	}
	b.inferencer = inference.NewGatewayInferencer(gw)
	return b.inferencer, nil
}

func (b *backendBuilder) resolveCredential(ctx context.Context) (string, error) {
	reference := strings.TrimSpace(b.backend.CredentialReference)
	if reference == "" {
		return "", nil
	}
	if b.credentials == nil {
		return "", fmt.Errorf("delegation backend credential %q is unavailable", reference)
	}
	return b.credentials(ctx, reference)
}
