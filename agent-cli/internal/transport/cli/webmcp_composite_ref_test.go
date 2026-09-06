package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func TestSplitCompositeTargetRef(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		browserID string
		targetID  string
		composite bool
	}{
		{name: "listed reference", value: "browser-43ad63a7d6b3aa5b025bd9a2/target-19c4534b68cb15fa63723a47", browserID: "browser-43ad63a7d6b3aa5b025bd9a2", targetID: "target-19c4534b68cb15fa63723a47", composite: true},
		{name: "bare target", value: "target-19c4534b68cb15fa63723a47", composite: false},
		{name: "empty", value: "", composite: false},
		{name: "empty browser half", value: "/target-a", composite: false},
		{name: "empty target half", value: "browser-a/", composite: false},
		{name: "unsafe characters", value: "browser a/target b", composite: false},
		{name: "extra separator", value: "browser-a/target-b/extra", composite: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			browserID, targetID, composite := splitCompositeTargetRef(testCase.value)
			if composite != testCase.composite || browserID != testCase.browserID || targetID != testCase.targetID {
				t.Fatalf("splitCompositeTargetRef(%q) = (%q, %q, %t), want (%q, %q, %t)",
					testCase.value, browserID, targetID, composite, testCase.browserID, testCase.targetID, testCase.composite)
			}
		})
	}
}

// TestProductionWebMCPCLISelectAcceptsListedCompositeReference locks the
// tabs->select contract for the exact "browserID/targetID" token that the
// human-readable tabs listing prints: handing that token back verbatim to
// `select --tab` must succeed in a fresh process, and a composite reference
// naming a different browser than an explicit --browser must fail closed.
// Reproduced live 2026-08-29 against pinned headless Chrome 152.0.7977.64:
// the composite form failed stale_selection/target_not_found while the bare
// target ID succeeded.
func TestProductionWebMCPCLISelectAcceptsListedCompositeReference(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/version" {
			http.NotFound(writer, request)
			return
		}
		browserWebSocket := "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/stable"
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"Browser":"Chrome/Test","Protocol-Version":"1.3","webSocketDebuggerUrl":%q}`, browserWebSocket)
	}))
	t.Cleanup(server.Close)

	runtime := &productionFakeRuntime{
		targets: []webmcp.Target{{
			ID:               "raw-tab",
			Type:             "page",
			Title:            "Composite fixture",
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
	if len(tabsData.Tabs) != 1 || tabsData.Tabs[0].BrowserID == "" || tabsData.Tabs[0].TargetID == "" {
		t.Fatalf("tabs = %+v", tabsData)
	}
	listed := tabsData.Tabs[0]
	composite := listed.BrowserID + "/" + listed.TargetID

	selected := executeShippedWebMCPCommand(t, configDir, newFactory(), "select", "--tab", composite, "--json")
	selectionEnvelope := requireDirectSuccess(t, selected)
	var selectedData WebMCPDirectContext
	decodeDirectData(t, selectionEnvelope.Data, &selectedData)
	if selectedData.BrowserID != listed.BrowserID || selectedData.TargetID != listed.TargetID || !selectedData.Connected || !selectedData.Ready {
		t.Fatalf("composite selection = %+v, listed=%+v", selectedData, listed)
	}

	mismatched := executeShippedWebMCPCommand(t, configDir, newFactory(), "select", "--browser", "browser-does-not-match", "--tab", composite, "--json")
	if mismatched.err == nil {
		t.Fatalf("mismatched composite selection unexpectedly succeeded: stdout=%s", mismatched.stdout)
	}
	mismatchEnvelope := decodeDirectEnvelope(t, mismatched.stdout)
	if mismatchEnvelope.OK || mismatchEnvelope.Error == nil {
		t.Fatalf("mismatched composite selection envelope = %+v", mismatchEnvelope)
	}
	if mismatchEnvelope.Error.Code != string(webmcp.ErrorStaleSelection) || mismatchEnvelope.Error.Details["reason"] != "selector_browser_mismatch" {
		t.Fatalf("mismatched composite selection error = %+v", mismatchEnvelope.Error)
	}
}
