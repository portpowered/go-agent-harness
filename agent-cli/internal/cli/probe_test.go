package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const probeSessionFixture = "../../../go-llm-gateway/pkg/testing/testdata/session-fixtures/session_healthy_multiturn_audio.session.json"

func probeFixtureObservation(t *testing.T) gatewaytesting.SessionReplayProbeReport {
	t.Helper()
	report, err := gatewaytesting.RunSessionReplayProbe(context.Background(), probeSessionFixture)
	if err != nil {
		t.Fatalf("probe fixture observation failed: %v", err)
	}
	return report
}

func writeProbeScenario(t *testing.T, dir, id string, count int) string {
	t.Helper()
	document := fmt.Sprintf(`{
		"id": %q,
		"steps": [
			{"type": "send_text", "text": "hello"},
			{"type": "close"}
		],
		"expectations": [
			{"type": "frame_count", "count": %d}
		]
	}`, id, count)
	path := filepath.Join(dir, id+".scenario.json")
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatalf("write scenario %s: %v", path, err)
	}
	return path
}

func decodeProbeLines(t *testing.T, stdout int, out string, errText string) ([]map[string]any, map[string]any) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != stdout {
		t.Fatalf("result line count = %d, want %d:\n%s", len(lines), stdout, out)
	}
	results := make([]map[string]any, 0, stdout)
	for _, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("decode result line %q: %v", line, err)
		}
		results = append(results, decoded)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(strings.Split(strings.TrimSpace(errText), "\n")[0]), &summary); err != nil {
		t.Fatalf("decode summary from stderr %q: %v", errText, err)
	}
	if summary["status"] == nil {
		t.Fatalf("last line is not a run summary: %q", lines[2])
	}
	return results, summary
}

func TestProbeRunAllPassExitZero(t *testing.T) {
	dir := t.TempDir()
	observation := probeFixtureObservation(t)
	first := writeProbeScenario(t, dir, "session_healthy_multiturn_audio_first", len(observation.Observations))
	second := writeProbeScenario(t, dir, "second", len(observation.Observations))

	run := executeCLI("probe", "run", first, second, "--replay", probeSessionFixture, "--json", "--scenario", writeProbeScenario(t, dir, "third", len(observation.Observations)))
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 3, run.stdout, run.stderr)
	if len(results) != 3 {
		t.Fatalf("result lines = %d, want 3", len(results))
	}
	for _, result := range results {
		if result["pass"] != true {
			t.Fatalf("scenario result not passing: %v", result)
		}
	}
	if summary["status"] != "pass" || summary["passed"] != float64(3) || summary["failed"] != float64(0) || summary["total"] != float64(3) {
		t.Fatalf("unexpected summary: %v", summary)
	}
}

func TestProbeRunFailureExitsNonZeroAndContinues(t *testing.T) {
	dir := t.TempDir()
	observation := probeFixtureObservation(t)
	passing := writeProbeScenario(t, dir, "session_healthy_multiturn_audio_passing", len(observation.Observations))
	failing := writeProbeScenario(t, dir, "failing", 999999)

	run := executeCLI("probe", "run", failing, passing, "--replay", probeSessionFixture, "--json")
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 2, run.stdout, run.stderr)
	var failedResult, passedResult map[string]any
	for _, result := range results {
		switch result["name"] {
		case "failing":
			failedResult = result
		case "session_healthy_multiturn_audio_passing":
			passedResult = result
		}
	}
	if failedResult == nil || passedResult == nil {
		t.Fatalf("missing expected results: %v", results)
	}
	if passedResult["pass"] != true {
		t.Fatalf("remaining scenario did not still execute and pass: %v", passedResult)
	}
	expectations, ok := failedResult["expectations"].([]any)
	if !ok || len(expectations) != 1 {
		t.Fatalf("failed scenario expectation outcomes missing: %v", failedResult)
	}
	outcome := expectations[0].(map[string]any)
	if outcome["passed"] != false || outcome["expected"] == "" || outcome["actual"] == "" {
		t.Fatalf("failed expectation lacks expected/actual detail: %v", outcome)
	}
	if summary["status"] != "fail" || summary["failed"] != float64(1) {
		t.Fatalf("unexpected summary: %v", summary)
	}
}

func TestProbeRunUnknownSelectionNamesIt(t *testing.T) {
	run := executeCLI("probe", "run", filepath.Join(t.TempDir(), "nope.json"), "--replay", probeSessionFixture)
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", run.exitCode)
	}
	if !strings.Contains(run.stderr+run.stdout, "nope.json") {
		t.Fatalf("error does not name the unknown selection: stdout=%q stderr=%q", run.stdout, run.stderr)
	}
}

func TestProbeRunRecordUnsupportedOffline(t *testing.T) {
	scenario := writeProbeScenario(t, t.TempDir(), "session_healthy_multiturn_audio", 4)
	run := executeCLI("probe", "run", scenario, "--record", t.TempDir())
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", run.exitCode)
	}
	if !strings.Contains(run.stderr, "--record is not supported") {
		t.Fatalf("unexpected record error: %q", run.stderr)
	}
}

func TestProbeRunMissingReplayFixtureErrors(t *testing.T) {
	scenario := writeProbeScenario(t, t.TempDir(), "session_healthy_multiturn_audio", 4)
	run := executeCLI("probe", "run", scenario, "--replay", filepath.Join(t.TempDir(), "absent.session.json"))
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", run.exitCode)
	}
	if !strings.Contains(run.stderr, "absent.session.json") {
		t.Fatalf("error does not name the missing fixture: %q", run.stderr)
	}
}

func TestProbeRunInvalidFixtureErrors(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken.session.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write broken fixture: %v", err)
	}
	scenario := writeProbeScenario(t, dir, "session_healthy_multiturn_audio", 4)
	run := executeCLI("probe", "run", scenario, "--replay", broken)
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", run.exitCode, run.stderr)
	}
	if !strings.Contains(run.stderr, "broken.session.json") {
		t.Fatalf("error does not name the invalid fixture: %q", run.stderr)
	}
}

func TestProbeRunRoutesOutSummaryFilesAndDecoration(t *testing.T) {
	dir := t.TempDir()
	scenario := writeProbeScenario(t, dir, "session_healthy_multiturn_audio", 4)
	outPath := filepath.Join(dir, "results.jsonl")
	summaryPath := filepath.Join(dir, "summary.jsonl")

	run := executeCLI("probe", "run", scenario, "--replay", probeSessionFixture, "--out", outPath, "--summary", summaryPath)
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 (deliberately failing expectation); stderr=%q", run.exitCode, run.stderr)
	}
	if !strings.Contains(run.stderr, "probe: 0/1 scenarios passed (fail)") {
		t.Fatalf("human-readable decoration missing without --json: %q", run.stderr)
	}
	outBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read --out file: %v", err)
	}
	outLines := strings.Split(strings.TrimSpace(string(outBytes)), "\n")
	if len(outLines) != 1 {
		t.Fatalf("--out file line count = %d, want 1: %q", len(outLines), outBytes)
	}
	var result map[string]any
	if json.Unmarshal([]byte(outLines[0]), &result) != nil || result["name"] != "session_healthy_multiturn_audio" {
		t.Fatalf("--out file content unexpected: %q", outBytes)
	}
	summaryBytes, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read --summary file: %v", err)
	}
	var summary map[string]any
	if json.Unmarshal(summaryBytes, &summary) != nil || summary["status"] != "fail" {
		t.Fatalf("--summary file content unexpected: %q", summaryBytes)
	}
}

func TestProbeRunBadOutputParentDirectoryErrors(t *testing.T) {
	dir := t.TempDir()
	scenario := writeProbeScenario(t, dir, "session_healthy_multiturn_audio", 4)
	run := executeCLI("probe", "run", scenario, "--replay", probeSessionFixture, "--out", filepath.Join(dir, "missing", "out.jsonl"))
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", run.exitCode)
	}
	if !strings.Contains(run.stderr, "--out") {
		t.Fatalf("error does not mention --out: %q", run.stderr)
	}
}
