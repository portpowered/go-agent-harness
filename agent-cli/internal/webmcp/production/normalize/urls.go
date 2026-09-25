// Package normalize owns the pure identity, URL-safety, and error translation
// rules shared by the production WebMCP composition. Nothing here performs I/O:
// callers hand in raw discovery or runtime values and receive the redacted,
// browser-neutral forms that may cross the public broker boundary.
package normalize

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
	schemeWS    = "ws"
	schemeWSS   = "wss"

	maxActivePort = 65535

	errActivePortPathInvalid = "active-port websocket path is invalid"
)

// HTTPTransportURL strips query and fragment data from an HTTP(S) endpoint.
// Values that are not HTTP(S) URLs are returned trimmed but otherwise intact.
func HTTPTransportURL(raw string) string {
	return transportURL(raw, schemeHTTP, schemeHTTPS)
}

// WSTransportURL strips query and fragment data from a WS(S) endpoint.
// Values that are not WS(S) URLs are returned trimmed but otherwise intact.
func WSTransportURL(raw string) string {
	return transportURL(raw, schemeWS, schemeWSS)
}

func transportURL(raw string, schemes ...string) string {
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

// SafePageURL returns an HTTP(S) page URL without credentials, query, or
// fragment. Any other value is reduced to the empty string.
func SafePageURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// SafeOrigin reduces a URL or origin to its lower-cased scheme://host form.
func SafeOrigin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}

// EndpointFromActivePort converts a DevToolsActivePort record into the raw
// loopback endpoint pair used only inside the runtime boundary.
func EndpointFromActivePort(record discovery.ActivePortRecord) (discovery.Endpoint, error) {
	if record.Port < 1 || record.Port > maxActivePort {
		return discovery.Endpoint{}, errors.New("active-port port is invalid")
	}
	path := strings.TrimSpace(record.BrowserWebSocketPath)
	if path == "" || strings.ContainsAny(path, "\r\n") {
		return discovery.Endpoint{}, errors.New(errActivePortPathInvalid)
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
		return discovery.Endpoint{}, errors.New(errActivePortPathInvalid)
	}
	return discovery.Endpoint{
		CDPURL:            fmt.Sprintf("http://127.0.0.1:%d/json/version", record.Port),
		BrowserWSEndpoint: fmt.Sprintf("ws://127.0.0.1:%d%s", record.Port, path),
	}, nil
}
