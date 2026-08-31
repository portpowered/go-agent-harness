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
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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

func TestV3BWrongFunctionCallOutputSubtypeFails(t *testing.T) {
	source := filepath.Join(v3bFixtureDir, "s2s-v3b-barge-in-tool-result-delivered.session.json")
	fixture := writeMutatedV3BFixture(t, source, "conversation.item.create", func(record map[string]any) {
		payload := record["payload"].(map[string]any)
		item := payload["item"].(map[string]any)
		item["type"] = "message"
	})
	scenarioPath := writeV3BScenario(t, "v3b-delivered-wrong-subtype", fixture, true)

	result, execErr := runV3BScenario(t, scenarioPath, fixture)
	if execErr == nil {
		t.Fatal("wrong item subtype must leave the tool result orphaned")
	}
	assertExpectationKindFails(t, result, "tool-result-delivered")
	assertExpectationKindFails(t, result, "no-orphaned-tool-result")
}

func TestV3BWrongDirectionDiscardFails(t *testing.T) {
	source := filepath.Join(v3bFixtureDir, "s2s-v3b-barge-in-tool-result-discarded.session.json")
	fixture := writeMutatedV3BFixture(t, source, "tool.result.discarded", func(record map[string]any) {
		record["direction"] = "server_to_client"
	})
	scenarioPath := writeV3BScenario(t, "v3b-discarded-wrong-direction", fixture, true)

	result, execErr := runV3BScenario(t, scenarioPath, fixture)
	if execErr == nil {
		t.Fatal("wrong-direction discard must leave the tool result orphaned")
	}
	assertExpectationKindFails(t, result, "tool-result-discarded")
	assertExpectationKindFails(t, result, "no-orphaned-tool-result")
}

func runV3BScenario(t *testing.T, scenarioPath, fixture string) (map[string]any, error) {
	t.Helper()
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
	return decodeSingleV3BResult(t, writer.StdoutString()), execErr
}

func writeMutatedV3BFixture(t *testing.T, source, recordType string, mutate func(map[string]any)) string {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source fixture: %v", err)
	}
	var capture map[string]any
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatalf("decode source fixture: %v", err)
	}
	records, ok := capture["records"].([]any)
	if !ok {
		t.Fatal("source fixture records are not an array")
	}
	found := false
	for _, raw := range records {
		record, ok := raw.(map[string]any)
		if !ok || record["type"] != recordType {
			continue
		}
		mutate(record)
		found = true
		break
	}
	if !found {
		t.Fatalf("source fixture has no %q record", recordType)
	}
	mutated, err := json.Marshal(capture)
	if err != nil {
		t.Fatalf("encode mutated fixture: %v", err)
	}
	var mutatedCapture gwtesting.SessionCapture
	if err := json.Unmarshal(mutated, &mutatedCapture); err != nil {
		t.Fatalf("decode mutated capture: %v", err)
	}
	sealed, err := gwtesting.SealSessionCapture(mutatedCapture)
	if err != nil {
		t.Fatalf("seal mutated capture: %v", err)
	}
	mutated, err = json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		t.Fatalf("encode sealed mutated fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "mutated.session.json")
	if err := os.WriteFile(path, mutated, 0o644); err != nil {
		t.Fatalf("write mutated fixture: %v", err)
	}
	return path
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
	if strings.HasPrefix(id, "v3b-discarded") {
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

func assertExpectationKindFails(t *testing.T, result map[string]any, want string) {
	t.Helper()
	for _, raw := range result["expectations"].([]any) {
		outcome := raw.(map[string]any)
		if outcome["kind"] == want {
			if outcome["passed"] != false {
				t.Fatalf("expectation %s must fail: %v", want, outcome)
			}
			return
		}
	}
	t.Fatalf("expected failed outcome for kind %q missing: %v", want, result)
}
