package cli

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

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
