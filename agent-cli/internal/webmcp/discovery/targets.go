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
	"sort"
	"strconv"
	"strings"
)

const (
	maxTargetIDBytes = 256
	maxTargetTitle   = 512
	maxTargetURL     = 4096
)

// targetEndpoint is retained only inside a Service so a normalized browser
// candidate can be refreshed without returning its transport credentials.
type targetEndpoint struct {
	httpURL   string
	browserWS string
}

// targetState keeps the raw target identity for a later runtime adapter. The
// value is never returned from this package's public discovery results.
type targetState struct {
	target        Target
	rawID         string
	pageWebSocket string
	generation    uint64
	closed        bool
}

// HashTargetIDMapper is the default deterministic opaque target ID mapper.
// Browser identity scopes the hash, so the same browser target ID on separate
// browser endpoints cannot select the wrong tab.
type HashTargetIDMapper struct{}

// TargetID implements TargetIDMapper.
func (HashTargetIDMapper) TargetID(identity TargetIdentity) string {
	digest := sha256.Sum256([]byte(identity.BrowserID + "\x00" + identity.RawID))
	return "target-" + hex.EncodeToString(digest[:12])
}

// resolvedEligibleOnly applies the C0 default for an omitted optional field.
func (o TargetListOptions) resolvedEligibleOnly() bool {
	if o.EligibleOnly == nil {
		return true
	}
	return *o.EligibleOnly
}

// ListTargets refreshes one already-discovered browser and returns normalized
// targets in deterministic order. The optional argument keeps the common
// no-filter call concise while preserving explicit false for eligible_only.
func (s *Service) ListTargets(ctx context.Context, browser BrowserCandidate, options ...TargetListOptions) ([]Target, error) {
	snapshot, err := s.ListTargetSnapshot(ctx, browser, options...)
	return snapshot.Targets, err
}

// ListTargetSnapshot refreshes one already-discovered browser, applies the
// supplied C0 filters, and emits browser.targets.snapshot. It never selects a
// different browser when options.BrowserID is supplied.
func (s *Service) ListTargetSnapshot(ctx context.Context, browser BrowserCandidate, options ...TargetListOptions) (TargetSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	listOptions := resolvedTargetListOptions(firstTargetListOptions(options))
	s.mu.Lock()
	defer s.mu.Unlock()

	if browser.ID == "" {
		return TargetSnapshot{}, newNoEligibleTab("", listOptions, 0)
	}
	if listOptions.BrowserID != "" && listOptions.BrowserID != browser.ID {
		return TargetSnapshot{Browsers: []BrowserCandidate{browser}, Filters: listOptions}, newNoEligibleTab(listOptions.BrowserID, listOptions, 0)
	}

	descriptors, failure := s.listTargetDescriptorsLocked(ctx, browser)
	if failure != nil {
		failure = enrichBrowserDisconnected(failure, browser.ID, listOptions.TargetID, "targets")
		s.noteBrowserDisconnectedFailureLocked(failure, browser.ID, listOptions.TargetID, "targets")
		return TargetSnapshot{Browsers: []BrowserCandidate{browser}, Filters: listOptions}, failure
	}
	allTargets, normalizeFailure := s.normalizeTargetsLocked(ctx, browser, descriptors)
	if normalizeFailure != nil {
		normalizeFailure = enrichBrowserDisconnected(normalizeFailure, browser.ID, listOptions.TargetID, "targets")
		s.noteBrowserDisconnectedFailureLocked(normalizeFailure, browser.ID, listOptions.TargetID, "targets")
		return TargetSnapshot{Browsers: []BrowserCandidate{browser}, Filters: listOptions}, normalizeFailure
	}

	snapshot := makeTargetSnapshot(browser, allTargets, listOptions)
	s.emit(EventTargetsSnapshot, browser.ID, targetSnapshotPayload(snapshot))

	filtered, unsupported := filterTargets(allTargets, listOptions)
	snapshot.Targets = filtered
	if unsupported != nil {
		return snapshot, unsupported
	}
	if len(filtered) == 0 {
		return snapshot, newNoEligibleTab(browser.ID, listOptions, snapshot.CandidateCount)
	}
	return snapshot, nil
}

// DiscoverAndListTargets performs discovery and then lists targets. When
// discovery finds more than one browser, an exact browser ID is mandatory;
// no endpoint is chosen by enumeration order.
func (s *Service) DiscoverAndListTargets(ctx context.Context, inputs ConnectionInputs, options TargetListOptions) (TargetSnapshot, error) {
	candidates, err := s.DiscoverAll(ctx, inputs)
	if err != nil {
		return TargetSnapshot{Filters: options}, err
	}
	selected := candidates
	if options.BrowserID != "" {
		selected = make([]BrowserCandidate, 0, 1)
		for _, candidate := range candidates {
			if candidate.ID == options.BrowserID {
				selected = append(selected, candidate)
			}
		}
		if len(selected) == 0 {
			return TargetSnapshot{Browsers: append([]BrowserCandidate(nil), candidates...), Filters: options}, newNoEligibleTab(options.BrowserID, options, 0)
		}
	}
	if len(selected) > 1 {
		ids := make([]string, 0, len(selected))
		for _, candidate := range selected {
			ids = append(ids, candidate.ID)
		}
		sort.Strings(ids)
		return TargetSnapshot{Browsers: append([]BrowserCandidate(nil), candidates...), Filters: options}, newAmbiguousBrowser(ids)
	}
	if len(selected) == 0 {
		return TargetSnapshot{Browsers: append([]BrowserCandidate(nil), candidates...), Filters: options}, newNoEligibleTab(options.BrowserID, options, 0)
	}

	return s.ListTargetSnapshot(ctx, selected[0], options)
}

// List is a concise alias for DiscoverAndListTargets for callers treating the
// service as a browser/tab catalog.
func (s *Service) List(ctx context.Context, inputs ConnectionInputs, options TargetListOptions) (TargetSnapshot, error) {
	return s.DiscoverAndListTargets(ctx, inputs, options)
}

// DiscoverAll returns every candidate in the winning discovery source tier.
// A successful explicit source still prevents lower-priority sources. Multiple
// configured/process candidates remain visible so callers can fail closed with
// ambiguous_browser instead of selecting an arbitrary endpoint.
func (s *Service) DiscoverAll(ctx context.Context, inputs ConnectionInputs) ([]BrowserCandidate, error) {
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
	configured := make([]endpointAttempt, 0, len(inputs.ConfiguredSources))
	var best *DiscoveryError
	for _, attempt := range attempts {
		if attempt.source == SourceConfigured {
			configured = append(configured, attempt)
			continue
		}
		candidate, failure := s.tryAttempt(ctx, attempt, inputs.AllowRemoteCDP)
		if candidate.ID != "" {
			s.emit(EventDiscoveryCompleted, candidate.ID, map[string]any{
				"candidate_count": 1,
				"source":          string(candidate.Source),
				"success":         true,
			})
			return []BrowserCandidate{candidate}, nil
		}
		if failure != nil && failure.Code == CodeBrowserDisconnected {
			s.noteBrowserDisconnectedFailureLocked(failure, "", "", "discovery")
			s.emit(EventDiscoveryCompleted, detailString(failure.Details, "browser_id"), map[string]any{
				"candidate_count": 0,
				"success":         false,
				"code":            string(failure.Code),
			})
			return nil, failure
		}
		best = preferFailure(best, failure)
	}

	candidates := make([]BrowserCandidate, 0)
	for _, attempt := range configured {
		candidate, failure := s.tryAttempt(ctx, attempt, inputs.AllowRemoteCDP)
		if candidate.ID != "" {
			if !containsBrowser(candidates, candidate.ID) {
				candidates = append(candidates, candidate)
			}
			continue
		}
		if failure != nil && failure.Code == CodeBrowserDisconnected {
			s.noteBrowserDisconnectedFailureLocked(failure, "", "", "discovery")
			s.emit(EventDiscoveryCompleted, detailString(failure.Details, "browser_id"), map[string]any{
				"candidate_count": 0,
				"success":         false,
				"code":            string(failure.Code),
			})
			return nil, failure
		}
		best = preferFailure(best, failure)
	}
	if len(candidates) > 0 {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
		s.emit(EventDiscoveryCompleted, "", map[string]any{
			"candidate_count": len(candidates),
			"success":         true,
		})
		return candidates, nil
	}

	if inputs.AllowProcessScan && s.processEnumerator != nil {
		infos, enumerateErr := s.processEnumerator.List(ctx)
		if enumerateErr != nil {
			if isBrowserDisconnected(enumerateErr) {
				failure := newBrowserDisconnectedFromError(enumerateErr, "", "", "process")
				s.noteBrowserDisconnectedFailureLocked(failure, "", "", "process")
				s.emit(EventDiscoveryCompleted, detailString(failure.Details, "browser_id"), map[string]any{
					"candidate_count": 0,
					"success":         false,
					"code":            string(failure.Code),
				})
				return nil, failure
			}
			best = preferFailure(best, newEndpointUnreachable(EndpointKindProcess, "non_loopback", "process", enumerateErr))
		} else {
			for _, info := range infos {
				if !info.DebuggingEnabled {
					continue
				}
				endpoint := info.Endpoint
				if strings.TrimSpace(endpoint.CDPURL) == "" && strings.TrimSpace(endpoint.BrowserWSEndpoint) == "" && info.UserDataDir != "" {
					active, readErr := s.activePortReader.Read(ctx, info.UserDataDir)
					if readErr != nil {
						failure := classifyActivePortError(readErr, SourceProcess)
						if failure.Code == CodeBrowserDisconnected {
							s.noteBrowserDisconnectedFailureLocked(failure, "", "", "active_port")
							s.emit(EventDiscoveryCompleted, detailString(failure.Details, "browser_id"), map[string]any{
								"candidate_count": 0,
								"success":         false,
								"code":            string(failure.Code),
							})
							return nil, failure
						}
						best = preferFailure(best, failure)
						continue
					}
					endpoint, readErr = endpointFromActivePort(active)
					if readErr != nil {
						failure := classifyActivePortError(readErr, SourceProcess)
						if failure.Code == CodeBrowserDisconnected {
							s.noteBrowserDisconnectedFailureLocked(failure, "", "", "active_port")
							return nil, failure
						}
						best = preferFailure(best, failure)
						continue
					}
				}
				if strings.TrimSpace(endpoint.CDPURL) == "" && strings.TrimSpace(endpoint.BrowserWSEndpoint) == "" {
					continue
				}
				candidate, failure := s.tryAttempt(ctx, endpointAttempt{
					source:  SourceProcess,
					kind:    EndpointKindProcess,
					resolve: func(context.Context) (Endpoint, error) { return endpoint, nil },
				}, inputs.AllowRemoteCDP)
				if candidate.ID != "" {
					if !containsBrowser(candidates, candidate.ID) {
						candidates = append(candidates, candidate)
					}
					continue
				}
				if failure != nil && failure.Code == CodeBrowserDisconnected {
					s.noteBrowserDisconnectedFailureLocked(failure, "", "", "process")
					s.emit(EventDiscoveryCompleted, detailString(failure.Details, "browser_id"), map[string]any{
						"candidate_count": 0,
						"success":         false,
						"code":            string(failure.Code),
					})
					return nil, failure
				}
				best = preferFailure(best, failure)
			}
		}
	}
	if len(candidates) > 0 {
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
		s.emit(EventDiscoveryCompleted, "", map[string]any{
			"candidate_count": len(candidates),
			"success":         true,
		})
		return candidates, nil
	}

	if best == nil {
		best = newEndpointNotFound(EndpointKindCDPHTTP, SourceConfigured)
	}
	s.emit(EventDiscoveryCompleted, "", map[string]any{
		"candidate_count": 0,
		"success":         false,
		"code":            string(best.Code),
	})
	return nil, best
}

func firstTargetListOptions(options []TargetListOptions) TargetListOptions {
	if len(options) == 0 {
		return TargetListOptions{}
	}
	return options[0]
}

func resolvedTargetListOptions(options TargetListOptions) TargetListOptions {
	if options.EligibleOnly == nil {
		options.EligibleOnly = Bool(true)
	}
	return options
}

func containsBrowser(candidates []BrowserCandidate, id string) bool {
	for _, candidate := range candidates {
		if candidate.ID == id {
			return true
		}
	}
	return false
}

func (s *Service) listTargetDescriptorsLocked(ctx context.Context, browser BrowserCandidate) ([]TargetDescriptor, *DiscoveryError) {
	if s.targetLister != nil {
		descriptors, err := s.targetLister.List(ctx, browser)
		if err != nil {
			return nil, classifyTargetListError(err, browser)
		}
		return append([]TargetDescriptor(nil), descriptors...), nil
	}
	endpoint, ok := s.endpoints[browser.ID]
	if !ok {
		return nil, newEndpointNotFound(EndpointKindConfigured, browser.Source)
	}
	if endpoint.httpURL == "" {
		return nil, newEndpointUnreachable(EndpointKindBrowserWebSocket, addressClass(browser.Loopback), "targets", errors.New("target list requires a target lister"))
	}

	parsed, parseErr := parseHTTPURL(endpoint.httpURL)
	if parseErr != nil {
		return nil, newProtocolInvalidAt("targets", "unknown", parseErr.reason, nil)
	}
	requestURL := *parsed
	requestURL.Path = targetListPath(requestURL.Path)
	requestURL.RawQuery = ""
	requestURL.Fragment = ""
	probeCtx, cancel := context.WithTimeout(ctx, s.probeTimeout)
	defer cancel()
	request, requestErr := http.NewRequestWithContext(probeCtx, http.MethodGet, requestURL.String(), nil)
	if requestErr != nil {
		return nil, newProtocolInvalidAt("targets", "unknown", "request_invalid", requestErr)
	}
	response, responseErr := s.httpClient.Do(request)
	if responseErr != nil {
		if isBrowserDisconnected(responseErr) {
			return nil, newBrowserDisconnectedFromError(responseErr, browser.ID, "", "targets")
		}
		return nil, newEndpointUnreachable(EndpointKindCDPHTTP, addressClass(browser.Loopback), "targets", responseErr)
	}
	if response == nil {
		return nil, newEndpointUnreachable(EndpointKindCDPHTTP, addressClass(browser.Loopback), "targets", errors.New("nil response"))
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	if response.StatusCode == 404 {
		return nil, newEndpointNotFound(EndpointKindCDPHTTP, browser.Source)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, newProtocolInvalidAt("targets", "http_"+strconv.Itoa(response.StatusCode), "http_status", nil)
	}
	if response.Body == nil {
		return nil, newProtocolInvalidAt("targets", "unknown", "missing_body", nil)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, s.maxTargetBytes+1))
	if readErr != nil {
		if isBrowserDisconnected(readErr) {
			return nil, newBrowserDisconnectedFromError(readErr, browser.ID, "", "targets")
		}
		return nil, newEndpointUnreachable(EndpointKindCDPHTTP, addressClass(browser.Loopback), "targets", readErr)
	}
	if int64(len(body)) > s.maxTargetBytes {
		return nil, newProtocolInvalidAt("targets", "unknown", "response_too_large", nil)
	}
	var descriptors []TargetDescriptor
	if err := json.Unmarshal(body, &descriptors); err != nil {
		return nil, newProtocolInvalidAt("targets", "unknown", "malformed_json", nil)
	}
	return descriptors, nil
}

func classifyTargetListError(err error, browser BrowserCandidate) *DiscoveryError {
	if isBrowserDisconnected(err) {
		return newBrowserDisconnectedFromError(err, browser.ID, "", "targets")
	}
	var discoveryErr *DiscoveryError
	if errors.As(err, &discoveryErr) {
		return discoveryErr
	}
	return newEndpointUnreachable(EndpointKindConfigured, addressClass(browser.Loopback), "targets", err)
}

func (s *Service) normalizeTargetsLocked(ctx context.Context, browser BrowserCandidate, descriptors []TargetDescriptor) ([]Target, *DiscoveryError) {
	if len(descriptors) == 0 {
		return []Target{}, nil
	}
	states := make(map[string]targetState, len(descriptors))
	for _, descriptor := range descriptors {
		target, state, failure := s.normalizeTarget(ctx, browser, descriptor)
		if failure != nil {
			return nil, failure
		}
		if _, exists := states[target.ID]; exists {
			return nil, newProtocolInvalidAt("targets", "unknown", "duplicate_target_id", nil)
		}
		states[target.ID] = state
	}
	ordered := make([]Target, 0, len(states))
	for _, state := range states {
		ordered = append(ordered, state.target)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ID != ordered[j].ID {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Origin < ordered[j].Origin
	})
	if s.targets[browser.ID] == nil {
		s.targets[browser.ID] = make(map[string]targetState, len(states))
	}
	for _, state := range states {
		state.closed = false
		s.targets[browser.ID][state.target.ID] = state
	}
	return ordered, nil
}

func (s *Service) normalizeTarget(ctx context.Context, browser BrowserCandidate, descriptor TargetDescriptor) (Target, targetState, *DiscoveryError) {
	rawID := strings.TrimSpace(descriptor.ID)
	if rawID == "" || len(rawID) > maxTargetIDBytes || hasControl(rawID) {
		return Target{}, targetState{}, newProtocolInvalidAt("targets", "unknown", "malformed_target_id", nil)
	}
	publicID := normalizeTargetPublicID(s.targetIDMapper.TargetID(TargetIdentity{BrowserID: browser.ID, RawID: rawID}), browser.ID, rawID)
	generation := uint64(1)
	if prior, ok := s.targets[browser.ID][publicID]; ok && prior.generation > 0 {
		generation = prior.generation
	}
	target := Target{
		BrowserID:  browser.ID,
		ID:         publicID,
		Type:       safeTargetType(descriptor.Type),
		Title:      boundedLabel(descriptor.Title, maxTargetTitle),
		Generation: generation,
	}
	safePageURL, origin, internal, urlReason := normalizePageURL(descriptor.URL)
	target.URL = safePageURL
	target.Origin = origin
	pageWebSocket, normalizedWebSocket := normalizePageWebSocket(descriptor.WebSocketDebuggerURL)
	target.WebSocketPresent = pageWebSocket
	target.ContinuityMarker = targetContinuityMarker(browser.ID, rawID, origin, safePageURL, normalizedWebSocket, descriptor)
	target.WebMCP, target.WebMCPKnown = descriptorWebMCP(descriptor)
	target.ToolCount, target.ToolCountKnown = descriptorToolCount(descriptor)

	structuralReason := ""
	switch {
	case target.Type != "page":
		structuralReason = "not_page"
	case internal:
		structuralReason = "internal_url"
	case urlReason != "":
		structuralReason = urlReason
	case !pageWebSocket:
		structuralReason = "page_websocket_required"
	case !s.originPolicy.Allows(origin):
		structuralReason = "origin_denied"
	}
	if structuralReason == "" && s.targetProbe != nil {
		capabilities, err := s.targetProbe.Probe(ctx, browser, target)
		if err != nil {
			return Target{}, targetState{}, classifyTargetListError(err, browser)
		}
		target.WebMCP = capabilities.WebMCP
		target.WebMCPKnown = true
		if capabilities.ToolCount >= 0 {
			target.ToolCount = capabilities.ToolCount
			target.ToolCountKnown = capabilities.ToolCountKnown || capabilities.ToolCount >= 0
		}
	}
	if structuralReason == "" && !target.WebMCP {
		structuralReason = "unsupported_webmcp"
	}
	target.Eligible = structuralReason == ""
	target.EligibilityReason = structuralReason
	return target, targetState{
		target:        target,
		rawID:         rawID,
		pageWebSocket: normalizedWebSocket,
		generation:    generation,
	}, nil
}

// targetContinuityMarker turns adapter-provided continuity metadata into a
// stable opaque value. When an adapter has no document marker, it hashes the
// complete raw page URL before falling back to safe display metadata. The raw
// URL never crosses the persistence boundary, while query/fragment-only
// navigation still changes the continuity claim.
func targetContinuityMarker(browserID, rawID, origin, pageURL, pageWebSocket string, descriptor TargetDescriptor) string {
	marker := strings.TrimSpace(descriptor.ContinuityMarker)
	if marker == "" {
		marker = strings.TrimSpace(descriptor.Continuity)
	}
	if marker == "" {
		marker = strings.TrimSpace(descriptor.DocumentID)
	}
	if marker == "" {
		rawPageURL := strings.TrimSpace(descriptor.URL)
		if rawPageURL != "" {
			digest := sha256.Sum256([]byte(rawPageURL))
			marker = "url-" + hex.EncodeToString(digest[:])
		}
	}
	if marker == "" {
		marker = pageURL
	}
	if marker == "" {
		marker = pageWebSocket
	}
	if marker == "" {
		marker = rawID
	}
	key := strings.Join([]string{browserID, rawID, origin, marker}, "\x00")
	digest := sha256.Sum256([]byte(key))
	return "continuity-" + hex.EncodeToString(digest[:12])
}

func normalizeTargetPublicID(value, browserID, rawID string) string {
	value = strings.TrimSpace(value)
	if publicIDPattern.MatchString(value) && value != rawID && !strings.ContainsAny(value, "/?#:@") {
		return value
	}
	return HashTargetIDMapper{}.TargetID(TargetIdentity{BrowserID: browserID, RawID: rawID})
}

func descriptorWebMCP(descriptor TargetDescriptor) (bool, bool) {
	if descriptor.WebMCPSupported != nil {
		return *descriptor.WebMCPSupported, true
	}
	if descriptor.WebMCP != nil {
		return *descriptor.WebMCP, true
	}
	// /json/list does not carry the experimental domain capability. Unknown is
	// deliberately not eligible; an injected TargetCapabilityProbe or explicit
	// descriptor field must prove support before a target can be advertised.
	return false, false
}

func descriptorToolCount(descriptor TargetDescriptor) (int, bool) {
	if descriptor.ToolCount != nil && *descriptor.ToolCount >= 0 {
		return *descriptor.ToolCount, true
	}
	if descriptor.Tools != nil {
		return len(descriptor.Tools), true
	}
	return 0, false
}

func safeTargetType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	if len(value) > 32 || hasControl(value) {
		return "redacted"
	}
	return value
}

func normalizePageURL(raw string) (safeURL, origin string, internal bool, reason string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", false, "url_required"
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return "redacted", "", false, "malformed_url"
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "redacted", "", true, "internal_url"
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "redacted", "", false, "malformed_url"
	}
	if parsed.User != nil || hasControl(trimmed) {
		return "redacted", "", false, "unsafe_url"
	}
	if parsed.Port() != "" {
		port, parseErr := strconv.Atoi(parsed.Port())
		if parseErr != nil || port < 1 || port > 65535 {
			return "redacted", "", false, "malformed_url"
		}
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Host = canonicalHost(parsed)
	safeURL = parsed.String()
	if len(safeURL) > maxTargetURL {
		return "redacted", "", false, "url_too_large"
	}
	origin = canonicalOrigin(parsed)
	return safeURL, origin, false, ""
}

func normalizePageWebSocket(raw string) (bool, string) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil || parsed.User != nil {
		return false, ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" || parsed.Hostname() == "" {
		return false, ""
	}
	path := parsed.Path
	const prefix = "/devtools/page/"
	if !strings.HasPrefix(path, prefix) || strings.TrimPrefix(path, prefix) == "" || hasControl(trimmed) {
		return false, ""
	}
	if parsed.Port() != "" {
		port, parseErr := strconv.Atoi(parsed.Port())
		if parseErr != nil || port < 1 || port > 65535 {
			return false, ""
		}
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return true, parsed.String()
}

func canonicalHost(parsed *url.URL) string {
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" || (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

func canonicalOrigin(parsed *url.URL) string {
	host := canonicalHost(parsed)
	return parsed.Scheme + "://" + host
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func targetListBaseURL(parsed *url.URL) string {
	copyURL := *parsed
	copyURL.RawQuery = ""
	copyURL.Fragment = ""
	copyURL.Path = targetListPath(copyURL.Path)
	return copyURL.String()
}

func targetListPath(path string) string {
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "/json/list"
	}
	if strings.HasSuffix(path, "/json/version") {
		return strings.TrimSuffix(path, "/json/version") + "/json/list"
	}
	if strings.HasSuffix(path, "/json/list") {
		return path
	}
	return path + "/json/list"
}

func filterTargets(targets []Target, options TargetListOptions) ([]Target, *DiscoveryError) {
	eligibleOnly := options.resolvedEligibleOnly()
	filtered := make([]Target, 0, len(targets))
	for _, target := range targets {
		if options.TargetID != "" && target.ID != options.TargetID {
			continue
		}
		if options.OriginContains != "" && !strings.Contains(target.Origin, options.OriginContains) {
			continue
		}
		if options.TargetID != "" && target.ID == options.TargetID && target.Type == "page" && !target.WebMCP {
			return nil, newUnsupportedWebMCP(target.BrowserID, target.ID)
		}
		if eligibleOnly {
			if !target.Eligible {
				continue
			}
			if !options.IncludeZeroToolPages && target.ToolCountKnown && target.ToolCount == 0 {
				continue
			}
		}
		filtered = append(filtered, target)
	}
	return filtered, nil
}

func makeTargetSnapshot(browser BrowserCandidate, targets []Target, options TargetListOptions) TargetSnapshot {
	eligibleCount := 0
	for _, target := range targets {
		if target.Eligible {
			eligibleCount++
		}
	}
	return TargetSnapshot{
		Browsers:       []BrowserCandidate{browser},
		Targets:        append([]Target(nil), targets...),
		CandidateCount: len(targets),
		EligibleCount:  eligibleCount,
		Filters:        options,
	}
}

func targetSnapshotPayload(snapshot TargetSnapshot) map[string]any {
	targets := make([]map[string]any, 0, len(snapshot.Targets))
	for _, target := range snapshot.Targets {
		targets = append(targets, map[string]any{
			"browser_id":        target.BrowserID,
			"id":                target.ID,
			"generation":        target.Generation,
			"type":              target.Type,
			"title":             target.Title,
			"url":               target.URL,
			"origin":            target.Origin,
			"websocket_present": target.WebSocketPresent,
			"webmcp":            target.WebMCP,
			"tool_count":        target.ToolCount,
			"eligible":          target.Eligible,
		})
	}
	return map[string]any{
		"candidate_count": len(snapshot.Targets),
		"eligible_count":  snapshot.EligibleCount,
		"returned_count":  len(snapshot.Targets),
		"targets":         targets,
	}
}

func (s *Service) rememberEndpoint(browserID string, endpoint targetEndpoint) {
	if browserID == "" {
		return
	}
	if existing, ok := s.endpoints[browserID]; ok {
		if endpoint.httpURL == "" {
			endpoint.httpURL = existing.httpURL
		}
		if endpoint.browserWS == "" {
			endpoint.browserWS = existing.browserWS
		}
	}
	s.endpoints[browserID] = endpoint
}

func newOriginPolicy(custom OriginPolicy, allowed, denied []string) OriginPolicy {
	policy := configuredOriginPolicy{
		custom:  custom,
		allowed: make(map[string]struct{}, len(allowed)),
		denied:  make(map[string]struct{}, len(denied)),
	}
	for _, value := range allowed {
		if origin := canonicalOriginValue(value); origin != "" {
			policy.allowed[origin] = struct{}{}
		}
	}
	for _, value := range denied {
		if origin := canonicalOriginValue(value); origin != "" {
			policy.denied[origin] = struct{}{}
		}
	}
	return policy
}

type configuredOriginPolicy struct {
	custom  OriginPolicy
	allowed map[string]struct{}
	denied  map[string]struct{}
}

func (p configuredOriginPolicy) Allows(origin string) bool {
	if _, denied := p.denied[origin]; denied {
		return false
	}
	if len(p.allowed) > 0 {
		if _, allowed := p.allowed[origin]; !allowed {
			return false
		}
	}
	return p.custom == nil || p.custom.Allows(origin)
}

func canonicalOriginValue(value string) string {
	_, origin, internal, reason := normalizePageURL(value)
	if internal || reason != "" {
		return ""
	}
	return origin
}

var _ TargetIDMapper = HashTargetIDMapper{}
