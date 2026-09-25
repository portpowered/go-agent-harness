package production

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/production/normalize"
)

// The recorders below wrap discovery's host seams so every raw endpoint the
// discovery service observes is remembered by the composition. Discovery then
// publishes only normalized IDs while the runtime can still reopen the exact
// browser behind one of them.

type activePortRecorder struct {
	delegate discovery.ActivePortReader
	owner    *composition
}

func (r *activePortRecorder) Read(ctx context.Context, userDataDir string) (discovery.ActivePortRecord, error) {
	if r == nil || r.delegate == nil {
		return discovery.ActivePortRecord{}, errors.New("active-port reader is unavailable")
	}
	record, err := r.delegate.Read(ctx, userDataDir)
	if err == nil && r.owner != nil {
		if endpoint, endpointErr := normalize.EndpointFromActivePort(record); endpointErr == nil {
			r.owner.rememberEndpointHint(endpoint)
		}
	}
	return record, err
}

type processRecorder struct {
	delegate discovery.ProcessEnumerator
	owner    *composition
}

func (e *processRecorder) List(ctx context.Context) ([]discovery.ProcessInfo, error) {
	if e == nil || e.delegate == nil {
		return nil, errors.New("process enumerator is unavailable")
	}
	infos, err := e.delegate.List(ctx)
	if err == nil && e.owner != nil {
		for _, info := range infos {
			if !endpointEmpty(info.Endpoint) {
				e.owner.rememberEndpointHint(info.Endpoint)
			}
		}
	}
	return infos, err
}

type versionRecordingClient struct {
	delegate discovery.HTTPClient
	owner    *composition
}

func (c *versionRecordingClient) Do(request *http.Request) (*http.Response, error) {
	if c == nil || c.delegate == nil {
		return nil, errors.New("HTTP client is unavailable")
	}
	response, err := c.delegate.Do(request)
	if err != nil || response == nil || response.Body == nil || c.owner == nil || request == nil {
		return response, err
	}
	if !strings.HasSuffix(strings.TrimRight(request.URL.Path, "/"), "/json/version") {
		return response, nil
	}
	response.Body = &versionBody{
		ReadCloser: response.Body,
		requestURL: request.URL,
		owner:      c.owner,
	}
	return response, nil
}

// versionBody tees a /json/version response so the composition can remember
// the browser websocket endpoint once discovery has consumed the body.
type versionBody struct {
	io.ReadCloser
	requestURL *url.URL
	owner      *composition
	data       bytes.Buffer
	once       sync.Once
}

func (b *versionBody) Read(data []byte) (int, error) {
	count, err := b.ReadCloser.Read(data)
	if count > 0 {
		_, _ = b.data.Write(data[:count])
	}
	return count, err
}

func (b *versionBody) Close() error {
	b.once.Do(func() {
		if b.owner != nil && b.requestURL != nil {
			b.owner.rememberVersionEndpoint(b.requestURL, b.data.Bytes())
		}
	})
	return b.ReadCloser.Close()
}

func (p *composition) rememberVersionEndpoint(requestURL *url.URL, data []byte) {
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
	if !ok || !normalize.OpaqueID(publicID) {
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
