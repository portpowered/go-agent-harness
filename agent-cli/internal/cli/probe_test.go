package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
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

const probeFixtureDir = "../../../go-llm-gateway/pkg/testing/testdata/session-fixtures"

const probeReportFixtureDir = "testdata/probe-reports"

func executeCLIWithInput(input string, args ...string) cliExecution {
	root := newTestRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetIn(strings.NewReader(input))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)

	exitCode := 0
	if root.Execute() != nil {
		exitCode = 1
	}
	return cliExecution{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
}

func TestProbeReportAggregatesFixtureArtifactsAndWritesBothOutputs(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "friction.json")
	summaryPath := filepath.Join(dir, "friction.txt")

	run := executeCLI(
		"probe", "report",
		"--out", filepath.Join(probeReportFixtureDir, "run-a.jsonl"),
		"--out", filepath.Join(probeReportFixtureDir, "run-b.jsonl"),
		"--json", jsonPath,
		"--summary", summaryPath,
	)
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 for fixture failures; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	if run.stdout != "" || run.stderr != "" {
		t.Fatalf("file outputs leaked to process streams: stdout=%q stderr=%q", run.stdout, run.stderr)
	}

	jsonBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read JSON report: %v", err)
	}
	var report probe.FrictionReport
	if err := json.Unmarshal(jsonBytes, &report); err != nil {
		t.Fatalf("decode JSON report %q: %v", jsonBytes, err)
	}
	if report.Total != 4 || report.Passed != 2 || report.Failed != 2 || report.Stuck != 0 {
		t.Fatalf("report totals = %+v, want total=4 passed=2 failed=2 stuck=0", report)
	}
	wantScenarios := []probe.ScenarioRollup{
		{Name: "checkout", Total: 2, Passed: 1, Failed: 1},
		{Name: "profile", Total: 1, Passed: 1},
		{Name: "search", Total: 1, Failed: 1},
	}
	if !reflect.DeepEqual(report.Scenarios, wantScenarios) {
		t.Fatalf("scenario rollups = %#v, want %#v", report.Scenarios, wantScenarios)
	}

	summaryBytes, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	summary := string(summaryBytes)
	for _, want := range []string{
		"Probe friction report",
		"Scenarios: 4 total, 2 passed, 2 failed, 0 stuck",
		"checkout: total=2 passed=1 failed=1 stuck=0",
		"authentication: 1",
		"rate_limited: 1",
		"Health: fail",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q does not contain %q", summary, want)
		}
	}
}

func TestProbeReportNoFailAndStdinUseDefaultStreams(t *testing.T) {
	input := `{"name":"stdin-case","pass":false,"expectations":[],"terminal_reason":"error:transport","error":"connection reset"}
{"total":1,"passed":0,"failed":1,"status":"fail"}
`
	run := executeCLIWithInput(input, "probe", "report", "--out", "-", "--no-fail")
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0 with --no-fail; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	var report probe.FrictionReport
	if err := json.Unmarshal([]byte(run.stdout), &report); err != nil {
		t.Fatalf("decode stdout report %q: %v", run.stdout, err)
	}
	if report.Total != 1 || report.Failed != 1 || report.Scenarios[0].Name != "stdin-case" {
		t.Fatalf("stdin report = %+v", report)
	}
	if !strings.Contains(run.stderr, "Health: fail") {
		t.Fatalf("default summary stderr = %q", run.stderr)
	}
}

func TestProbeReportMalformedAndMissingInputsAreTyped(t *testing.T) {
	dir := t.TempDir()
	malformed := filepath.Join(dir, "malformed.jsonl")
	if err := os.WriteFile(malformed, []byte("{\"name\":\"ok\",\"pass\":true}\nnot-json\n"), 0o644); err != nil {
		t.Fatalf("write malformed fixture: %v", err)
	}

	tests := []struct {
		name       string
		path       string
		wantLine   int
		wantErr    error
		wantSource string
	}{
		{name: "malformed line", path: malformed, wantLine: 2, wantErr: probe.ErrMalformedReport, wantSource: malformed},
		{name: "missing file", path: filepath.Join(dir, "missing.jsonl"), wantLine: 0, wantErr: os.ErrNotExist, wantSource: filepath.Join(dir, "missing.jsonl")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newTestRootCommand()
			root.SetArgs([]string{"probe", "report", "--out", tt.path})
			err := root.Execute()
			if err == nil {
				t.Fatal("expected report command error")
			}
			var reportErr *probe.FrictionReportError
			if !errors.As(err, &reportErr) {
				t.Fatalf("error = %v, want *probe.FrictionReportError", err)
			}
			if reportErr.Source != tt.wantSource || reportErr.Line != tt.wantLine {
				t.Fatalf("error context = %+v, want source=%q line=%d", reportErr, tt.wantSource, tt.wantLine)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
		})
	}
}

func TestProbeRunErrorAuthSuiteOfflineExitZero(t *testing.T) {
	run := executeCLI("probe", "run", "--replay", probeFixtureDir, "--json",
		"--scenario", "s2s-v6a-error-auth")
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 2, run.stdout, run.stderr)
	byName := map[string]map[string]any{}
	for _, result := range results {
		byName[result["name"].(string)] = result
	}
	authCase, ok := byName["s2s-v6a-error-auth-invalid-credentials"]
	if !ok || authCase["pass"] != true {
		t.Fatalf("invalid-credentials case missing or failing: %v", authCase)
	}
	if authCase["terminal_reason"] != "error:authentication" {
		t.Fatalf("invalid-credentials terminal reason = %v, want error:authentication", authCase["terminal_reason"])
	}
	healthy, ok := byName["s2s-v6a-error-auth-healthy-control"]
	if !ok || healthy["pass"] != true {
		t.Fatalf("healthy control case missing or failing: %v", healthy)
	}
	if healthy["terminal_reason"] != "disconnect" {
		t.Fatalf("healthy control terminal reason = %v, want disconnect", healthy["terminal_reason"])
	}
	if summary["status"] != "pass" || summary["passed"] != float64(2) || summary["failed"] != float64(0) {
		t.Fatalf("unexpected summary: %v", summary)
	}
}

func TestProbeRunMisclassifiedAuthExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	document := `{
		"id": "misclassified-auth",
		"steps": [{"type": "send_text", "text": "hello"}, {"type": "close"}],
		"expectations": [{"type": "terminal_reason", "value": "disconnect"}]
	}`
	scenario := filepath.Join(dir, "misclassified-auth.scenario.json")
	if err := os.WriteFile(scenario, []byte(document), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	run := executeCLI("probe", "run", scenario, "--replay",
		filepath.Join(probeFixtureDir, "s2s-v6a-error-auth-invalid-credentials.session.json"), "--json")
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["pass"] != false {
		t.Fatalf("misclassified auth case must fail: %v", results[0])
	}
	outcomes := results[0]["expectations"].([]any)
	outcome := outcomes[0].(map[string]any)
	if outcome["passed"] != false || !strings.Contains(fmt.Sprint(outcome["actual"]), "error:authentication") {
		t.Fatalf("failed outcome lacks misclassification detail: %v", outcome)
	}
	if summary["status"] != "fail" || summary["failed"] != float64(1) {
		t.Fatalf("unexpected summary: %v", summary)
	}
}

const s2sFixtureDir = "../../../go-llm-gateway/pkg/testing/testdata/session-fixtures"
const s2sScenarioDir = "../../../go-agent-loop/pkg/probe/testdata/scenarios"

func TestProbeRunS2SV1TextInAudioOutHappyPathExitZero(t *testing.T) {
	report, err := gatewaytesting.RunSessionReplayProbe(context.Background(),
		filepath.Join(s2sFixtureDir, "s2s_v1_text_in_audio_out.session.json"))
	if err != nil {
		t.Fatalf("replay probe failed: %v", err)
	}
	if len(report.Observations) != 9 || report.OutboundTicks != 1 {
		t.Fatalf("unexpected fixture observation: frames=%d ticks=%d", len(report.Observations), report.OutboundTicks)
	}

	run := executeCLI("probe", "run", "s2s-v1-text-in-audio-out",
		"--replay", s2sFixtureDir, "--json")
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["name"] != "s2s_v1_text_in_audio_out" || results[0]["pass"] != true {
		t.Fatalf("happy-path scenario did not pass: %v", results[0])
	}
	for _, expectation := range results[0]["expectations"].([]any) {
		if expectation.(map[string]any)["passed"] != true {
			t.Fatalf("all expectations should pass on happy path: %v", expectation)
		}
	}
	if summary["status"] != "pass" || summary["passed"] != float64(1) {
		t.Fatalf("unexpected summary: %v", summary)
	}
}

func TestProbeRunAbsentAuthErrorExitsNonZero(t *testing.T) {
	run := executeCLI("probe", "run", "--replay", probeFixtureDir, "--json",
		"--scenario", "s2s-v6a-error-auth-invalid-credentials",
		"--scenario", "s2s-v6a-error-auth-healthy-control")
	if run.exitCode != 0 {
		t.Fatalf("control setup exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}

	dir := t.TempDir()
	document := `{
		"id": "absent-error",
		"name": "s2s-v6a-error-auth-invalid-credentials",
		"steps": [{"type": "send_text", "text": "hello"}, {"type": "close"}],
		"expectations": [{"type": "terminal_reason", "value": "error:authentication"}]
	}`
	scenario := filepath.Join(dir, "absent-error.scenario.json")
	if err := os.WriteFile(scenario, []byte(document), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	run = executeCLI("probe", "run", scenario, "--replay",
		filepath.Join(probeFixtureDir, "s2s-v6a-error-auth-healthy-control.session.json"), "--json")
	if run.exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 (absent error must fail); stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, _ := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["pass"] != false {
		t.Fatalf("absent-error case must fail: %v", results[0])
	}
}

func TestProbeRunErrorMalformedResponseSuiteOfflineExitZero(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "results.jsonl")
	summaryPath := filepath.Join(t.TempDir(), "summary.jsonl")
	run := executeCLI("probe", "run", "--replay", probeFixtureDir, "--json",
		"--scenario", "s2s-v6d-error-malformed-response",
		"--out", outPath, "--summary", summaryPath)
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}

	outBytes, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatalf("read JSONL results: %v", readErr)
	}
	lines := strings.Split(strings.TrimSpace(string(outBytes)), "\n")
	if len(lines) != 2 {
		t.Fatalf("result line count = %d, want 2: %q", len(lines), outBytes)
	}
	results := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("decode result line %q: %v", line, err)
		}
		results = append(results, decoded)
	}
	summaryBytes, readErr := os.ReadFile(summaryPath)
	if readErr != nil {
		t.Fatalf("read summary artifact: %v", readErr)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(summaryBytes))), &summary); err != nil {
		t.Fatalf("decode summary artifact %q: %v", summaryBytes, err)
	}

	malformed, healthy := map[string]bool{"s2s-v6d-error-malformed-response-malformed": true}, map[string]bool{"s2s-v6d-error-malformed-response-healthy-control": true}
	for _, result := range results {
		switch result["name"] {
		case "s2s-v6d-error-malformed-response-malformed":
			delete(malformed, result["name"].(string))
			if result["pass"] != true || result["terminal_reason"] != "error:invalid_request" {
				t.Fatalf("malformed case must pass with error:invalid_request: %v", result)
			}
		case "s2s-v6d-error-malformed-response-healthy-control":
			delete(healthy, result["name"].(string))
			if result["pass"] != true || result["terminal_reason"] != "disconnect" {
				t.Fatalf("healthy control must pass with disconnect: %v", result)
			}
		}
	}
	if len(malformed) != 0 || len(healthy) != 0 {
		t.Fatalf("missing expected cases: %v", results)
	}
	if summary["status"] != "pass" || summary["passed"] != float64(2) || summary["failed"] != float64(0) || summary["total"] != float64(2) {
		t.Fatalf("unexpected summary: %v", summary)
	}
}

// TestProbeRunMalformedNegativeControlExitsNonZero is the documented negative
// control for the v6d vertical: feeding the healthy well-formed control
// fixture into the malformed-expectation case must fail with a non-zero exit,
// proving the malformed assertion has discriminating power.
func TestProbeRunMalformedNegativeControlExitsNonZero(t *testing.T) {
	run := executeCLI("probe", "run",
		"--replay", filepath.Join(probeFixtureDir, "s2s-v6d-error-malformed-response-healthy-control.session.json"),
		"--json", "--scenario", "s2s-v6d-error-malformed-response-malformed")
	if run.exitCode == 0 {
		t.Fatalf("negative control exit code = 0, want non-zero; stdout=%q stderr=%q", run.stdout, run.stderr)
	}
	results, _ := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["pass"] != false {
		t.Fatalf("negative control must fail: %v", results[0])
	}
}

func TestDeadguardBoundsHungScenarioExecution(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	hungExec := func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
		<-ctx.Done()
		return probe.ObservationSnapshot{}, ctx.Err()
	}
	exec := deadguardExec(hungExec, 50*time.Millisecond)

	scenario := probe.Scenario{ID: "hung", Name: "hung"}
	snapshot, err := exec(context.Background(), scenario)
	if err == nil {
		t.Fatalf("deadguard must fail a hung scenario execution")
	}
	if !strings.Contains(err.Error(), "deadguard") || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("deadguard error lacks indication: %v", err)
	}
	if snapshot.FrameCount != 0 || snapshot.TerminalReason != "" {
		t.Fatalf("hung scenario must not produce an observation: %+v", snapshot)
	}
}

func TestDeadguardDoesNotFireForQuickHealthyExecution(t *testing.T) {
	quickExec := func(ctx context.Context, scenario probe.Scenario) (probe.ObservationSnapshot, error) {
		return probe.ObservationSnapshot{FrameCount: 2, HasObservedTick: true, ObservedTick: 1, TerminalReason: "disconnect"}, nil
	}
	snapshot, err := deadguardExec(quickExec, 5*time.Second)(context.Background(), probe.Scenario{ID: "quick", Name: "quick"})
	if err != nil {
		t.Fatalf("deadguard fired spuriously for quick execution: %v", err)
	}
	if snapshot.FrameCount != 2 || snapshot.TerminalReason != "disconnect" {
		t.Fatalf("unexpected snapshot through deadguard: %+v", snapshot)
	}
}

const (
	v2aHappyFixture    = "testdata/probe-fixtures/s2s_v2a_audio_in_basic.session.json"
	v2aSilentFixture   = "testdata/probe-fixtures/s2s_v2a_audio_in_basic_no_response.session.json"
	v2aHappyScenario   = "testdata/probe-scenarios/s2s-v2a-audio-in-basic.scenario.json"
	v2aNoRespScenario  = "testdata/probe-scenarios/s2s-v2a-audio-in-basic-no-response.scenario.json"
	v2aExpectedReplies = "How can I help you today?"
)

func TestProbeRunV2AAudioInBasicPassesOffline(t *testing.T) {
	run := executeCLI("probe", "run", v2aHappyScenario, "--replay", v2aHappyFixture, "--json")
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["pass"] != true || results[0]["name"] != "s2s-v2a-audio-in-basic" {
		t.Fatalf("unexpected result line: %v", results[0])
	}
	outcomes := results[0]["expectations"].([]any)
	if len(outcomes) != 1 {
		t.Fatalf("expectation outcome count = %d, want 1", len(outcomes))
	}
	outcome := outcomes[0].(map[string]any)
	if outcome["passed"] != true || outcome["kind"] != "transcript-contains" {
		t.Fatalf("transcript expectation outcome unexpected: %v", outcome)
	}
	if summary["status"] != "pass" || summary["passed"] != float64(1) || summary["failed"] != float64(0) {
		t.Fatalf("unexpected summary: %v", summary)
	}
}

func TestProbeRunS2SV1TextInAudioOutEmptyResponseFails(t *testing.T) {
	scenario := filepath.Join(s2sScenarioDir, "s2s_v1_text_in_audio_out_empty_response.scenario.json")
	fixture := filepath.Join(s2sFixtureDir, "s2s_v1_text_in_audio_out_empty_response.session.json")

	outPath := filepath.Join(t.TempDir(), "results.jsonl")
	summaryPath := filepath.Join(t.TempDir(), "summary.jsonl")
	run := executeCLI("probe", "run", scenario, "--replay", fixture,
		"--out", outPath, "--summary", summaryPath)
	if run.exitCode == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%q stderr=%q", run.stdout, run.stderr)
	}
	if !strings.Contains(run.stderr, "1 of 1 probe scenarios failed") {
		t.Fatalf("failure not reported: %q", run.stderr)
	}

	outBytes, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatalf("read JSONL results: %v", readErr)
	}
	var result map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(string(outBytes))), &result) != nil {
		t.Fatalf("malformed JSONL result: %q", outBytes)
	}
	if result["pass"] != false {
		t.Fatalf("empty-response scenario must fail: %v", result)
	}
	failedKinds := map[string]bool{}
	for _, expectation := range result["expectations"].([]any) {
		outcome := expectation.(map[string]any)
		if outcome["passed"] == false {
			failedKinds[outcome["kind"].(string)] = true
		}
	}
	if !failedKinds["frame-count"] {
		t.Fatalf("JSONL must identify the failed frame-count expectation: %v", result["expectations"])
	}

	summaryBytes, readErr := os.ReadFile(summaryPath)
	if readErr != nil {
		t.Fatalf("read summary artifact: %v", readErr)
	}
	var summary map[string]any
	if json.Unmarshal(summaryBytes, &summary) != nil || summary["status"] != "fail" || summary["failed"] != float64(1) {
		t.Fatalf("summary artifact must record the failure: %q", summaryBytes)
	}
}

func TestProbeRunV2AAudioInBasicFailsWithoutResponse(t *testing.T) {
	run := executeCLI("probe", "run", v2aNoRespScenario, "--replay", v2aSilentFixture, "--json")
	if run.exitCode == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%q stderr=%q", run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["pass"] != false || results[0]["name"] != "s2s-v2a-audio-in-basic-no-response" {
		t.Fatalf("unexpected result line: %v", results[0])
	}
	outcomes := results[0]["expectations"].([]any)
	if len(outcomes) != 1 {
		t.Fatalf("expectation outcome count = %d, want 1", len(outcomes))
	}
	outcome := outcomes[0].(map[string]any)
	if outcome["passed"] != false {
		t.Fatalf("unmet expectation did not fail: %v", outcome)
	}
	if !strings.Contains(fmt.Sprint(outcome["expected"]), v2aExpectedReplies) {
		t.Fatalf("failure does not name the unmet expectation: %v", outcome)
	}
	if summary["status"] != "fail" || summary["failed"] != float64(1) {
		t.Fatalf("summary does not reflect the failure: %v", summary)
	}
}

const (
	v2eFixture16k        = "testdata/probe-fixtures/s2s_v2e_audio_in_truncated_16k.session.json"
	v2eFixture24k        = "testdata/probe-fixtures/s2s_v2e_audio_in_truncated_24k.session.json"
	v2eFixtureUncommited = "testdata/probe-fixtures/s2s_v2e_audio_in_truncated_uncommitted.session.json"
	v2eScenario16k       = "testdata/probe-scenarios/s2s-v2e-audio-in-truncated-16k.scenario.json"
	v2eScenario24k       = "testdata/probe-scenarios/s2s-v2e-audio-in-truncated-24k.scenario.json"
	v2eScenarioNegative  = "testdata/probe-scenarios/s2s-v2e-audio-in-truncated-uncommitted-negative.scenario.json"
)

func outcomeByKind(t *testing.T, outcomes []any, kind string) map[string]any {
	t.Helper()
	for _, raw := range outcomes {
		outcome, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if outcome["kind"] == kind {
			return outcome
		}
	}
	t.Fatalf("no %q expectation outcome in %v", kind, outcomes)
	return nil
}

func TestProbeRunV2EAudioInTruncated16kCommitsPartialUtterance(t *testing.T) {
	run := executeCLI("probe", "run", v2eScenario16k, "--replay", v2eFixture16k, "--json")
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["pass"] != true || results[0]["name"] != "s2s-v2e-audio-in-truncated-16k" {
		t.Fatalf("unexpected result line: %v", results[0])
	}
	disposition := outcomeByKind(t, results[0]["expectations"].([]any), "buffer-disposition")
	if disposition["passed"] != true {
		t.Fatalf("buffer disposition expectation did not pass: %v (stdout=%q)", disposition, run.stdout)
	}
	if summary["status"] != "pass" || summary["passed"] != float64(1) {
		t.Fatalf("unexpected summary: %v", summary)
	}
}

func TestProbeRunV2EAudioInTruncated24kDiscardsPartialUtterance(t *testing.T) {
	run := executeCLI("probe", "run", v2eScenario24k, "--replay", v2eFixture24k, "--json")
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, _ := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["pass"] != true || results[0]["name"] != "s2s-v2e-audio-in-truncated-24k" {
		t.Fatalf("unexpected result line: %v", results[0])
	}
	disposition := outcomeByKind(t, results[0]["expectations"].([]any), "buffer-disposition")
	if disposition["passed"] != true {
		t.Fatalf("buffer disposition expectation did not pass: %v (stdout=%q)", disposition, run.stdout)
	}
}

func TestProbeRunV2ENegativeControlFailsOnUncommittedBuffer(t *testing.T) {
	run := executeCLI("probe", "run", v2eScenarioNegative, "--replay", v2eFixtureUncommited, "--json")
	if run.exitCode == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%q stderr=%q", run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["pass"] != false || results[0]["name"] != "s2s-v2e-audio-in-truncated-uncommitted-negative" {
		t.Fatalf("unexpected result line: %v", results[0])
	}
	disposition := outcomeByKind(t, results[0]["expectations"].([]any), "buffer-disposition")
	if disposition["passed"] != false {
		t.Fatalf("uncommitted buffer must fail the proof: %v", disposition)
	}
	actual := strings.Trim(fmt.Sprint(disposition["actual"]), "\"")
	if actual != "uncommitted" {
		t.Fatalf("diagnostic actual disposition = %q, want uncommitted; stdout=%q", actual, run.stdout)
	}
	if !strings.Contains(fmt.Sprint(disposition["expected"]), "committed") {
		t.Fatalf("failure does not name the expected disposition: %v", disposition)
	}
	if summary["status"] != "fail" || summary["failed"] != float64(1) {
		t.Fatalf("summary does not reflect the failure: %v", summary)
	}
}

func TestV2EScenariosReferenceCommittedTruncatedCorpus(t *testing.T) {
	for _, name := range []string{"truncated_16k.wav", "truncated_24k.wav"} {
		path := filepath.Join("..", "..", "..", "go-agent-loop", "testdata", "audio", name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("committed truncated corpus fixture %s must be reused by the v2e scenarios: %v", path, err)
		}
	}
}
