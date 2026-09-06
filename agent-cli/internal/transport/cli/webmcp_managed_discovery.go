package cli

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

// managedWebMCPDiscoveryService injects the manager-owned loopback endpoint
// into every discovery/reconnect pass while leaving the neutral discovery
// service responsible for target identity, persistence, and detach-only
// selection cleanup.
type managedWebMCPDiscoveryService struct {
	owner    *productionWebMCPComposition
	delegate WebMCPDiscoveryService
}

func (s *managedWebMCPDiscoveryService) inputs(ctx context.Context, inputs discovery.ConnectionInputs) (discovery.ConnectionInputs, error) {
	if s == nil || s.delegate == nil {
		return discovery.ConnectionInputs{}, errors.New("managed WebMCP discovery service is unavailable")
	}
	if s.owner == nil {
		return inputs, nil
	}
	return s.owner.managedDiscoveryInputs(ctx, inputs)
}

func (s *managedWebMCPDiscoveryService) DiscoverAll(ctx context.Context, inputs discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error) {
	effective, err := s.inputs(ctx, inputs)
	if err != nil {
		return nil, err
	}
	return s.delegate.DiscoverAll(ctx, effective)
}

func (s *managedWebMCPDiscoveryService) ListTargetSnapshot(ctx context.Context, browser discovery.BrowserCandidate, options ...discovery.TargetListOptions) (discovery.TargetSnapshot, error) {
	if s == nil || s.delegate == nil {
		return discovery.TargetSnapshot{}, errors.New("managed WebMCP discovery service is unavailable")
	}
	if s.owner != nil {
		if _, err := s.owner.ensureManagedBrowser(ctx); err != nil {
			return discovery.TargetSnapshot{}, err
		}
	}
	return s.delegate.ListTargetSnapshot(ctx, browser, options...)
}

func (s *managedWebMCPDiscoveryService) Select(ctx context.Context, request discovery.TargetSelectionRequest) (discovery.Selection, error) {
	if s == nil || s.delegate == nil {
		return discovery.Selection{}, errors.New("managed WebMCP discovery service is unavailable")
	}
	if s.owner != nil {
		if _, err := s.owner.ensureManagedBrowser(ctx); err != nil {
			return discovery.Selection{}, err
		}
	}
	return s.delegate.Select(ctx, request)
}

func (s *managedWebMCPDiscoveryService) Selected() (discovery.Selection, bool) {
	if s == nil || s.delegate == nil {
		return discovery.Selection{}, false
	}
	return s.delegate.Selected()
}

func (s *managedWebMCPDiscoveryService) RefreshSelection(ctx context.Context) (discovery.Selection, error) {
	if s == nil || s.delegate == nil {
		return discovery.Selection{}, errors.New("managed WebMCP discovery service is unavailable")
	}
	if s.owner != nil {
		if _, err := s.owner.ensureManagedBrowser(ctx); err != nil {
			return discovery.Selection{}, err
		}
	}
	return s.delegate.RefreshSelection(ctx)
}

func (s *managedWebMCPDiscoveryService) Reconnect(ctx context.Context, inputs discovery.ConnectionInputs, options ...discovery.ReconnectOptions) (discovery.Selection, error) {
	if s == nil || s.delegate == nil {
		return discovery.Selection{}, errors.New("managed WebMCP discovery service is unavailable")
	}
	effective, err := s.inputs(ctx, inputs)
	if err != nil {
		return discovery.Selection{}, err
	}
	reconnector, ok := s.delegate.(interface {
		Reconnect(context.Context, discovery.ConnectionInputs, ...discovery.ReconnectOptions) (discovery.Selection, error)
	})
	if !ok {
		return discovery.Selection{}, errors.New("managed WebMCP discovery reconnect is unavailable")
	}
	return reconnector.Reconnect(ctx, effective, options...)
}

func (s *managedWebMCPDiscoveryService) LoadPersistedSelection(ctx context.Context) (discovery.PersistedSelection, bool, error) {
	if s == nil || s.delegate == nil {
		return discovery.PersistedSelection{}, false, errors.New("managed WebMCP discovery service is unavailable")
	}
	loader, ok := s.delegate.(interface {
		LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error)
	})
	if !ok {
		return discovery.PersistedSelection{}, false, nil
	}
	return loader.LoadPersistedSelection(ctx)
}
