package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/testkit"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

func TestProbeRunScenarioV2ExecutesBrowserFixtureWithoutReplayFlag(t *testing.T) {
	dir := t.TempDir()
	toolSchema := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	script := testkit.BrowserScript{
		Version: testkit.BrowserScriptVersion,
		Endpoint: testkit.BrowserEndpoint{
			Version: testkit.EndpointVersionInfo{
				Browser:              "Chrome/Fixture",
				ProtocolVersion:      "1.3",
				WebSocketDebuggerURL: "ws://fixture/browser",
			},
			Targets: []testkit.BrowserTarget{{
				ID:                   "tab-1",
				Type:                 "page",
				Title:                "Fixture",
				URL:                  "https://fixture.test/",
				WebSocketDebuggerURL: "ws://fixture/page/tab-1",
			}},
		},
		Operations: []testkit.BrowserScriptOperation{
			{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableLifecycle}, Result: json.RawMessage(`{}`)},
			{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableWebMCP}, Result: json.RawMessage(`{}`), Emit: []testkit.EmittedEvent{{
				Type: testkit.EmittedToolsAdded,
				Tools: []testkit.ToolDescriptor{{
					Name:        "read_state",
					Description: "Read fixture state",
					FrameID:     "frame-1",
					InputSchema: toolSchema,
					Annotations: json.RawMessage(`{"read_only":true}`),
				}},
			}}},
			{Expect: testkit.OperationExpectation{Type: testkit.OperationInvokeTool, FrameID: "frame-1", ToolName: "read_state", Input: json.RawMessage(`{}`)}, Result: json.RawMessage(`{"invocation_id":"browser-inv-1"}`), Emit: []testkit.EmittedEvent{{
				Type:         testkit.EmittedToolResponded,
				InvocationID: "browser-inv-1",
				Status:       "Completed",
				Output:       json.RawMessage(`{"value":42}`),
			}}},
		},
	}
	browserPath := filepath.Join(dir, "browser.json")
	browserData, err := json.Marshal(script)
	if err != nil {
		t.Fatalf("marshal browser fixture: %v", err)
	}
	if err := os.WriteFile(browserPath, browserData, 0o644); err != nil {
		t.Fatalf("write browser fixture: %v", err)
	}

	schemaDigest := sha256.Sum256([]byte(`{"additionalProperties":false,"properties":{},"type":"object"}`))
	stableRef, err := webmcp.StableToolRef(webmcp.ToolDescriptor{
		BrowserID:    "fixture-browser",
		TargetID:     "tab-1",
		FrameID:      "frame-1",
		Generation:   1,
		Name:         "read_state",
		Description:  "Read fixture state",
		InputSchema:  json.RawMessage(`{"additionalProperties":false,"properties":{},"type":"object"}`),
		SchemaDigest: fmt.Sprintf("%x", schemaDigest[:]),
		Origin:       "https://fixture.test",
		Annotations:  webmcp.ToolAnnotations{Raw: json.RawMessage(`{"read_only":true}`)},
	})
	if err != nil {
		t.Fatalf("derive stable tool ref: %v", err)
	}
	scenario := probe.ScenarioV2{
		SchemaVersion:  probe.ScenarioV2Version,
		ID:             "fixture-browser-happy",
		Name:           "fixture browser happy",
		BrowserFixture: "browser.json",
		Steps: []probe.ScenarioV2Step{
			{Type: probe.ScenarioV2StepBrowserConnect, BrowserID: "fixture-browser"},
			{Type: probe.ScenarioV2StepBrowserDiscover, BrowserID: "fixture-browser"},
			{Type: probe.ScenarioV2StepBrowserSelect, BrowserID: "fixture-browser", TargetID: "tab-1"},
			{Type: probe.ScenarioV2StepWebMCPWaitReady},
			{Type: probe.ScenarioV2StepWebMCPListTools, IncludeSchemas: true, HasIncludeSchemas: true},
			{Type: probe.ScenarioV2StepWebMCPInvoke, ToolRef: string(stableRef), InputJSON: `{}`, Reason: "read fixture state"},
			{Type: probe.ScenarioV2StepClose},
		},
		Expectations: []probe.ScenarioV2Expectation{
			{Type: probe.ScenarioV2ExpectationBrowserCountEquals, Equals: 1, HasEquals: true},
			{Type: probe.ScenarioV2ExpectationSelectedTabEquals, TargetID: "tab-1"},
			{Type: probe.ScenarioV2ExpectationToolCatalogContains, Name: "read_state"},
			{Type: probe.ScenarioV2ExpectationToolInvocationCount, Name: "read_state", Equals: 1, HasEquals: true},
			{Type: probe.ScenarioV2ExpectationToolResultJSONPathEquals, Name: "read_state", Path: "$.value", Value: json.RawMessage(`42`)},
			{Type: probe.ScenarioV2ExpectationNoPendingInvocations},
			{Type: probe.ScenarioV2ExpectationBrowserConnectionClosed},
		},
	}
	scenarioPath := filepath.Join(dir, "fixture-browser-happy.scenario.json")
	scenarioData, err := json.Marshal(scenario)
	if err != nil {
		t.Fatalf("marshal v2 scenario: %v", err)
	}
	if err := os.WriteFile(scenarioPath, scenarioData, 0o644); err != nil {
		t.Fatalf("write v2 scenario: %v", err)
	}
	run := executeCLI("probe", "run", scenarioPath, "--json")
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["id"] != "fixture-browser-happy" || results[0]["schema_version"] != probe.ScenarioV2Version || results[0]["pass"] != true {
		t.Fatalf("unexpected v2 result: %v", results[0])
	}
	if summary["status"] != "pass" || summary["passed"] != float64(1) {
		t.Fatalf("unexpected v2 summary: %v", summary)
	}
}

func TestProbeRunScenarioV2ReportsBrowserFixtureFailureAsResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken-browser.scenario.json")
	document := `{
  "schema_version": "probe.scenario.v2",
  "id": "broken-browser",
  "browser_fixture": "missing.browser.json",
  "steps": [{"type":"browser_connect","browser_id":"fixture-browser"}],
  "expectations": [{"type":"browser_count_equals","equals":1}]
}`
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatalf("write broken v2 scenario: %v", err)
	}

	run := executeCLI("probe", "run", path, "--json")
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["pass"] != false || !strings.Contains(results[0]["error"].(string), "browser fixture") {
		t.Fatalf("missing browser fixture failure result: %v", results[0])
	}
	if summary["status"] != "fail" || summary["failed"] != float64(1) {
		t.Fatalf("unexpected failure summary: %v", summary)
	}
}
