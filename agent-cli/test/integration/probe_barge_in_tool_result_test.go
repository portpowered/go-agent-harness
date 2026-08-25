package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
)

// v3bFixtureDir holds the recorded barge-in-during-tool-call session fixtures
// for the s2s v3b vertical. All evidence flows through the real CLI probe run
// command in replay mode; no internal loop functions are called directly.
var v3bFixtureDir = filepath.Join("testdata")

func TestV3BBargeInDuringToolCallDeliversToolResult(t *testing.T) {
	fixture := filepath.Join(v3bFixtureDir, "s2s-v3b-barge-in-tool-result-delivered.session.json")
	scenarioPath := writeV3BScenario(t, "v3b-delivered", fixture, true)

	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs([]string{"probe", "run", scenarioPath, "--replay", fixture, "--json"})
	if execErr := rootCmd.ExecuteContext(context.Background()); execErr != nil {
		t.Fatalf("delivered-result scenario must pass via CLI: %v\nstderr=%s", execErr, writer.StderrString())
	}
	result := decodeSingleV3BResult(t, writer.StdoutString())
	if result["pass"] != true {
		t.Fatalf("scenario must pass: %v", result)
	}
	assertExpectationKindsPass(t, result, "tool-result-delivered", "no-orphaned-tool-result", "terminal-reason")
}

func TestV3BBargeInDuringToolCallExplicitDiscard(t *testing.T) {
	fixture := filepath.Join(v3bFixtureDir, "s2s-v3b-barge-in-tool-result-discarded.session.json")
	scenarioPath := writeV3BScenario(t, "v3b-discarded", fixture, true)

	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs([]string{"probe", "run", scenarioPath, "--replay", fixture, "--json"})
	if execErr := rootCmd.ExecuteContext(context.Background()); execErr != nil {
		t.Fatalf("discard scenario must exit cleanly: %v\nstderr=%s", execErr, writer.StderrString())
	}
	result := decodeSingleV3BResult(t, writer.StdoutString())
	if result["pass"] != true {
		t.Fatalf("scenario must pass: %v", result)
	}
	assertExpectationKindsPass(t, result, "tool-result-discarded", "no-orphaned-tool-result", "terminal-reason")
}

func TestV3BNegativeControlOrphanedToolResultFails(t *testing.T) {
	fixture := filepath.Join(v3bFixtureDir, "s2s-v3b-barge-in-tool-result-orphaned.session.json")
	scenarioPath := writeV3BScenario(t, "v3b-orphaned", fixture, false)

	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs([]string{"probe", "run", scenarioPath, "--replay", fixture, "--json"})
	execErr := rootCmd.ExecuteContext(context.Background())
	if execErr == nil {
		t.Fatalf("orphaned tool result must fail the CLI run")
	}
	if !strings.Contains(execErr.Error(), "1 of 1 probe scenarios failed") {
		t.Fatalf("failure must be reported: %v", execErr)
	}
	result := decodeSingleV3BResult(t, writer.StdoutString())
	if result["pass"] != false {
		t.Fatalf("orphaned negative control must fail: %v", result)
	}
	outcomes := result["expectations"].([]any)
	failed := false
	for _, raw := range outcomes {
		outcome := raw.(map[string]any)
		if outcome["kind"] == "no-orphaned-tool-result" && outcome["passed"] == false {
			failed = true
			detail := fmt.Sprint(outcome["actual"])
			if !strings.Contains(detail, "call_v3b_weather") {
				t.Fatalf("failure must name the orphaned tool call: %v", outcome)
			}
		}
	}
	if !failed {
		t.Fatalf("no-orphaned-tool-result expectation must be the failing one: %v", outcomes)
	}
}

// writeV3BScenario writes an on-disk scenario JSON selecting the new
// measurable expectations, exercising the CLI scenario-file loading path.
func writeV3BScenario(t *testing.T, id, fixture string, expectNoOrphan bool) string {
	t.Helper()
	expectations := `[
		{"type": "tool_result_delivered", "tool_call_id": "call_v3b_weather"},
		{"type": "no_orphaned_tool_result"},
		{"type": "terminal_reason", "value": "synthetic"}
	]`
	if id == "v3b-discarded" {
		expectations = `[
			{"type": "tool_result_discarded", "tool_call_id": "call_v3b_weather"},
			{"type": "no_orphaned_tool_result"},
			{"type": "terminal_reason", "value": "synthetic_failure"}
		]`
	}
	if !expectNoOrphan {
		expectations = `[{"type": "no_orphaned_tool_result"}]`
	}
	document := `{
		"id": "` + id + `",
		"name": "` + id + `",
		"description": "v3b barge-in during tool call",
		"steps": [
			{"type": "send_text", "text": "what is the weather?"},
			{"type": "close"}
		],
		"expectations": ` + expectations + `
	}`
	path := filepath.Join(t.TempDir(), id+".scenario.json")
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return path
}

func decodeSingleV3BResult(t *testing.T, stdout string) map[string]any {
	t.Helper()
	line := strings.TrimSpace(stdout)
	if line == "" {
		t.Fatalf("no JSONL result on stdout")
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(line), &result); err != nil {
		t.Fatalf("decode result line %q: %v", line, err)
	}
	return result
}

func assertExpectationKindsPass(t *testing.T, result map[string]any, kinds ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, kind := range kinds {
		want[kind] = true
	}
	seen := map[string]bool{}
	for _, raw := range result["expectations"].([]any) {
		outcome := raw.(map[string]any)
		kind := outcome["kind"].(string)
		if want[kind] && outcome["passed"] != true {
			t.Fatalf("expectation %s must pass: %v", kind, outcome)
		}
		seen[kind] = true
	}
	for _, kind := range kinds {
		if !seen[kind] {
			t.Fatalf("expected outcome for kind %q missing from results: %v", kind, result)
		}
	}
}
