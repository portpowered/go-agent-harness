// Package discovery implements the browser-neutral WebMCP endpoint
// discovery boundary.
//
// The package deliberately stops at normalized browser/target records and a
// selection state boundary. It does not import a browser protocol
// implementation and does not expose transport URLs in discovery results,
// errors, selections, or semantic events. Browser runtimes can be added behind
// the endpoint, target, attach, and activation seams without changing this
// contract.
package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultProbeTimeout = 5 * time.Second

var (
	protocolVersionPattern = regexp.MustCompile(`^([0-9]+)\.([0-9]+)$`)
	publicIDPattern        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	incarnationIDPattern   = regexp.MustCompile(`^incarnation-[0-9a-f]{24}$`)
)

// Options supplies the side-effect seams for Service. Nil seams use safe
// standard-library defaults where possible. In particular, process discovery
// remains disabled unless the caller both enables it in ConnectionInputs and
// supplies a ProcessEnumerator.
type Options struct {
	HTTPClient        HTTPClient
	ActivePortReader  ActivePortReader
	ProcessEnumerator ProcessEnumerator
	WebSocketProbe    WebSocketProbe
	TargetLister      TargetLister
	TargetProbe       TargetCapabilityProbe
	CapabilityProbe   TargetCapabilityProbe
	TargetAttacher    TargetAttacher
	TargetRuntime     TargetAttacher
	Activator         TargetActivator
	TargetActivator   TargetActivator
	OriginPolicy      OriginPolicy
	AllowedOrigins    []string
	DeniedOrigins     []string
	IDMapper          IDMapper
	TargetIDMapper    TargetIDMapper
	EventSink         EventSink
	Clock             Clock
	MaxVersionBytes   int64
	MaxTargetBytes    int64
	ProbeTimeout      time.Duration
	// SelectionStore is the preferred injected persistence seam. Persistence,
	// SelectionPersistence, and Store are compatibility aliases for callers
	// that name the same Lane B port differently; the first non-nil value is
	// used. The value is intentionally accepted as any so byte-oriented and
	// typed fake stores can share this neutral package.
	SelectionStore       any
	Persistence          any
	SelectionPersistence any
	Store                any
	// PersistenceEnabled can explicitly turn persistence on or off. When it is
	// nil, supplying a store enables persistence; PersistSelection is a
	// convenient explicit opt-in for callers using a store alias.
	PersistenceEnabled *bool
	PersistSelection   bool
	DisablePersistence bool
}

// Service performs deterministic, serialized browser endpoint discovery.
// Serialized calls also keep semantic event sequence numbers monotonic when a
// service instance is reused by more than one command adapter.
type Service struct {
	mu                 sync.Mutex
	httpClient         HTTPClient
	activePortReader   ActivePortReader
	processEnumerator  ProcessEnumerator
	webSocketProbe     WebSocketProbe
	targetLister       TargetLister
	targetProbe        TargetCapabilityProbe
	targetAttacher     TargetAttacher
	activator          TargetActivator
	originPolicy       OriginPolicy
	idMapper           IDMapper
	targetIDMapper     TargetIDMapper
	eventSink          EventSink
	clock              Clock
	maxVersionBytes    int64
	maxTargetBytes     int64
	probeTimeout       time.Duration
	eventSequence      uint64
	endpoints          map[string]targetEndpoint
	targets            map[string]map[string]targetState
	browsers           map[string]BrowserCandidate
	selection          *Selection
	disconnected       map[string]browserDisconnectState
	lifecycleSeen      map[string]struct{}
	retiredBrowsers    map[string]struct{}
	pendingReleases    []*TargetHandle
	selectionStore     selectionStoreAdapter
	persistenceError   error
	persistenceEnabled bool
}

// New constructs a discovery service with standard-library defaults for the
// HTTP client, active-port reader, websocket validation, and opaque IDs.
func New(options Options) *Service {
	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	activePortReader := options.ActivePortReader
	if activePortReader == nil {
		activePortReader = FileActivePortReader{}
	}
	webSocketProbe := options.WebSocketProbe
	if webSocketProbe == nil {
		webSocketProbe = validatingWebSocketProbe{}
	}
	targetProbe := options.TargetProbe
	if targetProbe == nil {
		targetProbe = options.CapabilityProbe
	}
	targetAttacher := options.TargetAttacher
	if targetAttacher == nil {
		targetAttacher = options.TargetRuntime
	}
	activator := options.Activator
	if activator == nil {
		activator = options.TargetActivator
	}
	clock := options.Clock
	if clock == nil {
		clock = wallClock{}
	}
	idMapper := options.IDMapper
	if idMapper == nil {
		idMapper = HashIDMapper{}
	}
	targetIDMapper := options.TargetIDMapper
	if targetIDMapper == nil {
		targetIDMapper = HashTargetIDMapper{}
	}
	eventSink := options.EventSink
	if eventSink == nil {
		eventSink = EventFunc(nil)
	}
	maxVersionBytes := options.MaxVersionBytes
	if maxVersionBytes <= 0 {
		maxVersionBytes = DefaultMaxVersionBytes
	}
	maxTargetBytes := options.MaxTargetBytes
	if maxTargetBytes <= 0 {
		maxTargetBytes = DefaultMaxTargetBytes
	}
	probeTimeout := options.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = defaultProbeTimeout
	}
	configuredStore := firstConfiguredStore(options)
	selectionStore, persistenceError := adaptSelectionStore(configuredStore)
	persistenceEnabled := configuredStore != nil
	if options.PersistenceEnabled != nil {
		persistenceEnabled = *options.PersistenceEnabled
	}
	if options.PersistSelection {
		persistenceEnabled = true
	}
	if options.DisablePersistence {
		persistenceEnabled = false
	}
	return &Service{
		httpClient:         client,
		activePortReader:   activePortReader,
		processEnumerator:  options.ProcessEnumerator,
		webSocketProbe:     webSocketProbe,
		targetLister:       options.TargetLister,
		targetProbe:        targetProbe,
		targetAttacher:     targetAttacher,
		activator:          activator,
		originPolicy:       newOriginPolicy(options.OriginPolicy, options.AllowedOrigins, options.DeniedOrigins),
		idMapper:           idMapper,
		targetIDMapper:     targetIDMapper,
		eventSink:          eventSink,
		clock:              clock,
		maxVersionBytes:    maxVersionBytes,
		maxTargetBytes:     maxTargetBytes,
		probeTimeout:       probeTimeout,
		endpoints:          make(map[string]targetEndpoint),
		targets:            make(map[string]map[string]targetState),
		browsers:           make(map[string]BrowserCandidate),
		lifecycleSeen:      make(map[string]struct{}),
		retiredBrowsers:    make(map[string]struct{}),
		disconnected:       make(map[string]browserDisconnectState),
		selectionStore:     selectionStore,
		persistenceError:   persistenceError,
		persistenceEnabled: persistenceEnabled,
	}
}

// NewService is a descriptive constructor alias.
func NewService(options Options) *Service { return New(options) }

// Browser returns the normalized browser candidate currently known for an
// exact public browser ID. It is a read-only lookup for model-facing adapters;
// the returned value contains no endpoint credentials or transport URLs.
func (s *Service) Browser(browserID string) (BrowserCandidate, bool) {
	if s == nil {
		return BrowserCandidate{}, false
	}
	s.mu.Lock()
	defer s.unlockDiscovery()
	candidate, ok := s.browsers[strings.TrimSpace(browserID)]
	return candidate, ok
}

// HashIDMapper is the default deterministic opaque browser ID implementation.
// It hashes only normalized endpoint identity and emits no URL-shaped text.
type HashIDMapper struct{}

// BrowserID implements IDMapper.
func (HashIDMapper) BrowserID(identity BrowserIdentity) string {
	key := strings.Join([]string{
		strings.ToLower(identity.Scheme),
		strings.ToLower(identity.Host),
		identity.Port,
		identity.Path,
	}, "\x00")
	digest := sha256.Sum256([]byte(key))
	return "browser-" + hex.EncodeToString(digest[:12])
}

// Discover probes sources in the frozen order and returns the first valid
// browser candidate. A source failure permits the next source to be tried;
// a successful source stops discovery immediately. The returned error is a
// safe classified DiscoveryError.
func (s *Service) Discover(ctx context.Context, inputs ConnectionInputs) (BrowserCandidate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.unlockDiscovery()

	s.emitDiscoveryStarted()

	scan := &discoveryScan{}
	if candidate, done, err := s.discoverExplicitLocked(ctx, inputs, scan); done {
		return candidate, err
	}

	// Process enumeration is deliberately deferred until every higher-priority
	// source has failed. This makes the call boundary observable and avoids
	// scanning processes when a configured endpoint already works.
	if scan.best == nil || inputs.AllowProcessScan {
		if inputs.AllowProcessScan && s.processEnumerator != nil {
			if candidate, done, err := s.discoverProcessesLocked(ctx, inputs, scan); done {
				return candidate, err
			}
		}
	}

	best := scan.best
	if best == nil {
		best = newEndpointNotFound(EndpointKindCDPHTTP, Source(SourceConfigured))
	}
	if !scan.attempted && best.Code == "" {
		best = newEndpointNotFound(EndpointKindCDPHTTP, SourceConfigured)
	}
	s.emit(EventDiscoveryCompleted, "", map[string]any{
		"candidate_count": 0,
		"success":         false,
		"code":            string(best.Code),
	})
	return BrowserCandidate{}, best
}

// discoveryScan accumulates the non-terminal outcome of one discovery call:
// the most useful failure observed so far, whether any endpoint was tried,
// and (for DiscoverAll) the candidates of the current source tier.
type discoveryScan struct {
	best       *DiscoveryError
	attempted  bool
	candidates []BrowserCandidate
}

// discoverExplicitLocked tries the explicit, active-port, and configured
// sources in order. done reports that Discover must return the given result.
func (s *Service) discoverExplicitLocked(ctx context.Context, inputs ConnectionInputs, scan *discoveryScan) (BrowserCandidate, bool, error) {
	for _, attempt := range s.explicitAttempts(inputs) {
		if err := ctx.Err(); err != nil {
			failure := newEndpointUnreachable(attempt.kind, addressClassFromEndpointKind(attempt.kind), "discovery", err)
			scan.best = preferFailure(scan.best, failure)
			break
		}
		candidate, failure := s.tryAttempt(ctx, attempt, inputs.AllowRemoteCDP)
		if candidate.ID != "" {
			return s.completeDiscoverySuccess(candidate)
		}
		if failure != nil && failure.Code == CodeBrowserDisconnected {
			return s.completeDiscoveryDisconnected(failure, "discovery")
		}
		scan.attempted = scan.attempted || failure != nil
		scan.best = preferFailure(scan.best, failure)
	}
	return BrowserCandidate{}, false, nil
}

// discoverProcessesLocked enumerates debuggable browser processes after the
// higher-priority sources failed. done reports a terminal Discover result.
func (s *Service) discoverProcessesLocked(ctx context.Context, inputs ConnectionInputs, scan *discoveryScan) (BrowserCandidate, bool, error) {
	infos, err := s.processEnumerator.List(ctx)
	if err != nil {
		if isBrowserDisconnected(err) {
			return s.completeDiscoveryDisconnected(newBrowserDisconnectedFromError(err, "", "", "process"), "process")
		}
		scan.best = preferFailure(scan.best, newEndpointUnreachable(EndpointKindProcess, "non_loopback", "process", err))
		return BrowserCandidate{}, false, nil
	}
	for _, info := range infos {
		if candidate, done, err := s.discoverProcessLocked(ctx, info, inputs, scan); done {
			return candidate, done, err
		}
	}
	return BrowserCandidate{}, false, nil
}

func (s *Service) discoverProcessLocked(ctx context.Context, info ProcessInfo, inputs ConnectionInputs, scan *discoveryScan) (BrowserCandidate, bool, error) {
	if !info.DebuggingEnabled {
		return BrowserCandidate{}, false, nil
	}
	endpoint, disconnected, skip := s.resolveProcessEndpoint(ctx, info, scan)
	if disconnected != nil {
		return s.completeDiscoveryDisconnected(disconnected, "active_port")
	}
	if skip {
		return BrowserCandidate{}, false, nil
	}
	if strings.TrimSpace(endpoint.CDPURL) == "" && strings.TrimSpace(endpoint.BrowserWSEndpoint) == "" {
		return BrowserCandidate{}, false, nil
	}
	scan.attempted = true
	candidate, failure := s.tryAttempt(ctx, endpointAttempt{
		source:  SourceProcess,
		kind:    EndpointKindProcess,
		resolve: func(context.Context) (Endpoint, error) { return endpoint, nil },
	}, inputs.AllowRemoteCDP)
	if candidate.ID != "" {
		return s.completeDiscoverySuccess(candidate)
	}
	if failure != nil && failure.Code == CodeBrowserDisconnected {
		return s.completeDiscoveryDisconnected(failure, "process")
	}
	scan.best = preferFailure(scan.best, failure)
	return BrowserCandidate{}, false, nil
}

// resolveProcessEndpoint returns the process endpoint, reading the profile's
// active-port record when the process did not advertise one. A disconnected
// failure is terminal; skip reports a recorded non-terminal failure.
func (s *Service) resolveProcessEndpoint(ctx context.Context, info ProcessInfo, scan *discoveryScan) (Endpoint, *DiscoveryError, bool) {
	endpoint := info.Endpoint
	if endpoint.CDPURL != "" || endpoint.BrowserWSEndpoint != "" || info.UserDataDir == "" {
		return endpoint, nil, false
	}
	active, readErr := s.activePortReader.Read(ctx, info.UserDataDir)
	if readErr == nil {
		endpoint, readErr = endpointFromActivePort(active)
	}
	if readErr == nil {
		return endpoint, nil, false
	}
	failure := classifyActivePortError(readErr, SourceProcess)
	if failure.Code == CodeBrowserDisconnected {
		return Endpoint{}, failure, true
	}
	scan.best = preferFailure(scan.best, failure)
	return Endpoint{}, nil, true
}

func (s *Service) emitDiscoveryStarted() {
	s.emit(EventDiscoveryStarted, "", map[string]any{
		"source_plan": []string{
			string(SourceExplicitCDPHTTP),
			string(SourceExplicitBrowserWS),
			string(SourceDevToolsActivePort),
			string(SourceConfigured),
			string(SourceProcess),
		},
	})
}

func (s *Service) emitDiscoverySucceeded(candidate BrowserCandidate) {
	s.emit(EventDiscoveryCompleted, candidate.ID, map[string]any{
		"candidate_count": 1,
		"source":          string(candidate.Source),
		"success":         true,
	})
}

func (s *Service) completeDiscoverySuccess(candidate BrowserCandidate) (BrowserCandidate, bool, error) {
	s.emitDiscoverySucceeded(candidate)
	return candidate, true, nil
}

func (s *Service) completeDiscoveryDisconnected(failure *DiscoveryError, phase string) (BrowserCandidate, bool, error) {
	return BrowserCandidate{}, true, s.failDiscoveryDisconnectedLocked(failure, phase)
}

// failDiscoveryDisconnectedLocked records a terminal disconnected discovery
// failure, emits the failed completion event, and returns the failure.
func (s *Service) failDiscoveryDisconnectedLocked(failure *DiscoveryError, phase string) error {
	s.noteBrowserDisconnectedFailureLocked(failure, "", "", phase)
	s.emit(EventDiscoveryCompleted, detailString(failure.Details, "browser_id"), map[string]any{
		"candidate_count": 0,
		"success":         false,
		"code":            string(failure.Code),
	})
	return failure
}

type endpointAttempt struct {
	source  Source
	kind    EndpointKind
	resolve func(context.Context) (Endpoint, error)
}

func (s *Service) explicitAttempts(inputs ConnectionInputs) []endpointAttempt {
	attempts := make([]endpointAttempt, 0, 3+len(inputs.ConfiguredSources))
	if strings.TrimSpace(inputs.CDPURL) != "" {
		endpoint := Endpoint{CDPURL: inputs.CDPURL}
		attempts = append(attempts, endpointAttempt{
			source:  SourceExplicitCDPHTTP,
			kind:    EndpointKindCDPHTTP,
			resolve: func(context.Context) (Endpoint, error) { return endpoint, nil },
		})
	}
	if strings.TrimSpace(inputs.BrowserWSEndpoint) != "" {
		endpoint := Endpoint{BrowserWSEndpoint: inputs.BrowserWSEndpoint}
		attempts = append(attempts, endpointAttempt{
			source:  SourceExplicitBrowserWS,
			kind:    EndpointKindBrowserWebSocket,
			resolve: func(context.Context) (Endpoint, error) { return endpoint, nil },
		})
	}
	if strings.TrimSpace(inputs.UserDataDir) != "" {
		userDataDir := inputs.UserDataDir
		attempts = append(attempts, endpointAttempt{
			source: SourceDevToolsActivePort,
			kind:   EndpointKindActivePort,
			resolve: func(ctx context.Context) (Endpoint, error) {
				record, err := s.activePortReader.Read(ctx, userDataDir)
				if err != nil {
					return Endpoint{}, err
				}
				return endpointFromActivePort(record)
			},
		})
	}
	for _, configured := range inputs.ConfiguredSources {
		if configured == nil {
			continue
		}
		configured := configured
		attempts = append(attempts, endpointAttempt{
			source: SourceConfigured,
			kind:   EndpointKindConfigured,
			resolve: func(ctx context.Context) (Endpoint, error) {
				return configured.Resolve(ctx)
			},
		})
	}
	return attempts
}

func (s *Service) tryAttempt(ctx context.Context, attempt endpointAttempt, allowRemote bool) (BrowserCandidate, *DiscoveryError) {
	endpoint, err := attempt.resolve(ctx)
	if err != nil {
		if isBrowserDisconnected(err) {
			return BrowserCandidate{}, newBrowserDisconnectedFromError(err, "", "", "resolve")
		}
		if attempt.kind == EndpointKindActivePort {
			return BrowserCandidate{}, classifyActivePortError(err, attempt.source)
		}
		return BrowserCandidate{}, classifiedFrom(err, attempt.kind, attempt.source)
	}
	if strings.TrimSpace(endpoint.CDPURL) != "" {
		return s.tryHTTP(ctx, endpoint.CDPURL, attempt.source, attempt.kind, allowRemote)
	}
	if strings.TrimSpace(endpoint.BrowserWSEndpoint) != "" {
		return s.tryWebSocket(ctx, endpoint.BrowserWSEndpoint, attempt.source, attempt.kind, allowRemote)
	}
	return BrowserCandidate{}, newEndpointNotFound(attempt.kind, attempt.source)
}

func (s *Service) tryHTTP(ctx context.Context, rawURL string, source Source, kind EndpointKind, allowRemote bool) (BrowserCandidate, *DiscoveryError) {
	parsed, parseErr := parseHTTPURL(rawURL)
	if parseErr != nil {
		return BrowserCandidate{}, newProtocolInvalidAt("endpoint", "unknown", parseErr.reason, nil)
	}
	loopback := isLoopbackHost(parsed.Hostname())
	if !loopback && !allowRemote {
		return BrowserCandidate{}, newRemoteEndpointDenied(kind)
	}

	requestURL := *parsed
	requestURL.RawQuery = ""
	requestURL.Fragment = ""
	requestURL.Path = versionPath(requestURL.Path)
	probeCtx, cancel := context.WithTimeout(ctx, s.probeTimeout)
	defer cancel()
	request, requestErr := http.NewRequestWithContext(probeCtx, http.MethodGet, requestURL.String(), nil)
	if requestErr != nil {
		return BrowserCandidate{}, newProtocolInvalidAt("endpoint", "unknown", "request_invalid", requestErr)
	}
	response, responseErr := s.httpClient.Do(request)
	if responseErr != nil {
		if isBrowserDisconnected(responseErr) {
			return BrowserCandidate{}, newBrowserDisconnectedFromError(responseErr, s.browserIDForEndpoint(Endpoint{CDPURL: rawURL}), "", "version")
		}
		return BrowserCandidate{}, newEndpointUnreachable(kind, addressClass(loopback), "version", responseErr)
	}
	if response == nil {
		return BrowserCandidate{}, newEndpointUnreachable(kind, addressClass(loopback), "version", errors.New("nil response"))
	}
	if response.Body != nil {
		defer closeAfterRead(response.Body)
	}
	if response.StatusCode == http.StatusNotFound {
		return BrowserCandidate{}, newEndpointNotFound(kind, source)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return BrowserCandidate{}, newProtocolInvalidAt("version", "http_"+strconv.Itoa(response.StatusCode), "http_status", nil)
	}
	if response.Body == nil {
		return BrowserCandidate{}, newProtocolInvalidAt("version", "unknown", "missing_body", nil)
	}
	body, bodyErr := io.ReadAll(io.LimitReader(response.Body, s.maxVersionBytes+1))
	if bodyErr != nil {
		if isBrowserDisconnected(bodyErr) {
			return BrowserCandidate{}, newBrowserDisconnectedFromError(bodyErr, s.browserIDForEndpoint(Endpoint{CDPURL: rawURL}), "", "version")
		}
		return BrowserCandidate{}, newEndpointUnreachable(kind, addressClass(loopback), "version", bodyErr)
	}
	if int64(len(body)) > s.maxVersionBytes {
		return BrowserCandidate{}, newProtocolInvalidAt("version", "unknown", "response_too_large", nil)
	}
	var version BrowserVersion
	if err := json.Unmarshal(body, &version); err != nil {
		return BrowserCandidate{}, newProtocolInvalidAt("version", "unknown", "malformed_json", nil)
	}
	candidate, failure := s.candidateFromVersion(version, source, kind, parsed, allowRemote, true)
	if candidate.ID != "" {
		identity, _ := browserIdentityFromVersion(version, nil)
		s.rememberEndpoint(candidate.ID, targetEndpoint{
			httpURL:     targetListBaseURL(parsed),
			addressKey:  browserAddressKey(parsed.Scheme, parsed.Hostname(), parsed.Port()),
			identityKey: browserIdentityKey(identity),
		})
	}
	return candidate, failure
}

func (s *Service) tryWebSocket(ctx context.Context, rawURL string, source Source, kind EndpointKind, allowRemote bool) (BrowserCandidate, *DiscoveryError) {
	normalized, err := parseBrowserWebSocketURL(rawURL)
	if err != nil {
		return BrowserCandidate{}, newProtocolInvalidAt("endpoint", "unknown", err.reason, nil)
	}
	if !normalized.loopback && !allowRemote {
		return BrowserCandidate{}, newRemoteEndpointDenied(kind)
	}
	probeCtx, cancel := context.WithTimeout(ctx, s.probeTimeout)
	defer cancel()
	version, probeErr := s.webSocketProbe.Probe(probeCtx, normalized.url.String())
	if probeErr != nil {
		if isBrowserDisconnected(probeErr) {
			return BrowserCandidate{}, newBrowserDisconnectedFromError(probeErr, s.browserIDForEndpoint(Endpoint{BrowserWSEndpoint: rawURL}), "", "connect")
		}
		return BrowserCandidate{}, newEndpointUnreachable(kind, addressClass(normalized.loopback), "connect", probeErr)
	}
	if strings.TrimSpace(version.WebSocketDebuggerURL) == "" {
		version.WebSocketDebuggerURL = normalized.url.String()
	}
	candidate, failure := s.candidateFromVersion(version, source, kind, normalized.url, allowRemote, false)
	if candidate.ID != "" {
		identity, _ := browserIdentityFromVersion(version, normalized.url)
		s.rememberEndpoint(candidate.ID, targetEndpoint{
			browserWS:   normalized.url.String(),
			addressKey:  browserAddressKey(normalized.url.Scheme, normalized.url.Hostname(), normalized.url.Port()),
			identityKey: browserIdentityKey(identity),
		})
	}
	return candidate, failure
}

func (s *Service) candidateFromVersion(version BrowserVersion, source Source, kind EndpointKind, fallbackURL *url.URL, allowRemote, requireMetadata bool) (BrowserCandidate, *DiscoveryError) {
	product := safeProduct(version.Browser)
	if product == "" {
		if requireMetadata {
			return BrowserCandidate{}, newProtocolInvalidAt("version", "unknown", "missing_browser_product", nil)
		}
		product = "unknown"
	}
	protocol, protocolErr := safeProtocol(version.ProtocolVersion, requireMetadata)
	if protocolErr != "" {
		return BrowserCandidate{}, newProtocolInvalidAt("version", version.ProtocolVersion, protocolErr, nil)
	}
	if protocol == "" {
		protocol = "unknown"
	}

	wsRaw := strings.TrimSpace(version.WebSocketDebuggerURL)
	if wsRaw == "" && fallbackURL != nil {
		wsRaw = fallbackURL.String()
	}
	normalized, err := parseBrowserWebSocketURL(wsRaw)
	if err != nil {
		return BrowserCandidate{}, newProtocolInvalidAt("version", protocol, err.reason, nil)
	}
	if !normalized.loopback && !allowRemote {
		return BrowserCandidate{}, newRemoteEndpointDenied(kind)
	}
	identity := BrowserIdentity{
		Scheme:            normalized.url.Scheme,
		Host:              normalized.url.Hostname(),
		Port:              normalized.url.Port(),
		Path:              normalized.url.EscapedPath(),
		BrowserInstanceID: browserInstanceMetadata(version),
	}
	publicID := browserIDForIdentity(s.idMapper, identity)
	instanceID := browserIdentityClaim(identity)
	oldID := s.replacedBrowserIDLocked(identity, publicID)
	if oldID != "" && oldID == publicID {
		publicID = browserReplacementID(publicID, instanceID)
	}
	candidate := BrowserCandidate{
		ID:                publicID,
		Source:            source,
		Product:           product,
		Protocol:          protocol,
		BrowserInstanceID: instanceID,
		Loopback:          normalized.loopback,
	}
	if oldID != "" {
		s.retireReplacedBrowserLocked(oldID)
	}
	s.browsers[candidate.ID] = candidate
	s.emit(EventEndpointVersion, candidate.ID, map[string]any{
		"product":  product,
		"protocol": protocol,
		"source":   string(source),
	})
	return candidate, nil
}

// unlockDiscovery releases any target handles retired while a discovery pass
// held Service.mu. Detach callbacks are external code and must not run while
// the service lock is held.
func (s *Service) unlockDiscovery() {
	if s == nil {
		return
	}
	releases := append([]*TargetHandle(nil), s.pendingReleases...)
	s.pendingReleases = nil
	s.mu.Unlock()
	for _, handle := range releases {
		if handle != nil {
			_ = handle.Close()
		}
	}
}

func browserInstanceMetadata(version BrowserVersion) string {
	for _, value := range []string{version.BrowserInstanceID, version.IncarnationID} {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 256 || hasControl(value) || strings.ContainsAny(value, "/?#") || strings.Contains(value, "://") {
			continue
		}
		return value
	}
	return ""
}

func browserIdentityFromVersion(version BrowserVersion, fallback *url.URL) (BrowserIdentity, *parseURLFailure) {
	wsRaw := strings.TrimSpace(version.WebSocketDebuggerURL)
	if wsRaw == "" && fallback != nil {
		wsRaw = fallback.String()
	}
	normalized, failure := parseBrowserWebSocketURL(wsRaw)
	if failure != nil {
		return BrowserIdentity{}, failure
	}
	return BrowserIdentity{
		Scheme:            normalized.url.Scheme,
		Host:              normalized.url.Hostname(),
		Port:              normalized.url.Port(),
		Path:              normalized.url.EscapedPath(),
		BrowserInstanceID: browserInstanceMetadata(version),
	}, nil
}

func browserIdentityClaim(identity BrowserIdentity) string {
	marker := identity.BrowserInstanceID
	if marker == "" {
		// Chrome's browser websocket path is the protocol-level incarnation
		// marker when no adapter-specific instance ID is available.
		marker = identity.Path
	}
	digest := sha256.Sum256([]byte(marker))
	return "incarnation-" + hex.EncodeToString(digest[:12])
}

// BrowserIDForIdentity derives the same opaque browser ID used by discovery.
// It is provided for composition adapters that need to associate a transport
// response with a normalized candidate without exposing the endpoint.
func BrowserIDForIdentity(mapper IDMapper, identity BrowserIdentity) string {
	return browserIDForIdentity(mapper, identity)
}

func browserIDForIdentity(mapper IDMapper, identity BrowserIdentity) string {
	if mapper == nil {
		mapper = HashIDMapper{}
	}
	// BrowserInstanceID is a continuity claim, not part of the public
	// selector. It may be supplied by an adapter that has process-local
	// bookkeeping, and including it here would make a reference emitted by
	// `tabs` unusable by the next process. Keep the stable endpoint identity
	// (including the browser websocket path when available) as the locator;
	// persisted reconnects compare the separate BrowserInstanceID claim.
	stableIdentity := identity
	stableIdentity.BrowserInstanceID = ""
	return normalizePublicID(mapper.BrowserID(stableIdentity), stableIdentity)
}

// BrowserIDForVersion derives a candidate ID from a protocol version
// response. The boolean is false when the response has no valid browser
// websocket identity.
func BrowserIDForVersion(mapper IDMapper, version BrowserVersion) (string, bool) {
	identity, failure := browserIdentityFromVersion(version, nil)
	if failure != nil {
		return "", false
	}
	return browserIDForIdentity(mapper, identity), true
}

func normalizedBrowserInstanceID(browser BrowserCandidate) string {
	value := strings.TrimSpace(browser.BrowserInstanceID)
	if value == "" {
		return ""
	}
	if incarnationIDPattern.MatchString(value) {
		return value
	}
	if len(value) > 256 || hasControl(value) || strings.ContainsAny(value, "/?#") || strings.Contains(value, "://") {
		return ""
	}
	return browserIdentityClaim(BrowserIdentity{BrowserInstanceID: value})
}

func browserReplacementID(publicID, instanceID string) string {
	digest := sha256.Sum256([]byte("browser-replacement\x00" + publicID + "\x00" + instanceID))
	return "browser-" + hex.EncodeToString(digest[:12])
}

func (s *Service) replacedBrowserIDLocked(identity BrowserIdentity, publicID string) string {
	address := browserAddressKey(identity.Scheme, identity.Host, identity.Port)
	identityKey := browserIdentityKey(identity)
	if endpoint, ok := s.endpoints[publicID]; ok && endpoint.identityKey != "" && endpoint.identityKey != identityKey {
		return publicID
	}
	ids := make([]string, 0, len(s.endpoints))
	for browserID, endpoint := range s.endpoints {
		if browserID == publicID || endpointAddressKey(endpoint) == address {
			if endpoint.identityKey != "" && endpoint.identityKey != identityKey {
				ids = append(ids, browserID)
			}
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	return ids[0]
}

func (s *Service) retireReplacedBrowserLocked(browserID string) {
	if browserID == "" {
		return
	}
	if s.retiredBrowsers == nil {
		s.retiredBrowsers = make(map[string]struct{})
	}
	s.retiredBrowsers[browserID] = struct{}{}
	delete(s.endpoints, browserID)
	delete(s.browsers, browserID)
	if targetStates := s.targets[browserID]; targetStates != nil {
		for targetID, state := range targetStates {
			state.closed = true
			state.target.Eligible = false
			state.target.EligibilityReason = "browser_replaced"
			targetStates[targetID] = state
		}
	}
	if s.selection == nil || s.selection.BrowserID != browserID {
		return
	}
	selection := *s.selection
	ownership := string(TargetOwnershipExternal)
	if selection.Handle != nil {
		ownership = string(selection.Handle.Ownership())
		s.pendingReleases = append(s.pendingReleases, selection.Handle)
	}
	s.selection = nil
	s.emitTarget(EventTargetDetached, selection.BrowserID, selection.TargetID, selection.Generation, map[string]any{
		"generation":     selection.Generation,
		"reason":         "browser_replaced",
		"ownership_mode": ownership,
	})
}

func safeProduct(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > 128 || strings.ContainsAny(value, "\r\n?#") {
		return "redacted"
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "redacted"
		}
	}
	return value
}

func safeProtocol(value string, required bool) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", "missing_protocol_version"
		}
		return "", ""
	}
	if value == "unknown" && !required {
		return value, ""
	}
	matches := protocolVersionPattern.FindStringSubmatch(value)
	if len(matches) != 3 {
		return value, "unsupported_protocol_version"
	}
	major, err := strconv.Atoi(matches[1])
	if err != nil || major != 1 {
		return value, "unsupported_protocol_version"
	}
	return value, ""
}

func normalizePublicID(value string, identity BrowserIdentity) string {
	value = strings.TrimSpace(value)
	if publicIDPattern.MatchString(value) {
		return value
	}
	return HashIDMapper{}.BrowserID(identity)
}

func boundedLabel(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		value = value[:max]
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "redacted"
		}
	}
	return value
}

func classifyActivePortError(err error, source Source) *DiscoveryError {
	if isBrowserDisconnected(err) {
		return newBrowserDisconnectedFromError(err, "", "", "active_port")
	}
	if errors.Is(err, os.ErrNotExist) {
		return newEndpointNotFound(EndpointKindActivePort, source)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return newEndpointUnreachable(EndpointKindActivePort, "loopback", "active_port", err)
	}
	return newProtocolInvalidAt("active_port", "unknown", "malformed_active_port", nil)
}

func preferFailure(current, next *DiscoveryError) *DiscoveryError {
	if next == nil {
		return current
	}
	if current == nil || failureRank(next.Code) > failureRank(current.Code) {
		return next
	}
	return current
}

func failureRank(code Code) int {
	switch code {
	case CodeBrowserDisconnected:
		return 5
	case CodeRemoteEndpointDenied:
		return 4
	case CodeBrowserProtocolInvalid:
		return 3
	case CodeEndpointUnreachable:
		return 2
	case CodeEndpointNotFound:
		return 1
	default:
		return 0
	}
}

func (s *Service) emit(kind EventType, browserID string, payload map[string]any) {
	s.eventSequence++
	copyPayload := make(map[string]any, len(payload))
	for key, value := range payload {
		copyPayload[key] = value
	}
	s.eventSink.Emit(Event{
		Version:     BrowserEventsVersion,
		Sequence:    s.eventSequence,
		MonotonicMS: s.eventSequence,
		Type:        kind,
		BrowserID:   browserID,
		Payload:     copyPayload,
		Redaction: Redaction{
			Mode:  "redacted",
			Rules: []string{"url_query", "url_fragment", "raw_cdp_disabled"},
		},
	})
}

type validatingWebSocketProbe struct{}

func (validatingWebSocketProbe) Probe(ctx context.Context, endpoint string) (BrowserVersion, error) {
	if err := ctx.Err(); err != nil {
		return BrowserVersion{}, err
	}
	return BrowserVersion{
		Browser:              "unknown",
		ProtocolVersion:      "unknown",
		WebSocketDebuggerURL: endpoint,
	}, nil
}

// Compile-time assertions document the intended standard-library defaults.
var _ HTTPClient = (*http.Client)(nil)
var _ ActivePortReader = FileActivePortReader{}
var _ WebSocketProbe = validatingWebSocketProbe{}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now().UTC() }
