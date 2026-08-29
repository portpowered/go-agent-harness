package probe

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilesystemOracleCapturesEachIntermediateFamilyAState(t *testing.T) {
	root := t.TempDir()
	oracle, err := NewFilesystemOracle(root)
	if err != nil {
		t.Fatalf("NewFilesystemOracle: %v", err)
	}
	scenario := NewFamilyAScenario()

	if err := os.Mkdir(filepath.Join(root, "project"), 0o700); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	first, err := oracle.Checkpoint("checkpoint-create", "create-project-directory", time.Second, scenario.Actions[0].Oracle.Checkpoints)
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	if got := first.Entries[0].Type; got != FileTypeDirectory {
		t.Fatalf("create checkpoint type = %q, want directory", got)
	}

	readme := filepath.Join(root, "project", "README.md")
	if err := os.WriteFile(readme, []byte(FamilyAInitialREADME), 0o600); err != nil {
		t.Fatalf("write initial README: %v", err)
	}
	second, err := oracle.Checkpoint("checkpoint-add", "add-readme-content", 2*time.Second, scenario.Actions[1].Oracle.Checkpoints)
	if err != nil {
		t.Fatalf("add checkpoint: %v", err)
	}

	if err := os.WriteFile(readme, []byte(FamilyAFinalREADME), 0o600); err != nil {
		t.Fatalf("write final README: %v", err)
	}
	third, err := oracle.Checkpoint("checkpoint-revise", "revise-readme", 3*time.Second, scenario.Actions[2].Oracle.Checkpoints)
	if err != nil {
		t.Fatalf("revise checkpoint: %v", err)
	}
	fourth, err := oracle.Checkpoint("checkpoint-summary", "summarize-final-state", 4*time.Second, scenario.Actions[3].Oracle.Checkpoints)
	if err != nil {
		t.Fatalf("summary checkpoint: %v", err)
	}

	if err := VerifyFilesystemExpectations(scenario.Actions[1].Oracle.Checkpoints, second); err != nil {
		t.Fatalf("initial checkpoint changed after later write: %v", err)
	}
	if err := VerifyFilesystemExpectations(scenario.Actions[2].Oracle.Checkpoints, third); err != nil {
		t.Fatalf("final checkpoint mismatch: %v", err)
	}
	if err := VerifyFilesystemExpectations(scenario.Actions[3].Oracle.Checkpoints, fourth); err != nil {
		t.Fatalf("summary checkpoint mismatch: %v", err)
	}

	gotDirectoryHash, err := FilesystemDirectorySHA256(filepath.Join(root, "project"))
	if err != nil {
		t.Fatalf("FilesystemDirectorySHA256: %v", err)
	}
	if gotDirectoryHash == first.Entries[0].SHA256 {
		t.Fatal("directory fingerprint did not change after adding a file")
	}

	missing, err := oracle.CaptureCheckpoint("checkpoint-absence", "create-project-directory", 5*time.Second, []FilesystemExpectation{{Path: "project/missing.txt", Type: FileTypeAbsent}})
	if err != nil {
		t.Fatalf("absence checkpoint: %v", err)
	}
	if len(missing.Entries) != 1 || missing.Entries[0].Type != FileTypeAbsent || missing.Entries[0].SHA256 != "" || missing.Entries[0].Size != 0 {
		t.Fatalf("absence checkpoint = %+v, want explicit zero-sized absent fact", missing.Entries[0])
	}
}

func TestFilesystemOracleRejectsWrongIntermediateStateEvenWhenFinalStateIsCorrect(t *testing.T) {
	root := t.TempDir()
	oracle, err := NewFilesystemOracle(root)
	if err != nil {
		t.Fatalf("NewFilesystemOracle: %v", err)
	}
	scenario := NewFamilyAScenario()
	if err := os.MkdirAll(filepath.Join(root, "project"), 0o700); err != nil {
		t.Fatalf("create project: %v", err)
	}
	readme := filepath.Join(root, "project", "README.md")
	if err := os.WriteFile(readme, []byte(FamilyAFinalREADME), 0o600); err != nil {
		t.Fatalf("write final README: %v", err)
	}

	// This is the exact failure the intermediate oracle is meant to expose:
	// the final content exists, but the add-content checkpoint was taken after
	// the edit and therefore cannot be accepted as evidence for the earlier
	// action.
	delayed, err := oracle.CaptureCheckpoint("checkpoint-add", "add-readme-content", time.Second, scenario.Actions[1].Oracle.Checkpoints)
	if err != nil {
		t.Fatalf("capture delayed checkpoint: %v", err)
	}
	if err := VerifyFilesystemExpectations(scenario.Actions[1].Oracle.Checkpoints, delayed); !errors.Is(err, ErrFilesystemOracleMismatch) {
		t.Fatalf("delayed checkpoint error = %v, want filesystem mismatch", err)
	}

	results := familyAActionResults(scenario, []string{"checkpoint-create", "checkpoint-add", "checkpoint-revise", "checkpoint-summary"})
	results[1].Confirmed = true
	results[1].ConfirmedAt = time.Second
	verdict, err := EvaluateCustomerSimulation(scenario, results, []FilesystemCheckpoint{delayed}, nil, familyAProductTranscript())
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulation: %v", err)
	}
	if verdict.Pass {
		t.Fatalf("mechanical verdict unexpectedly passed: %+v", verdict)
	}
	if !mechanicalFindingContains(verdict, "confirmation_without_matching_side_effect") && !mechanicalFindingContains(verdict, "filesystem_checkpoint_mismatch") {
		t.Fatalf("mechanical findings = %+v, want intermediate action-specific mismatch", verdict.Findings)
	}
}

func TestCustomerSimulationMechanicalOracleRejectsSkippedActionAndStaleSummary(t *testing.T) {
	scenario := NewFamilyAScenario()
	results := familyAActionResults(scenario, []string{"checkpoint-create", "checkpoint-add", "checkpoint-revise", "checkpoint-summary"})
	results = append(results[:1], results[2:]...)
	checkpoints := []FilesystemCheckpoint{
		familyACheckpoint("checkpoint-create", "create-project-directory", 1*time.Second, scenario.Actions[0].Oracle.Checkpoints),
		familyACheckpoint("checkpoint-revise", "revise-readme", 3*time.Second, scenario.Actions[2].Oracle.Checkpoints),
		familyACheckpoint("checkpoint-summary", "summarize-final-state", 4*time.Second, scenario.Actions[3].Oracle.Checkpoints),
	}
	product := familyAProductTranscript()
	product[len(product)-1].Text = "The final project contains project/README.md, but its status is still draft."
	verdict, err := EvaluateCustomerSimulation(scenario, results, checkpoints, familyAToolObservations(), product)
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulation: %v", err)
	}
	if verdict.Pass {
		t.Fatal("mechanical verdict passed with a skipped action and stale summary")
	}
	for _, code := range []string{"missing_action", "summary_claims_absent_fact"} {
		if !mechanicalFindingContains(verdict, code) {
			t.Fatalf("mechanical findings = %+v, want %q", verdict.Findings, code)
		}
	}
}

func TestCustomerSimulationMechanicalOracleRejectsConfirmationBeforeToolResult(t *testing.T) {
	scenario := NewFamilyAScenario()
	results := familyAActionResults(scenario, []string{"checkpoint-create", "checkpoint-add", "checkpoint-revise", "checkpoint-summary"})
	results[1].ConfirmedAt = 1250 * time.Millisecond
	checkpoints := []FilesystemCheckpoint{
		familyACheckpoint("checkpoint-create", "create-project-directory", 1*time.Second, scenario.Actions[0].Oracle.Checkpoints),
		familyACheckpoint("checkpoint-add", "add-readme-content", 2*time.Second, scenario.Actions[1].Oracle.Checkpoints),
		familyACheckpoint("checkpoint-revise", "revise-readme", 3*time.Second, scenario.Actions[2].Oracle.Checkpoints),
		familyACheckpoint("checkpoint-summary", "summarize-final-state", 4*time.Second, scenario.Actions[3].Oracle.Checkpoints),
	}
	verdict, err := EvaluateCustomerSimulation(scenario, results, checkpoints, familyAToolObservations(), familyAProductTranscript())
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulation: %v", err)
	}
	if verdict.Pass {
		t.Fatal("mechanical verdict passed when confirmation preceded the tool result")
	}
	if !mechanicalFindingContains(verdict, "confirmation_before_tool_result") {
		t.Fatalf("mechanical findings = %+v, want confirmation_before_tool_result", verdict.Findings)
	}
}

func familyAActionResults(scenario CustomerScenario, checkpointIDs []string) []ActionResult {
	results := make([]ActionResult, 0, len(scenario.Actions))
	for index, action := range scenario.Actions {
		result := ActionResult{
			ActionID:      action.ID,
			TurnID:        "turn-" + string(rune('1'+index)),
			Confirmed:     true,
			ConfirmedAt:   time.Duration(index+1) * time.Second,
			Disposition:   DispositionCompleted,
			EvidenceRefs:  []string{filesystemCheckpointEvidenceRef, toolObservationEvidenceRef, productTranscriptEvidenceRef},
			CheckpointIDs: []string{checkpointIDs[index]},
		}
		if index < 3 {
			result.ToolObservationIDs = []string{"tool-" + string(rune('1'+index))}
		}
		results = append(results, result)
	}
	return results
}

func familyACheckpoint(id, actionID string, at time.Duration, expectations []FilesystemExpectation) FilesystemCheckpoint {
	entries := make([]FilesystemCheckpointEntry, 0, len(expectations))
	for _, expectation := range expectations {
		entry := FilesystemCheckpointEntry{Path: expectation.Path, Type: expectation.Type, SHA256: expectation.SHA256}
		if expectation.Type == FileTypeFile {
			entry.Size = int64(len(expectation.Content))
		}
		entries = append(entries, entry)
	}
	return FilesystemCheckpoint{ID: id, ActionID: actionID, At: at, Entries: entries}
}

func familyAToolObservations() []ToolObservation {
	return []ToolObservation{
		{ID: "tool-1", ActionID: "create-project-directory", TurnID: "turn-1", Tool: "exec", Status: "completed", At: 200 * time.Millisecond, Duration: 100 * time.Millisecond, ResultSeen: true, Summary: "(no output)"},
		{ID: "tool-2", ActionID: "add-readme-content", TurnID: "turn-2", Tool: "write_file", Status: "completed", At: 1200 * time.Millisecond, Duration: 100 * time.Millisecond, ResultSeen: true, Summary: "File written: project/README.md"},
		{ID: "tool-3", ActionID: "revise-readme", TurnID: "turn-3", Tool: "edit_file", Status: "completed", At: 2200 * time.Millisecond, Duration: 100 * time.Millisecond, ResultSeen: true, Summary: "File edited: project/README.md"},
	}
}

func familyAProductTranscript() []TranscriptEvent {
	return []TranscriptEvent{
		{ID: "product-1", TurnID: "turn-1", Speaker: TranscriptProduct, Text: "Created the project directory.", At: time.Second, Final: true},
		{ID: "product-2", TurnID: "turn-2", Speaker: TranscriptProduct, Text: "Added the README content.", At: 2 * time.Second, Final: true},
		{ID: "product-3", TurnID: "turn-3", Speaker: TranscriptProduct, Text: "Updated project/README.md: status is ready for review.", At: 3 * time.Second, Final: true},
		{ID: "product-4", TurnID: "turn-4", Speaker: TranscriptProduct, Text: "The final project contains project/README.md with status ready for review; no other files were created.", At: 4 * time.Second, Final: true},
	}
}

func mechanicalFindingContains(verdict MechanicalVerdict, code string) bool {
	for _, finding := range verdict.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func TestNewFamilyAScenarioIsVersionedAndNatural(t *testing.T) {
	scenario := NewFamilyAScenario()
	if err := scenario.Validate(); err != nil {
		t.Fatalf("NewFamilyAScenario validation: %v", err)
	}
	if len(FamilyASpokenScript()) != len(scenario.Actions) {
		t.Fatalf("spoken script length = %d, actions = %d", len(FamilyASpokenScript()), len(scenario.Actions))
	}
	for index, turn := range FamilyASpokenScript() {
		if turn.ActionID != scenario.Actions[index].ID || strings.TrimSpace(turn.Text) == "" {
			t.Fatalf("script turn %d = %+v, want natural wording for action %q", index, turn, scenario.Actions[index].ID)
		}
	}
}
