package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultProbeTimeout = 5 * time.Second

var (
	protocolVersionPattern = regexp.MustCompile(`^([0-9]+)\.([0-9]+)$`)
	publicIDPattern        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
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
}

// Service performs deterministic, serialized browser endpoint discovery.
// Serialized calls also keep semantic event sequence numbers monotonic when a
// service instance is reused by more than one command adapter.
type Service struct {
	mu                sync.Mutex
	httpClient        HTTPClient
	activePortReader  ActivePortReader
	processEnumerator ProcessEnumerator
	webSocketProbe    WebSocketProbe
	targetLister      TargetLister
	targetProbe       TargetCapabilityProbe
	targetAttacher    TargetAttacher
	activator         TargetActivator
	originPolicy      OriginPolicy
	idMapper          IDMapper
	targetIDMapper    TargetIDMapper
	eventSink         EventSink
	clock             Clock
	maxVersionBytes   int64
	maxTargetBytes    int64
	probeTimeout      time.Duration
	eventSequence     uint64
	endpoints         map[string]targetEndpoint
	targets           map[string]map[string]targetState
	browsers          map[string]BrowserCandidate
	selection         *Selection
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
	return &Service{
		httpClient:        client,
		activePortReader:  activePortReader,
		processEnumerator: options.ProcessEnumerator,
		webSocketProbe:    webSocketProbe,
		targetLister:      options.TargetLister,
		targetProbe:       targetProbe,
		targetAttacher:    targetAttacher,
		activator:         activator,
		originPolicy:      newOriginPolicy(options.OriginPolicy, options.AllowedOrigins, options.DeniedOrigins),
		idMapper:          idMapper,
		targetIDMapper:    targetIDMapper,
		eventSink:         eventSink,
		clock:             clock,
		maxVersionBytes:   maxVersionBytes,
		maxTargetBytes:    maxTargetBytes,
		probeTimeout:      probeTimeout,
		endpoints:         make(map[string]targetEndpoint),
		targets:           make(map[string]map[string]targetState),
		browsers:          make(map[string]BrowserCandidate),
	}
}

// NewService is a descriptive constructor alias.
func NewService(options Options) *Service { return New(options) }

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
	defer s.mu.Unlock()

	s.emit(EventDiscoveryStarted, "", map[string]any{
		"source_plan": []string{
			string(SourceExplicitCDPHTTP),
			string(SourceExplicitBrowserWS),
			string(SourceDevToolsActivePort),
			string(SourceConfigured),
			string(SourceProcess),
		},
	})

	attempts := s.explicitAttempts(inputs)
	var best *DiscoveryError
	attempted := false
	for _, attempt := range attempts {
		if err := ctx.Err(); err != nil {
			failure := newEndpointUnreachable(attempt.kind, addressClassFromEndpointKind(attempt.kind), "discovery", err)
			best = preferFailure(best, failure)
			break
		}
		candidate, failure := s.tryAttempt(ctx, attempt, inputs.AllowRemoteCDP)
		if candidate.ID != "" {
			s.emit(EventDiscoveryCompleted, candidate.ID, map[string]any{
				"candidate_count": 1,
				"source":          string(candidate.Source),
				"success":         true,
			})
			return candidate, nil
		}
		attempted = attempted || failure != nil
		best = preferFailure(best, failure)
	}

	// Process enumeration is deliberately deferred until every higher-priority
	// source has failed. This makes the call boundary observable and avoids
	// scanning processes when a configured endpoint already works.
	if best == nil || inputs.AllowProcessScan {
		if inputs.AllowProcessScan && s.processEnumerator != nil {
			infos, err := s.processEnumerator.List(ctx)
			if err != nil {
				best = preferFailure(best, newEndpointUnreachable(EndpointKindProcess, "non_loopback", "process", err))
			} else {
				for _, info := range infos {
					if !info.DebuggingEnabled {
						continue
					}
					endpoint := info.Endpoint
					if endpoint.CDPURL == "" && endpoint.BrowserWSEndpoint == "" && info.UserDataDir != "" {
						active, readErr := s.activePortReader.Read(ctx, info.UserDataDir)
						if readErr == nil {
							endpoint, readErr = endpointFromActivePort(active)
						}
						if readErr != nil {
							best = preferFailure(best, classifyActivePortError(readErr, SourceProcess))
							continue
						}
					}
					if strings.TrimSpace(endpoint.CDPURL) == "" && strings.TrimSpace(endpoint.BrowserWSEndpoint) == "" {
						continue
					}
					attempted = true
					candidate, failure := s.tryAttempt(ctx, endpointAttempt{
						source:  SourceProcess,
						kind:    EndpointKindProcess,
						resolve: func(context.Context) (Endpoint, error) { return endpoint, nil },
					}, inputs.AllowRemoteCDP)
					if candidate.ID != "" {
						s.emit(EventDiscoveryCompleted, candidate.ID, map[string]any{
							"candidate_count": 1,
							"source":          string(candidate.Source),
							"success":         true,
						})
						return candidate, nil
					}
					best = preferFailure(best, failure)
				}
			}
		}
	}

	if best == nil {
		best = newEndpointNotFound(EndpointKindCDPHTTP, Source(SourceConfigured))
	}
	if !attempted && best.Code == "" {
		best = newEndpointNotFound(EndpointKindCDPHTTP, SourceConfigured)
	}
	s.emit(EventDiscoveryCompleted, "", map[string]any{
		"candidate_count": 0,
		"success":         false,
		"code":            string(best.Code),
	})
	return BrowserCandidate{}, best
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
		return BrowserCandidate{}, newEndpointUnreachable(kind, addressClass(loopback), "version", responseErr)
	}
	if response == nil {
		return BrowserCandidate{}, newEndpointUnreachable(kind, addressClass(loopback), "version", errors.New("nil response"))
	}
	if response.Body != nil {
		defer response.Body.Close()
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
		s.rememberEndpoint(candidate.ID, targetEndpoint{
			httpURL: targetListBaseURL(parsed),
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
		return BrowserCandidate{}, newEndpointUnreachable(kind, addressClass(normalized.loopback), "connect", probeErr)
	}
	if strings.TrimSpace(version.WebSocketDebuggerURL) == "" {
		version.WebSocketDebuggerURL = normalized.url.String()
	}
	candidate, failure := s.candidateFromVersion(version, source, kind, normalized.url, allowRemote, false)
	if candidate.ID != "" {
		s.rememberEndpoint(candidate.ID, targetEndpoint{
			browserWS: normalized.url.String(),
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
		Scheme: normalized.url.Scheme,
		Host:   normalized.url.Hostname(),
		Port:   normalized.url.Port(),
		Path:   normalized.url.EscapedPath(),
	}
	publicID := normalizePublicID(s.idMapper.BrowserID(identity), identity)
	candidate := BrowserCandidate{
		ID:       publicID,
		Source:   source,
		Product:  product,
		Protocol: protocol,
		Loopback: normalized.loopback,
	}
	s.browsers[candidate.ID] = candidate
	s.emit(EventEndpointVersion, candidate.ID, map[string]any{
		"product":  product,
		"protocol": protocol,
		"source":   string(source),
	})
	return candidate, nil
}

type parseURLFailure struct{ reason string }

func (e *parseURLFailure) Error() string {
	if e == nil {
		return "invalid endpoint"
	}
	return e.reason
}

func parseHTTPURL(raw string) (*url.URL, *parseURLFailure) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return nil, &parseURLFailure{reason: "malformed_endpoint"}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, &parseURLFailure{reason: "unsupported_endpoint_scheme"}
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return nil, &parseURLFailure{reason: "missing_endpoint_host"}
	}
	if parsed.User != nil {
		return nil, &parseURLFailure{reason: "credentials_not_allowed"}
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, &parseURLFailure{reason: "invalid_endpoint_port"}
		}
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed, nil
}

type normalizedWebSocketURL struct {
	url      *url.URL
	loopback bool
}

func parseBrowserWebSocketURL(raw string) (normalizedWebSocketURL, *parseURLFailure) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return normalizedWebSocketURL{}, &parseURLFailure{reason: "malformed_browser_websocket"}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return normalizedWebSocketURL{}, &parseURLFailure{reason: "unsupported_websocket_scheme"}
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return normalizedWebSocketURL{}, &parseURLFailure{reason: "missing_websocket_host"}
	}
	if parsed.User != nil {
		return normalizedWebSocketURL{}, &parseURLFailure{reason: "credentials_not_allowed"}
	}
	if !strings.HasPrefix(parsed.Path, "/devtools/browser/") || strings.TrimPrefix(parsed.Path, "/devtools/browser/") == "" {
		if strings.HasPrefix(parsed.Path, "/devtools/page/") {
			return normalizedWebSocketURL{}, &parseURLFailure{reason: "page_websocket_not_browser_websocket"}
		}
		return normalizedWebSocketURL{}, &parseURLFailure{reason: "browser_websocket_path_required"}
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return normalizedWebSocketURL{}, &parseURLFailure{reason: "invalid_websocket_port"}
		}
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return normalizedWebSocketURL{url: parsed, loopback: isLoopbackHost(parsed.Hostname())}, nil
}

func versionPath(path string) string {
	path = strings.TrimRight(path, "/")
	if path == "" || path == "/" {
		return "/json/version"
	}
	if strings.HasSuffix(path, "/json/version") {
		return path
	}
	return path + "/json/version"
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") || strings.EqualFold(host, "localhost.") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func addressClass(loopback bool) string {
	if loopback {
		return "loopback"
	}
	return "non_loopback"
}

func addressClassFromEndpointKind(kind EndpointKind) string {
	if kind == EndpointKindCDPHTTP || kind == EndpointKindBrowserWebSocket || kind == EndpointKindActivePort {
		return "loopback"
	}
	return "non_loopback"
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
