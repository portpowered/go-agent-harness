package probe

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
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
	evidence.OriginalResponseStatus = string(DispositionCompleted)
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
			{ID: "tool-original", ActionID: FamilyBOriginalActionID, TurnID: "turn-1", Tool: "write_file", Status: string(DispositionCompleted), At: 450 * time.Millisecond, Duration: 100 * time.Millisecond, ResultSeen: true, Summary: "File written: draft/brief.md"},
			{ID: "tool-replacement", ActionID: FamilyBReplacementActionID, TurnID: "turn-2", Tool: "write_file", Status: string(DispositionCompleted), At: 1050 * time.Millisecond, Duration: 100 * time.Millisecond, ResultSeen: true, Summary: "File written: final/brief.md"},
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
		OriginalResponseStatus:       string(DispositionCancelled),
		ReplacementResponseStatus:    string(DispositionCompleted),
		CancellationEventRecorded:    true,
		CancellationResponseID:       "response-original",
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

// The media queue can reach the device before the normalized response stream.
// Cancellation must bind to the delivered response without stealing a later one.
func TestCorrectionBindsMediaBeforeNormalizedResponse(t *testing.T) {
	for _, mediaID := range []string{"original", "unrelated"} {
		t.Run(mediaID, func(t *testing.T) {
			p := customerSimulationStreamParser{scenario: NewFamilyBScenario()}
			boundary := &customerSimulationMediaBoundary{SampleCount: 24}
			boundary.Frame.PlaybackResponse.ResponseID = mediaID
			p.consume(customerSimulationRecordedMessage{at: time.Millisecond, media: boundary})
			p.consume(customerSimulationRecordedMessage{at: 2 * time.Millisecond, dir: transcript.DirectionIn, message: messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue()}})
			for i, id := range []string{"original", "replacement"} {
				at := time.Duration(3+i*2) * time.Millisecond
				p.consume(customerSimulationRecordedMessage{at: at, dir: transcript.DirectionOut, message: messages.StreamMessage{Type: messages.StreamTypeMessageStart, ResponseID: id, Value: messages.NewMessageStartValue()}})
				p.consume(customerSimulationRecordedMessage{at: at + time.Millisecond, dir: transcript.DirectionOut, message: messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ResponseID: id, Value: messages.NewMessageEndValue(messages.TokenUsage{})}})
			}
			p.finish()
			p.applyMediaBoundaries()
			if len(p.facts.responses) != 2 {
				t.Fatalf("responses = %+v", p.facts.responses)
			}
			original, replacement := p.facts.responses[0], p.facts.responses[1]
			matched := mediaID == "original"
			if original.AudioObserved != matched || original.Cancelled != matched || replacement.AudioObserved || replacement.Cancelled {
				t.Fatalf("media/cancellation crossed response identity: original=%+v replacement=%+v", original, replacement)
			}
			if p.facts.cancelResponseID != mediaID {
				t.Fatalf("cancel ID = %q, want %q", p.facts.cancelResponseID, mediaID)
			}
		})
	}
}

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
			assertFamilyDBundleFinalizes(t, scenario, termination, mechanical, !test.broken)
		})
	}
}

// assertFamilyDBundleFinalizes requires the bundle to reject validation until
// finalized and then to verify with the expected mechanical outcome.
func assertFamilyDBundleFinalizes(t *testing.T, scenario CustomerScenario, termination TerminationEvidence, mechanical MechanicalVerdict, wantPass bool) {
	t.Helper()
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
	if !manifest.Finalized || manifest.MechanicalPass != wantPass {
		t.Fatalf("manifest = %+v, want finalized and mechanical_pass=%t", manifest, wantPass)
	}
	if findArtifact(manifest.Artifacts, "events/termination.json").State != ArtifactStateAvailable {
		t.Fatalf("termination artifact = %+v, want available", findArtifact(manifest.Artifacts, "events/termination.json"))
	}
}

func familyDPositiveEvidence(method TerminationMethod) ([]ActionResult, []FilesystemCheckpoint, []TranscriptEvent, TerminationEvidence) {
	process := ProcessFacts{
		PID: 321, ExitCode: 0, ExitClassification: "normal", ChildWaited: true, WaitCount: 1,
		InputClosed: true, InputFinished: true, OutputClosed: true, StartedAt: 0, EndedAt: 2 * time.Second,
	}
	responseStatus := terminationStatusCompleted
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
		process.ExitClassification = duplexExitSIGINT
		process.SignalSent = true
		process.Signal = duplexSIGINTName
		process.SignalAt = 800 * time.Millisecond
		process.InputFinished = false
		responseStatus = terminationStatusInterrupted
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
