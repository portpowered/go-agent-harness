package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
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

const (
	v3aFixture16k            = "testdata/probe-fixtures/s2s-v3a-barge-in-basic-cancelled-16k.session.json"
	v3aFixture24k            = "testdata/probe-fixtures/s2s-v3a-barge-in-basic-cancelled-24k.session.json"
	v3aFixtureNoInterruption = "testdata/probe-fixtures/s2s-v3a-barge-in-basic-no-interruption.session.json"

	// v3aCancelTick16k/24k are the outbound logical ticks on which
	// RESPONSE.CANCEL crosses the client-to-provider path after replay injects
	// the committed overlap corpus frames.
	v3aCancelTick16k = 3
	v3aCancelTick24k = 4
)

func TestProbeRunV3ABargeInCancelled16kCancelsInFlightResponse(t *testing.T) {
	run := executeCLI("probe", "run", "s2s-v3a-barge-in-basic-cancelled-16k", "--replay", v3aFixture16k, "--json")
	assertV3ACancelledRun(t, run, "s2s-v3a-barge-in-basic-cancelled-16k", v3aCancelTick16k)
}

func TestProbeRunV3ABargeInCancelled24kCancelsWithinBound(t *testing.T) {
	run := executeCLI("probe", "run", "s2s-v3a-barge-in-basic-cancelled-24k", "--replay", v3aFixture24k, "--json")
	assertV3ACancelledRun(t, run, "s2s-v3a-barge-in-basic-cancelled-24k", v3aCancelTick24k)
}

func TestProbeRunV3ANoInterruptionControlCompletesWithoutCancel(t *testing.T) {
	run := executeCLI("probe", "run", "s2s-v3a-barge-in-basic-no-interruption", "--replay", v3aFixtureNoInterruption, "--json")
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["pass"] != true || results[0]["name"] != "s2s-v3a-barge-in-basic-no-interruption" {
		t.Fatalf("unexpected result line: %v", results[0])
	}
	cancelOutcomes := v3aOutcomesByKind(t, results[0], "response-cancel")
	if len(cancelOutcomes) != 1 {
		t.Fatalf("no-interruption control declares %d response-cancel outcomes, want 1", len(cancelOutcomes))
	}
	if cancelOutcomes[0]["passed"] != true {
		t.Fatalf("asserted absence must pass on an uninterrupted session: %v", cancelOutcomes[0])
	}
	if summary["status"] != "pass" || summary["failed"] != float64(0) {
		t.Fatalf("unexpected summary: %v", summary)
	}
}

// TestProbeRunV3ANegativeControlFailsWhenCancelSuppressed is the negative
// control verified once: the interrupting fixture is delivered but the cancel
// path is broken (the RESPONSE.CANCEL record is removed), and the run must
// fail with a clear expectation-mismatch reason naming the missing cancel.
func TestProbeRunV3ANegativeControlFailsWhenCancelSuppressed(t *testing.T) {
	source, err := os.ReadFile(v3aFixture16k)
	if err != nil {
		t.Fatalf("read committed v3a fixture: %v", err)
	}
	var capture map[string]any
	if err := json.Unmarshal(source, &capture); err != nil {
		t.Fatalf("decode committed v3a fixture: %v", err)
	}
	records := capture["records"].([]any)
	broken := make([]any, 0, len(records))
	for _, record := range records {
		if record.(map[string]any)["type"] == "response.cancel" {
			continue
		}
		broken = append(broken, record)
	}
	capture["records"] = broken
	fixture := filepath.Join(t.TempDir(), "s2s-v3a-barge-in-basic-cancelled-16k.session.json")
	encoded, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("encode broken fixture: %v", err)
	}
	if err := os.WriteFile(fixture, encoded, 0o644); err != nil {
		t.Fatalf("write broken fixture: %v", err)
	}

	run := executeCLI("probe", "run", "s2s-v3a-barge-in-basic-cancelled-16k", "--replay", fixture, "--json")
	if run.exitCode == 0 {
		t.Fatalf("suppressed cancel must fail the run; stdout=%q stderr=%q", run.stdout, run.stderr)
	}
	results, _ := decodeProbeLines(t, 1, run.stdout, run.stderr)
	for _, outcome := range v3aOutcomesByKind(t, results[0], "response-cancel") {
		message := fmt.Sprint(outcome["error"])
		if !strings.Contains(message, `probe expectation "response-cancel" mismatch`) ||
			!strings.Contains(message, "RESPONSE.CANCEL observed") {
			t.Fatalf("failure must name the missing cancel clearly: %v", outcome)
		}
	}
}

// TestV3AScenariosReferenceCommittedOverlapCorpus proves both sample-rate
// variants of the interrupting barge-in input load from the committed corpus
// at go-agent-loop/testdata/audio and carry real speech energy at their
// recorded rates — the same overlap_* utterances the scenarios reference.
func TestV3AScenariosReferenceCommittedOverlapCorpus(t *testing.T) {
	for _, variant := range []struct {
		name         string
		wantRate     int
		wantSilentAt float64
	}{
		{name: "overlap_16k.wav", wantRate: 16000},
		{name: "overlap_24k.wav", wantRate: 24000},
	} {
		path := filepath.Join("..", "..", "..", "go-agent-loop", "testdata", "audio", variant.name)
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("committed overlap corpus %s must load: %v", path, err)
		}
		rate, samples, err := wavio.Read(file)
		closeErr := file.Close()
		if err != nil {
			t.Fatalf("decode overlap corpus %s: %v", path, err)
		}
		if closeErr != nil {
			t.Fatalf("close overlap corpus %s: %v", path, closeErr)
		}
		if rate != variant.wantRate {
			t.Fatalf("overlap corpus %s sample rate = %d, want %d", variant.name, rate, variant.wantRate)
		}
		if len(samples) == 0 {
			t.Fatalf("overlap corpus %s decodes to no PCM16 samples", variant.name)
		}
		var sum float64
		for _, sample := range samples {
			sum += float64(sample) * float64(sample)
		}
		rms := math.Sqrt(sum / float64(len(samples)))
		if rms <= probe.AudioEnergyThreshold {
			t.Fatalf("overlap corpus %s RMS = %.2f must exceed the VAD threshold %.2f to plausibly barge in",
				variant.name, rms, probe.AudioEnergyThreshold)
		}
	}
}

// TestV3AReplayInjectsCompleteOverlapCorpusPCM proves that the positive replay
// path replaces sanitized append placeholders with every frame from the
// committed overlap WAV. The cancel remains in its recorded position while
// the rest of the user's utterance continues through the same replay wire.
func TestV3AReplayInjectsCompleteOverlapCorpusPCM(t *testing.T) {
	tests := []struct {
		name       string
		fixture    string
		corpusID   string
		wantRate   int
		wantCancel probe.LogicalTime
	}{
		{name: "16k", fixture: v3aFixture16k, corpusID: "overlap_16k", wantRate: wavio.Rate16kHz, wantCancel: v3aCancelTick16k},
		{name: "24k", fixture: v3aFixture24k, corpusID: "overlap_24k", wantRate: wavio.Rate24kHz, wantCancel: v3aCancelTick24k},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capture, err := gatewaytesting.LoadSessionCapture(test.fixture)
			if err != nil {
				t.Fatalf("load source capture: %v", err)
			}
			injected, err := injectReplayCorpusAudio(capture, test.corpusID)
			if err != nil {
				t.Fatalf("inject corpus: %v", err)
			}

			corpusPath, err := replayCorpusPath(test.corpusID)
			if err != nil {
				t.Fatalf("resolve corpus: %v", err)
			}
			wavBytes, err := os.ReadFile(corpusPath)
			if err != nil {
				t.Fatalf("read corpus: %v", err)
			}
			rate, samples, err := wavio.Read(bytes.NewReader(wavBytes))
			if err != nil {
				t.Fatalf("decode corpus: %v", err)
			}
			if rate != test.wantRate {
				t.Fatalf("corpus rate = %d, want %d", rate, test.wantRate)
			}

			want := make([]byte, 0, ((len(samples)+audio.FrameSize-1)/audio.FrameSize)*audio.FrameSize*2)
			got := make([]byte, 0, cap(want))
			appendCount := 0
			for start := 0; start < len(samples); start += audio.FrameSize {
				frame := make([]int16, audio.FrameSize)
				copy(frame, samples[start:])
				encoded := make([]byte, len(frame)*2)
				for index, sample := range frame {
					binary.LittleEndian.PutUint16(encoded[index*2:], uint16(sample))
				}
				want = append(want, encoded...)
			}
			for _, record := range injected.Records {
				if record.Direction != gatewaytesting.DirectionClientToServer || record.Type != "input_audio_buffer.append" {
					continue
				}
				appendCount++
				var event struct {
					Audio string `json:"audio"`
				}
				if err := json.Unmarshal(record.Payload, &event); err != nil {
					t.Fatalf("decode injected append: %v", err)
				}
				pcm, err := base64.StdEncoding.DecodeString(event.Audio)
				if err != nil {
					t.Fatalf("decode injected PCM: %v", err)
				}
				got = append(got, pcm...)
			}
			wantFrames := (len(samples) + audio.FrameSize - 1) / audio.FrameSize
			if appendCount != wantFrames {
				t.Fatalf("injected append count = %d, want %d", appendCount, wantFrames)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("injected append PCM differs from the complete corpus: got %d bytes, want %d", len(got), len(want))
			}

			var observation probe.ObservationSnapshot
			if err := deriveResponseCancelObservationFromCapture(injected, &observation); err != nil {
				t.Fatalf("derive cancel observation: %v", err)
			}
			if !observation.HasInterruptTick || observation.InterruptTick != 2 {
				t.Fatalf("interrupt tick = %d (present=%t), want actual first append tick 2", observation.InterruptTick, observation.HasInterruptTick)
			}
			if !observation.HasResponseCancel || observation.ResponseCancelTick != test.wantCancel {
				t.Fatalf("cancel tick = %d (present=%t), want %d", observation.ResponseCancelTick, observation.HasResponseCancel, test.wantCancel)
			}
		})
	}
}

func TestReplayCorpusLookupRejectsUnknownIDs(t *testing.T) {
	lookup := replayCorpusLookup{}
	for _, id := range []string{"", "made-up-corpus", "overlap_16k.wav"} {
		if lookup.Has(id) {
			t.Fatalf("unknown corpus ID %q was accepted", id)
		}
	}
	for _, id := range []string{"overlap_16k", "overlap_24k", "truncated_16k", "truncated_24k", "utterance-hello-there", "v3c-utterance-1"} {
		if !lookup.Has(id) {
			t.Fatalf("known corpus ID %q was rejected", id)
		}
	}
}

func assertV3ACancelledRun(t *testing.T, run cliExecution, name string, wantCancelTicks int) {
	t.Helper()
	if run.exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stdout=%q stderr=%q", run.exitCode, run.stdout, run.stderr)
	}
	results, summary := decodeProbeLines(t, 1, run.stdout, run.stderr)
	if results[0]["pass"] != true || results[0]["name"] != name {
		t.Fatalf("unexpected result line: %v", results[0])
	}
	cancelOutcomes := v3aOutcomesByKind(t, results[0], "response-cancel")
	if len(cancelOutcomes) != 1 {
		t.Fatalf("%s declares %d response-cancel outcomes, want one presence assertion", name, len(cancelOutcomes))
	}
	if cancelOutcomes[0]["passed"] != true {
		t.Fatalf("%s cancel expectation failed despite an in-flight cancellation: %v", name, cancelOutcomes[0])
	}
	latencyOutcomes := v3aOutcomesByKind(t, results[0], "latency-within-ticks")
	if len(latencyOutcomes) != 1 || latencyOutcomes[0]["passed"] != true {
		t.Fatalf("%s latency expectation failed despite an in-flight cancellation: %v", name, latencyOutcomes)
	}
	if got := results[0]["ticks"]; got.(float64) < float64(wantCancelTicks) {
		t.Fatalf("%s final tick count = %v, want at least cancel tick %d", name, got, wantCancelTicks)
	}
	if summary["status"] != "pass" || summary["passed"] != float64(1) {
		t.Fatalf("unexpected summary: %v", summary)
	}
}

// v3aOutcomesByKind returns every expectation outcome of one kind in result order.
func v3aOutcomesByKind(t *testing.T, result map[string]any, kind string) []map[string]any {
	t.Helper()
	raw, ok := result["expectations"].([]any)
	if !ok {
		t.Fatalf("result carries no expectation outcomes: %v", result)
	}
	outcomes := make([]map[string]any, 0, len(raw))
	for _, candidate := range raw {
		outcome := candidate.(map[string]any)
		if outcome["kind"] == kind {
			outcomes = append(outcomes, outcome)
		}
	}
	return outcomes
}
