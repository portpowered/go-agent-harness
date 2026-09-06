package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The committed shared fixture backing both s2s-v7a cases lives with the other
// registered-scenario fixtures owned by go-llm-gateway/pkg/testing.
const v7aMetricsFixture = "../../../../go-llm-gateway/pkg/testing/testdata/session-fixtures/s2s-v7a-metrics-modality.session.json"

// The lane's headline proof through the public probe entrypoint: text input,
// audio output, and one streamed tool call emit per-direction audio, text,
// and tool series whose totals reconcile exactly with the observed delta
// stream, and the run leaves a passing JSONL result artifact.
func TestProbeRunS2SV7AMetricsModalityReconcilesOffline(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "v7a-results.jsonl")
	run := executeCLI("probe", "run", "--replay", v7aMetricsFixture, "--json",
		"--scenario", "s2s-v7a-metrics-modality",
		"--out", outPath)
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	raw, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatalf("read JSONL results: %v", readErr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &result); err != nil {
		t.Fatalf("malformed JSONL result: %q", raw)
	}
	if result["pass"] != true || result["name"] != "s2s-v7a-metrics-modality" {
		t.Fatalf("unexpected result line: %v", result)
	}
	kinds := map[string]bool{}
	for _, expectation := range result["expectations"].([]any) {
		outcome := expectation.(map[string]any)
		if outcome["passed"] != true {
			t.Fatalf("expectation must pass: %v", outcome)
		}
		kinds[outcome["kind"].(string)] = true
	}
	if !kinds["metrics-reconcile"] || !kinds["transcript-contains"] {
		t.Fatalf("result must carry passing metrics-reconcile and transcript-contains outcomes: %v", kinds)
	}
}

// The negative control proves the reconciliation is enforced: the same fixture
// and expectations with an injected off-by-one overcount on output/tool must
// fail through the public CLI naming the metrics-reconcile kind with
// expected-vs-actual detail while the untouched transcript stays reconciled.
func TestProbeRunS2SV7AOvercountFailsNamingOutputTool(t *testing.T) {
	run := executeCLI("probe", "run", "--replay", v7aMetricsFixture, "--json",
		"--scenario", "s2s-v7a-metrics-modality-overcount")
	if run.exitCode == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%q stderr=%q", run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if summary["status"] != "fail" {
		t.Fatalf("summary must record failure: %v", summary)
	}
	result := results[0]
	if result["pass"] != false || result["name"] != "s2s-v7a-metrics-modality-overcount" {
		t.Fatalf("unexpected result line: %v", result)
	}
	failed := map[string]map[string]any{}
	for _, expectation := range result["expectations"].([]any) {
		outcome := expectation.(map[string]any)
		if outcome["passed"] == false {
			failed[outcome["kind"].(string)] = outcome
		} else if outcome["kind"] == "transcript-contains" {
			continue
		} else {
			t.Fatalf("only the metrics reconciliation may fail: %v", outcome)
		}
	}
	outcome, ok := failed["metrics-reconcile"]
	if !ok {
		t.Fatalf("injected overcount must fail the metrics-reconcile kind: %v", result["expectations"])
	}
	for _, field := range []string{"expected", "actual"} {
		value, _ := outcome[field].(string)
		if !strings.Contains(value, "output/tool") {
			t.Fatalf("%s detail must name output/tool, got %q", field, value)
		}
	}
	expected, _ := outcome["expected"].(string)
	actual, _ := outcome["actual"].(string)
	if !strings.Contains(expected, "16") || !strings.Contains(actual, "17") {
		t.Fatalf("detail must show observed sum 16 vs reported total 17, got expected=%q actual=%q", expected, actual)
	}
}
