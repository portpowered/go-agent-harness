package probe

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFamilyEScenarioDeclaresNaturalPatienceBoundaries(t *testing.T) {
	scenario := NewFamilyEScenario()
	if err := scenario.Validate(); err != nil {
		t.Fatalf("NewFamilyEScenario validation: %v", err)
	}
	if scenario.Family != ScenarioFamilyE || len(scenario.Actions) != 1 || scenario.Actions[0].ID != FamilyEActionID {
		t.Fatalf("Family E scenario = %+v, want one patience action", scenario)
	}
	script := FamilyESpokenScript()
	if len(script) != 1 || script[0].ActionID != FamilyEActionID || strings.TrimSpace(script[0].Text) == "" {
		t.Fatalf("Family E spoken script = %+v, want one natural customer turn", script)
	}
	for index, prompt := range FamilyERepromptScript() {
		if strings.TrimSpace(prompt) == "" {
			t.Fatalf("Family E re-prompt %d is empty", index)
		}
	}
	if got := FamilyEPatienceThresholds(); got.AbsoluteDeadAir <= got.Reprompt || got.MaxReprompts != 1 {
		t.Fatalf("Family E patience thresholds = %+v, want bounded re-prompt before absolute dead air", got)
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "customer-simulation", "family-e.scenario.json"))
	if err != nil {
		t.Fatalf("read Family E fixture: %v", err)
	}
	parsed, err := ParseCustomerScenario(data)
	if err != nil {
		t.Fatalf("parse Family E fixture: %v", err)
	}
	if parsed.ID != scenario.ID || parsed.Patience != scenario.Patience {
		t.Fatalf("parsed Family E fixture = %+v, want generated patience declaration", parsed)
	}
}

func TestFamilyEPatiencePolicyUsesControllableClockForNormalSlowRecoveryAndDeadAir(t *testing.T) {
	cases := []struct {
		name         string
		build        func(*testing.T, CustomerScenario) PatienceEvidence
		wantPass     bool
		wantReprompt int
		wantFinding  string
	}{
		{name: "normal latency", build: familyENormalEvidence, wantPass: true},
		{name: "slow but progressing work", build: familyESlowProgressEvidence, wantPass: true},
		{name: "recovery after one re-prompt", build: familyERecoveryEvidence, wantPass: true, wantReprompt: 1},
		{name: "terminal dead air", build: familyEDeadAirEvidence, wantFinding: "patience_dead_air"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			scenario := NewFamilyEScenario()
			evidence := test.build(t, scenario)
			results, checkpoints, product := familyEActionEvidence(scenario, evidence)
			verdict, err := EvaluateCustomerSimulationPatience(scenario, results, checkpoints, nil, product, evidence)
			if err != nil {
				t.Fatalf("EvaluateCustomerSimulationPatience: %v", err)
			}
			if verdict.Pass != test.wantPass {
				t.Fatalf("patience verdict = %+v, want pass=%t", verdict, test.wantPass)
			}
			if evidence.RepromptCount != test.wantReprompt {
				t.Fatalf("re-prompt count = %d, want %d", evidence.RepromptCount, test.wantReprompt)
			}
			if test.wantFinding != "" && !familyEFindingContains(verdict, test.wantFinding) {
				t.Fatalf("patience findings = %+v, want %q", verdict.Findings, test.wantFinding)
			}
			if test.wantFinding != "" {
				finding := familyEFinding(verdict, test.wantFinding)
				for _, fragment := range []string{"family-e-turn-1", "re-prompts=", "outstanding_tools=", "process_exit=", "customer impact:"} {
					if !strings.Contains(finding.Message, fragment) {
						t.Fatalf("dead-air finding = %q, want %q", finding.Message, fragment)
					}
				}
			}
		})
	}
}

func TestPatienceControllerRejectsEarlyAndUnboundedReprompts(t *testing.T) {
	scenario := NewFamilyEScenario()
	clock := NewPatienceTestClock()
	controller, err := NewPatienceController(scenario, FamilyEActionID, FamilyETurnID, clock)
	if err != nil {
		t.Fatalf("NewPatienceController: %v", err)
	}
	if err := controller.StartListening(); err != nil {
		t.Fatalf("StartListening: %v", err)
	}
	clock.Advance(500 * time.Millisecond)
	if _, err := controller.Reprompt(FamilyEReprompt(0)); !errors.Is(err, ErrPatienceDecisionDenied) {
		t.Fatalf("early re-prompt error = %v, want decision-denied error", err)
	}
	clock.Advance(3 * time.Second)
	if _, err := controller.Reprompt(FamilyEReprompt(0)); err != nil {
		t.Fatalf("threshold re-prompt: %v", err)
	}
	if _, err := controller.Reprompt(FamilyEReprompt(1)); !errors.Is(err, ErrPatienceDecisionDenied) {
		t.Fatalf("second immediate re-prompt error = %v, want bounded decision-denied error", err)
	}
	clock.Advance(500 * time.Millisecond)
	if _, err := controller.Decision(); err != nil {
		t.Fatalf("decision after one re-prompt: %v", err)
	}
}

func TestFamilyERejectsTimeoutRelabeledAsNaturalCompletion(t *testing.T) {
	scenario := NewFamilyEScenario()
	clock := NewPatienceTestClock()
	controller, err := NewPatienceController(scenario, FamilyEActionID, FamilyETurnID, clock)
	if err != nil {
		t.Fatalf("NewPatienceController: %v", err)
	}
	if err := controller.StartListening(); err != nil {
		t.Fatalf("StartListening: %v", err)
	}
	clock.Advance(100 * time.Millisecond)
	if err := controller.ObserveResponseStart("response began"); err != nil {
		t.Fatalf("ObserveResponseStart: %v", err)
	}
	clock.Advance(200 * time.Millisecond)
	if err := controller.Timeout(); err != nil {
		t.Fatalf("Timeout: %v", err)
	}
	evidence, err := controller.Evidence(familyEProcess(evidenceTerminalAt(controller), "timeout"), nil, nil)
	if err != nil {
		t.Fatalf("controller evidence: %v", err)
	}
	results, checkpoints, product := familyEActionEvidence(scenario, evidence)
	verdict, err := EvaluateCustomerSimulationPatience(scenario, results, checkpoints, nil, product, evidence)
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulationPatience: %v", err)
	}
	if verdict.Pass || !familyEFindingContains(verdict, "timeout_not_natural_completion") {
		t.Fatalf("timeout verdict = %+v, want explicit non-natural-completion finding", verdict)
	}
}

func TestFamilyEEvidenceBundleWritesPatienceTimeline(t *testing.T) {
	scenario := NewFamilyEScenario()
	evidence := familyENormalEvidence(t, scenario)
	results, checkpoints, product := familyEActionEvidence(scenario, evidence)
	mechanical, err := EvaluateCustomerSimulationPatience(scenario, results, checkpoints, nil, product, evidence)
	if err != nil {
		t.Fatalf("patience mechanical evaluation: %v", err)
	}
	if !mechanical.Pass {
		t.Fatalf("normal patience mechanical verdict = %+v, want pass", mechanical)
	}

	root := filepath.Join(t.TempDir(), "bundle")
	bundle, err := NewCustomerEvidenceBundle(root, scenario, "run-family-e", "hermetic-key")
	if err != nil {
		t.Fatalf("NewCustomerEvidenceBundle: %v", err)
	}
	bundle.Transcripts = PairedTranscripts{
		Customer: []TranscriptEvent{{ID: "customer-e", TurnID: FamilyETurnID, Speaker: TranscriptCustomer, Text: FamilyESpokenScript()[0].Text, At: 10 * time.Millisecond, Final: true}},
		Product:  product,
	}
	bundle.AudioTurnEvents = []AudioTurnEvent{
		{ID: "audio-in-e", TurnID: FamilyETurnID, Direction: "input", Kind: "speech", At: 10 * time.Millisecond, Duration: 20 * time.Millisecond, Bytes: 640},
		{ID: "audio-out-e", TurnID: FamilyETurnID, Direction: "output", Kind: "speech", At: evidence.ResponseStartedAt, Duration: 20 * time.Millisecond, Bytes: 640},
	}
	bundle.FilesystemCheckpoints = checkpoints
	bundle.Process = evidence.Process
	bundle.MechanicalVerdict = &mechanical
	bundle.Patience = &evidence
	bundle.ValidatorInput = &ValidatorInput{
		Scenario: scenario, CustomerTranscript: bundle.Transcripts.Customer, ProductTranscript: product,
		AudioTurnEvents: bundle.AudioTurnEvents, ToolObservations: nil, FilesystemCheckpoints: checkpoints,
		Process: evidence.Process, Mechanical: mechanical, Patience: &evidence,
		EvidenceRefs: append([]string{"scenario.json", "filesystem-checkpoints.jsonl", "mechanical-verdict.json"}, FamilyEPatienceEvidenceRefs()...),
	}
	bundle.ValidatorVerdict = &ValidatorVerdict{
		Verdict:      ValidatorWorked,
		Summary:      "The customer listened through observable response progress and received a truthful completion.",
		EvidenceRefs: FamilyEPatienceEvidenceRefs(),
	}
	recordDir := filepath.Join(t.TempDir(), "record-dir")
	if err := os.MkdirAll(recordDir, 0o700); err != nil {
		t.Fatalf("create record directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recordDir, "session.jsonl"), []byte("patience evidence\n"), 0o600); err != nil {
		t.Fatalf("write record file: %v", err)
	}
	if err := bundle.AddProductRecordDir(recordDir); err != nil {
		t.Fatalf("AddProductRecordDir: %v", err)
	}
	if err := bundle.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	manifest, err := VerifyCustomerEvidenceBundle(root)
	if err != nil {
		t.Fatalf("VerifyCustomerEvidenceBundle: %v", err)
	}
	if !manifest.Finalized || !manifest.MechanicalPass || manifest.ValidatorVerdict != ValidatorWorked {
		t.Fatalf("manifest = %+v, want finalized passing Family E bundle", manifest)
	}
	patienceArtifact := findArtifact(manifest.Artifacts, FamilyEPatienceEventPath)
	if patienceArtifact.State != ArtifactStateAvailable || patienceArtifact.Kind != ArtifactKindPatienceEvidence {
		t.Fatalf("patience artifact = %+v, want available patience evidence", patienceArtifact)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(FamilyEPatienceEventPath))); err != nil {
		t.Fatalf("patience artifact missing: %v", err)
	}
}

func familyENormalEvidence(t *testing.T, scenario CustomerScenario) PatienceEvidence {
	t.Helper()
	clock := NewPatienceTestClock()
	controller := familyEController(t, scenario, clock)
	clock.Advance(100 * time.Millisecond)
	if err := controller.ObserveResponseStart("response started promptly"); err != nil {
		t.Fatalf("ObserveResponseStart: %v", err)
	}
	clock.Advance(100 * time.Millisecond)
	if err := controller.ObserveProductSpeech(200*time.Millisecond, "completion speech"); err != nil {
		t.Fatalf("ObserveProductSpeech: %v", err)
	}
	clock.Advance(200 * time.Millisecond)
	if err := controller.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return familyEEvidenceFromController(t, controller, "normal")
}

func familyESlowProgressEvidence(t *testing.T, scenario CustomerScenario) PatienceEvidence {
	t.Helper()
	clock := NewPatienceTestClock()
	controller := familyEController(t, scenario, clock)
	clock.Advance(500 * time.Millisecond)
	if err := controller.ObserveResponseStart("response started before the response-start threshold"); err != nil {
		t.Fatalf("ObserveResponseStart: %v", err)
	}
	for index := 0; index < 3; index++ {
		clock.Advance(1500 * time.Millisecond)
		if err := controller.ObserveToolProgress(100*time.Millisecond, "tool made measurable progress"); err != nil {
			t.Fatalf("ObserveToolProgress %d: %v", index, err)
		}
		clock.Advance(100 * time.Millisecond)
	}
	if err := controller.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return familyEEvidenceFromController(t, controller, "slow-progress")
}

func familyERecoveryEvidence(t *testing.T, scenario CustomerScenario) PatienceEvidence {
	t.Helper()
	clock := NewPatienceTestClock()
	controller := familyEController(t, scenario, clock)
	clock.Advance(100 * time.Millisecond)
	if err := controller.ObserveResponseStart("response opened before the customer needed to check in"); err != nil {
		t.Fatalf("ObserveResponseStart: %v", err)
	}
	clock.Advance(3 * time.Second)
	if decision, err := controller.Decision(); err != nil || decision.Kind != PatienceDecisionReprompt {
		t.Fatalf("re-prompt decision = %+v, %v; want bounded re-prompt", decision, err)
	}
	if _, err := controller.Reprompt(FamilyEReprompt(0)); err != nil {
		t.Fatalf("Reprompt: %v", err)
	}
	clock.Advance(500 * time.Millisecond)
	if err := controller.ObserveProductSpeech(100*time.Millisecond, "response recovered after the check-in"); err != nil {
		t.Fatalf("ObserveProductSpeech: %v", err)
	}
	clock.Advance(100 * time.Millisecond)
	if err := controller.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return familyEEvidenceFromController(t, controller, "recovered")
}

func familyEDeadAirEvidence(t *testing.T, scenario CustomerScenario) PatienceEvidence {
	t.Helper()
	clock := NewPatienceTestClock()
	controller := familyEController(t, scenario, clock)
	clock.Advance(100 * time.Millisecond)
	if err := controller.ObserveResponseStart("response started, then stopped making observable progress"); err != nil {
		t.Fatalf("ObserveResponseStart: %v", err)
	}
	clock.Advance(scenario.Patience.AbsoluteDeadAir)
	if decision, err := controller.Decision(); err != nil || decision.Kind != PatienceDecisionDeadAir {
		t.Fatalf("dead-air decision = %+v, %v; want dead-air decision", decision, err)
	}
	if err := controller.DeclareDeadAir(); err != nil {
		t.Fatalf("DeclareDeadAir: %v", err)
	}
	return familyEEvidenceFromController(t, controller, "dead-air")
}

func familyEController(t *testing.T, scenario CustomerScenario, clock *ManualPatienceClock) *PatienceController {
	t.Helper()
	controller, err := NewPatienceController(scenario, FamilyEActionID, FamilyETurnID, clock)
	if err != nil {
		t.Fatalf("NewPatienceController: %v", err)
	}
	if err := controller.StartListening(); err != nil {
		t.Fatalf("StartListening: %v", err)
	}
	return controller
}

func familyEEvidenceFromController(t *testing.T, controller *PatienceController, label string) PatienceEvidence {
	t.Helper()
	evidence, err := controller.Evidence(familyEProcess(evidenceTerminalAt(controller), processClassification(label)), nil, nil)
	if err != nil {
		t.Fatalf("controller evidence %s: %v", label, err)
	}
	return evidence
}

func evidenceTerminalAt(controller *PatienceController) time.Duration {
	return controller.clock.Now().Sub(controller.startedAt)
}

func processClassification(label string) string {
	if label == "dead-air" {
		return "timeout"
	}
	return "normal"
}

func familyEProcess(endedAt time.Duration, classification string) ProcessFacts {
	return ProcessFacts{
		PID: 77, ExitCode: 0, ExitClassification: classification, ChildWaited: true, WaitCount: 1,
		InputClosed: true, InputFinished: true, OutputClosed: true, StartedAt: 0, EndedAt: endedAt,
	}
}

func familyEActionEvidence(scenario CustomerScenario, patience PatienceEvidence) ([]ActionResult, []FilesystemCheckpoint, []TranscriptEvent) {
	checkpoint := FilesystemCheckpoint{
		ID: "checkpoint-family-e", ActionID: FamilyEActionID, At: patience.TerminalAt,
		Entries: []FilesystemCheckpointEntry{{Path: "patience/marker.txt", Type: FileTypeAbsent}},
	}
	return []ActionResult{{
			ActionID: FamilyEActionID, TurnID: FamilyETurnID, Confirmed: true, ConfirmedAt: patience.TerminalAt,
			Disposition: DispositionCompleted, EvidenceRefs: defaultActionEvidenceRefs(), CheckpointIDs: []string{checkpoint.ID},
		}}, []FilesystemCheckpoint{checkpoint}, []TranscriptEvent{{
			ID: "product-family-e", TurnID: FamilyETurnID, Speaker: TranscriptProduct,
			Text: "The request is complete and the customer can stop listening.", At: patience.TerminalAt, Final: true,
		}}
}

func familyEFindingContains(verdict MechanicalVerdict, code string) bool {
	for _, finding := range verdict.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func familyEFinding(verdict MechanicalVerdict, code string) MechanicalFinding {
	for _, finding := range verdict.Findings {
		if finding.Code == code {
			return finding
		}
	}
	return MechanicalFinding{}
}
