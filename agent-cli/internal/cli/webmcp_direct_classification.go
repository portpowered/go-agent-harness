package cli

import (
	"context"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const (
	directNormalizedIDMaxLength  = 64
	directMaxAmbiguityCandidates = 32
	directMaxAmbiguityTitle      = 160
	directMaxAmbiguityOrigin     = 256
)

// normalizeDirectOpaqueID accepts the same bounded public-ID alphabet used by
// discovery. Direct commands receive normalized IDs from their broker, but
// keeping the output boundary defensive prevents malformed adapter values from
// becoming endpoint-shaped or otherwise unsafe candidate details.
func normalizeDirectOpaqueID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > directNormalizedIDMaxLength {
		return ""
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '_' && character != '-' {
			return ""
		}
	}
	return value
}

func sortedUniqueDirectIDs(values []string) []string {
	ids := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if normalized := normalizeDirectOpaqueID(value); normalized != "" {
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			ids = append(ids, normalized)
		}
	}
	sort.Strings(ids)
	return ids
}

func directSafeIDList(value any) []string {
	values := make([]string, 0)
	switch typed := value.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	}
	return sortedUniqueDirectIDs(values)
}

func directBrowserCandidateIDs(candidates []webmcp.BrowserCandidate) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, string(candidate.ID))
	}
	return sortedUniqueDirectIDs(ids)
}

func directTargetCandidateIDs(targets []webmcp.Target) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, string(target.ID))
	}
	return sortedUniqueDirectIDs(ids)
}

func directAmbiguityTargetIDs(targets []webmcp.Target) []string {
	ids := directTargetCandidateIDs(targets)
	if len(ids) > directMaxAmbiguityCandidates {
		return ids[:directMaxAmbiguityCandidates]
	}
	return ids
}

func directCandidateChoicesForTargets(browserID string, targets []webmcp.Target) []map[string]any {
	ordered := append([]webmcp.Target(nil), targets...)
	sort.SliceStable(ordered, func(i, j int) bool {
		leftID := normalizeDirectOpaqueID(string(ordered[i].ID))
		rightID := normalizeDirectOpaqueID(string(ordered[j].ID))
		if leftID != rightID {
			return leftID < rightID
		}
		leftOrigin := directCandidateOrigin(ordered[i])
		rightOrigin := directCandidateOrigin(ordered[j])
		if leftOrigin != rightOrigin {
			return leftOrigin < rightOrigin
		}
		return directCandidateTitle(ordered[i].Title) < directCandidateTitle(ordered[j].Title)
	})

	choices := make([]map[string]any, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, target := range ordered {
		targetID := normalizeDirectOpaqueID(string(target.ID))
		if targetID == "" {
			continue
		}
		if _, exists := seen[targetID]; exists {
			continue
		}
		seen[targetID] = struct{}{}
		choice := map[string]any{
			"browser_id": normalizeDirectOpaqueID(browserID),
			"target_id":  targetID,
		}
		if title := directCandidateTitle(target.Title); title != "" {
			choice["title"] = title
		}
		if origin := directCandidateOrigin(target); origin != "" {
			choice["origin"] = origin
		}
		choices = append(choices, choice)
		if len(choices) == directMaxAmbiguityCandidates {
			break
		}
	}
	return choices
}

func directSafeCandidateChoices(value any, fallbackBrowserID string, candidateIDs []string) []map[string]any {
	type metadata struct {
		browserID string
		targetID  string
		title     string
		origin    string
	}
	items := make([]metadata, 0)
	appendMap := func(choice map[string]any) {
		targetID := normalizeDirectOpaqueID(stringValue(choice["target_id"]))
		if targetID == "" {
			return
		}
		browserID := normalizeDirectOpaqueID(stringValue(choice["browser_id"]))
		if browserID == "" {
			browserID = normalizeDirectOpaqueID(fallbackBrowserID)
		}
		items = append(items, metadata{
			browserID: browserID,
			targetID:  targetID,
			title:     directSafeCandidateTitle(stringValue(choice["title"])),
			origin:    directSafeCandidateOrigin(stringValue(choice["origin"])),
		})
	}
	switch choices := value.(type) {
	case []map[string]any:
		for _, choice := range choices {
			appendMap(choice)
		}
	case []any:
		for _, value := range choices {
			if choice, ok := value.(map[string]any); ok {
				appendMap(choice)
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].targetID != items[j].targetID {
			return items[i].targetID < items[j].targetID
		}
		if items[i].browserID != items[j].browserID {
			return items[i].browserID < items[j].browserID
		}
		if items[i].title != items[j].title {
			return items[i].title < items[j].title
		}
		return items[i].origin < items[j].origin
	})

	ids := append([]string(nil), candidateIDs...)
	if len(ids) == 0 {
		for _, item := range items {
			ids = append(ids, item.targetID)
		}
	}
	ids = sortedUniqueDirectIDs(ids)
	if len(ids) > directMaxAmbiguityCandidates {
		ids = ids[:directMaxAmbiguityCandidates]
	}
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[string]metadata, len(items))
	for _, item := range items {
		if _, exists := byID[item.targetID]; !exists {
			byID[item.targetID] = item
		}
	}
	result := make([]map[string]any, 0, len(ids))
	for _, targetID := range ids {
		item := byID[targetID]
		browserID := item.browserID
		if browserID == "" {
			browserID = normalizeDirectOpaqueID(fallbackBrowserID)
		}
		choice := map[string]any{"browser_id": browserID, "target_id": targetID}
		if item.title != "" {
			choice["title"] = item.title
		}
		if item.origin != "" {
			choice["origin"] = item.origin
		}
		result = append(result, choice)
	}
	return result
}

func directCandidateTitle(value string) string {
	value = boundedDoctorText(value, directMaxAmbiguityTitle)
	if value == "" {
		return ""
	}
	if directContainsControl(value) || strings.Contains(value, "://") || strings.ContainsAny(value, "?#@") {
		return "redacted"
	}
	return value
}

func directSafeCandidateTitle(value string) string {
	return directCandidateTitle(value)
}

func directCandidateOrigin(target webmcp.Target) string {
	for _, value := range []string{target.Origin, target.URL} {
		if origin := directCanonicalCandidateOrigin(value); origin != "" {
			return origin
		}
	}
	return ""
}

func directSafeCandidateOrigin(value string) string {
	return directCanonicalCandidateOrigin(value)
}

func directCanonicalCandidateOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 4096 || directContainsControl(raw) {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.User != nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" || parsed.Hostname() == "" {
		return ""
	}
	port := parsed.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return ""
		}
		if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
			port = ""
		}
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	origin := scheme + "://" + host
	if len(origin) > directMaxAmbiguityOrigin {
		return ""
	}
	return origin
}

func directContainsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func normalizeDirectBrowserCandidates(candidates []webmcp.BrowserCandidate) []webmcp.BrowserCandidate {
	normalized := make([]webmcp.BrowserCandidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		id := normalizeDirectOpaqueID(string(candidate.ID))
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		candidate.ID = webmcp.BrowserID(id)
		seen[id] = struct{}{}
		normalized = append(normalized, candidate)
	}
	sort.SliceStable(normalized, func(i, j int) bool { return normalized[i].ID < normalized[j].ID })
	return normalized
}

// directPageTargetCandidates is the shared direct-CLI discovery boundary.
// Chrome exposes browser-owned UI surfaces through the same CDP target list
// as documents, but only an exact page target is selectable by this CLI.
func directPageTargetCandidates(targets []webmcp.Target) []webmcp.Target {
	pages := make([]webmcp.Target, 0, len(targets))
	for _, target := range targets {
		if target.Type != "page" {
			continue
		}
		pages = append(pages, target)
	}
	return pages
}

func directEligibleTargetMatches(targets []webmcp.Target, browser config.BrowserConfig) []webmcp.Target {
	matches := make([]webmcp.Target, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, possible := range directPageTargetCandidates(targets) {
		id := normalizeDirectOpaqueID(string(possible.ID))
		if id == "" || !possible.Eligible {
			continue
		}
		if browser.Selection.Origin != "" && safeOrigin(possible.Origin) != safeOrigin(browser.Selection.Origin) {
			continue
		}
		if err := directTargetPolicyError(possible, browser); err != nil {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		possible.ID = webmcp.TargetID(id)
		seen[id] = struct{}{}
		matches = append(matches, possible)
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return matches
}

// directNoEligibleTabError keeps the direct selection path aligned with the
// C0 no_eligible_tab envelope. The direct page candidate list is the complete
// enumeration at this boundary, so its length is the useful candidate count
// even when eligibility filtering removes every page.
func directNoEligibleTabError(browserID string, browser config.BrowserConfig, candidateCount int, reason string) error {
	if candidateCount < 0 {
		candidateCount = 0
	}
	filters := map[string]any{
		"eligible_only":           true,
		"include_zero_tool_pages": true,
	}
	if origin := safeOrigin(browser.Selection.Origin); origin != "" {
		filters["origin"] = origin
	}
	details := map[string]any{
		"browser_id":      browserID,
		"filters":         filters,
		"candidate_count": candidateCount,
	}
	if reason != "" {
		details["reason"] = boundedDirectReason(reason)
	}
	return webmcp.NewClassifiedError(webmcp.ErrorNoEligibleTab, "no eligible WebMCP target was found", details)
}

func discoverDirectBrowsers(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig, browserID string) ([]webmcp.BrowserCandidate, error) {
	candidates, err := discoverDirectCandidates(ctx, broker, browser)
	if err != nil {
		return nil, err
	}
	if browserID != "" {
		for _, candidate := range candidates {
			if string(candidate.ID) == browserID {
				return []webmcp.BrowserCandidate{candidate}, nil
			}
		}
		return nil, webmcp.NewClassifiedError(webmcp.ErrorStaleSelection, "the selected browser is no longer current", map[string]any{
			"browser_id":          browserID,
			"target_id":           "",
			"selected_generation": uint64(0),
			"reason":              "browser_not_found",
		})
	}
	return candidates, nil
}

func discoverDirectCandidates(ctx context.Context, broker webmcp.Broker, browser config.BrowserConfig) ([]webmcp.BrowserCandidate, error) {
	if broker == nil {
		return nil, webmcpRuntimeUnavailableError("discovery")
	}
	candidates, err := broker.Discover(ctx, webmcp.DiscoverOptions{
		ExplicitOnly:     browser.Connection.CDPURL != "" || browser.Connection.WSEndpoint != "",
		AllowProcessScan: browser.Connection.AllowProcessScan,
		AllowRemoteCDP:   browser.Connection.AllowRemoteCDP,
	})
	if err != nil {
		return nil, err
	}
	candidates = normalizeDirectBrowserCandidates(candidates)
	if len(candidates) == 0 {
		return nil, webmcp.NewClassifiedError(webmcp.ErrorEndpointNotFound, "browser endpoint was not found", map[string]any{
			"endpoint_kind": endpointKindFor(browser),
			"source":        string(webmcp.DiscoverySourceConfigured),
		})
	}
	return candidates, nil
}

// directEndpointAuthority returns only the normalized endpoint authority used
// to recognize a reachable replacement. It is an internal comparison key;
// endpoint paths, query values, credentials, and websocket addresses never
// cross the CLI result boundary.
func directEndpointAuthority(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "ws":
			port = strconv.Itoa(80)
		case "https", "wss":
			port = strconv.Itoa(443)
		default:
			return ""
		}
	}
	return net.JoinHostPort(host, port)
}

func directCandidateMatchesEndpoint(candidate webmcp.BrowserCandidate, browser config.BrowserConfig) bool {
	configured := []string{browser.Connection.CDPURL, browser.Connection.WSEndpoint}
	discovered := []string{candidate.HTTPURL, candidate.BrowserWSURL}
	for _, configuredEndpoint := range configured {
		configuredAuthority := directEndpointAuthority(configuredEndpoint)
		if configuredAuthority == "" {
			continue
		}
		for _, discoveredEndpoint := range discovered {
			if configuredAuthority == directEndpointAuthority(discoveredEndpoint) {
				return true
			}
		}
	}
	return false
}

// directReplacementReason identifies a live candidate at the configured
// endpoint that is not the retained browser identity. The browser instance
// claim is authoritative when present; an absent claim is also fail-closed
// because endpoint, target, and page metadata cannot establish continuity.
func directReplacementReason(candidates []webmcp.BrowserCandidate, browser config.BrowserConfig, stored WebMCPSelection) (string, bool) {
	for _, candidate := range candidates {
		if candidate.ID == "" || string(candidate.ID) == stored.BrowserID || !directCandidateMatchesEndpoint(candidate, browser) {
			continue
		}
		if stored.BrowserInstanceID != "" && candidate.BrowserInstanceID == stored.BrowserInstanceID {
			return "endpoint_changed", true
		}
		return "browser_instance_changed", true
	}
	return "", false
}

// splitCompositeTargetRef accepts the exact "browserID/targetID" reference
// form that the tabs, context, and tools outputs print, so a listed reference
// can be handed back verbatim to --tab. Both halves must be valid opaque IDs;
// any other shape is left untouched and treated as a bare target ID.
func splitCompositeTargetRef(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	browserPart, targetPart, found := strings.Cut(value, "/")
	if !found {
		return "", "", false
	}
	browserID := normalizeDirectOpaqueID(browserPart)
	targetID := normalizeDirectOpaqueID(targetPart)
	if browserID == "" || targetID == "" {
		return "", "", false
	}
	return browserID, targetID, true
}
