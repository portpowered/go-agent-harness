package production

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

type staticActivePort struct {
	record discovery.ActivePortRecord
	err    error
}

func (r staticActivePort) Read(context.Context, string) (discovery.ActivePortRecord, error) {
	return r.record, r.err
}

type staticProcesses []discovery.ProcessInfo

func (p staticProcesses) List(context.Context) ([]discovery.ProcessInfo, error) { return p, nil }

type staticHTTP struct{ body string }

func (c staticHTTP) Do(request *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(c.body)), Request: request}, nil
}

func TestRecordersRememberObservedEndpointHints(t *testing.T) {
	owner := newTestComposition(config.DefaultBrowserConfig(), &fakeRuntime{})
	reader := &activePortRecorder{delegate: staticActivePort{record: discovery.ActivePortRecord{Port: 9222, BrowserWebSocketPath: "/devtools/browser/a"}}, owner: owner}
	if _, err := reader.Read(context.Background(), "/profile"); err != nil {
		t.Fatalf("active-port read: %v", err)
	}
	processes := &processRecorder{delegate: staticProcesses{
		{Endpoint: discovery.Endpoint{CDPURL: "http://127.0.0.1:9333/json/version"}},
		{},
	}, owner: owner}
	if _, err := processes.List(context.Background()); err != nil {
		t.Fatalf("process list: %v", err)
	}
	owner.mu.Lock()
	hints := append([]discovery.Endpoint(nil), owner.hints...)
	owner.mu.Unlock()
	if len(hints) != 2 || hints[0].BrowserWSEndpoint != "ws://127.0.0.1:9222/devtools/browser/a" || hints[1].CDPURL == "" {
		t.Fatalf("remembered hints = %+v", hints)
	}
	if _, err := (&activePortRecorder{}).Read(context.Background(), ""); err == nil {
		t.Fatal("delegate-less active-port reader succeeded")
	}
	if _, err := (&processRecorder{}).List(context.Background()); err == nil {
		t.Fatal("delegate-less process enumerator succeeded")
	}
	response, err := (&versionRecordingClient{}).Do(nil)
	if response != nil {
		closeResponse(t, response)
	}
	if err == nil {
		t.Fatal("delegate-less HTTP client succeeded")
	}
}

func closeResponse(t *testing.T, response *http.Response) {
	t.Helper()
	if err := response.Body.Close(); err != nil {
		t.Errorf("close response: %v", err)
	}
}

func TestVersionRecordingClientRemembersOnlyOpaqueWebSocketEndpoints(t *testing.T) {
	owner := newTestComposition(config.DefaultBrowserConfig(), &fakeRuntime{})
	owner.idMapper = discovery.HashIDMapper{}
	client := &versionRecordingClient{
		delegate: staticHTTP{body: `{"Browser":"Chrome/Test","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/a?token=1"}`},
		owner:    owner,
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:9222/json/version?secret=1", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	owner.mu.Lock()
	endpoints := len(owner.endpoints)
	var remembered discovery.Endpoint
	for _, endpoint := range owner.endpoints {
		remembered = endpoint
	}
	owner.mu.Unlock()
	if endpoints != 1 || remembered.CDPURL != "http://127.0.0.1:9222/json/version" || remembered.BrowserWSEndpoint != "ws://127.0.0.1:9222/devtools/browser/a" {
		t.Fatalf("remembered endpoints = %d %+v", endpoints, remembered)
	}
	owner.rememberVersionEndpoint(&url.URL{}, []byte(`{"webSocketDebuggerUrl":"http://not-ws/path"}`))
	owner.rememberVersionEndpoint(&url.URL{}, []byte(`not json`))
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if len(owner.endpoints) != 1 {
		t.Fatalf("invalid versions were remembered: %+v", owner.endpoints)
	}
}

type reconnectingDiscovery struct {
	managedCompositionDiscoveryFake
	reconnects int
}

func (d *reconnectingDiscovery) Reconnect(context.Context, discovery.ConnectionInputs, ...discovery.ReconnectOptions) (discovery.Selection, error) {
	d.reconnects++
	return discovery.Selection{}, nil
}

func (d *reconnectingDiscovery) LoadPersistedSelection(context.Context) (discovery.PersistedSelection, bool, error) {
	return discovery.PersistedSelection{BrowserID: "browser-a"}, true, nil
}

func TestManagedDiscoveryServiceDelegatesOptionalSeams(t *testing.T) {
	delegate := &reconnectingDiscovery{}
	service := &managedDiscoveryService{delegate: delegate}
	ctx := context.Background()
	if _, err := service.Reconnect(ctx, discovery.ConnectionInputs{}); err != nil || delegate.reconnects != 1 {
		t.Fatalf("reconnect = %v calls=%d", err, delegate.reconnects)
	}
	if record, ok, err := service.LoadPersistedSelection(ctx); err != nil || !ok || record.BrowserID != "browser-a" {
		t.Fatalf("persisted = %+v/%t/%v", record, ok, err)
	}
	if _, err := service.ListTargetSnapshot(ctx, discovery.BrowserCandidate{}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := service.Select(ctx, discovery.TargetSelectionRequest{}); err != nil {
		t.Fatalf("select: %v", err)
	}
	if _, err := service.RefreshSelection(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, ok := service.Selected(); ok {
		t.Fatal("unexpected selection")
	}
	plain := &managedDiscoveryService{delegate: &managedCompositionDiscoveryFake{}}
	if _, err := plain.Reconnect(ctx, discovery.ConnectionInputs{}); err == nil {
		t.Fatal("reconnect without delegate support succeeded")
	}
	if _, ok, err := plain.LoadPersistedSelection(ctx); ok || err != nil {
		t.Fatalf("persisted without loader = %t/%v", ok, err)
	}
	var missing *managedDiscoveryService
	if _, err := missing.DiscoverAll(ctx, discovery.ConnectionInputs{}); err == nil {
		t.Fatal("nil service discovered")
	}
	for name, err := range map[string]error{
		"list":      errorOf(missing.ListTargetSnapshot(ctx, discovery.BrowserCandidate{})),
		"select":    errorOf(missing.Select(ctx, discovery.TargetSelectionRequest{})),
		"refresh":   errorOf(missing.RefreshSelection(ctx)),
		"reconnect": errorOf(missing.Reconnect(ctx, discovery.ConnectionInputs{})),
	} {
		if err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("%s on nil service = %v", name, err)
		}
	}
	if _, _, err := missing.LoadPersistedSelection(ctx); err == nil {
		t.Fatal("nil service loaded persisted selection")
	}
	if _, ok := missing.Selected(); ok {
		t.Fatal("nil service reported a selection")
	}
}

func errorOf[T any](_ T, err error) error { return err }
