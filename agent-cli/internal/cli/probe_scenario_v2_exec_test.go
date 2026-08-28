package cli

import (
	"bytes"
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
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestProbeRunScenarioV2ExecutesBrowserFixtureWithoutReplayFlag(t *testing.T) {
	dir := t.TempDir()
	toolSchema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"additionalProperties":false}`)
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
				URL:                  "https://fixture.test/?token=fixture-secret#fragment-secret",
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
			{Expect: testkit.OperationExpectation{Type: testkit.OperationInvokeTool, FrameID: "frame-1", ToolName: "read_state", Input: json.RawMessage(`{"count":9007199254740993}`)}, Result: json.RawMessage(`{"invocation_id":"browser-inv-1"}`), Emit: []testkit.EmittedEvent{{
				Type:         testkit.EmittedToolResponded,
				InvocationID: "browser-inv-1",
				Status:       "Completed",
				Output:       json.RawMessage(`{"value":9007199254740993}`),
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

	schemaDigest := sha256.Sum256([]byte(`{"additionalProperties":false,"properties":{"count":{"type":"integer"}},"type":"object"}`))
	stableRef, err := webmcp.StableToolRef(webmcp.ToolDescriptor{
		BrowserID:    "fixture-browser",
		TargetID:     "tab-1",
		FrameID:      "frame-1",
		Generation:   1,
		Name:         "read_state",
		Description:  "Read fixture state",
		InputSchema:  json.RawMessage(`{"additionalProperties":false,"properties":{"count":{"type":"integer"}},"type":"object"}`),
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
			{Type: probe.ScenarioV2StepWebMCPInvoke, ToolRef: string(stableRef), InputJSON: `{"count":9007199254740993}`, Reason: "read fixture state"},
			{Type: probe.ScenarioV2StepClose},
		},
		Expectations: []probe.ScenarioV2Expectation{
			{Type: probe.ScenarioV2ExpectationBrowserCountEquals, Equals: 1, HasEquals: true},
			{Type: probe.ScenarioV2ExpectationSelectedTabEquals, TargetID: "tab-1"},
			{Type: probe.ScenarioV2ExpectationToolCatalogContains, Name: "read_state"},
			{Type: probe.ScenarioV2ExpectationToolInvocationCount, Name: "read_state", Equals: 1, HasEquals: true},
			{Type: probe.ScenarioV2ExpectationToolResultJSONPathEquals, Name: "read_state", Path: "$.value", Value: json.RawMessage(`9007199254740993`)},
			{Type: probe.ScenarioV2ExpectationNoPendingInvocations},
			{Type: probe.ScenarioV2ExpectationBrowserConnectionClosed},
			{Type: probe.ScenarioV2ExpectationPageStateEquals, Path: "$", Value: json.RawMessage(`{}`)},
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
	recordingRoot := filepath.Join(dir, "evidence")
	run := executeCLI("probe", "run", scenarioPath, "--json", "--recording-root", recordingRoot)
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
	objective, ok := results[0]["objective_evidence"].(map[string]any)
	if !ok || objective["verified"] != true {
		t.Fatalf("objective evidence = %v, want verified", results[0]["objective_evidence"])
	}
	evidence, ok := results[0]["evidence"].(map[string]any)
	if !ok {
		t.Fatalf("missing evidence summary: %v", results[0])
	}
	manifestPath, ok := evidence["manifest_path"].(string)
	if !ok {
		t.Fatalf("manifest path = %v", evidence["manifest_path"])
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read finalized manifest: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode finalized manifest: %v", err)
	}
	if manifest["format_version"] != float64(2) {
		t.Fatalf("manifest format = %v, want v2", manifest["format_version"])
	}
	artifactEntries, ok := manifest["artifacts"].([]any)
	if !ok {
		t.Fatalf("manifest artifacts = %T", manifest["artifacts"])
	}
	artifactPaths := map[string]string{}
	for _, rawArtifact := range artifactEntries {
		artifact, ok := rawArtifact.(map[string]any)
		if !ok {
			t.Fatalf("manifest artifact = %T", rawArtifact)
		}
		path, _ := artifact["path"].(string)
		digest, _ := artifact["sha256"].(string)
		if _, duplicate := artifactPaths[path]; duplicate {
			t.Fatalf("manifest lists artifact %q more than once", path)
		}
		artifactPaths[path] = digest
	}
	for _, key := range []string{"provider_capture_path", "browser_events_path", "page_state_path", "workspace_snapshot_path", "objective_evidence_path"} {
		path, _ := evidence[key].(string)
		if path == "" {
			t.Fatalf("evidence %s is empty: %v", key, evidence)
		}
		if filepath.Dir(path) != filepath.Dir(manifestPath) {
			t.Fatalf("evidence %s = %q is outside manifest bundle %q", key, path, filepath.Dir(manifestPath))
		}
		relative, err := filepath.Rel(filepath.Dir(manifestPath), path)
		if err != nil {
			t.Fatalf("relative evidence path %s: %v", key, err)
		}
		if _, ok := artifactPaths[filepath.ToSlash(relative)]; !ok {
			t.Fatalf("evidence %s path %q is not listed in manifest: %v", key, relative, artifactPaths)
		}
	}
	browserData, readErr := os.ReadFile(evidence["browser_events_path"].(string))
	if readErr != nil {
		t.Fatalf("read browser event artifact: %v", readErr)
	}
	if !bytes.Contains(browserData, []byte(`9007199254740993`)) || bytes.Contains(browserData, []byte(`"9007199254740993"`)) {
		t.Fatalf("large integer was not retained as JSON data: %s", browserData)
	}
	if bytes.Contains(browserData, []byte("token=fixture-secret")) || bytes.Contains(browserData, []byte("fragment-secret")) {
		t.Fatalf("browser URL query or fragment survived redaction: %s", browserData)
	}
	var browserEvents []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(browserData), []byte{'\n'}) {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode browser event: %v", err)
		}
		browserEvents = append(browserEvents, event)
	}
	seenEventTypes := map[string]bool{}
	for _, event := range browserEvents {
		seenEventTypes[event["type"].(string)] = true
	}
	for _, eventType := range []string{
		"browser.discovery.started", "browser.discovery.completed", "browser.endpoint.version",
		"browser.targets.snapshot", "browser.target.selected", "browser.chrome.target_attached",
		"browser.webmcp.enabled", "browser.catalog.tool_added", "browser.catalog.ready",
		"browser.invocation.created", "browser.invocation.dispatched", "browser.invocation.completed",
		"browser.target.detached", "browser.chrome.target_closed",
	} {
		if !seenEventTypes[eventType] {
			t.Fatalf("browser event stream omitted %q: %s", eventType, browserData)
		}
	}
	browserManifest, ok := manifest["browser"].(map[string]any)
	if !ok {
		t.Fatalf("manifest browser object = %v", manifest["browser"])
	}
	browserArtifact, ok := browserManifest["artifact"].(map[string]any)
	if !ok || browserArtifact["path"] != "browser.events.jsonl" {
		t.Fatalf("browser manifest artifact = %v", browserManifest["artifact"])
	}
	if artifactPaths["browser.events.jsonl"] != browserArtifact["sha256"] {
		t.Fatalf("browser digest mismatch between manifest surfaces: %v vs %v", artifactPaths["browser.events.jsonl"], browserArtifact["sha256"])
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(manifestPath), "browser", "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected secondary browser manifest stat error = %v", err)
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

func TestProbeScenarioV2ObjectiveVerifierUsesPersistedPageState(t *testing.T) {
	scenario := probe.ScenarioV2{
		SchemaVersion:  probe.ScenarioV2Version,
		ID:             "oracle-negative-control",
		BrowserFixture: "fixture.browser.json",
		Expectations: []probe.ScenarioV2Expectation{{
			Type:  probe.ScenarioV2ExpectationPageStateEquals,
			Path:  "$.value",
			Value: json.RawMessage(`"expected"`),
		}},
	}
	events := []testkit.Event{{
		Version:   testkit.BrowserEventsVersion,
		Sequence:  1,
		Type:      testkit.EventBrowserDiscoveryCompleted,
		Payload:   testkit.MustJSONValue(map[string]any{"candidate_count": 1}),
		Redaction: testkit.RedactionMetadata{Mode: testkit.RedactionNone},
	}}
	wrong := verifyProbeScenarioV2EvidenceData(scenario, events, json.RawMessage(`{"value":"wrong"}`), gatewaytesting.SessionCapture{}, true)
	if wrong.Verified || !strings.Contains(wrong.Error, "page_state_equals") || strings.Contains(wrong.Error, "wrong") {
		t.Fatalf("wrong oracle verification = %+v, want a safe mismatch", wrong)
	}
	right := verifyProbeScenarioV2EvidenceData(scenario, events, json.RawMessage(`{"value":"expected"}`), gatewaytesting.SessionCapture{}, true)
	if !right.Verified {
		t.Fatalf("matching oracle verification = %+v, want verified", right)
	}
}
