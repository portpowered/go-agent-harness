package production

import (
	"context"
	"errors"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

const messageManagedDiscoveryUnavailable = "managed WebMCP discovery service is unavailable"

// managedDiscoveryService injects the manager-owned loopback endpoint into
// every discovery/reconnect pass while leaving the neutral discovery service
// responsible for target identity, persistence, and detach-only selection
// cleanup.
type managedDiscoveryService struct {
	owner    *composition
	delegate DiscoveryService
}

func managedDiscoveryUnavailable() error {
	return errors.New(messageManagedDiscoveryUnavailable)
}

func (s *managedDiscoveryService) inputs(ctx context.Context, inputs discovery.ConnectionInputs) (discovery.ConnectionInputs, error) {
	if s == nil || s.delegate == nil {
		return discovery.ConnectionInputs{}, managedDiscoveryUnavailable()
	}
	if s.owner == nil {
		return inputs, nil
	}
	return s.owner.managedDiscoveryInputs(ctx, inputs)
}

// ensureBrowser lazily launches the managed browser before a delegated call.
func (s *managedDiscoveryService) ensureBrowser(ctx context.Context) error {
	if s == nil || s.delegate == nil {
		return managedDiscoveryUnavailable()
	}
	if s.owner != nil {
		if _, err := s.owner.ensureManagedBrowser(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *managedDiscoveryService) DiscoverAll(ctx context.Context, inputs discovery.ConnectionInputs) ([]discovery.BrowserCandidate, error) {
	effective, err := s.inputs(ctx, inputs)
	if err != nil {
		return nil, err
	}
	return s.delegate.DiscoverAll(ctx, effective)
}

func (s *managedDiscoveryService) ListTargetSnapshot(ctx context.Context, browser discovery.BrowserCandidate, options ...discovery.TargetListOptions) (discovery.TargetSnapshot, error) {
	if err := s.ensureBrowser(ctx); err != nil {
		return discovery.TargetSnapshot{}, err
	}
	return s.delegate.ListTargetSnapshot(ctx, browser, options...)
}

func (s *managedDiscoveryService) Select(ctx context.Context, request discovery.TargetSelectionRequest) (discovery.Selection, error) {
	if err := s.ensureBrowser(ctx); err != nil {
		return discovery.Selection{}, err
	}
	return s.delegate.Select(ctx, request)
}

func (s *managedDiscoveryService) Selected() (discovery.Selection, bool) {
	if s == nil || s.delegate == nil {
		return discovery.Selection{}, false
	}
	return s.delegate.Selected()
}

func (s *managedDiscoveryService) RefreshSelection(ctx context.Context) (discovery.Selection, error) {
	if err := s.ensureBrowser(ctx); err != nil {
		return discovery.Selection{}, err
	}
	return s.delegate.RefreshSelection(ctx)
}

func (s *managedDiscoveryService) Reconnect(ctx context.Context, inputs discovery.ConnectionInputs, options ...discovery.ReconnectOptions) (discovery.Selection, error) {
	if s == nil || s.delegate == nil {
		return discovery.Selection{}, managedDiscoveryUnavailable()
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

func (s *managedDiscoveryService) LoadPersistedSelection(ctx context.Context) (discovery.PersistedSelection, bool, error) {
	if s == nil || s.delegate == nil {
		return discovery.PersistedSelection{}, false, managedDiscoveryUnavailable()
	}
	loader, ok := s.delegate.(interface {
		LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error)
	})
	if !ok {
		return discovery.PersistedSelection{}, false, nil
	}
	return loader.LoadPersistedSelection(ctx)
}
