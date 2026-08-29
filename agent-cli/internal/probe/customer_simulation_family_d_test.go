package probe

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFamilyDScenariosDeclareSeparateTerminationShapes(t *testing.T) {
	for _, method := range []TerminationMethod{TerminationSIGINT, TerminationNatural} {
		scenario := NewFamilyDScenario(method)
		if err := scenario.Validate(); err != nil {
			t.Fatalf("Family D %q scenario validation: %v", method, err)
		}
		if scenario.Family != ScenarioFamilyD || scenario.Termination != method || len(scenario.Actions) != 1 {
			t.Fatalf("Family D %q scenario = %+v, want one action and matching termination", method, scenario)
		}
		if len(FamilyDSpokenScript()) != 1 || FamilyDSpokenScript()[0].ActionID != FamilyDActionID || strings.TrimSpace(FamilyDSpokenScript()[0].Text) == "" {
			t.Fatalf("Family D spoken script = %+v, want one natural action turn", FamilyDSpokenScript())
		}
		if method == TerminationSIGINT && scenario.Interruption.Kind != InterruptionDuringOutput {
			t.Fatalf("SIGINT interruption = %+v, want during_output", scenario.Interruption)
		}
		if method == TerminationNatural && scenario.Interruption.Kind != InterruptionNone {
			t.Fatalf("natural interruption = %+v, want none", scenario.Interruption)
		}
	}
}

func TestFamilyDTerminationOracleAcceptsCleanSIGINTAndNaturalCompletion(t *testing.T) {
	for _, method := range []TerminationMethod{TerminationSIGINT, TerminationNatural} {
		scenario := NewFamilyDScenario(method)
		results, checkpoints, product, evidence := familyDPositiveEvidence(method)
		verdict, err := EvaluateCustomerSimulationTermination(scenario, results, checkpoints, nil, product, evidence)
		if err != nil {
			t.Fatalf("Family D %q evaluation: %v", method, err)
		}
		if !verdict.Pass || len(verdict.Findings) != 0 {
			t.Fatalf("Family D %q verdict = %+v, want pass without findings", method, verdict)
		}
	}
}

func TestFamilyDTerminationOracleRejectsIgnoredSIGINTOrphansAndUnresolvedNaturalWork(t *testing.T) {
	sigintScenario := NewFamilyDScenario(TerminationSIGINT)
	results, checkpoints, product, evidence := familyDPositiveEvidence(TerminationSIGINT)
	ignored := evidence
	ignored.SignalSent = false
	ignored.Signal = ""
	ignored.SignalAt = 0
	ignored.Process.SignalSent = false
	ignored.Process.Signal = ""
	ignored.Process.SignalAt = 0
	if err := ignored.Validate(sigintScenario); !errors.Is(err, ErrInvalidCustomerEvidence) {
		t.Fatalf("ignored SIGINT validation = %v, want invalid evidence", err)
	}

	orphaned := evidence
	orphaned.Process.DescendantsAlive = true
	verdict, err := EvaluateCustomerSimulationTermination(sigintScenario, results, checkpoints, nil, product, orphaned)
	if err != nil {
		t.Fatalf("orphaned SIGINT evaluation: %v", err)
	}
	if verdict.Pass || !familyDFindingContains(verdict, "orphan_process") {
		t.Fatalf("orphaned SIGINT verdict = %+v, want orphan_process finding", verdict)
	}

	outstandingTool := evidence
	outstandingTool.OutstandingToolIDs = []string{"tool-still-running"}
	verdict, err = EvaluateCustomerSimulationTermination(sigintScenario, results, checkpoints, nil, product, outstandingTool)
	if err != nil {
		t.Fatalf("outstanding SIGINT tool evaluation: %v", err)
	}
	if verdict.Pass || !familyDFindingContains(verdict, "unresolved_tool") {
		t.Fatalf("outstanding SIGINT tool verdict = %+v, want unresolved_tool finding", verdict)
	}

	naturalScenario := NewFamilyDScenario(TerminationNatural)
	naturalResults, naturalCheckpoints, naturalProduct, naturalEvidence := familyDPositiveEvidence(TerminationNatural)
	naturalEvidence.UnresolvedActionIDs = []string{FamilyDActionID}
	verdict, err = EvaluateCustomerSimulationTermination(naturalScenario, naturalResults, naturalCheckpoints, nil, naturalProduct, naturalEvidence)
	if err != nil {
		t.Fatalf("unresolved natural evaluation: %v", err)
	}
	if verdict.Pass || !familyDFindingContains(verdict, "unresolved_action") {
		t.Fatalf("unresolved natural verdict = %+v, want unresolved_action finding", verdict)
	}

	unreaped := evidence
	unreaped.Process.ChildWaited = false
	unreaped.Process.WaitCount = 0
	verdict, err = EvaluateCustomerSimulationTermination(sigintScenario, results, checkpoints, nil, product, unreaped)
	if err != nil {
		t.Fatalf("unreaped SIGINT evaluation: %v", err)
	}
	for _, code := range []string{"child_not_reaped", "child_reap_count"} {
		if !familyDFindingContains(verdict, code) {
			t.Fatalf("unreaped SIGINT verdict = %+v, want %q finding", verdict, code)
		}
	}
}

func TestFamilyDEvidenceFinalizesCleanAndBrokenOutcomes(t *testing.T) {
	for _, test := range []struct {
		name   string
		method TerminationMethod
		broken bool
	}{
		{name: "sigint", method: TerminationSIGINT},
		{name: "natural", method: TerminationNatural},
		{name: "broken-sigint", method: TerminationSIGINT, broken: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			scenario := NewFamilyDScenario(test.method)
			results, checkpoints, product, termination := familyDPositiveEvidence(test.method)
			process := termination.Process
			if test.broken {
				process.DescendantsAlive = true
				termination.Process = process
			}
			mechanical, err := EvaluateCustomerSimulationTermination(scenario, results, checkpoints, nil, product, termination)
			if err != nil {
				t.Fatalf("mechanical evaluation: %v", err)
			}
			if test.broken && mechanical.Pass {
				t.Fatal("broken evidence unexpectedly passed mechanical oracle")
			}
			if !test.broken && !mechanical.Pass {
				t.Fatalf("clean evidence failed mechanical oracle: %+v", mechanical)
			}

			bundle, err := newFamilyDBundle(t, scenario, termination, mechanical)
			if err != nil {
				t.Fatalf("create evidence bundle: %v", err)
			}
			if err := bundle.Validate(); !errors.Is(err, ErrInvalidCustomerEvidence) {
				t.Fatalf("unfinalized evidence validation = %v, want invalid evidence", err)
			}
			if err := bundle.Finalize(); err != nil {
				t.Fatalf("Finalize: %v", err)
			}
			manifest, err := VerifyCustomerEvidenceBundle(bundle.root)
			if err != nil {
				t.Fatalf("VerifyCustomerEvidenceBundle: %v", err)
			}
			if !manifest.Finalized || manifest.MechanicalPass != !test.broken {
				t.Fatalf("manifest = %+v, want finalized and mechanical_pass=%t", manifest, !test.broken)
			}
			if findArtifact(manifest.Artifacts, "events/termination.json").State != ArtifactStateAvailable {
				t.Fatalf("termination artifact = %+v, want available", findArtifact(manifest.Artifacts, "events/termination.json"))
			}
		})
	}
}

func familyDPositiveEvidence(method TerminationMethod) ([]ActionResult, []FilesystemCheckpoint, []TranscriptEvent, TerminationEvidence) {
	process := ProcessFacts{
		PID: 321, ExitCode: 0, ExitClassification: "normal", ChildWaited: true, WaitCount: 1,
		InputClosed: true, InputFinished: true, OutputClosed: true, StartedAt: 0, EndedAt: 2 * time.Second,
	}
	responseStatus := "completed"
	confirmed := true
	disposition := DispositionCompleted
	confirmedAt := 1200 * time.Millisecond
	satisfactionDeclared := true
	satisfactionAt := 1200 * time.Millisecond
	signalSent := false
	signal := ""
	signalAt := time.Duration(0)
	responseText := FamilyDResponseText
	if method == TerminationSIGINT {
		process.ExitClassification = "sigint"
		process.SignalSent = true
		process.Signal = duplexSIGINTName
		process.SignalAt = 800 * time.Millisecond
		process.InputFinished = false
		responseStatus = "interrupted"
		confirmed = false
		disposition = DispositionCancelled
		confirmedAt = 0
		satisfactionDeclared = false
		satisfactionAt = 0
		signalSent = true
		signal = duplexSIGINTName
		signalAt = 800 * time.Millisecond
		responseText = "I began the request and was speaking when the customer stopped the session."
	}
	checkpoint := FilesystemCheckpoint{
		ID: "checkpoint-termination", ActionID: FamilyDActionID, At: time.Second,
		Entries: []FilesystemCheckpointEntry{{Path: "termination/marker.txt", Type: FileTypeAbsent}},
	}
	result := ActionResult{
		ActionID: FamilyDActionID, TurnID: FamilyDActiveTurnID, Confirmed: confirmed, ConfirmedAt: confirmedAt,
		Disposition: disposition, OutcomeReason: "the selected termination shape was recorded", EvidenceRefs: terminationEvidenceRefs(),
		CheckpointIDs: []string{checkpoint.ID},
	}
	evidence := TerminationEvidence{
		Method: method, ActiveActionID: FamilyDActionID, ActiveTurnID: FamilyDActiveTurnID, ActiveResponseID: FamilyDActiveResponseID,
		ActiveResponseStatus: responseStatus, ActiveResponseStartedAt: 500 * time.Millisecond, ActiveResponseEndedAt: time.Second,
		SatisfactionDeclared: satisfactionDeclared, SatisfactionAt: satisfactionAt, SignalSent: signalSent, Signal: signal, SignalAt: signalAt,
		Process: process, EvidenceRefs: terminationEvidenceRefs(),
	}
	return []ActionResult{result}, []FilesystemCheckpoint{checkpoint}, []TranscriptEvent{{
		ID: "product-termination", TurnID: FamilyDActiveTurnID, Speaker: TranscriptProduct, Text: responseText, At: 700 * time.Millisecond, Final: true,
	}}, evidence
}

func newFamilyDBundle(t *testing.T, scenario CustomerScenario, termination TerminationEvidence, mechanical MechanicalVerdict) (*CustomerEvidenceBundle, error) {
	t.Helper()
	bundle, err := NewCustomerEvidenceBundle(filepath.Join(t.TempDir(), "bundle"), scenario, "run-"+scenario.ID, "hermetic-key")
	if err != nil {
		return nil, err
	}
	bundle.Transcripts = PairedTranscripts{
		Customer: []TranscriptEvent{{ID: "customer-termination", TurnID: FamilyDActiveTurnID, Speaker: TranscriptCustomer, Text: FamilyDSpokenScript()[0].Text, At: 100 * time.Millisecond, Final: true}},
		Product:  []TranscriptEvent{{ID: "product-termination", TurnID: FamilyDActiveTurnID, Speaker: TranscriptProduct, Text: terminationProductText(scenario.Termination), At: 700 * time.Millisecond, Final: true}},
	}
	bundle.AudioTurnEvents = []AudioTurnEvent{
		{ID: "input-termination", TurnID: FamilyDActiveTurnID, Direction: "input", Kind: "speech", At: 100 * time.Millisecond, Duration: 30 * time.Millisecond, Bytes: 960},
		{ID: "output-termination", TurnID: FamilyDActiveTurnID, Direction: "output", Kind: "speech", At: 500 * time.Millisecond, Duration: 30 * time.Millisecond, Bytes: 4},
	}
	bundle.FilesystemCheckpoints = []FilesystemCheckpoint{{
		ID: "checkpoint-termination", ActionID: FamilyDActionID, At: time.Second,
		Entries: []FilesystemCheckpointEntry{{Path: "termination/marker.txt", Type: FileTypeAbsent}},
	}}
	bundle.Process = termination.Process
	bundle.MechanicalVerdict = &mechanical
	bundle.Termination = &termination
	bundle.ValidatorInput = &ValidatorInput{
		Scenario: scenario, CustomerTranscript: bundle.Transcripts.Customer, ProductTranscript: bundle.Transcripts.Product,
		AudioTurnEvents: bundle.AudioTurnEvents, ToolObservations: bundle.ToolObservations, FilesystemCheckpoints: bundle.FilesystemCheckpoints,
		Process: bundle.Process, Mechanical: mechanical, Termination: &termination,
		EvidenceRefs: []string{"scenario.json", "transcripts/customer.jsonl", "transcripts/product.jsonl", "events/audio-turn-events.jsonl", "tool-observations.jsonl", "filesystem-checkpoints.jsonl", "process.json", "events/termination.json", "mechanical-verdict.json"},
	}
	if mechanical.Pass {
		bundle.ValidatorVerdict = &ValidatorVerdict{Verdict: ValidatorWorked, Summary: "The selected termination shape completed with all lifecycle evidence.", EvidenceRefs: terminationEvidenceRefs()}
	} else {
		bundle.ValidatorVerdict = &ValidatorVerdict{Verdict: ValidatorBroken, FirstFailingTurn: FamilyDActiveTurnID, Behavior: "A child descendant remained alive after termination.", Violation: "termination cleanup was incomplete", CustomerImpact: "The customer session left work running after it ended.", EvidenceRefs: terminationEvidenceRefs()}
	}
	recordDir := filepath.Join(t.TempDir(), "record-dir")
	if err := os.MkdirAll(recordDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(recordDir, "session.jsonl"), []byte("termination evidence\n"), 0o600); err != nil {
		return nil, err
	}
	if err := bundle.AddProductRecordDir(recordDir); err != nil {
		return nil, err
	}
	return bundle, nil
}

func terminationProductText(method TerminationMethod) string {
	if method == TerminationNatural {
		return FamilyDResponseText
	}
	return "I began the request and was speaking when the customer stopped the session."
}

func familyDFindingContains(verdict MechanicalVerdict, code string) bool {
	for _, finding := range verdict.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
