package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

func TestProductionWebMCPDoctorUsesLaneBTargetsAndNeutralRuntime(t *testing.T) {
	server, browserID, targetID, runtime := newProductionTestEndpoint(t)
	defer server.Close()

	configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: %q
  selection:
    browser: %q
    tab: %q
    persist: false
`, server.URL+"/json/version?secret=redact#fragment", browserID, targetID))

	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionHTTPClient(server.Client()),
	)
	command, stdout, stderr := executeDoctorCommand(t, configDir, factory, "--json")
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("production doctor: %v", err)
	}
	if stderr.String() != "" {
		t.Fatalf("production doctor stderr = %q", stderr.String())
	}
	report := decodeDoctorReport(t, stdout.String())
	if report.Status != doctorStatusReady || report.Error != nil {
		t.Fatalf("production doctor status/error = %s/%+v", report.Status, report.Error)
	}
	if len(report.Browsers) != 1 || report.Browsers[0].ID != browserID {
		t.Fatalf("production browser report = %+v", report.Browsers)
	}
	if report.PageTargets != 1 || report.EligiblePages != 1 || report.SelectedPage == nil || report.SelectedPage.TargetID != targetID {
		t.Fatalf("production target report = %+v counts=%d/%d", report.SelectedPage, report.PageTargets, report.EligiblePages)
	}
	if report.Catalog.ToolCount != 1 || !report.Catalog.Ready {
		t.Fatalf("production catalog = %+v", report.Catalog)
	}
	encoded := stdout.String()
	for _, secret := range []string{"secret=redact", "#fragment", "/devtools/browser/browser-token", "/devtools/page/raw-tab"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("production doctor exposed %q: %s", secret, encoded)
		}
	}
	if runtime.count("activate") != 0 {
		t.Fatalf("doctor activated a tab: %v", runtime.operationSnapshot())
	}
	if runtime.openHandleCount() != runtime.closeHandleCount() {
		t.Fatalf("production handles opened/closed = %d/%d: %v", runtime.openHandleCount(), runtime.closeHandleCount(), runtime.operationSnapshot())
	}
	if runtime.sessionCount("close") == 0 {
		t.Fatalf("production runtime never detached a target: %v", runtime.operationSnapshot())
	}
}

func TestProductionWebMCPDirectListsNormalizedBrowsersAndTabs(t *testing.T) {
	server, browserID, targetID, runtime := newProductionTestEndpoint(t)
	defer server.Close()
	configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  connection:
    cdp_url: %q
  selection:
    persist: false
`, server.URL+"/json/version?secret=redact#fragment"))
	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionHTTPClient(server.Client()),
	)

	browsers := executeDirectCommand(t, configDir, nil, factory, "browsers", "--json")
	browserEnvelope := requireDirectSuccess(t, browsers)
	var browserData WebMCPDirectBrowsersData
	decodeDirectData(t, browserEnvelope.Data, &browserData)
	if len(browserData.Browsers) != 1 || browserData.Browsers[0].ID != browserID || browserData.Browsers[0].Scope != "loopback" {
		t.Fatalf("production browsers = %+v", browserData)
	}

	tabs := executeDirectCommand(t, configDir, nil, factory, "tabs", "--browser", browserID, "--eligible", "--json")
	tabEnvelope := requireDirectSuccess(t, tabs)
	var tabData WebMCPDirectTabsData
	decodeDirectData(t, tabEnvelope.Data, &tabData)
	if len(tabData.Tabs) != 1 || tabData.Tabs[0].TargetID != targetID || tabData.Tabs[0].Origin != "https://fixture.test" || !tabData.Tabs[0].Eligible {
		t.Fatalf("production tabs = %+v", tabData)
	}
	for _, output := range []string{browsers.stdout, tabs.stdout} {
		for _, secret := range []string{"secret=redact", "#fragment", "/devtools/browser/browser-token", "/devtools/page/raw-tab"} {
			if strings.Contains(output, secret) {
				t.Fatalf("production listing exposed %q: %s", secret, output)
			}
		}
	}
	if runtime.count("activate") != 0 {
		t.Fatalf("listing activated a tab: %v", runtime.operationSnapshot())
	}
}

func TestProductionWebMCPHandlePreservesOpenTabAndNormalizesTargetIdentity(t *testing.T) {
	runtime := &productionFakeRuntime{openedTarget: webmcp.Target{
		ID:               "raw-opened-tab",
		Type:             "page",
		Title:            "Opened fixture",
		URL:              "https://opened.example.test/page?visible=yes#section",
		Origin:           "https://opened.example.test",
		WebSocketURL:     "ws://127.0.0.1/devtools/page/raw-opened-tab",
		ContinuityMarker: "raw-document-token",
		Eligible:         true,
	}}
	raw := &productionFakeHandle{runtime: runtime, candidate: webmcp.BrowserCandidate{ID: "raw-browser"}}
	owner := &productionWebMCPComposition{targetIDMapper: discovery.HashTargetIDMapper{}}
	handle := &productionWebMCPHandle{
		owner:     owner,
		candidate: webmcp.BrowserCandidate{ID: "browser-public"},
		raw:       raw,
		closed:    make(chan struct{}),
	}

	opened, err := handle.OpenTab(context.Background(), "https://opened.example.test/page?visible=yes#section")
	if err != nil {
		t.Fatalf("production open tab: %v", err)
	}
	wantID := discovery.HashTargetIDMapper{}.TargetID(discovery.TargetIdentity{BrowserID: "browser-public", RawID: "raw-opened-tab"})
	if opened.BrowserID != "browser-public" || string(opened.ID) != wantID || opened.URL != "https://opened.example.test/page" || opened.Origin != "https://opened.example.test" {
		t.Fatalf("normalized opened target = %+v, want browser=%q target=%q", opened, "browser-public", wantID)
	}
	if opened.WebSocketURL != "" || opened.ContinuityMarker != "" {
		t.Fatalf("opened target exposed transport identity: %+v", opened)
	}
	if runtime.count("open_tab") != 1 || runtime.openedURL != "https://opened.example.test/page?visible=yes#section" {
		t.Fatalf("raw open operations = %v URL=%q", runtime.operationSnapshot(), runtime.openedURL)
	}
}

func TestProductionWebMCPSessionRebasesRawGenerationForPersistedSelection(t *testing.T) {
	runtime := &productionFakeRuntime{}
	raw := &productionFakeSession{
		runtime: runtime,
		page: webmcp.PageContext{
			Key:        webmcp.PageKey{BrowserID: "browser-a", TargetID: "target-a"},
			Generation: 1,
			Connected:  true,
		},
		tool: webmcp.ToolDescriptor{
			Name:        "read_state",
			FrameID:     "frame-a",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
	}
	sessionValue, err := newProductionWebMCPSession(raw, webmcp.Target{
		BrowserID:  "browser-a",
		ID:         "target-a",
		Generation: 7,
	})
	if err != nil {
		t.Fatalf("construct production session: %v", err)
	}
	defer func() { _ = sessionValue.Close() }()

	if err := sessionValue.EnableWebMCP(context.Background()); err != nil {
		t.Fatalf("enable production session: %v", err)
	}
	select {
	case event := <-sessionValue.Events():
		if event.Type != webmcp.EventToolsAdded || event.Generation != 7 || len(event.Tools) != 1 || event.Tools[0].Generation != 7 {
			t.Fatalf("rebased production event = %+v, want generation seven", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rebased production catalog event")
	}
}

func TestProductionWebMCPSessionMapsRawScreenshotIdentity(t *testing.T) {
	raw := &productionScreenshotSession{
		productionFakeSession: &productionFakeSession{
			runtime: &productionFakeRuntime{},
			page: webmcp.PageContext{
				Key:        webmcp.PageKey{BrowserID: "browser-raw", TargetID: "raw-target"},
				Generation: 1,
				Connected:  true,
			},
		},
		screenshot: webmcp.PageScreenshot{
			BrowserID: "browser-raw",
			TargetID:  "raw-target",
			MIMEType:  "image/png",
			Bytes:     []byte{1, 2, 3},
			Width:     320,
			Height:    200,
		},
	}
	session, err := newProductionWebMCPSession(raw, webmcp.Target{
		BrowserID:  "browser-public",
		ID:         "target-public",
		Generation: 4,
	})
	if err != nil {
		t.Fatalf("construct production session: %v", err)
	}
	defer func() { _ = session.Close() }()

	capturer, ok := session.(webmcp.PageScreenshotter)
	if !ok {
		t.Fatal("production session does not expose page capture")
	}
	got, err := capturer.CapturePageScreenshot(context.Background())
	if err != nil {
		t.Fatalf("capture production screenshot: %v", err)
	}
	if got.BrowserID != "browser-public" || got.TargetID != "target-public" {
		t.Fatalf("production screenshot identity = %q/%q, want public selection", got.BrowserID, got.TargetID)
	}
	if string(got.Bytes) != string([]byte{1, 2, 3}) || got.MIMEType != "image/png" || got.Width != 320 || got.Height != 200 {
		t.Fatalf("production screenshot payload = %+v, want raw capture preserved", got)
	}
}

func TestProductionWebMCPCLIFreshTabsReferenceSurvivesIncarnationChurn(t *testing.T) {
	var server *httptest.Server
	var mu sync.Mutex
	versionCalls := 0
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/version" {
			http.NotFound(writer, request)
			return
		}
		mu.Lock()
		versionCalls++
		instance := fmt.Sprintf("process-local-%d", versionCalls)
		mu.Unlock()
		browserWebSocket := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/stable"
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"Browser":"Chrome/Test","Protocol-Version":"1.3","webSocketDebuggerUrl":%q,"browserInstanceId":%q}`, browserWebSocket, instance)
	}))
	t.Cleanup(server.Close)

	runtime := &productionFakeRuntime{
		targets: []webmcp.Target{{
			ID:               "raw-tab",
			Type:             "page",
			Title:            "Fresh-process fixture",
			URL:              "https://fixture.test/page",
			Origin:           "https://fixture.test",
			WebSocketURL:     "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/page/raw-tab",
			ContinuityMarker: "document-a",
		}},
		tool: webmcp.ToolDescriptor{
			Name:        "read_state",
			Description: "Read the fixture state.",
			FrameID:     "frame-1",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		},
	}

	configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: %q
  selection:
    persist: true
`, server.URL+"/json/version"))
	newFactory := func() WebMCPDoctorFactory {
		return NewProductionWebMCPDoctorFactory(
			WithWebMCPProductionRuntime(runtime),
			WithWebMCPProductionHTTPClient(server.Client()),
		)
	}

	tabs := executeShippedWebMCPCommand(t, configDir, newFactory(), "tabs", "--eligible", "--json")
	tabsEnvelope := requireDirectSuccess(t, tabs)
	var tabsData WebMCPDirectTabsData
	decodeDirectData(t, tabsEnvelope.Data, &tabsData)
	if len(tabsData.Tabs) != 1 || tabsData.Tabs[0].BrowserID == "" || tabsData.Tabs[0].TargetID == "" || !tabsData.Tabs[0].Eligible {
		t.Fatalf("fresh-process tabs = %+v", tabsData)
	}
	listed := tabsData.Tabs[0]

	selected := executeShippedWebMCPCommand(t, configDir, newFactory(), "select", "--tab", listed.TargetID, "--json")
	selectionEnvelope := requireDirectSuccess(t, selected)
	var selectedData WebMCPDirectContext
	decodeDirectData(t, selectionEnvelope.Data, &selectedData)
	if selectedData.BrowserID != listed.BrowserID || selectedData.TargetID != listed.TargetID ||
		!selectedData.Connected || !selectedData.Ready || !selectedData.CatalogReady || selectedData.Generation == 0 || selectedData.ToolCount != 1 {
		t.Fatalf("fresh-process selection = %+v, listed=%+v", selectedData, listed)
	}
	selection, err := NewFileWebMCPSelectionStore(configDir).Load()
	if err != nil {
		t.Fatalf("load fresh-process selection: %v", err)
	}
	if selection.BrowserID != listed.BrowserID || selection.TargetID != listed.TargetID || selection.Generation == 0 || selection.Origin != listed.Origin || selection.ContinuityMarker == "" {
		t.Fatalf("persisted fresh-process selection = %+v, listed=%+v", selection, listed)
	}
	mu.Lock()
	gotVersionCalls := versionCalls
	mu.Unlock()
	if gotVersionCalls != 2 {
		t.Fatalf("version calls = %d, want one discovery per CLI process", gotVersionCalls)
	}
}

func TestProductionWebMCPDirectSelectRecoversRestartedBrowserAndActivatesPersistedTarget(t *testing.T) {
	var server *httptest.Server
	var mu sync.Mutex
	versionCalls := 0
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/version" {
			http.NotFound(writer, request)
			return
		}
		mu.Lock()
		versionCalls++
		path := "/devtools/browser/restarted-old"
		instance := "restarted-old-instance"
		if versionCalls >= 2 {
			path = "/devtools/browser/restarted-new"
			instance = "restarted-new-instance"
		}
		mu.Unlock()
		browserWebSocket := "ws" + strings.TrimPrefix(server.URL, "http") + path
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"Browser":"Chrome/Test","Protocol-Version":"1.3","webSocketDebuggerUrl":%q,"browserInstanceId":%q}`, browserWebSocket, instance)
	}))
	t.Cleanup(server.Close)
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse restart fixture URL: %v", err)
	}

	oldBrowserID := discovery.HashIDMapper{}.BrowserID(discovery.BrowserIdentity{
		Scheme: "ws",
		Host:   "127.0.0.1",
		Port:   serverURL.Port(),
		Path:   "/devtools/browser/restarted-old",
	})
	newBrowserID := discovery.HashIDMapper{}.BrowserID(discovery.BrowserIdentity{
		Scheme: "ws",
		Host:   "127.0.0.1",
		Port:   serverURL.Port(),
		Path:   "/devtools/browser/restarted-new",
	})
	rawTargetID := "raw-restarted-tab"
	oldTargetID := discovery.HashTargetIDMapper{}.TargetID(discovery.TargetIdentity{BrowserID: oldBrowserID, RawID: rawTargetID})
	newTargetID := discovery.HashTargetIDMapper{}.TargetID(discovery.TargetIdentity{BrowserID: newBrowserID, RawID: rawTargetID})
	runtime := &productionFakeRuntime{
		targets: []webmcp.Target{{
			ID:               webmcp.TargetID(rawTargetID),
			Type:             "page",
			Title:            "Restart recovery fixture",
			URL:              "https://fixture.test/restart",
			Origin:           "https://fixture.test",
			WebSocketURL:     "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/page/" + rawTargetID,
			ContinuityMarker: "restart-document",
			Generation:       1,
		}},
		tool: webmcp.ToolDescriptor{
			Name:        "read_state",
			Description: "Read restart recovery state",
			FrameID:     "frame-1",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		},
	}
	configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  tools:
    enabled: true
    backend: webmcp
  connection:
    cdp_url: %q
  selection:
    persist: true
`, server.URL+"/json/version"))
	store := NewFileWebMCPSelectionStore(configDir)
	newFactory := func() WebMCPDoctorFactory {
		return NewProductionWebMCPDoctorFactory(
			WithWebMCPProductionRuntime(runtime),
			WithWebMCPProductionHTTPClient(server.Client()),
		)
	}

	// First CLI invocation records the browser before its restart.
	initial := executeDirectCommand(t, configDir, store, newFactory(), "select", "--browser", oldBrowserID, "--tab", string(oldTargetID), "--json")
	requireDirectSuccess(t, initial)
	oldRecord, err := store.Load()
	if err != nil {
		t.Fatalf("load pre-restart selection: %v", err)
	}
	if oldRecord.BrowserID != oldBrowserID || oldRecord.TargetID != string(oldTargetID) || oldRecord.BrowserInstanceID == "" {
		t.Fatalf("pre-restart selection = %+v", oldRecord)
	}

	// The next invocation sees the same endpoint and page URL, but a fresh
	// browser identity. Explicit select recovery must not be blocked by the
	// old record.
	recovered := executeDirectCommand(t, configDir, store, newFactory(), "select", "--auto-select", "single", "--json")
	requireDirectSuccess(t, recovered)
	newRecord, err := store.Load()
	if err != nil {
		t.Fatalf("load post-restart selection: %v", err)
	}
	if newRecord.BrowserID != newBrowserID || newRecord.TargetID != string(newTargetID) || newRecord.BrowserInstanceID == oldRecord.BrowserInstanceID {
		t.Fatalf("post-restart selection = %+v, old=%+v", newRecord, oldRecord)
	}

	// Activation is a separate invocation and must consume the replacement
	// record without requiring the caller to repeat either ID.
	activation := executeDirectCommand(t, configDir, store, newFactory(), "activate", "--json")
	requireDirectSuccess(t, activation)
	if runtime.count("activate") != 1 {
		t.Fatalf("restart recovery activation operations = %v", runtime.operationSnapshot())
	}
	mu.Lock()
	gotVersionCalls := versionCalls
	mu.Unlock()
	if gotVersionCalls != 3 {
		t.Fatalf("restart recovery version calls = %d, want one per CLI invocation", gotVersionCalls)
	}
}

func TestDefaultWebMCPDirectFactoryUsesProductionDiscovery(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/version" {
			http.NotFound(writer, request)
			return
		}
		browserWebSocket := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/default-browser"
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"Browser":"Chrome/Default","Protocol-Version":"1.3","webSocketDebuggerUrl":%q}`, browserWebSocket)
	}))
	defer server.Close()

	configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  connection:
    cdp_url: %q
  selection:
    persist: false
`, server.URL+"/json/version"))
	result := executeDirectCommand(t, configDir, nil, nil, "browsers", "--json")
	if result.err != nil {
		t.Fatalf("default browsers: %v\nstdout=%s\nstderr=%s", result.err, result.stdout, result.stderr)
	}
	envelope := requireDirectSuccess(t, result)
	var data WebMCPDirectBrowsersData
	decodeDirectData(t, envelope.Data, &data)
	if len(data.Browsers) != 1 || data.Browsers[0].Product != "Chrome/Default" || data.Browsers[0].Protocol != "1.3" {
		t.Fatalf("default discovery browsers = %+v", data.Browsers)
	}
}

func TestProductionWebMCPDirectFailuresRemainClassifiedAndFailClosed(t *testing.T) {
	t.Run("endpoint unreachable", func(t *testing.T) {
		server, _, _, runtime := newProductionTestEndpoint(t)
		server.Close()
		configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  connection:
    cdp_url: %q
  selection:
    persist: false
`, server.URL+"/json/version"))
		factory := NewProductionWebMCPDoctorFactory(
			WithWebMCPProductionRuntime(runtime),
			WithWebMCPProductionHTTPClient(server.Client()),
		)
		result := executeDirectCommand(t, configDir, nil, factory, "browsers", "--json")
		envelope := decodeDirectEnvelope(t, result.stdout)
		if result.err == nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorEndpointUnreachable) {
			t.Fatalf("unreachable result = %+v/%v", envelope, result.err)
		}
		if runtime.count("open") != 0 {
			t.Fatalf("unreachable endpoint opened runtime handles: %v", runtime.operationSnapshot())
		}
	})

	t.Run("remote endpoint denied", func(t *testing.T) {
		_, _, _, runtime := newProductionTestEndpoint(t)
		configDir := writeDoctorConfig(t, `
browser:
  connection:
    cdp_url: http://192.0.2.1:9222
  selection:
    persist: false
`)
		factory := NewProductionWebMCPDoctorFactory(WithWebMCPProductionRuntime(runtime))
		result := executeDirectCommand(t, configDir, nil, factory, "browsers", "--json")
		envelope := decodeDirectEnvelope(t, result.stdout)
		if result.err == nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorRemoteEndpointDenied) {
			t.Fatalf("remote-denied result = %+v/%v", envelope, result.err)
		}
		if runtime.count("open") != 0 {
			t.Fatalf("remote-denied endpoint opened runtime handles: %v", runtime.operationSnapshot())
		}
	})

	t.Run("ambiguous target", func(t *testing.T) {
		server, browserID, _, runtime := newProductionTestEndpoint(t)
		defer server.Close()
		runtime.mu.Lock()
		second := runtime.targets[0]
		second.ID = "raw-tab-2"
		second.Title = "Second fixture"
		second.URL = "https://fixture.test/second"
		runtime.targets = append(runtime.targets, second)
		runtime.mu.Unlock()
		configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  connection:
    cdp_url: %q
  selection:
    browser: %q
    auto_select: single
    persist: false
`, server.URL+"/json/version", browserID))
		factory := NewProductionWebMCPDoctorFactory(
			WithWebMCPProductionRuntime(runtime),
			WithWebMCPProductionHTTPClient(server.Client()),
		)
		result := executeDirectCommand(t, configDir, nil, factory, "select", "--browser", browserID, "--json")
		envelope := decodeDirectEnvelope(t, result.stdout)
		if result.err == nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorAmbiguousTab) {
			t.Fatalf("ambiguous result = %+v/%v", envelope, result.err)
		}
	})

	t.Run("no eligible target", func(t *testing.T) {
		server, browserID, _, runtime := newProductionTestEndpoint(t)
		defer server.Close()
		runtime.mu.Lock()
		runtime.targets = nil
		runtime.mu.Unlock()
		configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  connection:
    cdp_url: %q
  selection:
    browser: %q
    auto_select: single
    persist: false
`, server.URL+"/json/version", browserID))
		factory := NewProductionWebMCPDoctorFactory(
			WithWebMCPProductionRuntime(runtime),
			WithWebMCPProductionHTTPClient(server.Client()),
		)
		result := executeDirectCommand(t, configDir, nil, factory, "select", "--browser", browserID, "--json")
		envelope := decodeDirectEnvelope(t, result.stdout)
		if result.err == nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorNoEligibleTab) {
			t.Fatalf("no-eligible result = %+v/%v", envelope, result.err)
		}
	})

	t.Run("persistence failure", func(t *testing.T) {
		server, browserID, targetID, runtime := newProductionTestEndpoint(t)
		defer server.Close()
		store := &failingProductionSelectionStore{}
		configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  connection:
    cdp_url: %q
  selection:
    persist: true
`, server.URL+"/json/version"))
		factory := NewProductionWebMCPDoctorFactory(
			WithWebMCPProductionRuntime(runtime),
			WithWebMCPProductionHTTPClient(server.Client()),
			WithWebMCPProductionSelectionStore(store),
		)
		result := executeDirectCommand(t, configDir, nil, factory, "select", "--browser", browserID, "--tab", targetID, "--json")
		envelope := decodeDirectEnvelope(t, result.stdout)
		if result.err == nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorTargetAttachFailed) {
			t.Fatalf("persistence-failure result = %+v/%v", envelope, result.err)
		}
		if store.saves != 1 {
			t.Fatalf("persistence saves = %d, want one", store.saves)
		}
		if runtime.count("session_close") != runtime.count("attach") {
			t.Fatalf("persistence failure leaked sessions: %v", runtime.operationSnapshot())
		}
	})
}

func TestProductionWebMCPOperationsPersistContinuityAndActivateOnlyExplicitly(t *testing.T) {
	server, browserID, targetID, runtime := newProductionTestEndpoint(t)
	defer server.Close()
	configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  connection:
    cdp_url: %q
  selection:
    persist: true
`, server.URL+"/json/version"))
	store := NewFileWebMCPSelectionStore(configDir)
	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionHTTPClient(server.Client()),
	)

	selected := executeDirectCommand(t, configDir, store, factory, "select", "--browser", browserID, "--tab", targetID, "--json")
	selectionEnvelope := requireDirectSuccess(t, selected)
	var selectedContext WebMCPDirectContext
	decodeDirectData(t, selectionEnvelope.Data, &selectedContext)
	if selectedContext.BrowserID != browserID || selectedContext.TargetID != targetID {
		t.Fatalf("selected context = %+v", selectedContext)
	}
	if runtime.count("activate") != 0 {
		t.Fatalf("select activated a tab: %v", runtime.operationSnapshot())
	}
	record, err := store.Load()
	if err != nil {
		t.Fatalf("load persisted selection: %v", err)
	}
	if record.BrowserID != browserID || record.TargetID != targetID || record.Generation == 0 || record.ContinuityMarker == "" {
		t.Fatalf("persisted selection = %+v", record)
	}
	if strings.Contains(record.ContinuityMarker, "/") || strings.Contains(record.ContinuityMarker, "://") {
		t.Fatalf("continuity marker contains transport data: %q", record.ContinuityMarker)
	}

	runtime.resetOperations()
	activated := executeDirectCommand(t, configDir, store, factory, "activate", "--browser", browserID, "--tab", targetID, "--json")
	requireDirectSuccess(t, activated)
	if runtime.count("activate") != 1 {
		t.Fatalf("explicit activate calls = %d: %v", runtime.count("activate"), runtime.operationSnapshot())
	}
}

func TestProductionWebMCPFactoryPreservesStalePersistedSelection(t *testing.T) {
	server, browserID, targetID, runtime := newProductionTestEndpoint(t)
	defer server.Close()
	configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  connection:
    cdp_url: %q
  selection:
    persist: true
`, server.URL+"/json/version"))
	store := NewFileWebMCPSelectionStore(configDir)
	if err := store.Save(WebMCPSelection{
		Version:          WebMCPSelectionVersion,
		EndpointID:       browserID,
		BrowserID:        browserID,
		TargetID:         targetID,
		Origin:           "https://different.example",
		ContinuityMarker: "continuity-old",
		Generation:       99,
		SelectedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save stale selection: %v", err)
	}
	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionHTTPClient(server.Client()),
	)
	result := executeDirectCommand(t, configDir, store, factory, "context", "--json")
	if result.err == nil {
		t.Fatalf("stale selection unexpectedly succeeded: %+v", result)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("stale selection envelope = %+v", envelope)
	}
	if runtime.count("session_close") != runtime.count("attach") {
		t.Fatalf("stale selection left an attached target behind: %v", runtime.operationSnapshot())
	}
}

func TestProductionWebMCPFactoryRejectsNavigationWithoutAdapterContinuity(t *testing.T) {
	server, browserID, targetID, runtime := newProductionTestEndpoint(t)
	defer server.Close()
	runtime.mu.Lock()
	runtime.targets[0].ContinuityMarker = ""
	runtime.mu.Unlock()
	configDir := writeDoctorConfig(t, fmt.Sprintf(`
browser:
  connection:
    cdp_url: %q
  selection:
    persist: true
`, server.URL+"/json/version"))
	store := NewFileWebMCPSelectionStore(configDir)
	factory := NewProductionWebMCPDoctorFactory(
		WithWebMCPProductionRuntime(runtime),
		WithWebMCPProductionHTTPClient(server.Client()),
	)
	selected := executeDirectCommand(t, configDir, store, factory, "select", "--browser", browserID, "--tab", targetID, "--json")
	requireDirectSuccess(t, selected)
	oldRecord, err := store.Load()
	if err != nil {
		t.Fatalf("load initial navigation selection: %v", err)
	}

	runtime.mu.Lock()
	runtime.targets[0].URL = "https://fixture.test/other?secret=new#fragment"
	runtime.mu.Unlock()
	result := executeDirectCommand(t, configDir, store, factory, "context", "--json")
	if result.err == nil {
		t.Fatalf("navigated selection unexpectedly succeeded: %+v", result)
	}
	envelope := decodeDirectEnvelope(t, result.stdout)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != string(webmcp.ErrorStaleSelection) {
		t.Fatalf("navigated selection envelope = %+v", envelope)
	}
	newRecord, err := store.Load()
	if err != nil {
		t.Fatalf("load selection after navigation: %v", err)
	}
	if newRecord.ContinuityMarker != oldRecord.ContinuityMarker {
		t.Fatalf("navigation overwrote stale continuity marker: old=%q new=%q", oldRecord.ContinuityMarker, newRecord.ContinuityMarker)
	}
}

func newProductionTestEndpoint(t *testing.T) (*httptest.Server, string, string, *productionFakeRuntime) {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/version" {
			http.NotFound(writer, request)
			return
		}
		browserWebSocket := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/browser-token"
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"Browser":"Chrome/Test","Protocol-Version":"1.3","webSocketDebuggerUrl":%q}`, browserWebSocket)
	}))
	browserWebSocket := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/browser-token"
	parsed, err := url.Parse(browserWebSocket)
	if err != nil {
		t.Fatalf("parse browser websocket: %v", err)
	}
	browserID := discovery.HashIDMapper{}.BrowserID(discovery.BrowserIdentity{
		Scheme: parsed.Scheme,
		Host:   parsed.Hostname(),
		Port:   parsed.Port(),
		Path:   parsed.EscapedPath(),
	})
	rawTargetID := "raw-tab"
	targetID := discovery.HashTargetIDMapper{}.TargetID(discovery.TargetIdentity{BrowserID: browserID, RawID: rawTargetID})
	tool := webmcp.ToolDescriptor{
		Name:        "read_state",
		Description: "Read fixture state",
		FrameID:     "frame-1",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
		Origin:      "https://fixture.test",
	}
	target := webmcp.Target{
		ID:               webmcp.TargetID(rawTargetID),
		Type:             "page",
		Title:            "Fixture",
		URL:              "https://fixture.test/page?secret=query#fragment",
		Origin:           "https://fixture.test",
		WebSocketURL:     "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/page/raw-tab",
		ContinuityMarker: "document-a",
	}
	runtime := &productionFakeRuntime{targets: []webmcp.Target{target}, tool: tool}
	return server, browserID, targetID, runtime
}

type productionFakeRuntime struct {
	mu           sync.Mutex
	targets      []webmcp.Target
	openedTarget webmcp.Target
	openedURL    string
	tool         webmcp.ToolDescriptor
	operations   []string
}

type failingProductionSelectionStore struct {
	saves int
}

func (s *failingProductionSelectionStore) Load() (WebMCPSelection, error) {
	return WebMCPSelection{}, nil
}

func (s *failingProductionSelectionStore) Save(WebMCPSelection) error {
	s.saves++
	return errors.New("selection persistence is unavailable")
}

func (r *productionFakeRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.record("open")
	return &productionFakeHandle{runtime: r, candidate: candidate}, nil
}

func (r *productionFakeRuntime) record(operation string) {
	r.mu.Lock()
	r.operations = append(r.operations, operation)
	r.mu.Unlock()
}

func (r *productionFakeRuntime) operationsSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.operations...)
}

func (r *productionFakeRuntime) operationSnapshot() []string { return r.operationsSnapshot() }

func (r *productionFakeRuntime) resetOperations() {
	r.mu.Lock()
	r.operations = nil
	r.mu.Unlock()
}

func (r *productionFakeRuntime) count(want string) int {
	count := 0
	for _, operation := range r.operationsSnapshot() {
		if operation == want {
			count++
		}
	}
	return count
}

func (r *productionFakeRuntime) openHandleCount() int { return r.count("open") }

func (r *productionFakeRuntime) closeHandleCount() int { return r.count("handle_close") }

func (r *productionFakeRuntime) sessionCount(want string) int { return r.count("session_" + want) }

type productionFakeHandle struct {
	runtime   *productionFakeRuntime
	candidate webmcp.BrowserCandidate
	closed    bool
	mu        sync.Mutex
}

func (h *productionFakeHandle) Candidate() webmcp.BrowserCandidate { return h.candidate }

func (h *productionFakeHandle) ListTargets(ctx context.Context) ([]webmcp.Target, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, webmcp.ErrClosed
	}
	h.runtime.record("list")
	h.runtime.mu.Lock()
	targets := append([]webmcp.Target(nil), h.runtime.targets...)
	h.runtime.mu.Unlock()
	for index := range targets {
		targets[index].BrowserID = h.candidate.ID
	}
	return targets, nil
}

func (h *productionFakeHandle) Activate(ctx context.Context, targetID webmcp.TargetID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.runtime.record("activate")
	return nil
}

func (h *productionFakeHandle) OpenTab(ctx context.Context, rawURL string) (webmcp.Target, error) {
	if err := ctx.Err(); err != nil {
		return webmcp.Target{}, err
	}
	h.runtime.mu.Lock()
	h.runtime.openedURL = rawURL
	opened := h.runtime.openedTarget
	h.runtime.mu.Unlock()
	h.runtime.record("open_tab")
	return opened, nil
}

func (h *productionFakeHandle) Attach(ctx context.Context, targetID webmcp.TargetID, ownership webmcp.TargetOwnership) (webmcp.TargetSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.runtime.record("attach")
	h.runtime.mu.Lock()
	target := h.runtime.targets[0]
	tool := h.runtime.tool
	h.runtime.mu.Unlock()
	page := webmcp.PageContext{
		Key:        webmcp.PageKey{BrowserID: h.candidate.ID, TargetID: target.ID},
		Title:      target.Title,
		URL:        target.URL,
		Origin:     target.Origin,
		Generation: target.Generation,
		Connected:  true,
	}
	return &productionFakeSession{runtime: h.runtime, page: page, ownership: ownership, tool: tool}, nil
}

func (h *productionFakeHandle) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()
	h.runtime.record("handle_close")
	return nil
}

type productionFakeSession struct {
	runtime   *productionFakeRuntime
	page      webmcp.PageContext
	ownership webmcp.TargetOwnership
	tool      webmcp.ToolDescriptor
	events    chan webmcp.BrowserEvent
	done      chan struct{}
	once      sync.Once
	mu        sync.Mutex
	ready     bool
}

type productionScreenshotSession struct {
	*productionFakeSession
	screenshot webmcp.PageScreenshot
}

func (s *productionScreenshotSession) CapturePageScreenshot(ctx context.Context) (webmcp.PageScreenshot, error) {
	if err := ctx.Err(); err != nil {
		return webmcp.PageScreenshot{}, err
	}
	return s.screenshot, nil
}

func (s *productionFakeSession) init() {
	s.once.Do(func() {
		s.events = make(chan webmcp.BrowserEvent, 4)
		s.done = make(chan struct{})
	})
}

func (s *productionFakeSession) Context() webmcp.PageContext {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	page := s.page
	page.Ready = s.ready
	return page
}

func (s *productionFakeSession) Ownership() webmcp.TargetOwnership { return s.ownership }

func (s *productionFakeSession) EnableWebMCP(ctx context.Context) error {
	s.init()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
	s.runtime.record("enable")
	tool := s.tool
	tool.BrowserID = s.page.Key.BrowserID
	tool.TargetID = s.page.Key.TargetID
	tool.Generation = s.page.Generation
	s.events <- webmcp.BrowserEvent{Type: webmcp.EventToolsAdded, Tools: []webmcp.ToolDescriptor{tool}, Generation: s.page.Generation}
	return nil
}

func (s *productionFakeSession) Events() <-chan webmcp.BrowserEvent {
	s.init()
	return s.events
}

func (s *productionFakeSession) InvokeWebMCP(context.Context, webmcp.FrameID, string, json.RawMessage) (webmcp.InvocationID, error) {
	return "inv-production-test", nil
}

func (s *productionFakeSession) CancelWebMCP(context.Context, webmcp.InvocationID) error { return nil }

func (s *productionFakeSession) Done() <-chan struct{} {
	s.init()
	return s.done
}

func (s *productionFakeSession) Err() error { return nil }

func (s *productionFakeSession) Close() error {
	s.init()
	s.once.Do(func() {})
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return nil
	default:
	}
	close(s.events)
	close(s.done)
	s.mu.Unlock()
	s.runtime.record("session_close")
	return nil
}

var (
	_ webmcp.BrowserRuntime   = (*productionFakeRuntime)(nil)
	_ webmcp.BrowserHandle    = (*productionFakeHandle)(nil)
	_ webmcp.BrowserTabOpener = (*productionFakeHandle)(nil)
	_ webmcp.TargetSession    = (*productionFakeSession)(nil)
)
