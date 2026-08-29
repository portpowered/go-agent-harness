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

const directNormalizedIDMaxLength = 64

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

func directEligibleTargetMatches(targets []webmcp.Target, browser config.BrowserConfig) []webmcp.Target {
	matches := make([]webmcp.Target, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, possible := range targets {
		id := normalizeDirectOpaqueID(string(possible.ID))
		if id == "" || (possible.Type != "" && !strings.EqualFold(possible.Type, "page")) || !possible.Eligible {
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
// C0 no_eligible_tab envelope. The broker target list is the complete
// enumeration at this boundary, so its length is the useful candidate count
// even when eligibility filtering removes every target.
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
