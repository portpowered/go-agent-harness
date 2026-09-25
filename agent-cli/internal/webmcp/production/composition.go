package production

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

const messageEndpointNotFound = "browser endpoint was not found"

// composition is the one command-scoped owner of discovery, the raw browser
// runtime, and the optional managed browser. It restores raw endpoints only
// inside the runtime boundary for exact normalized browser IDs.
type composition struct {
	mu sync.Mutex

	browser         config.BrowserConfig
	configDir       string
	inputs          discovery.ConnectionInputs
	discovery       DiscoveryService
	runtime         webmcp.BrowserRuntime
	catalog         webmcp.DevToolsCatalog
	httpClient      *http.Client
	managedManager  *chrome.ManagedBrowserManager
	managedBrowser  *chrome.ManagedBrowser
	managedStart    chan struct{}
	managedStarting bool
	managedErr      error
	closed          bool
	closeOnce       sync.Once
	closeErr        error
	activePort      discovery.ActivePortReader
	idMapper        discovery.IDMapper
	targetIDMapper  discovery.TargetIDMapper
	clock           discovery.Clock

	coreCandidates map[string]webmcp.BrowserCandidate
	laneCandidates map[string]discovery.BrowserCandidate
	endpoints      map[string]discovery.Endpoint
	hints          []discovery.Endpoint
}

var (
	_ webmcp.BrowserDiscoverer = (*composition)(nil)
	_ webmcp.BrowserRuntime    = (*composition)(nil)
)

func endpointNotFound() error {
	return webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, messageEndpointNotFound, nil)
}

// Discover is the only browser enumeration path used by the production
// broker. Discovery supplies normalized candidates; this adapter restores the
// raw endpoint only inside the runtime boundary for the exact ID.
func (p *composition) Discover(ctx context.Context, options webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if p == nil || p.discovery == nil {
		return nil, endpointNotFound()
	}
	inputs, err := p.managedDiscoveryInputs(ctx, p.inputs)
	if err != nil {
		return nil, err
	}
	if options.ExplicitOnly {
		inputs.UserDataDir = ""
		inputs.ConfiguredSources = nil
		inputs.AllowProcessScan = false
	}
	inputs.AllowProcessScan = inputs.AllowProcessScan && options.AllowProcessScan
	inputs.AllowRemoteCDP = options.AllowRemoteCDP
	laneCandidates, err := p.discovery.DiscoverAll(ctx, inputs)
	if err != nil {
		return nil, normalize.DiscoveryError(err)
	}
	result := make([]webmcp.BrowserCandidate, 0, len(laneCandidates))
	for _, laneCandidate := range laneCandidates {
		candidate, candidateErr := p.coreCandidateForLane(ctx, laneCandidate)
		if candidateErr != nil {
			return nil, candidateErr
		}
		result = append(result, candidate)
	}
	return result, nil
}

// Open retains one command-scoped raw browser handle. Target discovery still
// flows through the discovery service so all public target IDs and continuity
// markers remain normalized there.
func (p *composition) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	if p == nil || p.runtime == nil {
		return nil, endpointNotFound()
	}
	if _, err := p.ensureManagedBrowser(ctx); err != nil {
		return nil, err
	}
	enriched, err := p.enrichCoreCandidate(ctx, candidate)
	if err != nil {
		return nil, err
	}
	raw, err := p.runtime.Open(ctx, enriched)
	if err != nil {
		return nil, err
	}
	return &handle{
		owner:     p,
		candidate: enriched,
		raw:       raw,
		closed:    make(chan struct{}),
	}, nil
}

func (p *composition) coreCandidateForLane(ctx context.Context, laneCandidate discovery.BrowserCandidate) (webmcp.BrowserCandidate, error) {
	if laneCandidate.ID == "" {
		return webmcp.BrowserCandidate{}, webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "browser discovery returned an empty browser ID", nil)
	}
	p.mu.Lock()
	if candidate, ok := p.coreCandidates[laneCandidate.ID]; ok {
		p.mu.Unlock()
		return candidate, nil
	}
	p.mu.Unlock()

	endpoint, err := p.endpointForLane(ctx, laneCandidate)
	if err != nil {
		return webmcp.BrowserCandidate{}, err
	}
	candidate := webmcp.BrowserCandidate{
		ID:                webmcp.BrowserID(laneCandidate.ID),
		Source:            normalize.DiscoverySource(laneCandidate.Source),
		Product:           laneCandidate.Product,
		Protocol:          laneCandidate.Protocol,
		BrowserInstanceID: laneCandidate.BrowserInstanceID,
		Loopback:          laneCandidate.Loopback,
		Explicit:          laneCandidate.Source == discovery.SourceExplicitCDPHTTP || laneCandidate.Source == discovery.SourceExplicitBrowserWS,
		HTTPURL:           normalize.HTTPTransportURL(endpoint.CDPURL),
		BrowserWSURL:      normalize.WSTransportURL(endpoint.BrowserWSEndpoint),
		UserDataDir:       p.browser.Connection.UserDataDir,
	}
	p.mu.Lock()
	p.coreCandidates[laneCandidate.ID] = candidate
	p.laneCandidates[laneCandidate.ID] = laneCandidate
	p.mu.Unlock()
	return candidate, nil
}

func (p *composition) enrichCoreCandidate(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserCandidate, error) {
	if candidate.ID == "" {
		return webmcp.BrowserCandidate{}, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser is no longer current", map[string]any{"reason": "browser_id_required"})
	}
	p.mu.Lock()
	if known, ok := p.coreCandidates[string(candidate.ID)]; ok {
		p.mu.Unlock()
		return known, nil
	}
	laneCandidate, ok := p.laneCandidates[string(candidate.ID)]
	p.mu.Unlock()
	if ok {
		return p.coreCandidateForLane(ctx, laneCandidate)
	}
	// A caller may open an exact normalized candidate without first calling
	// Discover. Reconstruct only its public identity and use configured source
	// values for the transport; never enumerate a replacement browser.
	return p.coreCandidateForLane(ctx, configuredLaneCandidate(candidate))
}

func (p *composition) laneCandidateForCore(candidate webmcp.BrowserCandidate) discovery.BrowserCandidate {
	p.mu.Lock()
	if lane, ok := p.laneCandidates[string(candidate.ID)]; ok {
		p.mu.Unlock()
		return lane
	}
	p.mu.Unlock()
	return configuredLaneCandidate(candidate)
}

func configuredLaneCandidate(candidate webmcp.BrowserCandidate) discovery.BrowserCandidate {
	return discovery.BrowserCandidate{
		ID:                string(candidate.ID),
		Source:            discovery.SourceConfigured,
		Product:           candidate.Product,
		Protocol:          candidate.Protocol,
		BrowserInstanceID: candidate.BrowserInstanceID,
		Loopback:          candidate.Loopback,
	}
}

func (p *composition) publicTargetID(browserID string, rawID webmcp.TargetID) webmcp.TargetID {
	value := ""
	if p != nil && p.targetIDMapper != nil {
		value = strings.TrimSpace(p.targetIDMapper.TargetID(discovery.TargetIdentity{BrowserID: browserID, RawID: string(rawID)}))
	}
	if normalize.OpaqueID(value) && value != string(rawID) {
		return webmcp.TargetID(value)
	}
	return webmcp.TargetID(discovery.HashTargetIDMapper{}.TargetID(discovery.TargetIdentity{BrowserID: browserID, RawID: string(rawID)}))
}

func (p *composition) rememberEndpoint(browserID string, endpoint discovery.Endpoint) {
	if p == nil || browserID == "" || endpoint.CDPURL == "" && endpoint.BrowserWSEndpoint == "" {
		return
	}
	p.mu.Lock()
	p.endpoints[browserID] = endpoint
	p.mu.Unlock()
}

func (p *composition) rememberEndpointHint(endpoint discovery.Endpoint) {
	if p == nil || endpoint.CDPURL == "" && endpoint.BrowserWSEndpoint == "" {
		return
	}
	p.mu.Lock()
	p.hints = append(p.hints, endpoint)
	p.mu.Unlock()
}
