package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
)

type targetHTTPClient struct {
	requests  []*http.Request
	responses []*http.Response
}

func (c *targetHTTPClient) Do(request *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, request)
	if len(c.responses) == 0 {
		return nil, errors.New("target HTTP response queue exhausted")
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func targetJSONResponse(body string, status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func targetDescriptor(rawID, title, pageURL string, tools int) TargetDescriptor {
	webmcp := true
	return TargetDescriptor{
		ID:                   rawID,
		Type:                 "page",
		Title:                title,
		URL:                  pageURL,
		WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/" + rawID,
		WebMCPSupported:      &webmcp,
		ToolCount:            &tools,
	}
}

func TestListTargetsNormalizesJSONListAndRedactsTransportData(t *testing.T) {
	listJSON := `[
  {"id":"page-z","type":"page","title":"Orders","url":"https://Example.TEST/orders?session=page-secret#details","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/page/page-z?token=ws-secret","webmcpSupported":true,"toolCount":2},
  {"id":"zero","type":"page","title":"Empty","url":"https://zero.test/","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/page/zero","webmcp":true,"toolCount":0},
  {"id":"background","type":"background_page","title":"Extension","url":"chrome-extension://extension-id/background.html","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/page/background","webmcpSupported":true,"toolCount":4},
  {"id":"internal","type":"page","title":"Settings","url":"chrome://settings","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/page/internal","webmcpSupported":true,"toolCount":1},
  {"id":"no-websocket","type":"page","title":"No socket","url":"https://socketless.test","webmcpSupported":true,"toolCount":1}
]`
	client := &targetHTTPClient{responses: []*http.Response{
		targetJSONResponse(validVersionJSON("ws://127.0.0.1:9222/devtools/browser/browser-secret"), http.StatusOK),
		targetJSONResponse(listJSON, http.StatusOK),
	}}
	recorder := &eventRecorder{}
	service := New(Options{HTTPClient: client, EventSink: recorder})
	browser, err := service.Discover(context.Background(), ConnectionInputs{CDPURL: "http://127.0.0.1:9222?token=http-secret#fragment"})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	snapshot, err := service.ListTargetSnapshot(context.Background(), browser)
	if err != nil {
		t.Fatalf("ListTargetSnapshot() error = %v", err)
	}
	if snapshot.CandidateCount != 5 || snapshot.EligibleCount != 2 {
		t.Fatalf("snapshot counts = candidate %d eligible %d, want 5 and 2", snapshot.CandidateCount, snapshot.EligibleCount)
	}
	if len(snapshot.Targets) != 1 {
		t.Fatalf("default target count = %d, want one non-zero eligible page", len(snapshot.Targets))
	}
	target := snapshot.Targets[0]
	if target.Type != "page" || target.Title != "Orders" || target.URL != "https://example.test/orders" || target.Origin != "https://example.test" {
		t.Fatalf("normalized target = %#v", target)
	}
	if !target.WebSocketPresent || !target.WebMCP || !target.Eligible || target.ToolCount != 2 || !target.ToolCountKnown {
		t.Fatalf("target capability = %#v", target)
	}
	wantID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: "page-z"})
	if target.ID != wantID {
		t.Fatalf("target ID = %q, want stable opaque ID %q", target.ID, wantID)
	}
	if got := client.requests[1].URL.String(); got != "http://127.0.0.1:9222/json/list" {
		t.Fatalf("target request URL = %q, want query-free /json/list", got)
	}
	if got := eventTypes(recorder.events); strings.Join(got, ",") != "browser.discovery.started,browser.endpoint.version,browser.discovery.completed,browser.targets.snapshot" {
		t.Fatalf("event types = %v", got)
	}
	encoded, marshalErr := json.Marshal(struct {
		Browser  BrowserCandidate
		Snapshot TargetSnapshot
		Events   []Event
	}{browser, snapshot, recorder.events})
	if marshalErr != nil {
		t.Fatalf("marshal normalized target values: %v", marshalErr)
	}
	for _, secret := range []string{"http-secret", "page-secret", "ws-secret", "browser-secret", "ws://", "devtools/page/"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public target data contains %q: %s", secret, encoded)
		}
	}
}

func TestListTargetsStableIDsAndDeterministicOrdering(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-fixed", Source: SourceConfigured, Loopback: true}
	wireOrder := []TargetDescriptor{
		targetDescriptor("raw-b", "B", "https://b.test", 1),
		targetDescriptor("raw-a", "A", "https://a.test", 1),
	}
	first := append([]TargetDescriptor(nil), wireOrder...)
	lister := TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
		return append([]TargetDescriptor(nil), first...), nil
	})
	service := New(Options{TargetLister: lister})
	gotFirst, err := service.ListTargets(context.Background(), browser)
	if err != nil {
		t.Fatalf("first ListTargets() error = %v", err)
	}
	if len(gotFirst) != 2 {
		t.Fatalf("first target count = %d, want 2", len(gotFirst))
	}
	firstIDs := []string{gotFirst[0].ID, gotFirst[1].ID}
	wantIDs := append([]string(nil), firstIDs...)
	sort.Strings(wantIDs)
	if !equalStrings(firstIDs, wantIDs) {
		t.Fatalf("target IDs = %v, want sorted %v", firstIDs, wantIDs)
	}

	first = []TargetDescriptor{wireOrder[1], wireOrder[0]}
	gotSecond, err := service.ListTargets(context.Background(), browser)
	if err != nil {
		t.Fatalf("second ListTargets() error = %v", err)
	}
	secondIDs := []string{gotSecond[0].ID, gotSecond[1].ID}
	if !equalStrings(secondIDs, firstIDs) {
		t.Fatalf("IDs changed with wire order: first %v second %v", firstIDs, secondIDs)
	}
}

func TestListTargetsAppliesFiltersAndZeroToolDefault(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-filter", Source: SourceConfigured, Loopback: true}
	zero := 0
	webmcp := true
	descriptors := []TargetDescriptor{
		{ID: "zero", Type: "page", Title: "Zero", URL: "https://zero.test", WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/zero", WebMCPSupported: &webmcp, ToolCount: &zero},
		targetDescriptor("write", "Write", "https://allowed.test/write", 2),
		targetDescriptor("other", "Other", "https://other.test", 1),
	}
	service := New(Options{
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return descriptors, nil
		}),
		AllowedOrigins: []string{"https://allowed.test", "https://zero.test"},
	})

	snapshot, err := service.ListTargetSnapshot(context.Background(), browser)
	if err != nil {
		t.Fatalf("default snapshot error = %v", err)
	}
	if len(snapshot.Targets) != 1 || snapshot.Targets[0].Origin != "https://allowed.test" {
		t.Fatalf("default filtered targets = %#v", snapshot.Targets)
	}

	includeZero := TargetListOptions{IncludeZeroToolPages: true, OriginContains: "zero"}
	snapshot, err = service.ListTargetSnapshot(context.Background(), browser, includeZero)
	if err != nil {
		t.Fatalf("include-zero snapshot error = %v", err)
	}
	if len(snapshot.Targets) != 1 || snapshot.Targets[0].ToolCount != 0 {
		t.Fatalf("include-zero targets = %#v", snapshot.Targets)
	}

	allTargets, err := service.ListTargets(context.Background(), browser, TargetListOptions{EligibleOnly: Bool(false)})
	if err != nil {
		t.Fatalf("eligible_only=false error = %v", err)
	}
	if len(allTargets) != 3 {
		t.Fatalf("eligible_only=false targets = %d, want all 3", len(allTargets))
	}
}

func TestListTargetsReturnsNoEligibleAndUnsupportedClassifications(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-errors", Source: SourceConfigured, Loopback: true}
	unsupported := false
	descriptor := TargetDescriptor{
		ID:                   "unsupported-page",
		Type:                 "page",
		Title:                "Unsupported",
		URL:                  "https://unsupported.test",
		WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/unsupported-page",
		WebMCPSupported:      &unsupported,
	}
	service := New(Options{TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
		return []TargetDescriptor{descriptor}, nil
	})})

	snapshot, err := service.ListTargetSnapshot(context.Background(), browser)
	noEligible := assertDiscoveryError(t, err, CodeNoEligibleTab)
	if snapshot.CandidateCount != 1 || snapshot.EligibleCount != 0 || noEligible.Retryable != true {
		t.Fatalf("no-eligible result = snapshot %#v error %#v", snapshot, noEligible)
	}
	if len(snapshot.Targets) != 0 {
		t.Fatalf("no-eligible targets = %#v", snapshot.Targets)
	}

	targetID := (HashTargetIDMapper{}).TargetID(TargetIdentity{BrowserID: browser.ID, RawID: descriptor.ID})
	_, err = service.ListTargetSnapshot(context.Background(), browser, TargetListOptions{TargetID: targetID})
	unsupportedErr := assertDiscoveryError(t, err, CodeUnsupportedWebMCP)
	if unsupportedErr.Details["browser_id"] != browser.ID || unsupportedErr.Details["target_id"] != targetID {
		t.Fatalf("unsupported details = %#v", unsupportedErr.Details)
	}
}

func TestDiscoverAndListTargetsRequiresExactBrowserWhenSeveralConfigured(t *testing.T) {
	client := &targetHTTPClient{responses: []*http.Response{
		targetJSONResponse(validVersionJSON("ws://127.0.0.1:9222/devtools/browser/one"), http.StatusOK),
		targetJSONResponse(validVersionJSON("ws://127.0.0.1:9223/devtools/browser/two"), http.StatusOK),
	}}
	service := New(Options{HTTPClient: client})
	inputs := ConnectionInputs{ConfiguredSources: []ConfiguredSource{
		StaticConfiguredSource{SourceName: "one", Value: Endpoint{CDPURL: "http://127.0.0.1:9222"}},
		StaticConfiguredSource{SourceName: "two", Value: Endpoint{CDPURL: "http://127.0.0.1:9223"}},
	}}
	_, err := service.DiscoverAndListTargets(context.Background(), inputs, TargetListOptions{})
	ambiguous := assertDiscoveryError(t, err, CodeAmbiguousBrowser)
	ids, ok := ambiguous.Details["candidate_browser_ids"].([]string)
	if !ok || len(ids) != 2 || !sort.StringsAreSorted(ids) {
		t.Fatalf("ambiguous browser IDs = %#v", ambiguous.Details["candidate_browser_ids"])
	}
	if len(client.requests) != 2 {
		t.Fatalf("version requests = %d, want both configured sources", len(client.requests))
	}
}

func TestTargetCapabilityProbeControlsEligibility(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-capability", Source: SourceConfigured, Loopback: true}
	probeCalls := 0
	service := New(Options{
		TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
			return []TargetDescriptor{targetDescriptor("page", "Page", "https://capability.test", 7)}, nil
		}),
		TargetProbe: TargetCapabilityProbeFunc(func(_ context.Context, _ BrowserCandidate, target Target) (TargetCapabilities, error) {
			probeCalls++
			if target.Origin != "https://capability.test" || target.ID == "" {
				t.Fatalf("probe received unsafe/incomplete target = %#v", target)
			}
			return TargetCapabilities{WebMCP: false, ToolCount: 0, ToolCountKnown: true}, nil
		}),
	})

	_, err := service.ListTargets(context.Background(), browser)
	assertDiscoveryError(t, err, CodeNoEligibleTab)
	if probeCalls != 1 {
		t.Fatalf("capability probe calls = %d, want 1", probeCalls)
	}
}

func TestListTargetsUnknownCapabilityIsNotEligibleWithoutProbe(t *testing.T) {
	browser := BrowserCandidate{ID: "browser-unknown-capability", Source: SourceConfigured, Loopback: true}
	descriptor := TargetDescriptor{
		ID:                   "unknown-capability",
		Type:                 "page",
		Title:                "Unknown",
		URL:                  "https://unknown-capability.test",
		WebSocketDebuggerURL: "ws://127.0.0.1:9222/devtools/page/unknown-capability",
	}
	service := New(Options{TargetLister: TargetListerFunc(func(context.Context, BrowserCandidate) ([]TargetDescriptor, error) {
		return []TargetDescriptor{descriptor}, nil
	})})

	snapshot, err := service.ListTargetSnapshot(context.Background(), browser)
	assertDiscoveryError(t, err, CodeNoEligibleTab)
	if snapshot.CandidateCount != 1 || snapshot.EligibleCount != 0 || len(snapshot.Targets) != 0 {
		t.Fatalf("unknown capability snapshot = %#v, want one ineligible candidate", snapshot)
	}

	allTargets, err := service.ListTargets(context.Background(), browser, TargetListOptions{EligibleOnly: Bool(false)})
	if err != nil {
		t.Fatalf("ListTargets(eligible_only=false): %v", err)
	}
	if len(allTargets) != 1 || allTargets[0].WebMCP || allTargets[0].WebMCPKnown || allTargets[0].Eligible || allTargets[0].EligibilityReason != "unsupported_webmcp" {
		t.Fatalf("unknown capability target = %#v, want explicitly unsupported", allTargets)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
