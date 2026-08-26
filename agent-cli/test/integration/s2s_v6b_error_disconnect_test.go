package integration

// s2s-v6b-error-disconnect proves that an offline provider transport close is
// terminal, attributable to the provider, and marked partial after output has
// started. The healthy control proves that an explicit response completion is
// a distinct complete terminal outcome. All assertions drive the production
// CLI with real argv and use only committed replay fixtures.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/probe"
)

const (
	v6bDeadline = 60 * time.Second
	v6bInput    = "tell me about transport failures"
	v6bPartial  = "v6b midstream answer cut off when the transport dropped"
	v6bCapture  = "s2s-v6b-error-disconnect-mid-session.session.json"
	v6bHealthy  = "s2s-v6b-error-disconnect-healthy-control.session.json"
)

type v6bCLIResult struct {
	description string
	args        []string
	stdout      string
	stderr      string
	err         error
	elapsed     time.Duration
}

func (r v6bCLIResult) diagnostics() string {
	return fmt.Sprintf(
		"%s\nargv: %q\nexit/error: %v\nelapsed vs deadline: %s / %s\nstdout:\n%s\nstderr:\n%s",
		r.description, r.args, r.err, r.elapsed.Round(time.Millisecond), v6bDeadline,
		r.stdout, r.stderr,
	)
}

func runV6BRootCommand(t *testing.T, description string, args ...string) v6bCLIResult {
	t.Helper()

	agentCLI, err := wire.InitializeMockAgentCLI(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer must not run during v6b replay")},
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	writer := NewTestWriter()
	root := agentCLI.Generate()
	root.SetOut(writer.Stdout())
	root.SetErr(writer.Stderr())
	root.SetArgs(args)

	ctx, cancel := context.WithTimeout(context.Background(), v6bDeadline)
	defer cancel()
	start := time.Now()
	execErr := root.ExecuteContext(ctx)
	return v6bCLIResult{
		description: description,
		args:        args,
		stdout:      writer.StdoutString(),
		stderr:      writer.StderrString(),
		err:         execErr,
		elapsed:     time.Since(start),
	}
}

func decodeV6BJSONLLines(t *testing.T, data, source string) []map[string]any {
	t.Helper()
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		t.Fatalf("%s is empty", source)
	}
	lines := strings.Split(trimmed, "\n")
	objects := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var object map[string]any
		if err := json.Unmarshal([]byte(line), &object); err != nil {
			t.Fatalf("decode %s line %q: %v", source, line, err)
		}
		objects = append(objects, object)
	}
	return objects
}

func readV6BSummary(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read summary artifact %q: %v", path, err)
	}
	var summary map[string]any
	if err := json.Unmarshal(data, &summary); err != nil {
		t.Fatalf("decode summary artifact %q: %v", path, err)
	}
	return summary
}

func assertV6BResult(t *testing.T, result map[string]any, id, reason, provenance, outputState string) {
	t.Helper()
	if result["name"] != id {
		t.Fatalf("result name = %v, want %s", result["name"], id)
	}
	if result["pass"] != true {
		t.Fatalf("result %s did not pass: %v", id, result)
	}
	for field, want := range map[string]string{
		"terminal_reason":     reason,
		"terminal_provenance": provenance,
		"output_state":        outputState,
	} {
		if result[field] != want {
			t.Fatalf("result %s %s = %v, want %s", id, field, result[field], want)
		}
	}
	outcomes, ok := result["expectations"].([]any)
	if !ok || len(outcomes) != 4 {
		t.Fatalf("result %s expectations = %v, want four passing outcomes", id, result["expectations"])
	}
	for _, raw := range outcomes {
		outcome, ok := raw.(map[string]any)
		if !ok || outcome["passed"] != true {
			t.Fatalf("result %s has failed expectation outcome: %v", id, raw)
		}
	}
}

func TestProbeRunV6BDisconnectSuiteOfflineExitZero(t *testing.T) {
	capture := locateSharedSessionFixture(t, v6bCapture)
	locateSharedSessionFixture(t, v6bHealthy)
	outPath := filepath.Join(t.TempDir(), "v6b-results.jsonl")
	summaryPath := filepath.Join(t.TempDir(), "v6b-summary.json")

	run := runV6BRootCommand(t, "v6b disconnect suite", "probe", "run",
		"--replay", filepath.Dir(capture),
		"--json", "--out", outPath, "--summary", summaryPath,
		"--scenario", probe.ScenarioIDS2SV6BErrorDisconnect)
	if run.err != nil || run.elapsed >= v6bDeadline {
		t.Fatalf("v6b suite must exit promptly and successfully; diagnostics:\n%s", run.diagnostics())
	}

	results := decodeV6BJSONLLines(t, mustReadFile(t, outPath), "v6b results")
	if len(results) != 2 {
		t.Fatalf("v6b result line count = %d, want 2: %v", len(results), results)
	}
	byName := make(map[string]map[string]any, len(results))
	for _, result := range results {
		byName[result["name"].(string)] = result
	}
	assertV6BResult(t, byName[probe.ScenarioIDS2SV6BDisconnectMidSession], probe.ScenarioIDS2SV6BDisconnectMidSession, "disconnect", "provider", "partial")
	assertV6BResult(t, byName[probe.ScenarioIDS2SV6BHealthyControl], probe.ScenarioIDS2SV6BHealthyControl, "complete", "provider", "complete")

	summary := readV6BSummary(t, summaryPath)
	for field, want := range map[string]any{
		"total": float64(2), "passed": float64(2), "failed": float64(0), "status": "pass",
	} {
		if summary[field] != want {
			t.Fatalf("summary %s = %v, want %v", field, summary[field], want)
		}
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result artifact %q: %v", path, err)
	}
	return string(data)
}

func writeV6BCorruptedScenario(t *testing.T, kind, value string) string {
	t.Helper()
	document := fmt.Sprintf(`{
		"id": "s2s-v6b-corrupt-%s",
		"steps": [
			{"type": "send_text", "text": %q},
			{"type": "close"}
		],
		"expectations": [
			{"type": "terminal_reason", "value": "disconnect"},
			{"type": "terminal_provenance", "value": "provider"},
			{"type": "output_state", "value": "partial"},
			{"type": "transcript_contains", "text": %q}
		]
	}`, kind, v6bInput, v6bPartial)
	path := filepath.Join(t.TempDir(), "corrupted-"+kind+".scenario.json")
	// Replace only the selected terminal expectation so every run exercises
	// one independent member of the terminal triple.
	var raw map[string]any
	if err := json.Unmarshal([]byte(document), &raw); err != nil {
		t.Fatalf("decode corrupted scenario template: %v", err)
	}
	expectations := raw["expectations"].([]any)
	index := map[string]int{"terminal_reason": 0, "terminal_provenance": 1, "output_state": 2}[kind]
	expectations[index].(map[string]any)["value"] = value
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("encode corrupted scenario: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write corrupted scenario: %v", err)
	}
	return path
}

func TestProbeRunV6BTerminalTripleRejectsEveryCorruptedExpectation(t *testing.T) {
	capture := locateSharedSessionFixture(t, v6bCapture)
	for _, test := range []struct {
		kind   string
		wrong  string
		actual string
	}{
		{kind: "terminal_reason", wrong: "complete", actual: "disconnect"},
		{kind: "terminal_provenance", wrong: "session", actual: "provider"},
		{kind: "output_state", wrong: "none", actual: "partial"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			scenario := writeV6BCorruptedScenario(t, test.kind, test.wrong)
			run := runV6BRootCommand(t, "corrupted "+test.kind, "probe", "run",
				"--replay", capture, "--json", "--scenario", scenario)
			if run.err == nil || run.elapsed >= v6bDeadline {
				t.Fatalf("corrupted %s must fail promptly; diagnostics:\n%s", test.kind, run.diagnostics())
			}
			results := decodeV6BJSONLLines(t, run.stdout, "corrupted result")
			if len(results) != 1 {
				t.Fatalf("corrupted result count = %d, want 1", len(results))
			}
			result := results[0]
			if result["pass"] != false || result["terminal_reason"] != "disconnect" ||
				result["terminal_provenance"] != "provider" || result["output_state"] != "partial" {
				t.Fatalf("corrupted %s did not expose observed triple: %v", test.kind, result)
			}
			var failed map[string]any
			for _, raw := range result["expectations"].([]any) {
				outcome := raw.(map[string]any)
				if outcome["kind"] == strings.ReplaceAll(test.kind, "_", "-") {
					failed = outcome
				}
			}
			if failed == nil || failed["passed"] != false || failed["expected"] != fmt.Sprintf("%q", test.wrong) || failed["actual"] != fmt.Sprintf("%q", test.actual) {
				t.Fatalf("corrupted %s outcome = %v, want expected=%q actual=%q", test.kind, failed, test.wrong, test.actual)
			}
		})
	}
}

func TestSessionCommandV6BDisconnectReportsProviderPartialTerminalRecord(t *testing.T) {
	capture := locateSharedSessionFixture(t, v6bCapture)
	run := runV6BRootCommand(t, "v6b session disconnect", "session",
		"--replay", capture, "--provider", "grok", "--model", "grok-4-v6b-midstream-disconnect",
		"tell", "me", "about", "transport", "failures")
	if run.err != nil || run.elapsed >= v6bDeadline {
		t.Fatalf("session disconnect must terminate promptly; diagnostics:\n%s", run.diagnostics())
	}
	if !strings.Contains(run.stdout, v6bPartial) {
		t.Fatalf("partial provider output missing; diagnostics:\n%s", run.diagnostics())
	}
	const terminal = "[session terminal: classification=transport terminal_reason=provider_close terminal_provenance=provider output_state=partial]"
	if strings.Count(run.stdout, "[session terminal:") != 1 || !strings.Contains(run.stdout, terminal) {
		t.Fatalf("provider partial terminal record missing or duplicated; diagnostics:\n%s", run.diagnostics())
	}
	if !strings.Contains(run.stdout, "[session closed: provider_closed]") {
		t.Fatalf("legacy provider close marker missing; diagnostics:\n%s", run.diagnostics())
	}
}
