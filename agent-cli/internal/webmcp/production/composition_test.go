package production

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

const (
	openedPublicBrowser = "browser-public"
	openedRawURL        = "https://opened.example.test/page?visible=yes#section"
)

func fixtureBrowserConfig(cdpURL string) config.BrowserConfig {
	browser := config.DefaultBrowserConfig()
	browser.Tools.Enabled = true
	browser.Connection.CDPURL = cdpURL
	browser.Selection.Persist = false
	return browser
}

func TestHandlePreservesOpenTabAndNormalizesTargetIdentity(t *testing.T) {
	runtime := &fakeRuntime{openedTarget: webmcp.Target{
		ID:               "raw-opened-tab",
		Type:             "page",
		Title:            "Opened fixture",
		URL:              openedRawURL,
		Origin:           "https://opened.example.test",
		WebSocketURL:     "ws://127.0.0.1/devtools/page/raw-opened-tab",
		ContinuityMarker: "raw-document-token",
		Eligible:         true,
	}}
	raw := &fakeHandle{runtime: runtime, candidate: webmcp.BrowserCandidate{ID: "raw-browser"}}
	owner := &composition{targetIDMapper: discovery.HashTargetIDMapper{}}
	bridge := &handle{
		owner:     owner,
		candidate: webmcp.BrowserCandidate{ID: openedPublicBrowser},
		raw:       raw,
		closed:    make(chan struct{}),
	}

	opened, err := bridge.OpenTab(context.Background(), openedRawURL)
	if err != nil {
		t.Fatalf("production open tab: %v", err)
	}
	wantID := discovery.HashTargetIDMapper{}.TargetID(discovery.TargetIdentity{BrowserID: openedPublicBrowser, RawID: "raw-opened-tab"})
	if opened.BrowserID != openedPublicBrowser || string(opened.ID) != wantID || opened.URL != "https://opened.example.test/page" || opened.Origin != "https://opened.example.test" {
		t.Fatalf("normalized opened target = %+v, want browser=%q target=%q", opened, openedPublicBrowser, wantID)
	}
	if opened.WebSocketURL != "" || opened.ContinuityMarker != "" {
		t.Fatalf("opened target exposed transport identity: %+v", opened)
	}
	if runtime.count(opOpenTab) != 1 || runtime.openedURL != openedRawURL {
		t.Fatalf("raw open operations = %v URL=%q", runtime.snapshot(), runtime.openedURL)
	}
	if err := bridge.Close(); err != nil {
		t.Fatalf("close handle: %v", err)
	}
	if _, err := bridge.OpenTab(context.Background(), openedRawURL); !errors.Is(err, webmcp.ErrClosed) {
		t.Fatalf("closed handle OpenTab error = %v, want ErrClosed", err)
	}
}

func TestExactSelectionDoesNotProbeUnrelatedRestoredTab(t *testing.T) {
	cases := []struct {
		name        string
		selectedTab string
		browserID   string
	}{
		{name: "bare selection", selectedTab: "target-selected", browserID: "browser-selected"},
		{name: "composite selection", selectedTab: "browser-selected/target-selected", browserID: "browser-selected"},
		{name: "composite other browser", selectedTab: "browser-other/target-suspended", browserID: "browser-selected"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var browser config.BrowserConfig
			browser.Selection.Tab = testCase.selectedTab
			probe := targetProbe{owner: &composition{browser: browser}}
			// No runtime is installed: calling the renderer would panic. Listing an
			// unrelated tab must leave its capability unknown without touching it.
			capability, err := probe.Probe(context.Background(), discovery.BrowserCandidate{ID: testCase.browserID},
				discovery.Target{ID: "target-suspended"})
			if err != nil || capability.DomainKnown || capability.PageToolsKnown || capability.ToolCount != -1 {
				t.Fatalf("unrelated capability should remain unknown: %+v, %v", capability, err)
			}
		})
	}
}

func TestFactoryComposesDiscoveryRuntimeAndBroker(t *testing.T) {
	fixture := newFixtureEndpoint(t)
	build := NewFactory(WithRuntime(fixture.runtime), WithHTTPClient(fixture.server.Client()))
	runtime, err := build(fixtureBrowserConfig(fixture.server.URL + "/json/version?secret=redact#fragment"))
	if err != nil {
		t.Fatalf("build runtime: %v", err)
	}
	ctx := context.Background()
	candidates, err := runtime.Broker.Discover(ctx, webmcp.DiscoverOptions{ExplicitOnly: true})
	if err != nil || len(candidates) != 1 || string(candidates[0].ID) != fixture.browserID {
		t.Fatalf("discover = %+v/%v, want %s", candidates, err, fixture.browserID)
	}
	if strings.Contains(candidates[0].HTTPURL, "secret") || strings.Contains(candidates[0].HTTPURL, "#") {
		t.Fatalf("candidate kept endpoint query/fragment: %q", candidates[0].HTTPURL)
	}
	targets, err := runtime.Broker.ListTargets(ctx, webmcp.BrowserSelector{BrowserID: candidates[0].ID})
	if err != nil || len(targets) != 1 || string(targets[0].ID) != fixture.targetID {
		t.Fatalf("targets = %+v/%v, want %s", targets, err, fixture.targetID)
	}
	if targets[0].URL != fixtureOrigin+"/page" || targets[0].Origin != fixtureOrigin {
		t.Fatalf("target URL/origin not redacted: %+v", targets[0])
	}
	page, err := runtime.Broker.Select(ctx, webmcp.TargetSelector{BrowserID: candidates[0].ID, TargetID: targets[0].ID})
	if err != nil || string(page.Key.TargetID) != fixture.targetID {
		t.Fatalf("select = %+v/%v", page, err)
	}
	assertCatalogServesCandidate(t, runtime, candidates[0])
	if err := runtime.Broker.Close(); err != nil {
		t.Fatalf("close broker: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	if fixture.runtime.count(opActivate) != 0 {
		t.Fatalf("composition activated a tab: %v", fixture.runtime.snapshot())
	}
	if fixture.runtime.count(opAttach) != fixture.runtime.count(opSessionEnd) {
		t.Fatalf("composition leaked raw sessions: %v", fixture.runtime.snapshot())
	}
}

func assertCatalogServesCandidate(t *testing.T, runtime Runtime, candidate webmcp.BrowserCandidate) {
	t.Helper()
	ctx := context.Background()
	version, err := runtime.Catalog.Version(ctx, candidate)
	if err != nil || version.Browser != "Chrome/Test" {
		t.Fatalf("catalog version = %+v/%v", version, err)
	}
	catalogTargets, err := runtime.Catalog.ListTargets(ctx, candidate)
	if err != nil || len(catalogTargets) != 1 {
		t.Fatalf("catalog targets = %+v/%v", catalogTargets, err)
	}
}

func TestProbeTargetReportsUnsupportedDomainAsKnownWithoutTools(t *testing.T) {
	fixture := newFixtureEndpoint(t)
	owner := newTestComposition(fixtureBrowserConfig(fixture.server.URL+"/json/version"), &unsupportedRuntime{fakeRuntime: fixture.runtime})
	lane := discovery.BrowserCandidate{ID: fixture.browserID, Source: discovery.SourceExplicitCDPHTTP}
	capability, err := owner.probeTarget(context.Background(), lane, discovery.Target{ID: fixture.targetID})
	if err != nil || !capability.DomainKnown || capability.DomainSupported || capability.ToolCount != -1 {
		t.Fatalf("unsupported probe = %+v/%v", capability, err)
	}
	if fixture.runtime.count(opOpen) != fixture.runtime.count(opHandleClose) {
		t.Fatalf("probe leaked handles: %v", fixture.runtime.snapshot())
	}
}

func TestEndpointForLaneFailsClosedWithoutTransport(t *testing.T) {
	owner := newTestComposition(config.DefaultBrowserConfig(), &fakeRuntime{})
	_, err := owner.endpointForLane(context.Background(), discovery.BrowserCandidate{ID: "browser-a", Source: discovery.SourceProcess})
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != webmcp.ErrorEndpointUnreachable {
		t.Fatalf("endpoint error = %v, want endpoint unreachable", err)
	}
	owner.rememberEndpointHint(discovery.Endpoint{BrowserWSEndpoint: "ws://127.0.0.1:9222/devtools/browser/a"})
	endpoint, err := owner.endpointForLane(context.Background(), discovery.BrowserCandidate{ID: "browser-a", Source: discovery.SourceProcess})
	if err != nil || endpoint.BrowserWSEndpoint == "" {
		t.Fatalf("hinted endpoint = %+v/%v", endpoint, err)
	}
}

func newTestComposition(browser config.BrowserConfig, runtime webmcp.BrowserRuntime) *composition {
	return &composition{
		browser:        browser,
		runtime:        runtime,
		targetIDMapper: discovery.HashTargetIDMapper{},
		coreCandidates: make(map[string]webmcp.BrowserCandidate),
		laneCandidates: make(map[string]discovery.BrowserCandidate),
		endpoints:      make(map[string]discovery.Endpoint),
	}
}

// unsupportedRuntime attaches sessions whose page lacks the WebMCP domain.
type unsupportedRuntime struct{ *fakeRuntime }

func (r *unsupportedRuntime) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	raw, err := r.fakeRuntime.Open(ctx, candidate)
	if err != nil {
		return nil, err
	}
	fake, ok := raw.(*fakeHandle)
	if !ok {
		return nil, errors.New("unexpected fake handle")
	}
	return &unsupportedHandle{fakeHandle: fake}, nil
}

type unsupportedHandle struct{ *fakeHandle }

func (h *unsupportedHandle) Attach(ctx context.Context, targetID webmcp.TargetID, ownership webmcp.TargetOwnership) (webmcp.TargetSession, error) {
	session, err := h.fakeHandle.Attach(ctx, targetID, ownership)
	if err != nil {
		return nil, err
	}
	fake, ok := session.(*fakeSession)
	if !ok {
		return nil, errors.New("unexpected fake session")
	}
	fake.enableErr = webmcp.NewClassifiedError(webmcp.ErrorUnsupportedWebMCP, "unsupported", nil)
	return fake, nil
}
