package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

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
		DocumentReadyState:    target.DocumentReadyState,
		DocumentLoading:       target.DocumentLoading,
		DocumentLoadingKnown:  target.DocumentLoadingKnown,
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
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return
	}
	publicID, ok := discovery.BrowserIDForVersion(p.idMapper, version)
	if !ok || !productionOpaqueID(publicID) {
		return
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
