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

// The s2s v3c vertical is verified exclusively through the real CLI probe
// run command over the committed hermetic replay fixtures: three mid-response
// barge-ins within one session must cancel exactly once each and reconcile
// cumulative message counts with no loss or duplication. Negative controls —
// both committed violating fixtures and runtime-mutated copies of the pristine
// fixture — must fail naming the invariant they break.
//
// Fixture structure (testdata/s2s-v3c-barge-in-repeated*.session.json): per
// interrupted turn, append+commit opens a user turn whose response streams a
// partial delta before speech_started + response.cancel interrupt it; the
// interrupting utterance then appends+commits and its replacement response
// runs created -> delta -> done. A closing turn completes cleanly. That is 7
// committed user turns, 7 created responses (3 interrupted + 3 replacements +
// 1 closing), 4 delivered assistant turns, and zero post-cancel deltas.

var v3cFixtureDir = filepath.Join("testdata")

const v3cPositiveScenario = "s2s-v3c-barge-in-repeated"

func runV3CScenario(t *testing.T, selection string, replay string) (map[string]any, error) {
	t.Helper()
	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	writer := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(writer.Stdout())
	rootCmd.SetErr(writer.Stderr())
	rootCmd.SetArgs([]string{"probe", "run", "--replay", replay, "--json", selection})
	execErr := rootCmd.ExecuteContext(context.Background())
	return decodeSingleV3CResult(t, writer.StdoutString()), execErr
}

func decodeSingleV3CResult(t *testing.T, stdout string) map[string]any {
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

func v3cOutcomeKindsPass(t *testing.T, result map[string]any, kinds ...string) {
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

func v3cOutcomeKindFailsWithDetail(t *testing.T, result map[string]any, kind, detail string) {
	t.Helper()
	for _, raw := range result["expectations"].([]any) {
		outcome := raw.(map[string]any)
		if outcome["kind"] != kind || outcome["passed"] != false {
			continue
		}
		text := fmt.Sprint(outcome["actual"]) + " " + fmt.Sprint(outcome["error"])
		if !strings.Contains(text, detail) {
			t.Fatalf("failure of %s must name %q: %v", kind, detail, outcome)
		}
		return
	}
	t.Fatalf("expected failed outcome for kind %q missing: %v", kind, result)
}

func TestS2SV3CBargeInRepeatedReconcilesViaCLIReplay(t *testing.T) {
	result, execErr := runV3CScenario(t, v3cPositiveScenario,
		filepath.Join(v3cFixtureDir, "s2s-v3c-barge-in-repeated.session.json"))
	if execErr != nil {
		t.Fatalf("positive scenario must pass via CLI: %v", execErr)
	}
	if result["pass"] != true {
		t.Fatalf("positive scenario must pass: %v", result)
	}
	v3cOutcomeKindsPass(t, result,
		"barge-in-cancel-once", "message-counts-reconcile", "terminal-reason")
}

func TestS2SV3CBargeInRepeatedPositiveSelectsFixtureFromDirectory(t *testing.T) {
	// Selecting the positive case against the whole replay directory proves
	// fixture resolution matches scenario ID to fixture stem.
	result, execErr := runV3CScenario(t, v3cPositiveScenario, v3cFixtureDir)
	if execErr != nil {
		t.Fatalf("positive scenario must pass when fixtures resolve by stem: %v", execErr)
	}
	v3cOutcomeKindsPass(t, result, "barge-in-cancel-once", "message-counts-reconcile")
}

func TestS2SV3CNegativeControlsFailThroughCLI(t *testing.T) {
	cases := []struct {
		name        string
		scenario    string
		fixture     string
		failingKind string
		detail      string
	}{
		{
			name:        "double cancel fails cancel-exactly-once",
			scenario:    "s2s-v3c-barge-in-repeated-double-cancel",
			fixture:     "s2s-v3c-barge-in-repeated-double-cancel.session.json",
			failingKind: "barge-in-cancel-once",
			detail:      "stray or duplicate cancels",
		},
		{
			name:        "dropped commit fails composition reconciliation",
			scenario:    "s2s-v3c-barge-in-repeated-dropped-commit",
			fixture:     "s2s-v3c-barge-in-repeated-dropped-commit.session.json",
			failingKind: "message-counts-reconcile",
			detail:      "user_turns: expected 7, actual 6",
		},
		{
			name:        "duplicated delivered turn fails composition reconciliation",
			scenario:    "s2s-v3c-barge-in-repeated-duplicated-turn",
			fixture:     "s2s-v3c-barge-in-repeated-duplicated-turn.session.json",
			failingKind: "message-counts-reconcile",
			detail:      "assistant_delivered: expected 4, actual 5",
		},
	}
	for _, testCase := range cases {
		result, execErr := runV3CScenario(t, testCase.scenario, filepath.Join(v3cFixtureDir, testCase.fixture))
		if execErr == nil {
			t.Fatalf("%s: negative control must fail the CLI run", testCase.name)
		}
		if !strings.Contains(execErr.Error(), "1 of 1 probe scenarios failed") {
			t.Fatalf("%s: failure must be reported: %v", testCase.name, execErr)
		}
		if result["pass"] != false {
			t.Fatalf("%s: negative control must fail: %v", testCase.name, result)
		}
		v3cOutcomeKindFailsWithDetail(t, result, testCase.failingKind, testCase.detail)
	}
}

// writeMutatedV3CFixture clones a committed fixture, applies a record-level
// mutation, renumbers sequences/timestamps, and writes it to a temp dir.
func writeMutatedV3CFixture(t *testing.T, source string, mutate func(records []map[string]any) []map[string]any) string {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source fixture: %v", err)
	}
	var capture map[string]any
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatalf("decode source fixture: %v", err)
	}
	rawRecords, ok := capture["records"].([]any)
	if !ok {
		t.Fatal("source fixture records are not an array")
	}
	records := make([]map[string]any, 0, len(rawRecords))
	for _, raw := range rawRecords {
		record, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("fixture record is not an object")
		}
		records = append(records, record)
	}
	records = mutate(records)
	for index, record := range records {
		record["sequence"] = index + 1
		record["timestamp_ms"] = index + 1
	}
	capture["records"] = records
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

func v3cRecordIndexes(records []map[string]any, eventType string) []int {
	indexes := []int{}
	for index, record := range records {
		if record["type"] == eventType {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

// Mutating the pristine fixture to duplicate one delivered assistant turn —
// its response.created, transcript delta, and response.done — must fail the
// composition reconciliation through the same CLI path.
func TestS2SV3CMutatingFixtureToDuplicateDeliveredMessageFails(t *testing.T) {
	source := filepath.Join(v3cFixtureDir, "s2s-v3c-barge-in-repeated.session.json")
	fixture := writeMutatedV3CFixture(t, source, func(records []map[string]any) []map[string]any {
		done := v3cRecordIndexes(records, "response.done")
		last := done[len(done)-1]
		block := []map[string]any{
			deepCopyV3CRecord(records[last-2]), // response.created
			deepCopyV3CRecord(records[last-1]), // transcript delta
			deepCopyV3CRecord(records[last]),   // response.done
		}
		duplicated := append(append([]map[string]any{}, records[:last+1]...), block...)
		return append(duplicated, records[last+1:]...)
	})
	result, execErr := runV3CScenario(t, v3cPositiveScenario, fixture)
	if execErr == nil {
		t.Fatal("duplicating a delivered assistant turn must fail the CLI run")
	}
	v3cOutcomeKindFailsWithDetail(t, result, "message-counts-reconcile",
		"assistant_delivered: expected 4, actual 5")
}

func deepCopyV3CRecord(record map[string]any) map[string]any {
	clone, err := json.Marshal(record)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(clone, &out) != nil {
		return nil
	}
	return out
}

// Dropping one committed user message from the pristine fixture must fail the
// composition reconciliation: the committed turn is lost.
func TestS2SV3CMutatingFixtureToDropCommittedMessageFails(t *testing.T) {
	source := filepath.Join(v3cFixtureDir, "s2s-v3c-barge-in-repeated.session.json")
	fixture := writeMutatedV3CFixture(t, source, func(records []map[string]any) []map[string]any {
		commits := v3cRecordIndexes(records, "input_audio_buffer.commit")
		drop := commits[len(commits)-1]
		return append(append([]map[string]any{}, records[:drop]...), records[drop+1:]...)
	})
	result, execErr := runV3CScenario(t, v3cPositiveScenario, fixture)
	if execErr == nil {
		t.Fatal("dropping a committed user message must fail the CLI run")
	}
	v3cOutcomeKindFailsWithDetail(t, result, "message-counts-reconcile",
		"user_turns: expected 7, actual 6")
}

// Duplicating one response.cancel on the pristine fixture double-cancels an
// interrupted response and must fail cancel-exactly-once.
func TestS2SV3CMutatingFixtureToDoubleCancelResponseFails(t *testing.T) {
	source := filepath.Join(v3cFixtureDir, "s2s-v3c-barge-in-repeated.session.json")
	fixture := writeMutatedV3CFixture(t, source, func(records []map[string]any) []map[string]any {
		cancels := v3cRecordIndexes(records, "response.cancel")
		target := cancels[0]
		doubled := append(append([]map[string]any{}, records[:target+1]...), deepCopyV3CRecord(records[target]))
		return append(doubled, records[target+1:]...)
	})
	result, execErr := runV3CScenario(t, v3cPositiveScenario, fixture)
	if execErr == nil {
		t.Fatal("double-cancelling an interrupted response must fail the CLI run")
	}
	v3cOutcomeKindFailsWithDetail(t, result, "barge-in-cancel-once",
		"stray or duplicate cancels")
}
