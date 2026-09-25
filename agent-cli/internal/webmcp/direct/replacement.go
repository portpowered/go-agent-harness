package direct

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
)

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
	schemeWS    = "ws"
	schemeWSS   = "wss"

	defaultPortInsecure = 80
	defaultPortSecure   = 443
	maxPort             = 65535
)

// endpointAuthority returns only the normalized endpoint authority used to
// recognize a reachable replacement. It is an internal comparison key;
// endpoint paths, query values, credentials, and websocket addresses never
// cross the CLI result boundary.
func endpointAuthority(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" {
		switch strings.ToLower(parsed.Scheme) {
		case schemeHTTP, schemeWS:
			port = strconv.Itoa(defaultPortInsecure)
		case schemeHTTPS, schemeWSS:
			port = strconv.Itoa(defaultPortSecure)
		default:
			return ""
		}
	}
	return net.JoinHostPort(host, port)
}

func candidateMatchesEndpoint(candidate webmcp.BrowserCandidate, browser config.BrowserConfig) bool {
	configured := []string{browser.Connection.CDPURL, browser.Connection.WSEndpoint}
	discovered := []string{candidate.HTTPURL, candidate.BrowserWSURL}
	for _, configuredEndpoint := range configured {
		configuredAuthority := endpointAuthority(configuredEndpoint)
		if configuredAuthority == "" {
			continue
		}
		for _, discoveredEndpoint := range discovered {
			if configuredAuthority == endpointAuthority(discoveredEndpoint) {
				return true
			}
		}
	}
	return false
}

// ReplacementReason identifies a live candidate at the configured endpoint
// that is not the retained browser identity. The browser instance claim is
// authoritative when present; an absent claim is also fail-closed because
// endpoint, target, and page metadata cannot establish continuity.
func ReplacementReason(candidates []webmcp.BrowserCandidate, browser config.BrowserConfig, stored selectionstore.Selection) (string, bool) {
	for _, candidate := range candidates {
		if candidate.ID == "" || string(candidate.ID) == stored.BrowserID || !candidateMatchesEndpoint(candidate, browser) {
			continue
		}
		if stored.BrowserInstanceID != "" && candidate.BrowserInstanceID == stored.BrowserInstanceID {
			return "endpoint_changed", true
		}
		return "browser_instance_changed", true
	}
	return "", false
}
