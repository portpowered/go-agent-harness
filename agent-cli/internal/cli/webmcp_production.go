package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/chrome"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

// WebMCPProductionOptions contains the neutral seams used by the production
// WebMCP factory. The options are intentionally transport-neutral so command
// tests can replace discovery or Chrome without changing the CLI contract.
type WebMCPProductionOptions struct {
	Runtime               webmcp.BrowserRuntime
	Catalog               webmcp.DevToolsCatalog
	Discovery             WebMCPDiscoveryService
	HTTPClient            discovery.HTTPClient
	ActivePortReader      discovery.ActivePortReader
	ProcessEnumerator     discovery.ProcessEnumerator
	IDMapper              discovery.IDMapper
	TargetIDMapper        discovery.TargetIDMapper
	Clock                 discovery.Clock
	SelectionStore        any
	SelectionStoreFactory func() any
}

// WebMCPProductionOption customizes one production factory dependency.
type WebMCPProductionOption func(*WebMCPProductionOptions)

func WithWebMCPProductionRuntime(runtime webmcp.BrowserRuntime) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.Runtime = runtime }
}

func WithWebMCPProductionCatalog(catalog webmcp.DevToolsCatalog) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.Catalog = catalog }
}

func WithWebMCPProductionDiscovery(service WebMCPDiscoveryService) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.Discovery = service }
}

func WithWebMCPProductionHTTPClient(client discovery.HTTPClient) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.HTTPClient = client }
}

func WithWebMCPProductionActivePortReader(reader discovery.ActivePortReader) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.ActivePortReader = reader }
}

func WithWebMCPProductionProcessEnumerator(enumerator discovery.ProcessEnumerator) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.ProcessEnumerator = enumerator }
}

func WithWebMCPProductionIDMapper(mapper discovery.IDMapper) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.IDMapper = mapper }
}

func WithWebMCPProductionTargetIDMapper(mapper discovery.TargetIDMapper) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.TargetIDMapper = mapper }
}

func WithWebMCPProductionClock(clock discovery.Clock) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.Clock = clock }
}

func WithWebMCPProductionSelectionStore(store any) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.SelectionStore = store }
}

// WithWebMCPProductionSelectionStoreFactory defers selection-store creation
// until command execution. This keeps a parsed --config-dir override aligned
// with the store used by the direct command.
func WithWebMCPProductionSelectionStoreFactory(factory func() any) WebMCPProductionOption {
	return func(options *WebMCPProductionOptions) { options.SelectionStoreFactory = factory }
}

// NewProductionWebMCPDoctorFactory composes browser discovery, the neutral
// broker, and Chrome's browser runtime for the actual CLI routes. Construction
// remains lazy: no browser endpoint is opened until a command invokes the
// returned factory runtime.
func NewProductionWebMCPDoctorFactory(options ...WebMCPProductionOption) WebMCPDoctorFactory {
	resolved := WebMCPProductionOptions{}
	for _, option := range options {
		if option != nil {
			option(&resolved)
		}
	}

	return func(browser config.BrowserConfig) (WebMCPDoctorRuntime, error) {
		if err := browser.Validate(); err != nil {
			return WebMCPDoctorRuntime{}, fmt.Errorf("resolve browser config: %w", err)
		}
		if err := validateDoctorEndpoints(browser); err != nil {
			return WebMCPDoctorRuntime{}, err
		}

		httpClient := resolved.HTTPClient
		if httpClient == nil {
			httpClient = http.DefaultClient
		}
		activePortReader := resolved.ActivePortReader
		if activePortReader == nil {
			activePortReader = discovery.FileActivePortReader{}
		}
		idMapper := resolved.IDMapper
		if idMapper == nil {
			idMapper = discovery.HashIDMapper{}
		}
		targetIDMapper := resolved.TargetIDMapper
		if targetIDMapper == nil {
			targetIDMapper = discovery.HashTargetIDMapper{}
		}

		runtime := resolved.Runtime
		if runtime == nil {
			runtimeOptions := make([]chrome.Option, 0, 1)
			if client, ok := httpClient.(*http.Client); ok {
				runtimeOptions = append(runtimeOptions, chrome.WithHTTPClient(client))
			}
			runtime = chrome.NewRuntime(runtimeOptions...)
		}
		catalog := resolved.Catalog
		if catalog == nil {
			if catalogRuntime, ok := runtime.(webmcp.DevToolsCatalog); ok {
				catalog = catalogRuntime
			}
		}

		composition := &productionWebMCPComposition{
			browser:        browser,
			inputs:         productionDiscoveryInputs(browser),
			runtime:        runtime,
			catalog:        catalog,
			activePort:     activePortReader,
			idMapper:       idMapper,
			targetIDMapper: targetIDMapper,
			clock:          resolved.Clock,
			coreCandidates: make(map[string]webmcp.BrowserCandidate),
			laneCandidates: make(map[string]discovery.BrowserCandidate),
			endpoints:      make(map[string]discovery.Endpoint),
		}

		var service WebMCPDiscoveryService = resolved.Discovery
		if service == nil {
			selectionStore := resolved.SelectionStore
			if resolved.SelectionStoreFactory != nil {
				selectionStore = resolved.SelectionStoreFactory()
			}
			serviceOptions := discovery.Options{
				HTTPClient:        &productionHTTPClient{delegate: httpClient, owner: composition},
				ActivePortReader:  &productionActivePortReader{delegate: activePortReader, owner: composition},
				TargetLister:      productionTargetLister{owner: composition},
				TargetProbe:       productionTargetProbe{owner: composition},
				IDMapper:          idMapper,
				TargetIDMapper:    targetIDMapper,
				AllowedOrigins:    append([]string(nil), browser.Policy.AllowedOrigins...),
				DeniedOrigins:     append([]string(nil), browser.Policy.DeniedOrigins...),
				Clock:             resolved.Clock,
				ProcessEnumerator: nil,
				SelectionStore:    productionSelectionStore(selectionStore),
			}
			if resolved.ProcessEnumerator != nil {
				serviceOptions.ProcessEnumerator = &productionProcessEnumerator{
					delegate: resolved.ProcessEnumerator,
					owner:    composition,
				}
			}
			persistenceEnabled := browser.Selection.Persist && serviceOptions.SelectionStore != nil
			serviceOptions.PersistenceEnabled = &persistenceEnabled
			service = discovery.New(serviceOptions)
		}
		composition.discovery = service

		brokerOptions := webmcp.BrokerOptions{
			Runtime:           composition,
			Discoverer:        composition,
			ToolRefFactory:    webmcp.StableToolRef,
			CancelOnInterrupt: browser.Policy.CancelOnInterrupt,
			MaxInputBytes:     browser.Limits.MaxInputBytes,
			MaxResultBytes:    browser.Limits.MaxResultBytes,
			InvocationTimeout: browser.Limits.InvocationTimeout,
		}
		if resolved.Clock != nil {
			brokerOptions.Clock = resolved.Clock
		}
		broker := webmcp.NewBroker(brokerOptions)

		var closeService func() error
		if closer, ok := service.(interface{ Close() error }); ok {
			closeService = closer.Close
		}
		return WebMCPDoctorRuntime{
			Broker:    broker,
			Discovery: service,
			Catalog:   &productionWebMCPCatalog{owner: composition},
			Close:     closeService,
		}, nil
	}
}

// configDirForGlobalFlags is kept at the composition boundary so the route
// can give the production factory the same selection path as direct commands.
func configDirForGlobalFlags(globalFlags *flags.GlobalFlags) string {
	if globalFlags == nil {
		return ""
	}
	return globalFlags.ConfigDir()
}

// defaultWebMCPDoctorFactory is the single default composition used by
// direct commands and router fallbacks. The selection store is created only
// after flags have been parsed, so --config-dir applies consistently without
// making command construction touch the filesystem.
func defaultWebMCPDoctorFactory(globalFlags *flags.GlobalFlags) WebMCPDoctorFactory {
	return NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionSelectionStoreFactory(func() any {
			return NewFileWebMCPSelectionStore(configDirForGlobalFlags(globalFlags))
		}),
	)
}

func webmcpRuntimeUnavailableError(phase string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "the WebMCP browser runtime is unavailable", map[string]any{
		"phase": phase,
	})
}

func webmcpRuntimeFactoryError(err error) error {
	if err == nil {
		return nil
	}
	var classified *webmcp.ClassifiedError
	if errors.As(err, &classified) && classified != nil {
		return err
	}
	return webmcp.NewClassifiedError(webmcp.ErrorBrowserProtocol, "the WebMCP browser runtime could not be constructed", map[string]any{
		"phase": "runtime_factory",
	})
}

func productionDiscoveryInputs(browser config.BrowserConfig) discovery.ConnectionInputs {
	return discovery.ConnectionInputs{
		CDPURL:            browser.Connection.CDPURL,
		BrowserWSEndpoint: browser.Connection.WSEndpoint,
		UserDataDir:       browser.Connection.UserDataDir,
		AllowProcessScan:  browser.Connection.AllowProcessScan,
		AllowRemoteCDP:    browser.Connection.AllowRemoteCDP,
	}
}

type productionWebMCPComposition struct {
	mu sync.Mutex

	browser        config.BrowserConfig
	inputs         discovery.ConnectionInputs
	discovery      WebMCPDiscoveryService
	runtime        webmcp.BrowserRuntime
	catalog        webmcp.DevToolsCatalog
	activePort     discovery.ActivePortReader
	idMapper       discovery.IDMapper
	targetIDMapper discovery.TargetIDMapper
	clock          discovery.Clock

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
		ID:           webmcp.BrowserID(laneCandidate.ID),
		Source:       productionDiscoverySource(laneCandidate.Source),
		Product:      laneCandidate.Product,
		Protocol:     laneCandidate.Protocol,
		Loopback:     laneCandidate.Loopback,
		Explicit:     laneCandidate.Source == discovery.SourceExplicitCDPHTTP || laneCandidate.Source == discovery.SourceExplicitBrowserWS,
		HTTPURL:      productionHTTPTransportURL(endpoint.CDPURL),
		BrowserWSURL: productionWSTransportURL(endpoint.BrowserWSEndpoint),
		UserDataDir:  p.browser.Connection.UserDataDir,
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
		ID:       string(candidate.ID),
		Source:   discovery.SourceConfigured,
		Product:  candidate.Product,
		Protocol: candidate.Protocol,
		Loopback: candidate.Loopback,
	}
	return p.coreCandidateForLane(ctx, laneCandidate)
}

func (p *productionWebMCPComposition) endpointForLane(ctx context.Context, laneCandidate discovery.BrowserCandidate) (discovery.Endpoint, error) {
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
		ID:       string(candidate.ID),
		Source:   discovery.SourceConfigured,
		Product:  candidate.Product,
		Protocol: candidate.Protocol,
		Loopback: candidate.Loopback,
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

type productionTargetLister struct{ owner *productionWebMCPComposition }

func (l productionTargetLister) List(ctx context.Context, browser discovery.BrowserCandidate) ([]discovery.TargetDescriptor, error) {
	if l.owner == nil {
		return nil, webmcp.ErrClosed
	}
	return l.owner.listRawTargetDescriptors(ctx, browser)
}

type productionTargetProbe struct{ owner *productionWebMCPComposition }

func (p productionTargetProbe) Probe(ctx context.Context, browser discovery.BrowserCandidate, target discovery.Target) (discovery.TargetCapabilities, error) {
	if p.owner == nil {
		return discovery.TargetCapabilities{}, webmcp.ErrClosed
	}
	return p.owner.probeTarget(ctx, browser, target)
}

type productionWebMCPCatalog struct{ owner *productionWebMCPComposition }

func (c *productionWebMCPCatalog) Version(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserVersion, error) {
	if c == nil || c.owner == nil {
		return webmcp.BrowserVersion{}, webmcp.ErrClosed
	}
	enriched, err := c.owner.enrichCoreCandidate(ctx, candidate)
	if err != nil {
		return webmcp.BrowserVersion{}, err
	}
	if c.owner.catalog != nil {
		return c.owner.catalog.Version(ctx, enriched)
	}
	return webmcp.BrowserVersion{
		Browser:              enriched.Product,
		ProtocolVersion:      enriched.Protocol,
		WebSocketDebuggerURL: enriched.BrowserWSURL,
	}, nil
}

func (c *productionWebMCPCatalog) ListTargets(ctx context.Context, candidate webmcp.BrowserCandidate) ([]webmcp.Target, error) {
	if c == nil || c.owner == nil {
		return nil, webmcp.ErrClosed
	}
	return (&productionWebMCPHandle{owner: c.owner, candidate: candidate, closed: make(chan struct{})}).ListTargets(ctx)
}

type productionTargetSession struct {
	raw       webmcp.TargetSession
	target    webmcp.Target
	events    chan webmcp.BrowserEvent
	done      chan struct{}
	stop      chan struct{}
	flush     chan chan struct{}
	stopOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

func newProductionWebMCPSession(raw webmcp.TargetSession, target webmcp.Target) (webmcp.TargetSession, error) {
	if raw == nil {
		return nil, webmcp.NewClassifiedError(webmcp.ErrorTargetAttachFailed, "the selected browser target could not be initialized", nil)
	}
	session := &productionTargetSession{
		raw:    raw,
		target: target,
		events: make(chan webmcp.BrowserEvent, 128),
		done:   make(chan struct{}),
		stop:   make(chan struct{}),
		flush:  make(chan chan struct{}),
	}
	go session.forwardEvents()
	return session, nil
}

func (s *productionTargetSession) Context() webmcp.PageContext {
	page := s.raw.Context()
	page.Key = webmcp.PageKey{BrowserID: s.target.BrowserID, TargetID: s.target.ID}
	if s.target.Title != "" {
		page.Title = s.target.Title
	}
	if s.target.URL != "" {
		page.URL = s.target.URL
	}
	if s.target.Origin != "" {
		page.Origin = s.target.Origin
	}
	if s.target.Generation > 0 {
		page.Generation = s.target.Generation
	}
	return page
}

func (s *productionTargetSession) Ownership() webmcp.TargetOwnership { return s.raw.Ownership() }

func (s *productionTargetSession) EnableWebMCP(ctx context.Context) error {
	if err := s.raw.EnableWebMCP(ctx); err != nil {
		return err
	}
	// The neutral broker flushes the session immediately after enablement.
	// Bridge adapters have one additional forwarding hop, so synchronize that
	// hop before returning; otherwise a just-emitted ToolsAdded event could
	// arrive after the broker's flush and make a ready catalog appear empty.
	return s.flushEvents(ctx)
}

func (s *productionTargetSession) Events() <-chan webmcp.BrowserEvent { return s.events }

func (s *productionTargetSession) InvokeWebMCP(ctx context.Context, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	return s.raw.InvokeWebMCP(ctx, frameID, toolName, input)
}

func (s *productionTargetSession) CancelWebMCP(ctx context.Context, invocationID webmcp.InvocationID) error {
	return s.raw.CancelWebMCP(ctx, invocationID)
}

func (s *productionTargetSession) Done() <-chan struct{} { return s.done }

func (s *productionTargetSession) Err() error { return s.raw.Err() }

func (s *productionTargetSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.stopOnce.Do(func() { close(s.stop) })
		s.closeErr = s.raw.Close()
		<-s.done
	})
	return s.closeErr
}

func (s *productionTargetSession) flushEvents(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ack := make(chan struct{})
	select {
	case s.flush <- ack:
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-ack:
		return nil
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *productionTargetSession) forwardEvents() {
	defer close(s.events)
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case ack := <-s.flush:
			if !s.drainRawEvents() {
				return
			}
			close(ack)
		case <-s.raw.Done():
			return
		case event, ok := <-s.raw.Events():
			if !ok {
				return
			}
			if !s.forwardEvent(event) {
				return
			}
		}
	}
}

func (s *productionTargetSession) drainRawEvents() bool {
	for {
		select {
		case <-s.stop:
			return false
		case event, ok := <-s.raw.Events():
			if !ok {
				return false
			}
			if !s.forwardEvent(event) {
				return false
			}
		default:
			return true
		}
	}
}

func (s *productionTargetSession) forwardEvent(event webmcp.BrowserEvent) bool {
	event.BrowserID = s.target.BrowserID
	event.TargetID = s.target.ID
	if event.Generation == 0 {
		event.Generation = s.target.Generation
	}
	for index := range event.Tools {
		event.Tools[index].BrowserID = s.target.BrowserID
		event.Tools[index].TargetID = s.target.ID
		if event.Tools[index].Generation == 0 {
			event.Tools[index].Generation = event.Generation
		}
	}
	select {
	case s.events <- event:
		return true
	case <-s.stop:
		return false
	}
}

func productionNeutralTarget(browserID webmcp.BrowserID, target discovery.Target) webmcp.Target {
	return webmcp.Target{
		BrowserID:             browserID,
		ID:                    webmcp.TargetID(target.ID),
		Type:                  target.Type,
		Title:                 target.Title,
		URL:                   productionSafePageURL(target.URL),
		Origin:                productionSafeOrigin(target.Origin),
		ContinuityMarker:      target.ContinuityMarker,
		Generation:            target.Generation,
		Eligible:              target.Eligible,
		EligibilityReason:     target.EligibilityReason,
		WebMCPDomainSupported: target.WebMCPDomainSupported,
		PageToolsReady:        target.PageToolsReady,
		PageToolsKnown:        target.PageToolsKnown,
		PageToolsEvidence:     target.PageToolsEvidence,
	}
}

func productionDiscoverySource(source discovery.Source) webmcp.DiscoverySource {
	switch source {
	case discovery.SourceExplicitCDPHTTP, discovery.SourceExplicitBrowserWS:
		return webmcp.DiscoverySourceExplicit
	case discovery.SourceDevToolsActivePort:
		return webmcp.DiscoverySourceActivePort
	case discovery.SourceProcess:
		return webmcp.DiscoverySourceProcess
	case discovery.SourceConfigured:
		fallthrough
	default:
		return webmcp.DiscoverySourceConfigured
	}
}

func productionDiscoveryError(err error) error {
	if err == nil {
		return nil
	}
	var discoveryErr *discovery.DiscoveryError
	if !errors.As(err, &discoveryErr) || discoveryErr == nil {
		return err
	}
	code := webmcp.ErrorCode(discoveryErr.Code)
	if !webmcp.IsKnownErrorCode(code) {
		code = webmcp.ErrorBrowserProtocol
	}
	return &webmcp.ClassifiedError{
		Code:      code,
		Message:   discoveryErr.Message,
		Retryable: discoveryErr.Retryable,
		Details:   discoveryErr.Details,
		Cause:     discoveryErr,
	}
}

func isUnsupportedWebMCPError(err error) bool {
	var classified *webmcp.ClassifiedError
	return errors.As(err, &classified) && classified != nil && classified.Code == webmcp.ErrorUnsupportedWebMCP
}

func productionOpaqueID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func productionHTTPTransportURL(raw string) string {
	return productionTransportURL(raw, "http", "https")
}

func productionWSTransportURL(raw string) string {
	return productionTransportURL(raw, "ws", "wss")
}

func productionTransportURL(raw string, schemes ...string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" {
		return strings.TrimSpace(raw)
	}
	for _, scheme := range schemes {
		if strings.EqualFold(parsed.Scheme, scheme) {
			parsed.RawQuery = ""
			parsed.Fragment = ""
			return parsed.String()
		}
	}
	return strings.TrimSpace(raw)
}

func productionSafePageURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func productionSafeOrigin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}

func productionEndpointFromActivePort(record discovery.ActivePortRecord) (discovery.Endpoint, error) {
	if record.Port < 1 || record.Port > 65535 {
		return discovery.Endpoint{}, errors.New("active-port port is invalid")
	}
	path := strings.TrimSpace(record.BrowserWebSocketPath)
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return discovery.Endpoint{}, errors.New("active-port websocket path is invalid")
	}
	if parsed, err := url.Parse(path); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return discovery.Endpoint{
			CDPURL:            "http://" + parsed.Host + "/json/version",
			BrowserWSEndpoint: parsed.String(),
		}, nil
	}
	if !strings.HasPrefix(path, "/") {
		return discovery.Endpoint{}, errors.New("active-port websocket path is invalid")
	}
	return discovery.Endpoint{
		CDPURL:            fmt.Sprintf("http://127.0.0.1:%d/json/version", record.Port),
		BrowserWSEndpoint: fmt.Sprintf("ws://127.0.0.1:%d%s", record.Port, path),
	}, nil
}

type productionActivePortReader struct {
	delegate discovery.ActivePortReader
	owner    *productionWebMCPComposition
}

func (r *productionActivePortReader) Read(ctx context.Context, userDataDir string) (discovery.ActivePortRecord, error) {
	if r == nil || r.delegate == nil {
		return discovery.ActivePortRecord{}, errors.New("active-port reader is unavailable")
	}
	record, err := r.delegate.Read(ctx, userDataDir)
	if err == nil && r.owner != nil {
		if endpoint, endpointErr := productionEndpointFromActivePort(record); endpointErr == nil {
			r.owner.rememberEndpointHint(endpoint)
		}
	}
	return record, err
}

type productionProcessEnumerator struct {
	delegate discovery.ProcessEnumerator
	owner    *productionWebMCPComposition
}

func (e *productionProcessEnumerator) List(ctx context.Context) ([]discovery.ProcessInfo, error) {
	if e == nil || e.delegate == nil {
		return nil, errors.New("process enumerator is unavailable")
	}
	infos, err := e.delegate.List(ctx)
	if err == nil && e.owner != nil {
		for _, info := range infos {
			if info.Endpoint.CDPURL != "" || info.Endpoint.BrowserWSEndpoint != "" {
				e.owner.rememberEndpointHint(info.Endpoint)
			}
		}
	}
	return infos, err
}

type productionHTTPClient struct {
	delegate discovery.HTTPClient
	owner    *productionWebMCPComposition
}

func (c *productionHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if c == nil || c.delegate == nil {
		return nil, errors.New("HTTP client is unavailable")
	}
	response, err := c.delegate.Do(request)
	if err != nil || response == nil || response.Body == nil || c.owner == nil || request == nil {
		return response, err
	}
	if !strings.HasSuffix(strings.TrimRight(request.URL.Path, "/"), "/json/version") {
		return response, err
	}
	response.Body = &productionVersionBody{
		ReadCloser: response.Body,
		requestURL: request.URL,
		owner:      c.owner,
	}
	return response, nil
}

type productionVersionBody struct {
	io.ReadCloser
	requestURL *url.URL
	owner      *productionWebMCPComposition
	data       bytes.Buffer
	once       sync.Once
}

func (b *productionVersionBody) Read(data []byte) (int, error) {
	count, err := b.ReadCloser.Read(data)
	if count > 0 {
		_, _ = b.data.Write(data[:count])
	}
	return count, err
}

func (b *productionVersionBody) Close() error {
	b.once.Do(func() {
		if b.owner != nil && b.requestURL != nil {
			b.owner.rememberVersionEndpoint(b.requestURL, b.data.Bytes())
		}
	})
	return b.ReadCloser.Close()
}

func (p *productionWebMCPComposition) rememberVersionEndpoint(requestURL *url.URL, data []byte) {
	if p == nil || requestURL == nil {
		return
	}
	var version discovery.BrowserVersion
	if json.Unmarshal(data, &version) != nil || strings.TrimSpace(version.WebSocketDebuggerURL) == "" {
		return
	}
	parsed, err := url.Parse(strings.TrimSpace(version.WebSocketDebuggerURL))
	if err != nil || parsed.Host == "" || parsed.Path == "" {
		return
	}
	identity := discovery.BrowserIdentity{
		Scheme: strings.ToLower(parsed.Scheme),
		Host:   strings.ToLower(parsed.Hostname()),
		Port:   parsed.Port(),
		Path:   parsed.EscapedPath(),
	}
	if identity.Scheme != "ws" && identity.Scheme != "wss" {
		return
	}
	publicID := ""
	if p.idMapper != nil {
		publicID = p.idMapper.BrowserID(identity)
	}
	if !productionOpaqueID(publicID) {
		publicID = discovery.HashIDMapper{}.BrowserID(identity)
	}
	requestCopy := *requestURL
	requestCopy.RawQuery = ""
	requestCopy.Fragment = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	p.rememberEndpoint(publicID, discovery.Endpoint{
		CDPURL:            requestCopy.String(),
		BrowserWSEndpoint: parsed.String(),
	})
}

type productionCLISelectionStore struct{ store WebMCPSelectionStore }

func (s productionCLISelectionStore) Load(ctx context.Context) (discovery.PersistedSelection, error) {
	if err := contextErrorForProduction(ctx); err != nil {
		return discovery.PersistedSelection{}, err
	}
	if s.store == nil {
		return discovery.PersistedSelection{}, discovery.ErrSelectionNotFound
	}
	selection, err := s.store.Load()
	if err != nil {
		return discovery.PersistedSelection{}, err
	}
	if selection.BrowserID == "" && selection.TargetID == "" {
		return discovery.PersistedSelection{}, discovery.ErrSelectionNotFound
	}
	return discovery.PersistedSelection{
		Version:          uint(selection.Version),
		EndpointID:       selection.EndpointID,
		BrowserID:        selection.BrowserID,
		TargetID:         selection.TargetID,
		Origin:           selection.Origin,
		ContinuityMarker: selection.ContinuityMarker,
		Generation:       selection.Generation,
		SelectedAt:       selection.SelectedAt,
	}, nil
}

func (s productionCLISelectionStore) Save(ctx context.Context, record discovery.PersistedSelection) error {
	if err := contextErrorForProduction(ctx); err != nil {
		return err
	}
	if s.store == nil {
		return errors.New("WebMCP selection store is unavailable")
	}
	return s.store.Save(WebMCPSelection{
		Version:          int(record.Version),
		EndpointID:       record.EndpointID,
		BrowserID:        record.BrowserID,
		TargetID:         record.TargetID,
		Origin:           record.Origin,
		ContinuityMarker: record.ContinuityMarker,
		Generation:       record.Generation,
		SelectedAt:       record.SelectedAt,
	})
}

func productionSelectionStore(value any) any {
	if value == nil {
		return nil
	}
	if store, ok := value.(WebMCPSelectionStore); ok {
		return productionCLISelectionStore{store: store}
	}
	return value
}

func contextErrorForProduction(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

var (
	_ webmcp.BrowserDiscoverer = (*productionWebMCPComposition)(nil)
	_ webmcp.BrowserRuntime    = (*productionWebMCPComposition)(nil)
	_ webmcp.BrowserHandle     = (*productionWebMCPHandle)(nil)
	_ webmcp.TargetSession     = (*productionTargetSession)(nil)
	_ webmcp.DevToolsCatalog   = (*productionWebMCPCatalog)(nil)
)
