package direct

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/selectionstore"
)

func TestNormalizeOpaqueIDAndSafeIDList(t *testing.T) {
	if got := NormalizeOpaqueID("  tab-a_1 "); got != "tab-a_1" {
		t.Fatalf("NormalizeOpaqueID = %q", got)
	}
	for _, value := range []string{"", "tab a", "ws://x", strings.Repeat("a", 65)} {
		if got := NormalizeOpaqueID(value); got != "" {
			t.Fatalf("NormalizeOpaqueID(%q) = %q, want empty", value, got)
		}
	}
	if got := SafeIDList([]any{"tab-b", 3, "tab-a", "tab-b", "bad id"}); !reflect.DeepEqual(got, []string{"tab-a", "tab-b"}) {
		t.Fatalf("SafeIDList([]any) = %v", got)
	}
	if got := SafeIDList([]string{"b", "a"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("SafeIDList([]string) = %v", got)
	}
	if got := SafeIDList("tab-a"); len(got) != 0 {
		t.Fatalf("SafeIDList(string) = %v", got)
	}
	if got := BrowserCandidateIDs([]webmcp.BrowserCandidate{{ID: "b"}, {ID: "a"}, {ID: "a"}}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("BrowserCandidateIDs = %v", got)
	}
}

func TestAmbiguityChoicesAreBoundedSortedAndRedacted(t *testing.T) {
	targets := make([]webmcp.Target, 0, MaxAmbiguityCandidates+2)
	for index := MaxAmbiguityCandidates + 1; index >= 0; index-- {
		targets = append(targets, webmcp.Target{ID: webmcp.TargetID(fmt.Sprintf("tab-%02d", index)), Title: "Title?secret", URL: "https://Page.Test:443/a"})
	}
	targets = append(targets, webmcp.Target{ID: "bad id"}, webmcp.Target{ID: "tab-01", Title: "zz"})
	if got := AmbiguityTargetIDs(targets); len(got) != MaxAmbiguityCandidates || got[0] != "tab-00" {
		t.Fatalf("AmbiguityTargetIDs = %v", got)
	}
	choices := CandidateChoicesForTargets("browser-a", targets)
	if len(choices) != MaxAmbiguityCandidates {
		t.Fatalf("choices = %d, want %d", len(choices), MaxAmbiguityCandidates)
	}
	want := map[string]any{choiceBrowserID: "browser-a", choiceTargetID: "tab-00", choiceTitle: redactedTitle, choiceOrigin: "https://page.test"}
	if !reflect.DeepEqual(choices[0], want) {
		t.Fatalf("first choice = %+v, want %+v", choices[0], want)
	}
}

func TestSafeCandidateChoicesResanitizesDetailValues(t *testing.T) {
	value := []any{
		map[string]any{choiceTargetID: "tab-b", choiceTitle: "Plain", choiceOrigin: "http://host.test:80/path"},
		map[string]any{choiceTargetID: "tab-a", choiceBrowserID: "browser-x", choiceOrigin: "ftp://host.test"},
		map[string]any{choiceTargetID: "bad id"},
		"not a map",
	}
	got := SafeCandidateChoices(value, "browser-a", nil)
	want := []map[string]any{
		{choiceBrowserID: "browser-x", choiceTargetID: "tab-a"},
		{choiceBrowserID: "browser-a", choiceTargetID: "tab-b", choiceTitle: "Plain", choiceOrigin: "http://host.test"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SafeCandidateChoices = %+v, want %+v", got, want)
	}
	explicit := SafeCandidateChoices([]map[string]any{{choiceTargetID: "tab-a"}}, "browser-a", []string{"tab-c", "tab-a"})
	if len(explicit) != 2 || explicit[1][choiceTargetID] != "tab-c" || explicit[1][choiceBrowserID] != "browser-a" {
		t.Fatalf("explicit IDs = %+v", explicit)
	}
	if got := SafeCandidateChoices(nil, "", nil); got != nil {
		t.Fatalf("empty choices = %+v, want nil", got)
	}
}

func TestCanonicalCandidateOrigin(t *testing.T) {
	cases := map[string]string{
		"https://[::1]:8443/x":                               "https://[::1]:8443",
		"https://host.test:443":                              "https://host.test",
		"http://host.test:99999":                             "",
		"https://user@host.test":                             "",
		"https://host.test/\x01":                             "",
		"https://" + strings.Repeat("a", maxAmbiguityOrigin): "",
	}
	for raw, want := range cases {
		if got := canonicalCandidateOrigin(raw); got != want {
			t.Fatalf("canonicalCandidateOrigin(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestEligibleTargetMatchesFiltersPagesOriginAndPolicy(t *testing.T) {
	targets := []webmcp.Target{
		{ID: "tab-c", Type: targetTypePage, Origin: "https://ok.test", Eligible: true},
		{ID: "tab-a", Type: targetTypePage, Origin: "https://ok.test", Eligible: true},
		{ID: "tab-a", Type: targetTypePage, Origin: "https://ok.test", Eligible: true},
		{ID: "tab-d", Type: targetTypePage, Origin: "https://denied.test", Eligible: true},
		{ID: "tab-e", Type: targetTypePage, Origin: "https://ok.test"},
		{ID: "worker", Type: "service_worker", Origin: "https://ok.test", Eligible: true},
		{ID: "tab-f", Type: targetTypePage, Origin: "https://other.test", Eligible: true},
	}
	browser := config.BrowserConfig{Policy: config.BrowserPolicyConfig{DeniedOrigins: []string{"https://denied.test"}, AllowedOrigins: []string{"https://ok.test", "https://denied.test"}}}
	var ids []string
	for _, target := range EligibleTargetMatches(targets, browser) {
		ids = append(ids, string(target.ID))
	}
	if !reflect.DeepEqual(ids, []string{"tab-a", "tab-c"}) {
		t.Fatalf("EligibleTargetMatches = %v", ids)
	}
	browser.Selection.Origin = "https://other.test"
	if got := EligibleTargetMatches(targets, browser); len(got) != 0 {
		t.Fatalf("origin-filtered matches = %+v", got)
	}
}

func TestPolicyErrorsAndNames(t *testing.T) {
	policy := config.BrowserPolicyConfig{DeniedOrigins: []string{"https://denied.test"}, AllowedOrigins: []string{"https://ok.test"}}
	browser := config.BrowserConfig{Policy: policy}
	if err := TargetPolicyError(webmcp.Target{Origin: "https://ok.test/page"}, browser); err != nil {
		t.Fatalf("allowed origin error = %v", err)
	}
	for origin, name := range map[string]string{"https://denied.test": PolicyDeniedOrigins, "https://else.test": PolicyAllowedOrigins} {
		err := TargetPolicyError(webmcp.Target{Origin: origin}, browser)
		requireClassified(t, err, webmcp.ErrorOriginDenied, detailPolicy, name)
		requireClassified(t, OriginDeniedError(origin, policy), webmcp.ErrorOriginDenied, detailPolicy, name)
	}
	kinds := map[string]config.BrowserConnectionConfig{
		"http": {CDPURL: "http://x"}, "websocket": {WSEndpoint: "ws://x"}, "profile": {UserDataDir: "/p"}, "discovery": {},
	}
	for want, connection := range kinds {
		if got := EndpointKind(config.BrowserConfig{Connection: connection}); got != want {
			t.Fatalf("EndpointKind(%+v) = %q, want %q", connection, got, want)
		}
	}
}

func TestNoEligibleTabErrorAndBoundedReason(t *testing.T) {
	browser := config.BrowserConfig{Selection: config.BrowserSelectionConfig{Origin: "https://ok.test/x?y"}}
	err := NoEligibleTabError("browser-a", browser, -1, "  loading ")
	requireClassified(t, err, webmcp.ErrorNoEligibleTab, detailReason, "loading")
	details := requireDetails(t, err, webmcp.ErrorNoEligibleTab)
	filters, ok := details["filters"].(map[string]any)
	if !ok || details[detailCandidateCount] != 0 || filters["origin"] != "https://ok.test" {
		t.Fatalf("details = %+v", details)
	}
	cases := map[string]string{"": reasonFallback, "bad\x01": reasonFallback, strings.Repeat("r", 90): strings.Repeat("r", maxReasonLength)}
	for raw, want := range cases {
		if got := BoundedReason(raw); got != want {
			t.Fatalf("BoundedReason(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestReplacementReasonMatchesConfiguredEndpointAuthority(t *testing.T) {
	browser := config.BrowserConfig{Connection: config.BrowserConnectionConfig{CDPURL: "http://127.0.0.1/json/version", WSEndpoint: "::bad"}}
	stored := selectionstore.Selection{BrowserID: "browser-a", BrowserInstanceID: "incarnation-1"}
	same := webmcp.BrowserCandidate{ID: "browser-a", HTTPURL: "http://127.0.0.1:80"}
	elsewhere := webmcp.BrowserCandidate{ID: "browser-b", HTTPURL: "https://127.0.0.1"}
	if reason, ok := ReplacementReason([]webmcp.BrowserCandidate{same, elsewhere}, browser, stored); ok {
		t.Fatalf("unexpected replacement %q", reason)
	}
	replaced := webmcp.BrowserCandidate{ID: "browser-b", BrowserWSURL: "ws://127.0.0.1/devtools", BrowserInstanceID: "incarnation-2"}
	if reason, ok := ReplacementReason([]webmcp.BrowserCandidate{replaced}, browser, stored); !ok || reason != "browser_instance_changed" {
		t.Fatalf("replacement = %q/%t", reason, ok)
	}
	replaced.BrowserInstanceID = stored.BrowserInstanceID
	if reason, ok := ReplacementReason([]webmcp.BrowserCandidate{replaced}, browser, stored); !ok || reason != "endpoint_changed" {
		t.Fatalf("replacement = %q/%t", reason, ok)
	}
	if got := endpointAuthority("wss://Host.Test/x"); got != "host.test:443" {
		t.Fatalf("endpointAuthority = %q", got)
	}
	if got := endpointAuthority("ftp://host.test"); got != "" {
		t.Fatalf("endpointAuthority(ftp) = %q", got)
	}
}

func requireClassified(t *testing.T, err error, code webmcp.ErrorCode, key string, value any) {
	t.Helper()
	if details := requireDetails(t, err, code); details[key] != value {
		t.Fatalf("error = %#v, want %s with %s=%v", err, code, key, value)
	}
}

// requireDetails asserts err classifies as code and returns its details.
func requireDetails(t *testing.T, err error, code webmcp.ErrorCode) map[string]any {
	t.Helper()
	var classified *webmcp.ClassifiedError
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error = %#v, want %s", err, code)
	}
	return classified.Details
}
