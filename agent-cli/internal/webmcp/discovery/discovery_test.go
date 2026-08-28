package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeHTTPClient struct {
	calls    []*http.Request
	response *http.Response
	err      error
}

func (f *fakeHTTPClient) Do(request *http.Request) (*http.Response, error) {
	f.calls = append(f.calls, request)
	return f.response, f.err
}

type fakeWebSocketProbe struct {
	calls   []string
	version BrowserVersion
	err     error
}

func (f *fakeWebSocketProbe) Probe(_ context.Context, endpoint string) (BrowserVersion, error) {
	f.calls = append(f.calls, endpoint)
	return f.version, f.err
}

type fakeActivePortReader struct {
	calls  []string
	record ActivePortRecord
	err    error
}

func (f *fakeActivePortReader) Read(_ context.Context, userDataDir string) (ActivePortRecord, error) {
	f.calls = append(f.calls, userDataDir)
	return f.record, f.err
}

type fakeProcessEnumerator struct {
	calls []int
	items []ProcessInfo
	err   error
}

func (f *fakeProcessEnumerator) List(_ context.Context) ([]ProcessInfo, error) {
	f.calls = append(f.calls, len(f.calls)+1)
	return f.items, f.err
}

type eventRecorder struct {
	events []Event
}

func (r *eventRecorder) Emit(event Event) {
	r.events = append(r.events, event)
}

func versionResponse(body string, status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func validVersionJSON(ws string) string {
	return `{"Browser":"Chrome/151.0.0.0","Protocol-Version":"1.3","webSocketDebuggerUrl":"` + ws + `"}`
}

func assertDiscoveryError(t *testing.T, err error, code Code) *DiscoveryError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got nil", code)
	}
	var discoveryErr *DiscoveryError
	if !errors.As(err, &discoveryErr) {
		t.Fatalf("error type = %T, want *DiscoveryError", err)
	}
	if discoveryErr.Code != code {
		t.Fatalf("error code = %q, want %q", discoveryErr.Code, code)
	}
	return discoveryErr
}

func TestDiscoverHTTPVersionNormalizesAndRedacts(t *testing.T) {
	httpClient := &fakeHTTPClient{response: versionResponse(validVersionJSON("ws://127.0.0.1:9222/devtools/browser/browser-token?secret=ws-secret#fragment"), http.StatusOK)}
	recorder := &eventRecorder{}
	service := New(Options{HTTPClient: httpClient, EventSink: recorder})

	candidate, err := service.Discover(context.Background(), ConnectionInputs{
		CDPURL: "http://127.0.0.1:9222?secret=http-secret#fragment",
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if candidate.Product != "Chrome/151.0.0.0" || candidate.Protocol != "1.3" || !candidate.Loopback {
		t.Fatalf("candidate = %#v, want normalized Chrome version and loopback", candidate)
	}
	if !publicIDPattern.MatchString(candidate.ID) || strings.Contains(candidate.ID, "127") || strings.Contains(candidate.ID, "/") {
		t.Fatalf("candidate ID = %q, want opaque identifier", candidate.ID)
	}
	if len(httpClient.calls) != 1 {
		t.Fatalf("HTTP calls = %d, want 1", len(httpClient.calls))
	}
	if got := httpClient.calls[0].URL.String(); got != "http://127.0.0.1:9222/json/version" {
		t.Fatalf("requested URL = %q, want query-free /json/version", got)
	}
	if got := eventTypes(recorder.events); strings.Join(got, ",") != "browser.discovery.started,browser.endpoint.version,browser.discovery.completed" {
		t.Fatalf("event types = %v, want started/version/completed", got)
	}
	if recorder.events[1].BrowserID != candidate.ID {
		t.Fatalf("version event browser ID = %q, want %q", recorder.events[1].BrowserID, candidate.ID)
	}
	encoded, marshalErr := json.Marshal(struct {
		Candidate BrowserCandidate
		Events    []Event
	}{candidate, recorder.events})
	if marshalErr != nil {
		t.Fatalf("marshal public discovery values: %v", marshalErr)
	}
	for _, secret := range []string{"http-secret", "ws-secret", "devtools/browser/browser-token", "ws://"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public discovery JSON contains %q: %s", secret, encoded)
		}
	}
}

func TestDiscoverStopsAfterHigherPrioritySuccess(t *testing.T) {
	httpClient := &fakeHTTPClient{response: versionResponse(validVersionJSON("ws://127.0.0.1:9222/devtools/browser/high"), http.StatusOK)}
	webSocket := &fakeWebSocketProbe{version: BrowserVersion{Browser: "Chrome/ignored", ProtocolVersion: "1.3", WebSocketDebuggerURL: "ws://127.0.0.1:9223/devtools/browser/lower"}}
	activePort := &fakeActivePortReader{record: ActivePortRecord{Port: 9224, BrowserWebSocketPath: "/devtools/browser/active"}}
	configuredCalls := 0
	process := &fakeProcessEnumerator{items: []ProcessInfo{{
		DebuggingEnabled: true,
		Endpoint:         Endpoint{CDPURL: "http://127.0.0.1:9225"},
	}}}
	service := New(Options{
		HTTPClient:        httpClient,
		WebSocketProbe:    webSocket,
		ActivePortReader:  activePort,
		ProcessEnumerator: process,
	})

	candidate, err := service.Discover(context.Background(), ConnectionInputs{
		CDPURL:            "http://127.0.0.1:9222",
		BrowserWSEndpoint: "ws://127.0.0.1:9223/devtools/browser/lower",
		UserDataDir:       "/profile",
		ConfiguredSources: []ConfiguredSource{ConfiguredSourceFunc{
			SourceName: "configured-profile",
			ResolveFunc: func(context.Context) (Endpoint, error) {
				configuredCalls++
				return Endpoint{CDPURL: "http://127.0.0.1:9226"}, nil
			},
		}},
		AllowProcessScan: true,
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if candidate.Source != SourceExplicitCDPHTTP {
		t.Fatalf("candidate source = %q, want %q", candidate.Source, SourceExplicitCDPHTTP)
	}
	if len(httpClient.calls) != 1 || len(webSocket.calls) != 0 || len(activePort.calls) != 0 || configuredCalls != 0 || len(process.calls) != 0 {
		t.Fatalf("lower-priority calls: HTTP=%d WS=%d active=%d configured=%d process=%d; want only first HTTP", len(httpClient.calls), len(webSocket.calls), len(activePort.calls), configuredCalls, len(process.calls))
	}
}

func TestDiscoverFallsThroughInOrderAndProcessScanIsOptIn(t *testing.T) {
	t.Run("explicit websocket follows failed HTTP", func(t *testing.T) {
		httpClient := &fakeHTTPClient{response: versionResponse("missing", http.StatusNotFound)}
		webSocket := &fakeWebSocketProbe{version: BrowserVersion{
			Browser:              "Chrome/151",
			ProtocolVersion:      "1.3",
			WebSocketDebuggerURL: "ws://127.0.0.1:9223/devtools/browser/second",
		}}
		activePort := &fakeActivePortReader{record: ActivePortRecord{Port: 9224, BrowserWebSocketPath: "/devtools/browser/active"}}
		service := New(Options{HTTPClient: httpClient, WebSocketProbe: webSocket, ActivePortReader: activePort})

		candidate, err := service.Discover(context.Background(), ConnectionInputs{
			CDPURL:            "http://127.0.0.1:9222",
			BrowserWSEndpoint: "ws://127.0.0.1:9223/devtools/browser/second?secret=gone#fragment",
			UserDataDir:       "/profile",
		})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
		if candidate.Source != SourceExplicitBrowserWS || len(httpClient.calls) != 1 || len(webSocket.calls) != 1 || len(activePort.calls) != 0 {
			t.Fatalf("candidate/calls = %#v, HTTP=%d WS=%d active=%d", candidate, len(httpClient.calls), len(webSocket.calls), len(activePort.calls))
		}
		if webSocket.calls[0] != "ws://127.0.0.1:9223/devtools/browser/second" {
			t.Fatalf("websocket probe endpoint = %q, want query-free endpoint", webSocket.calls[0])
		}
	})

	t.Run("process enumerator is never called when disabled", func(t *testing.T) {
		process := &fakeProcessEnumerator{items: []ProcessInfo{{
			DebuggingEnabled: true,
			Endpoint:         Endpoint{CDPURL: "http://127.0.0.1:9222"},
		}}}
		service := New(Options{ProcessEnumerator: process})
		_, err := service.Discover(context.Background(), ConnectionInputs{})
		assertDiscoveryError(t, err, CodeEndpointNotFound)
		if len(process.calls) != 0 {
			t.Fatalf("process calls = %d, want 0 when process scan is disabled", len(process.calls))
		}
	})

	t.Run("process enumerator is called only when enabled", func(t *testing.T) {
		httpClient := &fakeHTTPClient{response: versionResponse(validVersionJSON("ws://127.0.0.1:9230/devtools/browser/process"), http.StatusOK)}
		process := &fakeProcessEnumerator{items: []ProcessInfo{{
			PID:              42,
			DebuggingEnabled: true,
			Endpoint:         Endpoint{CDPURL: "http://127.0.0.1:9230"},
		}}}
		service := New(Options{HTTPClient: httpClient, ProcessEnumerator: process})
		candidate, err := service.Discover(context.Background(), ConnectionInputs{AllowProcessScan: true})
		if err != nil {
			t.Fatalf("Discover() error = %v", err)
		}
		if candidate.Source != SourceProcess || len(process.calls) != 1 || len(httpClient.calls) != 1 {
			t.Fatalf("candidate/calls = %#v, process=%d HTTP=%d", candidate, len(process.calls), len(httpClient.calls))
		}
	})
}

func TestDiscoverClassifiesEndpointFailuresWithoutLeakingEndpointData(t *testing.T) {
	tests := []struct {
		name       string
		inputs     ConnectionInputs
		client     *fakeHTTPClient
		wantCode   Code
		wantDetail map[string]any
		wantHTTP   int
	}{
		{
			name:     "missing endpoint",
			inputs:   ConnectionInputs{CDPURL: "http://127.0.0.1:9222"},
			client:   &fakeHTTPClient{response: versionResponse("", http.StatusNotFound)},
			wantCode: CodeEndpointNotFound,
			wantDetail: map[string]any{
				"endpoint_kind": "cdp_http",
				"source":        "explicit_cdp_http",
			},
			wantHTTP: 1,
		},
		{
			name:     "unreachable endpoint",
			inputs:   ConnectionInputs{CDPURL: "http://127.0.0.1:9222?token=secret"},
			client:   &fakeHTTPClient{err: errors.New("dial tcp user:pass@127.0.0.1:9222: refused")},
			wantCode: CodeEndpointUnreachable,
			wantDetail: map[string]any{
				"endpoint_kind": "cdp_http",
				"address_class": "loopback",
				"phase":         "version",
			},
			wantHTTP: 1,
		},
		{
			name:     "remote endpoint denied",
			inputs:   ConnectionInputs{CDPURL: "http://203.0.113.7:9222"},
			client:   &fakeHTTPClient{response: versionResponse("should not be read", http.StatusOK)},
			wantCode: CodeRemoteEndpointDenied,
			wantDetail: map[string]any{
				"endpoint_kind": "cdp_http",
				"network_class": "non_loopback",
				"required_flag": "browser-allow-remote-cdp",
			},
			wantHTTP: 0,
		},
		{
			name:     "malformed version JSON",
			inputs:   ConnectionInputs{CDPURL: "http://127.0.0.1:9222"},
			client:   &fakeHTTPClient{response: versionResponse(`{"Browser":`, http.StatusOK)},
			wantCode: CodeBrowserProtocolInvalid,
			wantDetail: map[string]any{
				"phase":       "version",
				"protocol":    "unknown",
				"reason_code": "malformed_json",
			},
			wantHTTP: 1,
		},
		{
			name:     "unsupported protocol",
			inputs:   ConnectionInputs{CDPURL: "http://127.0.0.1:9222"},
			client:   &fakeHTTPClient{response: versionResponse(`{"Browser":"Chrome/151","Protocol-Version":"2.0","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/id"}`, http.StatusOK)},
			wantCode: CodeBrowserProtocolInvalid,
			wantDetail: map[string]any{
				"phase":       "version",
				"protocol":    "2.0",
				"reason_code": "unsupported_protocol_version",
			},
			wantHTTP: 1,
		},
		{
			name:     "page websocket is not browser websocket",
			inputs:   ConnectionInputs{CDPURL: "http://127.0.0.1:9222"},
			client:   &fakeHTTPClient{response: versionResponse(validVersionJSON("ws://127.0.0.1:9222/devtools/page/page-id?token=secret"), http.StatusOK)},
			wantCode: CodeBrowserProtocolInvalid,
			wantDetail: map[string]any{
				"phase":       "version",
				"protocol":    "1.3",
				"reason_code": "page_websocket_not_browser_websocket",
			},
			wantHTTP: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New(Options{HTTPClient: test.client})
			_, err := service.Discover(context.Background(), test.inputs)
			discoveryErr := assertDiscoveryError(t, err, test.wantCode)
			if len(test.client.calls) != test.wantHTTP {
				t.Fatalf("HTTP calls = %d, want %d", len(test.client.calls), test.wantHTTP)
			}
			if !reflectDeepEqual(test.wantDetail, discoveryErr.Details) {
				t.Fatalf("details = %#v, want %#v", discoveryErr.Details, test.wantDetail)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "ws://") || strings.Contains(err.Error(), "127.0.0.1") {
				t.Fatalf("error message leaked endpoint data: %q", err)
			}
			encoded, marshalErr := json.Marshal(discoveryErr)
			if marshalErr != nil {
				t.Fatalf("marshal DiscoveryError: %v", marshalErr)
			}
			if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "user:pass") || strings.Contains(string(encoded), "ws://") {
				t.Fatalf("error JSON leaked endpoint data: %s", encoded)
			}
		})
	}
}

func TestDiscoverRejectsMalformedExplicitEndpointAndPageWebSocket(t *testing.T) {
	client := &fakeHTTPClient{response: versionResponse(validVersionJSON("ws://127.0.0.1:9222/devtools/browser/id"), http.StatusOK)}
	service := New(Options{HTTPClient: client})

	_, err := service.Discover(context.Background(), ConnectionInputs{CDPURL: "file:///tmp/debug"})
	protocolErr := assertDiscoveryError(t, err, CodeBrowserProtocolInvalid)
	if protocolErr.Details["reason_code"] != "unsupported_endpoint_scheme" {
		t.Fatalf("malformed endpoint reason = %#v", protocolErr.Details)
	}
	if len(client.calls) != 0 {
		t.Fatalf("malformed endpoint HTTP calls = %d, want 0", len(client.calls))
	}

	_, err = service.Discover(context.Background(), ConnectionInputs{BrowserWSEndpoint: "ws://127.0.0.1:9222/devtools/page/page-id"})
	protocolErr = assertDiscoveryError(t, err, CodeBrowserProtocolInvalid)
	if protocolErr.Details["reason_code"] != "page_websocket_not_browser_websocket" {
		t.Fatalf("page websocket reason = %#v", protocolErr.Details)
	}
}

func TestFileActivePortReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DevToolsActivePort")
	if err := os.WriteFile(path, []byte("9222\n/devtools/browser/active-id\n"), 0o600); err != nil {
		t.Fatalf("write active port: %v", err)
	}
	record, err := (FileActivePortReader{}).Read(context.Background(), dir)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if record != (ActivePortRecord{Port: 9222, BrowserWebSocketPath: "/devtools/browser/active-id"}) {
		t.Fatalf("record = %#v", record)
	}

	if err := os.WriteFile(path, []byte("not-a-port\n/devtools/browser/active-id\n"), 0o600); err != nil {
		t.Fatalf("write malformed active port: %v", err)
	}
	if _, err := (FileActivePortReader{}).Read(context.Background(), dir); err == nil {
		t.Fatal("malformed active port returned nil error")
	}
}

func eventTypes(events []Event) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, string(event.Type))
	}
	return types
}

// reflect.DeepEqual is kept behind a tiny helper so map values in this test
// stay readable without adding a dependency to the production package.
func reflectDeepEqual(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}
