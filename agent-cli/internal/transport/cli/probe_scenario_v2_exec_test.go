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
)

const (
	probeV2FixtureBrowserID = "fixture-browser"
	probeV2FixtureTabID     = "tab-1"
	probeV2ReadStateTool    = "read_state"
	probeV2FixtureFrameID   = "frame-1"
	probeV2BrowserEvents    = "browser.events.jsonl"
	probeV2EvidenceKey      = "evidence"
	probeV2BrowserEventsKey = "browser_events_path"
	probeV2FixtureOrigin    = "https://fixture.test"
)

// probeV2EvidencePathKeys are the result evidence fields that must each name
// an artifact listed in the bundle manifest.
func probeV2EvidencePathKeys() []string {
	return []string{"provider_capture_path", probeV2BrowserEventsKey, "page_state_path", "workspace_snapshot_path", "objective_evidence_path"}
}

func probeV2FixtureScript(pageURL, browserWSURL, pageWSURL string, tools []testkit.ToolDescriptor, extra ...testkit.BrowserScriptOperation) testkit.BrowserScript {
	operations := []testkit.BrowserScriptOperation{
		{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableLifecycle}, Result: json.RawMessage(`{}`)},
		{Expect: testkit.OperationExpectation{Type: testkit.OperationEnableWebMCP}, Result: json.RawMessage(`{}`), Emit: []testkit.EmittedEvent{{
			Type:  testkit.EmittedToolsAdded,
			Tools: tools,
		}}},
	}
	return testkit.BrowserScript{
		Version: testkit.BrowserScriptVersion,
		Endpoint: testkit.BrowserEndpoint{
			Version: testkit.EndpointVersionInfo{Browser: "Chrome/Fixture", ProtocolVersion: "1.3", WebSocketDebuggerURL: browserWSURL},
			Targets: []testkit.BrowserTarget{{ID: probeV2FixtureTabID, Type: "page", Title: "Fixture", URL: pageURL, WebSocketDebuggerURL: pageWSURL}},
		},
		Operations: append(operations, extra...),
	}
}

func writeProbeV2JSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", filepath.Base(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
}

func probeV2HappyScenario(t *testing.T) probe.ScenarioV2 {
	t.Helper()
	canonicalSchema := `{"additionalProperties":false,"properties":{"count":{"type":"integer"}},"type":"object"}`
	schemaDigest := sha256.Sum256([]byte(canonicalSchema))
	stableRef, err := webmcp.StableToolRef(webmcp.ToolDescriptor{
		BrowserID: probeV2FixtureBrowserID, TargetID: probeV2FixtureTabID, FrameID: probeV2FixtureFrameID, Generation: 1,
		Name: probeV2ReadStateTool, Description: "Read fixture state", InputSchema: json.RawMessage(canonicalSchema),
		SchemaDigest: fmt.Sprintf("%x", schemaDigest[:]), Origin: probeV2FixtureOrigin,
		Annotations: webmcp.ToolAnnotations{Raw: json.RawMessage(`{"read_only":true}`)},
	})
	if err != nil {
		t.Fatalf("derive stable tool ref: %v", err)
	}
	return probe.ScenarioV2{
		SchemaVersion:  probe.ScenarioV2Version,
		ID:             "fixture-browser-happy",
		Name:           "fixture browser happy",
		BrowserFixture: "browser.json",
		Steps: []probe.ScenarioV2Step{
			{Type: probe.ScenarioV2StepBrowserConnect, BrowserID: probeV2FixtureBrowserID},
			{Type: probe.ScenarioV2StepBrowserDiscover, BrowserID: probeV2FixtureBrowserID},
			{Type: probe.ScenarioV2StepBrowserSelect, BrowserID: probeV2FixtureBrowserID, TargetID: probeV2FixtureTabID},
			{Type: probe.ScenarioV2StepWebMCPWaitReady},
			{Type: probe.ScenarioV2StepWebMCPListTools, IncludeSchemas: true, HasIncludeSchemas: true},
			{Type: probe.ScenarioV2StepWebMCPInvoke, ToolRef: string(stableRef), InputJSON: `{"count":9007199254740993}`, Reason: "read fixture state"},
			{Type: probe.ScenarioV2StepClose},
		},
		Expectations: []probe.ScenarioV2Expectation{
			{Type: probe.ScenarioV2ExpectationBrowserCountEquals, Equals: 1, HasEquals: true},
			{Type: probe.ScenarioV2ExpectationSelectedTabEquals, TargetID: probeV2FixtureTabID},
			{Type: probe.ScenarioV2ExpectationToolCatalogContains, Name: probeV2ReadStateTool},
			{Type: probe.ScenarioV2ExpectationToolInvocationCount, Name: probeV2ReadStateTool, Equals: 1, HasEquals: true},
			{Type: probe.ScenarioV2ExpectationToolResultJSONPathEquals, Name: probeV2ReadStateTool, Path: "$.value", Value: json.RawMessage(`9007199254740993`)},
			{Type: probe.ScenarioV2ExpectationChromeOperationOrder, Operations: []string{"connect", "select", "invoke"}},
			{Type: probe.ScenarioV2ExpectationNoUnexpectedChromeOperations, Operations: []string{"connect", "discover", "select", "attach", "list_tools", "invoke", "detach", "close"}},
			{Type: probe.ScenarioV2ExpectationGeneratedCDPMethodOrder, Methods: []string{"WebMCP.enable", "WebMCP.invokeTool"}},
			{Type: probe.ScenarioV2ExpectationNoUnexpectedGeneratedCDPMethods, Methods: []string{"WebMCP.enable", "WebMCP.invokeTool"}},
			{Type: probe.ScenarioV2ExpectationNoPendingInvocations},
			{Type: probe.ScenarioV2ExpectationBrowserConnectionClosed},
			{Type: probe.ScenarioV2ExpectationPageStateEquals, Path: "$", Value: json.RawMessage(`{}`)},
		},
	}
}

func TestProbeRunScenarioV2ExecutesBrowserFixtureWithoutReplayFlag(t *testing.T) {
	dir := t.TempDir()
	toolSchema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"additionalProperties":false}`)
	invoke := testkit.BrowserScriptOperation{
		Expect: testkit.OperationExpectation{Type: testkit.OperationInvokeTool, FrameID: probeV2FixtureFrameID, ToolName: probeV2ReadStateTool, Input: json.RawMessage(`{"count":9007199254740993}`)},
		Result: json.RawMessage(`{"invocation_id":"browser-inv-1"}`),
		Emit: []testkit.EmittedEvent{{
			Type: testkit.EmittedToolResponded, InvocationID: "browser-inv-1", Status: "Completed", Output: json.RawMessage(`{"value":9007199254740993}`),
		}},
	}
	tools := []testkit.ToolDescriptor{{
		Name: probeV2ReadStateTool, Description: "Read fixture state", FrameID: probeV2FixtureFrameID,
		InputSchema: toolSchema, Annotations: json.RawMessage(`{"read_only":true}`),
	}}
	writeProbeV2JSON(t, filepath.Join(dir, "browser.json"), probeV2FixtureScript("https://fixture.test/?token=fixture-secret#fragment-secret", "ws://fixture/browser", "ws://fixture/page/tab-1", tools, invoke))
	scenarioPath := filepath.Join(dir, "fixture-browser-happy.scenario.json")
	writeProbeV2JSON(t, scenarioPath, probeV2HappyScenario(t))

	run := executeCLI("probe", "run", scenarioPath, "--json", "--recording-root", filepath.Join(dir, "evidence"))
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["id"] != "fixture-browser-happy" || results[0]["schema_version"] != probe.ScenarioV2Version || results[0]["pass"] != true {
		t.Fatalf("unexpected v2 result: %v", results[0])
	}
	if summary["status"] != probeStatusPass || summary["passed"] != float64(1) {
		t.Fatalf("unexpected v2 summary: %v", summary)
	}
	if objective, ok := results[0]["objective_evidence"].(map[string]any); !ok || objective["verified"] != true {
		t.Fatalf("objective evidence = %v, want verified", results[0]["objective_evidence"])
	}
	evidence, ok := results[0][probeV2EvidenceKey].(map[string]any)
	if !ok {
		t.Fatalf("missing evidence summary: %v", results[0])
	}
	manifestPath, manifest := readProbeV2Manifest(t, evidence)
	artifactPaths := probeV2ManifestArtifacts(t, manifest)
	assertProbeV2EvidenceListed(t, evidence, manifestPath, artifactPaths)
	assertProbeV2BrowserEvents(t, readProbeV2EvidenceFile(t, evidence, probeV2BrowserEventsKey))
	assertProbeV2BrowserManifest(t, manifest, manifestPath, artifactPaths)
}

func readProbeV2Manifest(t *testing.T, evidence map[string]any) (string, map[string]any) {
	t.Helper()
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
	return manifestPath, manifest
}

func probeV2ManifestArtifacts(t *testing.T, manifest map[string]any) map[string]string {
	t.Helper()
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
		path, pathOK := artifact["path"].(string)
		digest, digestOK := artifact["sha256"].(string)
		if !pathOK || !digestOK {
			t.Fatalf("manifest artifact fields = %v", artifact)
		}
		if _, duplicate := artifactPaths[path]; duplicate {
			t.Fatalf("manifest lists artifact %q more than once", path)
		}
		artifactPaths[path] = digest
	}
	return artifactPaths
}

func assertProbeV2EvidenceListed(t *testing.T, evidence map[string]any, manifestPath string, artifactPaths map[string]string) {
	t.Helper()
	for _, key := range probeV2EvidencePathKeys() {
		path, ok := evidence[key].(string)
		if !ok || path == "" {
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
}

func readProbeV2EvidenceFile(t *testing.T, evidence map[string]any, key string) []byte {
	t.Helper()
	path, ok := evidence[key].(string)
	if !ok {
		t.Fatalf("evidence %s = %v", key, evidence[key])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read evidence %s: %v", key, err)
	}
	return data
}

func assertProbeV2BrowserEvents(t *testing.T, browserData []byte) {
	t.Helper()
	if !bytes.Contains(browserData, []byte(`9007199254740993`)) || bytes.Contains(browserData, []byte(`"9007199254740993"`)) {
		t.Fatalf("large integer was not retained as JSON data: %s", browserData)
	}
	if bytes.Contains(browserData, []byte("token=fixture-secret")) || bytes.Contains(browserData, []byte("fragment-secret")) {
		t.Fatalf("browser URL query or fragment survived redaction: %s", browserData)
	}
	seenEventTypes := map[string]bool{}
	for _, line := range bytes.Split(bytes.TrimSpace(browserData), []byte{'\n'}) {
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode browser event: %v", err)
		}
		seenEventTypes[event.Type] = true
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
}

func assertProbeV2BrowserManifest(t *testing.T, manifest map[string]any, manifestPath string, artifactPaths map[string]string) {
	t.Helper()
	browserManifest, ok := manifest["browser"].(map[string]any)
	if !ok {
		t.Fatalf("manifest browser object = %v", manifest["browser"])
	}
	browserArtifact, ok := browserManifest["artifact"].(map[string]any)
	if !ok || browserArtifact["path"] != probeV2BrowserEvents {
		t.Fatalf("browser manifest artifact = %v", browserManifest["artifact"])
	}
	if artifactPaths[probeV2BrowserEvents] != browserArtifact["sha256"] {
		t.Fatalf("browser digest mismatch between manifest surfaces: %v vs %v", artifactPaths[probeV2BrowserEvents], browserArtifact["sha256"])
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
	errorText, isText := results[0]["error"].(string)
	if results[0]["pass"] != false || !isText || !strings.Contains(errorText, "browser fixture") {
		t.Fatalf("missing browser fixture failure result: %v", results[0])
	}
	if summary["status"] != probeStatusFail || summary["failed"] != float64(1) {
		t.Fatalf("unexpected failure summary: %v", summary)
	}
}

const probeV2DivergenceDocument = `{
  "schema_version": "probe.scenario.v2",
  "id": "safe-divergence",
  "browser_fixture": "browser.json",
  "steps": [
    {"type":"browser_connect","browser_id":"fixture-browser"},
    {"type":"browser_discover","browser_id":"fixture-browser"},
    {"type":"browser_select","browser_id":"fixture-browser","target_id":"tab-1"},
    {"type":"close"}
  ],
  "expectations": [
    {"type":"selected_origin_equals","origin":"https://expected.example/?token=expected-secret#expected-fragment"},
    {"type":"browser_connection_closed"}
  ]
}`

func TestProbeRunScenarioV2ReportsSafeFirstObjectiveDivergenceAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	tools := []testkit.ToolDescriptor{{Name: "diagnostic_state", Description: "Diagnostic state tool", FrameID: probeV2FixtureFrameID, InputSchema: json.RawMessage(`{}`)}}
	script := probeV2FixtureScript("https://fixture.test/?token=actual-secret#actual-fragment", "ws://fixture/browser?credential=endpoint-secret", "ws://fixture/page/tab-1?credential=page-secret", tools)
	writeProbeV2JSON(t, filepath.Join(dir, "browser.json"), script)
	scenarioPath := filepath.Join(dir, "divergence.scenario.json")
	if err := os.WriteFile(scenarioPath, []byte(probeV2DivergenceDocument), 0o644); err != nil {
		t.Fatalf("write v2 scenario: %v", err)
	}
	run := executeCLI("probe", "run", scenarioPath, "--json", "--recording-root", filepath.Join(dir, "evidence"))
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	result := results[0]
	if result["pass"] != false || summary["status"] != probeStatusFail {
		t.Fatalf("divergent result = %v summary=%v", result, summary)
	}
	assertProbeV2SafeDivergenceError(t, result)
	assertProbeV2DivergenceIdentity(t, result)
	evidence, ok := result[probeV2EvidenceKey].(map[string]any)
	if !ok {
		t.Fatalf("missing evidence summary for divergence: %v", result)
	}
	browserEvents := readProbeV2EvidenceFile(t, evidence, probeV2BrowserEventsKey)
	if !bytes.Contains(browserEvents, []byte(`browser.chrome.target_closed`)) {
		t.Fatalf("cleanup event missing after objective divergence: %s", browserEvents)
	}
	for _, secret := range []string{"actual-secret", "actual-fragment", "page-secret"} {
		if bytes.Contains(browserEvents, []byte(secret)) {
			t.Fatalf("browser evidence leaked endpoint data %q: %s", secret, browserEvents)
		}
	}
}

func assertProbeV2SafeDivergenceError(t *testing.T, result map[string]any) {
	t.Helper()
	errorText, ok := result["error"].(string)
	if !ok {
		t.Fatalf("divergence result has no error text: %v", result)
	}
	for _, secret := range []string{"expected-secret", "actual-secret", "expected-fragment", "actual-fragment", "endpoint-secret", "page-secret"} {
		if strings.Contains(errorText, secret) {
			t.Fatalf("divergence error leaked %q: %s", secret, errorText)
		}
	}
	for _, marker := range []string{"safe-divergence", "selected_origin_equals", "expectation[0]"} {
		if !strings.Contains(errorText, marker) {
			t.Fatalf("divergence error lacks stable context %q: %s", marker, errorText)
		}
	}
}

func assertProbeV2DivergenceIdentity(t *testing.T, result map[string]any) {
	t.Helper()
	divergence, ok := result["divergence"].(map[string]any)
	if !ok {
		t.Fatalf("missing structured divergence: %v", result)
	}
	if divergence["expectation_index"] != float64(0) || divergence["expectation_type"] != string(probe.ScenarioV2ExpectationSelectedOriginEquals) || divergence["evidence_artifact"] != probeV2BrowserEvents {
		t.Fatalf("unexpected divergence identity: %v", divergence)
	}
	if position, ok := divergence["event_position"].(float64); !ok || position <= 0 {
		t.Fatalf("divergence event position = %v, want positive position", divergence["event_position"])
	}
	if divergence["expected"] != "https://expected.example/" || divergence["actual"] != probeV2FixtureOrigin {
		t.Fatalf("unexpected redacted origin divergence: %v", divergence)
	}
}
