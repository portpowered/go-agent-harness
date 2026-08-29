package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

func TestProbeRunCommittedWebMCPExamples(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	examples := filepath.Join(filepath.Dir(sourcePath), "..", "..", "testdata", "probe-scenarios", "webmcp")
	recordingRoot := filepath.Join(t.TempDir(), "recordings")
	paths := []string{
		filepath.Join(examples, "happy-page-tool.scenario.json"),
		filepath.Join(examples, "stale-ref-recovery.scenario.json"),
	}
	runArgs := append([]string{"probe", "run"}, paths...)
	runArgs = append(runArgs, "--json", "--recording-root", recordingRoot)
	run := executeCLI(runArgs...)
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, len(paths), run.stdout, run.stderr)
	if summary["status"] != "pass" || summary["total"] != float64(2) || summary["passed"] != float64(2) || summary["failed"] != float64(0) {
		t.Fatalf("unexpected WebMCP example summary: %v", summary)
	}

	byID := make(map[string]map[string]any, len(results))
	for _, result := range results {
		id, _ := result["id"].(string)
		byID[id] = result
		if result["schema_version"] != probe.ScenarioV2Version || result["pass"] != true {
			t.Fatalf("unexpected WebMCP example result: %v", result)
		}
		objective, ok := result["objective_evidence"].(map[string]any)
		if !ok || objective["verified"] != true {
			t.Fatalf("objective evidence for %q = %v, want verified", id, result["objective_evidence"])
		}
		assertProbeScenarioV2ExampleEvidence(t, result)
	}

	happy := byID["webmcp-happy-page-tool"]
	if happy == nil {
		t.Fatalf("happy example result missing: %v", results)
	}
	recovery := byID["webmcp-stale-ref-recovery"]
	if recovery == nil {
		t.Fatalf("recovery example result missing: %v", results)
	}
	happyEvents := readProbeScenarioV2ExampleArtifact(t, happy, "browser_events_path")
	if !strings.Contains(happyEvents, "browser.invocation.completed") || !strings.Contains(happyEvents, "read_state") {
		t.Fatalf("happy browser evidence omits successful page-tool invocation: %s", happyEvents)
	}
	recoveryEvents := readProbeScenarioV2ExampleArtifact(t, recovery, "browser_events_path")
	for _, marker := range []string{"browser.page.generation_changed", "stale_tool_ref", "browser.catalog.ready", "browser.invocation.completed"} {
		if !strings.Contains(recoveryEvents, marker) {
			t.Fatalf("recovery browser evidence omits %q: %s", marker, recoveryEvents)
		}
	}
}

func assertProbeScenarioV2ExampleEvidence(t *testing.T, result map[string]any) {
	t.Helper()
	evidence, ok := result["evidence"].(map[string]any)
	if !ok {
		t.Fatalf("result %q has no evidence summary: %v", result["id"], result)
	}
	manifestPath, ok := evidence["manifest_path"].(string)
	if !ok || manifestPath == "" {
		t.Fatalf("result %q manifest path = %v", result["id"], evidence["manifest_path"])
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read %q manifest: %v", result["id"], err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("decode %q manifest: %v", result["id"], err)
	}
	if manifest["format_version"] != float64(2) {
		t.Fatalf("%q manifest format = %v, want 2", result["id"], manifest["format_version"])
	}
	artifacts, ok := manifest["artifacts"].([]any)
	if !ok || len(artifacts) == 0 {
		t.Fatalf("%q manifest artifacts = %v", result["id"], manifest["artifacts"])
	}
	artifactPaths := make(map[string]bool, len(artifacts))
	for _, rawArtifact := range artifacts {
		artifact, ok := rawArtifact.(map[string]any)
		if !ok {
			t.Fatalf("%q manifest artifact = %T", result["id"], rawArtifact)
		}
		path, _ := artifact["path"].(string)
		if path == "" || artifactPaths[path] {
			t.Fatalf("%q manifest has duplicate or empty artifact path: %v", result["id"], artifact)
		}
		artifactPaths[path] = true
	}
	for _, key := range []string{"provider_capture_path", "browser_events_path", "page_state_path", "workspace_snapshot_path", "objective_evidence_path"} {
		path, _ := evidence[key].(string)
		if path == "" {
			t.Fatalf("%q evidence %s is empty: %v", result["id"], key, evidence)
		}
		if filepath.Dir(path) != filepath.Dir(manifestPath) {
			t.Fatalf("%q evidence %s = %q is outside manifest bundle", result["id"], key, path)
		}
		relative, err := filepath.Rel(filepath.Dir(manifestPath), path)
		if err != nil || !artifactPaths[filepath.ToSlash(relative)] {
			t.Fatalf("%q evidence %s = %q is not listed in manifest", result["id"], key, path)
		}
		if data, err := os.ReadFile(path); err != nil || len(data) == 0 {
			t.Fatalf("%q evidence %s unreadable or empty: %v", result["id"], key, err)
		}
	}
}

func readProbeScenarioV2ExampleArtifact(t *testing.T, result map[string]any, key string) string {
	t.Helper()
	evidence := result["evidence"].(map[string]any)
	path := evidence[key].(string)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q artifact %s: %v", result["id"], key, err)
	}
	return string(data)
}
