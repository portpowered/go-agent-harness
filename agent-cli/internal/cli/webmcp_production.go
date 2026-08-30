package cli

import (
	"context"
	"errors"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"net/http"
	"strings"
	"sync"
)

type productionWebMCPComposition struct {
	mu sync.Mutex

	browser         config.BrowserConfig
	configDir       string
	inputs          discovery.ConnectionInputs
	discovery       WebMCPDiscoveryService
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

// Discover is the only browser enumeration path used by the production
// broker. Discovery supplies normalized candidates; this adapter restores the
// raw endpoint only inside the runtime boundary for the exact ID.
func (p *productionWebMCPComposition) Discover(ctx context.Context, options webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if p == nil || p.discovery == nil {
		return nil, webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, "browser endpoint was not found", nil)
	}
	inputs := p.inputs
	var err error
	inputs, err = p.managedDiscoveryInputs(ctx, inputs)
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
		return nil, productionDiscoveryError(err)
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
func (p *productionWebMCPComposition) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	if p == nil || p.runtime == nil {
		return nil, webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, "browser endpoint was not found", nil)
	}
	if _, err := p.ensureManagedBrowser(ctx); err != nil {
		return nil, err
	}
	enriched, err := p.enrichCoreCandidate(ctx, candidate)
	if err != nil {
		return nil, err
	}
	handle, err := p.runtime.Open(ctx, enriched)
	if err != nil {
		return nil, err
	}
	return &productionWebMCPHandle{
		owner:     p,
		candidate: enriched,
		raw:       handle,
		closed:    make(chan struct{}),
	}, nil
}

func (p *productionWebMCPComposition) coreCandidateForLane(ctx context.Context, laneCandidate discovery.BrowserCandidate) (webmcp.BrowserCandidate, error) {
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
		Source:            productionDiscoverySource(laneCandidate.Source),
		Product:           laneCandidate.Product,
		Protocol:          laneCandidate.Protocol,
		BrowserInstanceID: laneCandidate.BrowserInstanceID,
		Loopback:          laneCandidate.Loopback,
		Explicit:          laneCandidate.Source == discovery.SourceExplicitCDPHTTP || laneCandidate.Source == discovery.SourceExplicitBrowserWS,
		HTTPURL:           productionHTTPTransportURL(endpoint.CDPURL),
		BrowserWSURL:      productionWSTransportURL(endpoint.BrowserWSEndpoint),
		UserDataDir:       p.browser.Connection.UserDataDir,
	}
	p.mu.Lock()
	p.coreCandidates[laneCandidate.ID] = candidate
	p.laneCandidates[laneCandidate.ID] = laneCandidate
	p.mu.Unlock()
	return candidate, nil
}

func (p *productionWebMCPComposition) enrichCoreCandidate(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserCandidate, error) {
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
	laneCandidate = discovery.BrowserCandidate{
		ID:                string(candidate.ID),
		Source:            discovery.SourceConfigured,
		Product:           candidate.Product,
		Protocol:          candidate.Protocol,
		BrowserInstanceID: candidate.BrowserInstanceID,
		Loopback:          candidate.Loopback,
	}
	return p.coreCandidateForLane(ctx, laneCandidate)
}

func (p *productionWebMCPComposition) endpointForLane(ctx context.Context, laneCandidate discovery.BrowserCandidate) (discovery.Endpoint, error) {
	if p != nil && p.browser.UsesManagedBrowser() && p.browser.BrowserBackendEnabled() {
		browser, err := p.ensureManagedBrowser(ctx)
		if err != nil {
			return discovery.Endpoint{}, err
		}
		if browser == nil {
			return discovery.Endpoint{}, webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, "browser endpoint was not found", nil)
		}
		endpoint := discovery.Endpoint{CDPURL: browser.Endpoint().CDPURL}
		p.rememberEndpoint(laneCandidate.ID, endpoint)
		return endpoint, nil
	}
	p.mu.Lock()
	if endpoint, ok := p.endpoints[laneCandidate.ID]; ok {
		p.mu.Unlock()
		return endpoint, nil
	}
	hints := append([]discovery.Endpoint(nil), p.hints...)
	p.mu.Unlock()

	var endpoint discovery.Endpoint
	switch laneCandidate.Source {
	case discovery.SourceExplicitCDPHTTP:
		endpoint.CDPURL = p.browser.Connection.CDPURL
	case discovery.SourceExplicitBrowserWS:
		endpoint.BrowserWSEndpoint = p.browser.Connection.WSEndpoint
	case discovery.SourceDevToolsActivePort:
		if len(hints) > 0 {
			endpoint = hints[0]
		} else if p.browser.Connection.UserDataDir != "" {
			endpoint = p.endpointFromActivePort(ctx, p.browser.Connection.UserDataDir)
		}
	case discovery.SourceProcess, discovery.SourceConfigured:
		if len(hints) > 0 {
			endpoint = hints[0]
		}
	}
	if endpoint.CDPURL == "" && endpoint.BrowserWSEndpoint == "" {
		if p.browser.Connection.CDPURL != "" && laneCandidate.Source != discovery.SourceDevToolsActivePort && laneCandidate.Source != discovery.SourceProcess {
			endpoint.CDPURL = p.browser.Connection.CDPURL
		}
		if p.browser.Connection.WSEndpoint != "" && endpoint.CDPURL == "" {
			endpoint.BrowserWSEndpoint = p.browser.Connection.WSEndpoint
		}
	}
	if endpoint.CDPURL == "" && endpoint.BrowserWSEndpoint == "" {
		return discovery.Endpoint{}, webmcp.NewClassifiedError(webmcp.ErrorEndpointUnreachable, "the discovered browser endpoint could not be resolved for the selected browser", map[string]any{
			"phase": "runtime_endpoint",
		})
	}
	return endpoint, nil
}

func (p *productionWebMCPComposition) managedDiscoveryInputs(ctx context.Context, inputs discovery.ConnectionInputs) (discovery.ConnectionInputs, error) {
	if p == nil || !p.browser.UsesManagedBrowser() || !p.browser.BrowserBackendEnabled() {
		return inputs, nil
	}
	browser, err := p.ensureManagedBrowser(ctx)
	if err != nil {
		return discovery.ConnectionInputs{}, err
	}
	if browser == nil {
		return discovery.ConnectionInputs{}, webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, "browser endpoint was not found", nil)
	}
	inputs.CDPURL = browser.Endpoint().CDPURL
	inputs.BrowserWSEndpoint = ""
	inputs.UserDataDir = ""
	inputs.ConfiguredSources = nil
	inputs.AllowProcessScan = false
	return inputs, nil
}

// ensureManagedBrowser is the one lazy launch boundary for the production
// composition. Concurrent commands wait for the first acquisition and then
// share the exact persisted browser instead of starting overlapping Chrome
// processes.
func (p *productionWebMCPComposition) ensureManagedBrowser(ctx context.Context) (*chrome.ManagedBrowser, error) {
	if p == nil || !p.browser.UsesManagedBrowser() || !p.browser.BrowserBackendEnabled() {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, webmcp.ErrClosed
		}
		if p.managedBrowser != nil {
			browser := p.managedBrowser
			p.mu.Unlock()
			return browser, nil
		}
		if p.managedErr != nil {
			err := p.managedErr
			p.mu.Unlock()
			return nil, err
		}
		if p.managedStarting {
			done := p.managedStart
			p.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		p.managedStarting = true
		p.managedStart = make(chan struct{})
		done := p.managedStart
		manager := p.managedManager
		configDir := p.configDir
		startupURL := p.browser.ManagedStartupURL()
		headless := p.browser.Managed.Headless
		httpClient := p.httpClient
		p.mu.Unlock()

		var browser *chrome.ManagedBrowser
		var err error
		if manager == nil {
			err = errors.New("managed browser lifecycle manager is unavailable")
		} else {
			err = nil
			browser, err = manager.Acquire(ctx, chrome.ManagedBrowserLaunchOptions{
				ConfigDir:  configDir,
				StartupURL: startupURL,
				Headless:   headless,
				HTTPClient: httpClient,
			})
		}
		p.mu.Lock()
		p.managedStarting = false
		if err != nil {
			p.managedErr = err
		} else {
			p.managedBrowser = browser
		}
		close(done)
		p.mu.Unlock()
		return browser, err
	}
}

// Close releases the selected target through discovery and, only when the
// managed close-on-exit policy is enabled, closes the exact managed browser.
// The default policy leaves process, profile, and state warm for the next
// session. External browser configurations never enter this branch.
func (p *productionWebMCPComposition) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		start := p.managedStart
		p.mu.Unlock()
		if start != nil {
			<-start
		}
		p.mu.Lock()
		service := p.discovery
		browser := p.managedBrowser
		closeOnExit := p.browser.Managed.CloseOnExit
		p.mu.Unlock()
		var serviceErr error
		if closer, ok := service.(interface{ Close() error }); ok {
			serviceErr = closer.Close()
		}
		var browserErr error
		if closeOnExit && browser != nil {
			browserErr = browser.Close()
		}
		p.closeErr = errors.Join(serviceErr, browserErr)
	})
	return p.closeErr
}

func (p *productionWebMCPComposition) endpointFromActivePort(ctx context.Context, userDataDir string) discovery.Endpoint {
	if p == nil || p.activePort == nil {
		return discovery.Endpoint{}
	}
	record, err := p.activePort.Read(ctx, userDataDir)
	if err != nil {
		return discovery.Endpoint{}
	}
	endpoint, err := productionEndpointFromActivePort(record)
	if err != nil {
		return discovery.Endpoint{}
	}
	p.rememberEndpointHint(endpoint)
	return endpoint
}

func (p *productionWebMCPComposition) laneCandidateForCore(candidate webmcp.BrowserCandidate) discovery.BrowserCandidate {
	p.mu.Lock()
	if lane, ok := p.laneCandidates[string(candidate.ID)]; ok {
		p.mu.Unlock()
		return lane
	}
	p.mu.Unlock()
	return discovery.BrowserCandidate{
		ID:                string(candidate.ID),
		Source:            discovery.SourceConfigured,
		Product:           candidate.Product,
		Protocol:          candidate.Protocol,
		BrowserInstanceID: candidate.BrowserInstanceID,
		Loopback:          candidate.Loopback,
	}
}

func (p *productionWebMCPComposition) rawCandidateForLane(ctx context.Context, lane discovery.BrowserCandidate) (webmcp.BrowserCandidate, error) {
	return p.coreCandidateForLane(ctx, lane)
}

func (p *productionWebMCPComposition) listRawTargetDescriptors(ctx context.Context, lane discovery.BrowserCandidate) ([]discovery.TargetDescriptor, error) {
	candidate, err := p.rawCandidateForLane(ctx, lane)
	if err != nil {
		return nil, err
	}
	handle, err := p.runtime.Open(ctx, candidate)
	if err != nil {
		return nil, err
	}
	defer func() { _ = handle.Close() }()
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		return nil, err
	}
	descriptors := make([]discovery.TargetDescriptor, 0, len(targets))
	for _, target := range targets {
		descriptors = append(descriptors, discovery.TargetDescriptor{
			ID:                   string(target.ID),
			Type:                 target.Type,
			Title:                target.Title,
			URL:                  target.URL,
			WebSocketDebuggerURL: target.WebSocketURL,
			ContinuityMarker:     target.ContinuityMarker,
		})
	}
	return descriptors, nil
}

func (p *productionWebMCPComposition) probeTarget(ctx context.Context, lane discovery.BrowserCandidate, target discovery.Target) (discovery.TargetCapabilities, error) {
	candidate, err := p.rawCandidateForLane(ctx, lane)
	if err != nil {
		return discovery.TargetCapabilities{}, err
	}
	handle, err := p.runtime.Open(ctx, candidate)
	if err != nil {
		return discovery.TargetCapabilities{}, err
	}
	rawTarget, err := p.rawTargetForPublicID(ctx, handle, lane.ID, webmcp.TargetID(target.ID))
	if err != nil {
		_ = handle.Close()
		return discovery.TargetCapabilities{}, err
	}
	session, err := handle.Attach(ctx, rawTarget.ID, webmcp.TargetOwnershipExternal)
	if err != nil {
		_ = handle.Close()
		return discovery.TargetCapabilities{}, err
	}
	enableErr := session.EnableWebMCP(ctx)
	capabilities := discovery.TargetCapabilities{
		WebMCP:          true,
		DomainSupported: true,
		DomainKnown:     true,
		ToolCount:       -1,
	}
	if enableErr == nil {
		page := session.Context()
		capabilities.PageToolsReady = page.CatalogReady
		capabilities.PageToolsKnown = page.CatalogReady
		capabilities.PageToolsEvidence = page.CatalogEvidence
		capabilities.DocumentReadyState = page.DocumentReadyState
		capabilities.DocumentLoading = page.DocumentLoading
		capabilities.DocumentLoadingKnown = page.DocumentLoadingKnown
		for {
			select {
			case event, ok := <-session.Events():
				if !ok {
					goto drained
				}
				switch event.Type {
				case webmcp.EventCatalogReady:
					capabilities.PageToolsReady = true
					capabilities.PageToolsKnown = event.ToolCountKnown
					capabilities.PageToolsEvidence = "page_producer"
					if event.ToolCountKnown {
						capabilities.ToolCount = event.ToolCount
						capabilities.ToolCountKnown = true
					}
				case webmcp.EventToolsAdded:
					if len(event.Tools) > 0 {
						capabilities.PageToolsReady = true
						capabilities.PageToolsKnown = true
						capabilities.PageToolsEvidence = "tools_added"
						capabilities.ToolCount = len(event.Tools)
						capabilities.ToolCountKnown = true
					}
				}
			default:
				goto drained
			}
		}
	}
drained:
	sessionCloseErr := session.Close()
	handleCloseErr := handle.Close()
	cleanupErr := errors.Join(sessionCloseErr, handleCloseErr)
	if enableErr != nil {
		if isUnsupportedWebMCPError(enableErr) {
			return discovery.TargetCapabilities{ToolCount: -1, DomainKnown: true}, cleanupErr
		}
		return discovery.TargetCapabilities{}, errors.Join(enableErr, cleanupErr)
	}
	if cleanupErr != nil {
		return discovery.TargetCapabilities{}, cleanupErr
	}
	return capabilities, nil
}

func (p *productionWebMCPComposition) rawTargetForPublicID(ctx context.Context, handle webmcp.BrowserHandle, browserID string, publicID webmcp.TargetID) (webmcp.Target, error) {
	targets, err := handle.ListTargets(ctx)
	if err != nil {
		return webmcp.Target{}, err
	}
	for _, target := range targets {
		if target.ID == publicID || p.publicTargetID(browserID, target.ID) == publicID {
			return target, nil
		}
	}
	return webmcp.Target{}, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser target is no longer current", map[string]any{
		"browser_id": browserID,
		"target_id":  string(publicID),
		"reason":     "target_not_found",
	})
}

func (p *productionWebMCPComposition) publicTargetID(browserID string, rawID webmcp.TargetID) webmcp.TargetID {
	value := ""
	if p != nil && p.targetIDMapper != nil {
		value = strings.TrimSpace(p.targetIDMapper.TargetID(discovery.TargetIdentity{BrowserID: browserID, RawID: string(rawID)}))
	}
	if productionOpaqueID(value) && value != string(rawID) {
		return webmcp.TargetID(value)
	}
	return webmcp.TargetID(discovery.HashTargetIDMapper{}.TargetID(discovery.TargetIdentity{BrowserID: browserID, RawID: string(rawID)}))
}

func (p *productionWebMCPComposition) rememberEndpoint(browserID string, endpoint discovery.Endpoint) {
	if p == nil || browserID == "" || endpoint.CDPURL == "" && endpoint.BrowserWSEndpoint == "" {
		return
	}
	p.mu.Lock()
	p.endpoints[browserID] = endpoint
	p.mu.Unlock()
}

func (p *productionWebMCPComposition) rememberEndpointHint(endpoint discovery.Endpoint) {
	if p == nil || endpoint.CDPURL == "" && endpoint.BrowserWSEndpoint == "" {
		return
	}
	p.mu.Lock()
	p.hints = append(p.hints, endpoint)
	p.mu.Unlock()
}

type productionWebMCPHandle struct {
	owner     *productionWebMCPComposition
	candidate webmcp.BrowserCandidate
	raw       webmcp.BrowserHandle
	closeOnce sync.Once
	closeErr  error
	closed    chan struct{}
}

func (h *productionWebMCPHandle) Candidate() webmcp.BrowserCandidate {
	if h == nil {
		return webmcp.BrowserCandidate{}
	}
	return h.candidate
}

func (h *productionWebMCPHandle) ListTargets(ctx context.Context) ([]webmcp.Target, error) {
	if h == nil || h.owner == nil || h.isClosed() {
		return nil, webmcp.ErrClosed
	}
	lane := h.owner.laneCandidateForCore(h.candidate)
	snapshot, err := h.owner.discovery.ListTargetSnapshot(ctx, lane, discovery.TargetListOptions{
		BrowserID:            lane.ID,
		EligibleOnly:         discovery.Bool(false),
		IncludeZeroToolPages: true,
	})
	if err != nil {
		var discoveryErr *discovery.DiscoveryError
		if errors.As(err, &discoveryErr) && discoveryErr.Code == discovery.CodeNoEligibleTab && len(snapshot.Targets) == 0 {
			return []webmcp.Target{}, nil
		}
		return nil, productionDiscoveryError(err)
	}
	targets := make([]webmcp.Target, 0, len(snapshot.Targets))
	for _, target := range snapshot.Targets {
		targets = append(targets, productionNeutralTarget(h.candidate.ID, target))
	}
	return targets, nil
}

func (h *productionWebMCPHandle) Activate(ctx context.Context, targetID webmcp.TargetID) error {
	if h == nil || h.owner == nil || h.raw == nil || h.isClosed() {
		return webmcp.ErrClosed
	}
	rawTarget, err := h.owner.rawTargetForPublicID(ctx, h.raw, string(h.candidate.ID), targetID)
	if err != nil {
		return err
	}
	return h.raw.Activate(ctx, rawTarget.ID)
}

func (h *productionWebMCPHandle) Attach(ctx context.Context, targetID webmcp.TargetID, ownership webmcp.TargetOwnership) (webmcp.TargetSession, error) {
	if h == nil || h.owner == nil || h.raw == nil || h.isClosed() {
		return nil, webmcp.ErrClosed
	}
	lane := h.owner.laneCandidateForCore(h.candidate)
	rawTarget, err := h.owner.rawTargetForPublicID(ctx, h.raw, string(h.candidate.ID), targetID)
	if err != nil {
		return nil, err
	}
	rawSession, err := h.raw.Attach(ctx, rawTarget.ID, ownership)
	if err != nil {
		return nil, err
	}
	selection, selectErr := h.owner.discovery.Select(ctx, discovery.TargetSelectionRequest{
		Browser:   lane,
		BrowserID: lane.ID,
		TargetID:  string(targetID),
		Reason:    "broker_exact_selection",
	})
	if selectErr != nil {
		_ = rawSession.Close()
		return nil, productionDiscoveryError(selectErr)
	}
	publicTarget := productionNeutralTarget(h.candidate.ID, selection.Target)
	if publicTarget.ID == "" {
		publicTarget = webmcp.Target{
			BrowserID:  h.candidate.ID,
			ID:         targetID,
			Type:       rawTarget.Type,
			Title:      rawTarget.Title,
			URL:        productionSafePageURL(rawTarget.URL),
			Origin:     productionSafeOrigin(rawTarget.Origin),
			Eligible:   true,
			Generation: 1,
		}
	}
	return newProductionWebMCPSession(rawSession, publicTarget)
}

func (h *productionWebMCPHandle) Close() error {
	if h == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		if h.raw != nil {
			h.closeErr = h.raw.Close()
		}
		if h.closed != nil {
			close(h.closed)
		}
	})
	if h.closed != nil {
		<-h.closed
	}
	return h.closeErr
}

func (h *productionWebMCPHandle) isClosed() bool {
	if h == nil || h.closed == nil {
		return false
	}
	select {
	case <-h.closed:
		return true
	default:
		return false
	}
}

var (
	_ webmcp.BrowserDiscoverer = (*productionWebMCPComposition)(nil)
	_ webmcp.BrowserRuntime    = (*productionWebMCPComposition)(nil)
	_ webmcp.BrowserHandle     = (*productionWebMCPHandle)(nil)
	_ webmcp.TargetSession     = (*productionTargetSession)(nil)
	_ webmcp.DevToolsCatalog   = (*productionWebMCPCatalog)(nil)
)
