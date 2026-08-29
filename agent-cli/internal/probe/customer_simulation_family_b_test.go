package probe

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFamilyBScenarioIsVersionedAndDeclaresCorrection(t *testing.T) {
	scenario := NewFamilyBScenario()
	if err := scenario.Validate(); err != nil {
		t.Fatalf("NewFamilyBScenario validation: %v", err)
	}
	if scenario.Family != ScenarioFamilyB || scenario.Interruption.Kind != InterruptionDuringOutput {
		t.Fatalf("Family B scenario = %+v, want output interruption", scenario)
	}
	if len(scenario.Actions) != 2 || scenario.Actions[0].ID != FamilyBOriginalActionID || scenario.Actions[1].ID != FamilyBReplacementActionID {
		t.Fatalf("Family B action order = %+v, want original then replacement", scenario.Actions)
	}
	script := FamilyBSpokenScript()
	if len(script) != 2 || script[0].ActionID != FamilyBOriginalActionID || script[1].ActionID != FamilyBReplacementActionID {
		t.Fatalf("Family B spoken script = %+v, want original and correction turns", script)
	}
	if script[0].Text == "" || script[1].Text == "" || script[0].Text == script[1].Text {
		t.Fatalf("Family B script does not contain distinct natural turns: %+v", script)
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "customer-simulation", "family-b.scenario.json"))
	if err != nil {
		t.Fatalf("read Family B fixture: %v", err)
	}
	parsed, err := ParseCustomerScenario(data)
	if err != nil {
		t.Fatalf("parse Family B fixture: %v", err)
	}
	if parsed.ID != scenario.ID || parsed.Actions[1].Oracle.Checkpoints[1].SHA256 != familyBReplacementReleaseNoteHash {
		t.Fatalf("parsed Family B fixture = %+v, want generated scenario identity and replacement hash", parsed)
	}
}

func TestCorrectionEvidenceRoundTripAndShapeValidation(t *testing.T) {
	scenario := NewFamilyBScenario()
	evidence := familyBCorrectionEvidence()
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("marshal correction evidence: %v", err)
	}
	var parsed CorrectionEvidence
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal correction evidence: %v", err)
	}
	if err := parsed.Validate(scenario); err != nil {
		t.Fatalf("validate correction evidence: %v", err)
	}

	bad := evidence
	bad.OriginalActionID = bad.ReplacementActionID
	if err := bad.Validate(scenario); err == nil || !errors.Is(err, ErrInvalidCustomerEvidence) {
		t.Fatalf("same original/replacement validation error = %v, want invalid evidence", err)
	}
	bad = evidence
	bad.OriginalResponseEndedAt = bad.OriginalResponseStartedAt - time.Nanosecond
	if err := bad.Validate(scenario); err == nil || !errors.Is(err, ErrInvalidCustomerEvidence) {
		t.Fatalf("reversed response validation error = %v, want invalid evidence", err)
	}
}

func TestFamilyBCorrectionOracleAcceptsCompletedOriginalAndReplacement(t *testing.T) {
	scenario := NewFamilyBScenario()
	results, checkpoints, tools, product := familyBPositiveEvidence(scenario)
	verdict, err := EvaluateCustomerSimulationCorrection(scenario, results, checkpoints, tools, product, familyBCorrectionEvidence())
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulationCorrection: %v", err)
	}
	if !verdict.Pass || len(verdict.Findings) != 0 {
		t.Fatalf("Family B mechanical verdict = %+v, want pass without findings", verdict)
	}
}

func TestFamilyBCorrectionOracleRejectsIgnoredCorrection(t *testing.T) {
	scenario := NewFamilyBScenario()
	results, checkpoints, tools, product := familyBPositiveEvidence(scenario)
	evidence := familyBCorrectionEvidence()
	evidence.OriginalResponseStatus = "completed"
	verdict, err := EvaluateCustomerSimulationCorrection(scenario, results, checkpoints, tools, product, evidence)
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulationCorrection: %v", err)
	}
	if verdict.Pass || !familyBMechanicalFindingContains(verdict, "correction_ignored") {
		t.Fatalf("ignored correction verdict = %+v, want correction_ignored finding", verdict)
	}
}

func TestFamilyBCorrectionOracleRejectsSilentlyPartialOriginal(t *testing.T) {
	scenario := NewFamilyBScenario()
	results, checkpoints, tools, product := familyBPositiveEvidence(scenario)
	checkpoints[0] = FilesystemCheckpoint{
		ID:       "checkpoint-original",
		ActionID: FamilyBOriginalActionID,
		At:       700 * time.Millisecond,
		Entries: []FilesystemCheckpointEntry{
			{Path: "draft/brief.md", Type: FileTypeFile, SHA256: familyBReplacementReleaseNoteHash, Size: int64(len(FamilyBReplacementReleaseNote))},
			{Path: "final/brief.md", Type: FileTypeAbsent},
		},
	}
	verdict, err := EvaluateCustomerSimulationCorrection(scenario, results, checkpoints, tools, product, familyBCorrectionEvidence())
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulationCorrection: %v", err)
	}
	if verdict.Pass || !familyBMechanicalFindingContains(verdict, "filesystem_checkpoint_mismatch") || !familyBMechanicalFindingContains(verdict, "confirmation_without_matching_side_effect") {
		t.Fatalf("partial original verdict = %+v, want action-specific filesystem findings", verdict)
	}
}

func TestFamilyBCorrectionOracleRejectsSkippedReplacement(t *testing.T) {
	scenario := NewFamilyBScenario()
	results, checkpoints, tools, product := familyBPositiveEvidence(scenario)
	results = results[:1]
	checkpoints = checkpoints[:1]
	tools = tools[:1]
	product = product[:1]
	verdict, err := EvaluateCustomerSimulationCorrection(scenario, results, checkpoints, tools, product, familyBCorrectionEvidence())
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulationCorrection: %v", err)
	}
	if verdict.Pass || !familyBMechanicalFindingContains(verdict, "missing_action") || !familyBMechanicalFindingContains(verdict, "replacement_not_verified") {
		t.Fatalf("skipped replacement verdict = %+v, want missing/replacement findings", verdict)
	}
}

func TestFamilyBCorrectionOracleRejectsUnresolvedToolsAndOrphans(t *testing.T) {
	scenario := NewFamilyBScenario()
	results, checkpoints, tools, product := familyBPositiveEvidence(scenario)
	evidence := familyBCorrectionEvidence()
	evidence.OutstandingToolIDs = []string{"call-still-running"}
	evidence.UnresolvedActionIDs = []string{FamilyBReplacementActionID}
	evidence.Process.DescendantsAlive = true
	evidence.Process.ChildWaited = false
	verdict, err := EvaluateCustomerSimulationCorrection(scenario, results, checkpoints, tools, product, evidence)
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulationCorrection: %v", err)
	}
	for _, code := range []string{"unresolved_tool", "unresolved_action", "orphan_process", "child_not_reaped"} {
		if !familyBMechanicalFindingContains(verdict, code) {
			t.Fatalf("unresolved Family B verdict = %+v, want %q finding", verdict, code)
		}
	}
}

func familyBPositiveEvidence(scenario CustomerScenario) ([]ActionResult, []FilesystemCheckpoint, []ToolObservation, []TranscriptEvent) {
	return []ActionResult{
			{
				ActionID:           FamilyBOriginalActionID,
				TurnID:             "turn-1",
				Confirmed:          true,
				ConfirmedAt:        650 * time.Millisecond,
				Disposition:        DispositionCancelled,
				OutcomeReason:      "correction interrupted the original action after the draft write; preserved draft bytes were recorded",
				EvidenceRefs:       defaultActionEvidenceRefs(),
				CheckpointIDs:      []string{"checkpoint-original"},
				ToolObservationIDs: []string{"tool-original"},
			},
			{
				ActionID:           FamilyBReplacementActionID,
				TurnID:             "turn-2",
				Confirmed:          true,
				ConfirmedAt:        1400 * time.Millisecond,
				Disposition:        DispositionCompleted,
				EvidenceRefs:       defaultActionEvidenceRefs(),
				CheckpointIDs:      []string{"checkpoint-replacement"},
				ToolObservationIDs: []string{"tool-replacement"},
			},
		}, []FilesystemCheckpoint{
			familyBCheckpoint("checkpoint-original", FamilyBOriginalActionID, 600*time.Millisecond, scenario.Actions[0].Oracle.Checkpoints),
			familyBCheckpoint("checkpoint-replacement", FamilyBReplacementActionID, 1300*time.Millisecond, scenario.Actions[1].Oracle.Checkpoints),
		}, []ToolObservation{
			{ID: "tool-original", ActionID: FamilyBOriginalActionID, TurnID: "turn-1", Tool: "write_file", Status: "completed", At: 450 * time.Millisecond, Duration: 100 * time.Millisecond, ResultSeen: true, Summary: "File written: draft/brief.md"},
			{ID: "tool-replacement", ActionID: FamilyBReplacementActionID, TurnID: "turn-2", Tool: "write_file", Status: "completed", At: 1050 * time.Millisecond, Duration: 100 * time.Millisecond, ResultSeen: true, Summary: "File written: final/brief.md"},
		}, []TranscriptEvent{
			{ID: "product-turn-1", TurnID: "turn-1", Speaker: TranscriptProduct, Text: "Created draft/brief.md and kept the original draft while I explained the next step.", At: 650 * time.Millisecond, Final: true},
			{ID: "product-turn-2", TurnID: "turn-2", Speaker: TranscriptProduct, Text: "Created final/brief.md as the corrected release note.", At: 1400 * time.Millisecond, Final: true},
		}
}

func familyBCorrectionEvidence() CorrectionEvidence {
	return CorrectionEvidence{
		OriginalActionID:             FamilyBOriginalActionID,
		ReplacementActionID:          FamilyBReplacementActionID,
		OriginalTurnID:               "turn-1",
		CorrectionTurnID:             "turn-2",
		OriginalResponseID:           "response-original",
		OriginalResponseStartedAt:    700 * time.Millisecond,
		CorrectionStartedAt:          800 * time.Millisecond,
		CancellationSentAt:           750 * time.Millisecond,
		OriginalResponseEndedAt:      900 * time.Millisecond,
		ReplacementResponseStartedAt: 1000 * time.Millisecond,
		ReplacementResponseEndedAt:   1200 * time.Millisecond,
		OriginalResponseStatus:       "cancelled",
		ReplacementResponseStatus:    "completed",
		Process: &ProcessFacts{
			PID:                123,
			ExitCode:           0,
			ExitClassification: "normal",
			ChildWaited:        true,
			InputClosed:        true,
			OutputClosed:       true,
			StartedAt:          0,
			EndedAt:            2 * time.Second,
		},
	}
}

func familyBCheckpoint(id, actionID string, at time.Duration, expectations []FilesystemExpectation) FilesystemCheckpoint {
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

func familyBMechanicalFindingContains(verdict MechanicalVerdict, code string) bool {
	for _, finding := range verdict.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
