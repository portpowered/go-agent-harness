package production

import (
	"context"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

func endpointEmpty(endpoint discovery.Endpoint) bool {
	return endpoint.CDPURL == "" && endpoint.BrowserWSEndpoint == ""
}

// endpointForLane restores the raw transport endpoint for one normalized
// browser candidate. Managed browsers always use the manager-owned loopback
// endpoint; external browsers resolve from remembered endpoints, discovery
// hints, and finally the configured connection values.
func (p *composition) endpointForLane(ctx context.Context, laneCandidate discovery.BrowserCandidate) (discovery.Endpoint, error) {
	if p.managedEnabled() {
		return p.managedEndpoint(ctx, laneCandidate.ID)
	}
	p.mu.Lock()
	if endpoint, ok := p.endpoints[laneCandidate.ID]; ok {
		p.mu.Unlock()
		return endpoint, nil
	}
	hints := append([]discovery.Endpoint(nil), p.hints...)
	p.mu.Unlock()

	endpoint := p.sourceEndpoint(ctx, laneCandidate.Source, hints)
	if endpointEmpty(endpoint) {
		endpoint = p.configuredEndpoint(laneCandidate.Source)
	}
	if endpointEmpty(endpoint) {
		return discovery.Endpoint{}, webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, "the discovered browser endpoint could not be resolved for the selected browser", map[string]any{
			"phase": "runtime_endpoint",
		})
	}
	return endpoint, nil
}

func (p *composition) managedEndpoint(ctx context.Context, browserID string) (discovery.Endpoint, error) {
	browser, err := p.ensureManagedBrowser(ctx)
	if err != nil {
		return discovery.Endpoint{}, err
	}
	if browser == nil {
		return discovery.Endpoint{}, endpointNotFound()
	}
	endpoint := discovery.Endpoint{CDPURL: browser.Endpoint().CDPURL}
	p.rememberEndpoint(browserID, endpoint)
	return endpoint, nil
}

func (p *composition) sourceEndpoint(ctx context.Context, source discovery.Source, hints []discovery.Endpoint) discovery.Endpoint {
	switch source {
	case discovery.SourceExplicitCDPHTTP:
		return discovery.Endpoint{CDPURL: p.browser.Connection.CDPURL}
	case discovery.SourceExplicitBrowserWS:
		return discovery.Endpoint{BrowserWSEndpoint: p.browser.Connection.WSEndpoint}
	case discovery.SourceDevToolsActivePort:
		if len(hints) > 0 {
			return hints[0]
		}
		if p.browser.Connection.UserDataDir != "" {
			return p.endpointFromActivePort(ctx, p.browser.Connection.UserDataDir)
		}
	case discovery.SourceProcess, discovery.SourceConfigured:
		if len(hints) > 0 {
			return hints[0]
		}
	}
	return discovery.Endpoint{}
}

// configuredEndpoint falls back to the configured connection values. A CDP
// URL never stands in for an active-port or process-discovered browser.
func (p *composition) configuredEndpoint(source discovery.Source) discovery.Endpoint {
	var endpoint discovery.Endpoint
	if p.browser.Connection.CDPURL != "" && source != discovery.SourceDevToolsActivePort && source != discovery.SourceProcess {
		endpoint.CDPURL = p.browser.Connection.CDPURL
	}
	if p.browser.Connection.WSEndpoint != "" && endpoint.CDPURL == "" {
		endpoint.BrowserWSEndpoint = p.browser.Connection.WSEndpoint
	}
	return endpoint
}

func (p *composition) endpointFromActivePort(ctx context.Context, userDataDir string) discovery.Endpoint {
	if p == nil || p.activePort == nil {
		return discovery.Endpoint{}
	}
	record, err := p.activePort.Read(ctx, userDataDir)
	if err != nil {
		return discovery.Endpoint{}
	}
	endpoint, err := normalize.EndpointFromActivePort(record)
	if err != nil {
		return discovery.Endpoint{}
	}
	p.rememberEndpointHint(endpoint)
	return endpoint
}

func (p *composition) managedDiscoveryInputs(ctx context.Context, inputs discovery.ConnectionInputs) (discovery.ConnectionInputs, error) {
	if !p.managedEnabled() {
		return inputs, nil
	}
	browser, err := p.ensureManagedBrowser(ctx)
	if err != nil {
		return discovery.ConnectionInputs{}, err
	}
	if browser == nil {
		return discovery.ConnectionInputs{}, endpointNotFound()
	}
	inputs.CDPURL = browser.Endpoint().CDPURL
	inputs.BrowserWSEndpoint = ""
	inputs.UserDataDir = ""
	inputs.ConfiguredSources = nil
	inputs.AllowProcessScan = false
	return inputs, nil
}
