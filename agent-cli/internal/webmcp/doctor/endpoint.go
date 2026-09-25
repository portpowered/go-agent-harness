package doctor

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

const (
	scopeUnknown     = "unknown"
	scopeLoopback    = "loopback"
	scopeNonLoopback = "non_loopback"

	schemeWS  = "ws"
	schemeWSS = "wss"
)

// EndpointFor describes the configured endpoint lane with its address
// redacted.
func EndpointFor(browser config.BrowserConfig) Endpoint {
	endpoint := Endpoint{Source: "none configured", Scope: scopeUnknown}
	switch {
	case browser.Connection.CDPURL != "":
		endpoint.Source = "explicit HTTP URL"
		endpoint.Address = redactEndpoint(browser.Connection.CDPURL)
	case browser.Connection.WSEndpoint != "":
		endpoint.Source = "explicit WebSocket URL"
		endpoint.Address = redactEndpoint(browser.Connection.WSEndpoint)
	case browser.Connection.UserDataDir != "":
		endpoint.Source = "browser profile DevToolsActivePort"
		endpoint.Address = "<profile redacted>"
		endpoint.Scope = "local profile"
	case browser.Connection.AllowProcessScan:
		endpoint.Source = "process discovery"
		endpoint.Scope = "local process"
	}
	if endpoint.Address != "" && endpoint.Scope == scopeUnknown {
		endpoint.Scope = endpointScope(endpoint.Address)
	}
	return endpoint
}

// EndpointForCandidate describes a discovered browser endpoint with its
// address redacted.
func EndpointForCandidate(candidate webmcp.BrowserCandidate) Endpoint {
	endpoint := Endpoint{Source: "discovered browser endpoint", Scope: scopeUnknown}
	switch {
	case candidate.HTTPURL != "":
		endpoint.Source = "discovered HTTP URL"
		endpoint.Address = redactEndpoint(candidate.HTTPURL)
	case candidate.BrowserWSURL != "":
		endpoint.Source = "discovered WebSocket URL"
		endpoint.Address = redactEndpoint(candidate.BrowserWSURL)
	}
	if endpoint.Address != "" {
		endpoint.Scope = endpointScope(endpoint.Address)
	} else if candidate.Loopback {
		endpoint.Scope = scopeLoopback
	}
	return endpoint
}

// ValidateEndpoints rejects configured endpoints that are not absolute
// URLs of the expected schemes or that carry credentials.
func ValidateEndpoints(browser config.BrowserConfig) error {
	for _, endpoint := range []struct {
		name    string
		value   string
		schemes []string
	}{
		{name: "browser.connection.cdp_url", value: browser.Connection.CDPURL, schemes: []string{"http", "https"}},
		{name: "browser.connection.ws_endpoint", value: browser.Connection.WSEndpoint, schemes: []string{schemeWS, schemeWSS}},
	} {
		if endpoint.value == "" {
			continue
		}
		parsed, err := url.Parse(endpoint.value)
		if err != nil || parsed.Host == "" || !slices.Contains(endpoint.schemes, strings.ToLower(parsed.Scheme)) {
			return fmt.Errorf("%s is not a valid browser endpoint", endpoint.name)
		}
		if parsed.User != nil {
			return fmt.Errorf("%s must not contain endpoint credentials", endpoint.name)
		}
	}
	return nil
}

func endpointScope(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return scopeUnknown
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" {
		return scopeLoopback
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return scopeLoopback
	}
	return scopeNonLoopback
}

func redactEndpoint(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "<redacted endpoint>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	if parsed.Scheme == schemeWS || parsed.Scheme == schemeWSS {
		parsed.Path = "/<redacted>"
		parsed.RawPath = ""
	}
	return parsed.String()
}

func candidateIsLoopback(candidate webmcp.BrowserCandidate) bool {
	if candidate.Loopback {
		return true
	}
	if candidate.HTTPURL != "" {
		return endpointScope(candidate.HTTPURL) == scopeLoopback
	}
	if candidate.BrowserWSURL != "" {
		return endpointScope(candidate.BrowserWSURL) == scopeLoopback
	}
	return false
}

func browserFromCandidate(candidate webmcp.BrowserCandidate) Browser {
	scope := scopeUnknown
	if candidateIsLoopback(candidate) {
		scope = scopeLoopback
	} else if candidate.HTTPURL != "" {
		scope = endpointScope(candidate.HTTPURL)
	} else if candidate.BrowserWSURL != "" {
		scope = endpointScope(candidate.BrowserWSURL)
	}
	return Browser{
		ID:       string(candidate.ID),
		Product:  normalize.BoundedText(candidate.Product, maxProductLength),
		Protocol: normalize.BoundedText(candidate.Protocol, maxProtocolLength),
		Scope:    scope,
	}
}
