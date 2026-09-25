package normalize

import (
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
)

func TestTransportURLsDropQueryAndFragmentOnlyForMatchingSchemes(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "http", got: HTTPTransportURL(" http://127.0.0.1:9222/json/version?secret=1#frag "), want: "http://127.0.0.1:9222/json/version"},
		{name: "http rejects ws", got: HTTPTransportURL("ws://127.0.0.1:9222/devtools?x=1"), want: "ws://127.0.0.1:9222/devtools?x=1"},
		{name: "ws", got: WSTransportURL("WSS://host.test/devtools/browser/a?token=1"), want: "wss://host.test/devtools/browser/a"},
		{name: "hostless", got: WSTransportURL(" /relative "), want: "/relative"},
	}
	for _, testCase := range cases {
		if testCase.got != testCase.want {
			t.Fatalf("%s = %q, want %q", testCase.name, testCase.got, testCase.want)
		}
	}
}

func TestSafePageURLAndOriginRedactCredentialsAndPaths(t *testing.T) {
	if got := SafePageURL("https://user:pass@page.test/a?b=1#c"); got != "https://page.test/a" {
		t.Fatalf("SafePageURL = %q", got)
	}
	for _, raw := range []string{"file:///etc/passwd", "about:blank", "::bad"} {
		if got := SafePageURL(raw); got != "" {
			t.Fatalf("SafePageURL(%q) = %q, want empty", raw, got)
		}
	}
	if got := SafeOrigin("HTTPS://Page.Test/path?q"); got != "https://page.test" {
		t.Fatalf("SafeOrigin = %q", got)
	}
	if got := SafeOrigin("not a url"); got != "" {
		t.Fatalf("SafeOrigin(invalid) = %q", got)
	}
}

func TestEndpointFromActivePort(t *testing.T) {
	endpoint, err := EndpointFromActivePort(discovery.ActivePortRecord{Port: 9222, BrowserWebSocketPath: "/devtools/browser/a"})
	if err != nil || endpoint.CDPURL != "http://127.0.0.1:9222/json/version" || endpoint.BrowserWSEndpoint != "ws://127.0.0.1:9222/devtools/browser/a" {
		t.Fatalf("relative endpoint = %+v/%v", endpoint, err)
	}
	endpoint, err = EndpointFromActivePort(discovery.ActivePortRecord{Port: 9222, BrowserWebSocketPath: "ws://127.0.0.1:9333/devtools/browser/b?x=1"})
	if err != nil || endpoint.CDPURL != "http://127.0.0.1:9333/json/version" || strings.Contains(endpoint.BrowserWSEndpoint, "?") {
		t.Fatalf("absolute endpoint = %+v/%v", endpoint, err)
	}
	for _, record := range []discovery.ActivePortRecord{
		{Port: 0, BrowserWebSocketPath: "/a"},
		{Port: 70000, BrowserWebSocketPath: "/a"},
		{Port: 9222, BrowserWebSocketPath: ""},
		{Port: 9222, BrowserWebSocketPath: "/a\n/b"},
		{Port: 9222, BrowserWebSocketPath: "relative"},
	} {
		if _, err := EndpointFromActivePort(record); err == nil {
			t.Fatalf("EndpointFromActivePort(%+v) accepted invalid record", record)
		}
	}
}

func TestOpaqueIDAndCompositeTargetRef(t *testing.T) {
	if !OpaqueID("browser-a_1") || OpaqueID("") || OpaqueID("has space") || OpaqueID(strings.Repeat("a", 65)) {
		t.Fatal("OpaqueID accepted or rejected the wrong values")
	}
	browserID, targetID, composite := SplitCompositeTargetRef(" browser-a/target-b ")
	if !composite || browserID != "browser-a" || targetID != "target-b" {
		t.Fatalf("composite = %q/%q/%t", browserID, targetID, composite)
	}
	for _, value := range []string{"target-b", "/target-b", "browser-a/", "browser a/target b", "browser-a/target-b/extra"} {
		if _, _, composite := SplitCompositeTargetRef(value); composite {
			t.Fatalf("SplitCompositeTargetRef(%q) reported composite", value)
		}
	}
}

func TestNeutralTargetAndDiscoverySource(t *testing.T) {
	target := NeutralTarget("browser-a", discovery.Target{ID: "target-a", URL: "https://page.test/a?b#c", Origin: "HTTPS://Page.Test", Eligible: true})
	if target.BrowserID != "browser-a" || target.ID != "target-a" || target.URL != "https://page.test/a" || target.Origin != "https://page.test" || !target.Eligible {
		t.Fatalf("NeutralTarget = %+v", target)
	}
	sources := map[discovery.Source]webmcp.DiscoverySource{
		discovery.SourceExplicitCDPHTTP:    webmcp.DiscoverySourceExplicit,
		discovery.SourceExplicitBrowserWS:  webmcp.DiscoverySourceExplicit,
		discovery.SourceDevToolsActivePort: webmcp.DiscoverySourceActivePort,
		discovery.SourceProcess:            webmcp.DiscoverySourceProcess,
		discovery.SourceConfigured:         webmcp.DiscoverySourceConfigured,
		discovery.Source("unknown"):        webmcp.DiscoverySourceConfigured,
	}
	for source, want := range sources {
		if got := DiscoverySource(source); got != want {
			t.Fatalf("DiscoverySource(%q) = %q, want %q", source, got, want)
		}
	}
}

func TestDiscoveryErrorClassification(t *testing.T) {
	if DiscoveryError(nil) != nil {
		t.Fatal("DiscoveryError(nil) != nil")
	}
	plain := errors.New("plain")
	if !errors.Is(DiscoveryError(plain), plain) {
		t.Fatal("plain error was not preserved")
	}
	known := &discovery.DiscoveryError{Code: discovery.CodeNoEligibleTab, Message: "none", Retryable: true}
	var classified *webmcp.ClassifiedError
	if !errors.As(DiscoveryError(known), &classified) || classified.Code != webmcp.ErrorNoEligibleTab || !classified.Retryable || classified.Message != "none" {
		t.Fatalf("known classification = %+v", classified)
	}
	unknown := &discovery.DiscoveryError{Code: discovery.Code("surprise"), Message: "odd"}
	if !errors.As(DiscoveryError(unknown), &classified) || classified.Code != webmcp.ErrorBrowserProtocol {
		t.Fatalf("unknown classification = %+v", classified)
	}
	if !IsUnsupportedWebMCPError(webmcp.NewClassifiedError(webmcp.ErrorUnsupportedWebMCP, "no", nil)) || IsUnsupportedWebMCPError(plain) {
		t.Fatal("IsUnsupportedWebMCPError misclassified")
	}
}
