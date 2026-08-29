package probe

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFamilyCScenarioDeclaresTextFirstImageSecondIteration(t *testing.T) {
	scenario := NewFamilyCScenario()
	if err := scenario.Validate(); err != nil {
		t.Fatalf("NewFamilyCScenario validation: %v", err)
	}
	if scenario.Family != ScenarioFamilyC || len(scenario.ImageEvents) != 1 || scenario.ImageEvents[0].AfterActionID != FamilyCTextActionID {
		t.Fatalf("Family C image declaration = %+v, want one image after %q", scenario.ImageEvents, FamilyCTextActionID)
	}
	if len(scenario.Actions) != 3 || scenario.Actions[2].ID != FamilyCImageActionID {
		t.Fatalf("Family C actions = %+v, want two text actions followed by image action", scenario.Actions)
	}
	script := FamilyCSpokenScript()
	if len(script) != len(scenario.Actions) {
		t.Fatalf("Family C script length = %d, want %d", len(script), len(scenario.Actions))
	}
	for index, turn := range script {
		if turn.ActionID != scenario.Actions[index].ID || strings.TrimSpace(turn.Text) == "" {
			t.Fatalf("Family C script turn %d = %+v, want natural wording for %q", index, turn, scenario.Actions[index].ID)
		}
	}
	thirdTurn := strings.ToLower(script[2].Text)
	for _, visualFact := range []string{"indigo", "#4f46e5", "pixel"} {
		if strings.Contains(thirdTurn, visualFact) {
			t.Fatalf("image meaning %q was encoded in the customer speech %q", visualFact, script[2].Text)
		}
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "customer-simulation", "family-c.scenario.json"))
	if err != nil {
		t.Fatalf("read Family C fixture: %v", err)
	}
	parsed, err := ParseCustomerScenario(data)
	if err != nil {
		t.Fatalf("parse Family C fixture: %v", err)
	}
	if parsed.ID != scenario.ID || parsed.ImageEvents[0].SHA256 != FamilyCImageFixtureSHA256 {
		t.Fatalf("parsed Family C fixture = %+v, want generated scenario identity and fixture digest", parsed)
	}
}

func TestMixedModalEvidenceRoundTripAndShapeValidation(t *testing.T) {
	scenario := NewFamilyCScenario()
	evidence := familyCPositiveMixedModal(scenario)
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("marshal mixed-modal evidence: %v", err)
	}
	var parsed MixedModalEvidence
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal mixed-modal evidence: %v", err)
	}
	if err := parsed.Validate(scenario); err != nil {
		t.Fatalf("validate mixed-modal evidence: %v", err)
	}

	bad := evidence
	bad.ExpectedSHA256 = strings.Repeat("0", 64)
	if err := bad.Validate(scenario); err == nil || !errors.Is(err, ErrInvalidCustomerEvidence) {
		t.Fatalf("wrong expected image hash error = %v, want invalid evidence", err)
	}
	bad = evidence
	bad.Delivery = MixedModalDelivery("scheduled_turn")
	if err := bad.Validate(scenario); err == nil || !errors.Is(err, ErrInvalidCustomerEvidence) {
		t.Fatalf("invalid delivery error = %v, want invalid evidence", err)
	}
	bad = evidence
	bad.ImageObserved = false
	bad.ObservedSHA256 = ""
	bad.Supported = true
	if err := bad.Validate(scenario); err == nil || !errors.Is(err, ErrMissingEvidence) {
		t.Fatalf("supported missing image error = %v, want missing evidence", err)
	}
}

func TestFamilyCMixedModalOracleAcceptsOrderedFixtureGrounding(t *testing.T) {
	scenario := NewFamilyCScenario()
	results, checkpoints, tools, product := familyCPositiveEvidence(scenario)
	verdict, err := EvaluateCustomerSimulationMixedModal(scenario, results, checkpoints, tools, product, familyCPositiveMixedModal(scenario))
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulationMixedModal: %v", err)
	}
	if !verdict.Pass || len(verdict.Findings) != 0 {
		t.Fatalf("Family C mechanical verdict = %+v, want pass without findings", verdict)
	}
}

func TestFamilyCMixedModalOracleRejectsWrongImage(t *testing.T) {
	scenario := NewFamilyCScenario()
	results, checkpoints, tools, product := familyCPositiveEvidence(scenario)
	evidence := familyCPositiveMixedModal(scenario)
	evidence.Delivery = MixedModalDeliveryWrongImage
	evidence.ObservedSHA256 = sha256HexBytes([]byte("different valid image bytes"))
	verdict, err := EvaluateCustomerSimulationMixedModal(scenario, results, checkpoints, tools, product, evidence)
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulationMixedModal wrong-image control: %v", err)
	}
	if verdict.Pass || !familyCMechanicalFindingContains(verdict, "wrong_image_payload") || !familyCMechanicalFindingContains(verdict, "image_not_mid_session") {
		t.Fatalf("wrong-image verdict = %+v, want fixture and boundary findings", verdict)
	}
}

func TestFamilyCMixedModalOracleRejectsPreloadedImage(t *testing.T) {
	scenario := NewFamilyCScenario()
	results, checkpoints, tools, product := familyCPositiveEvidence(scenario)
	evidence := familyCPositiveMixedModal(scenario)
	evidence.Delivery = MixedModalDeliveryPreloaded
	evidence.ImageSentAt = 100 * time.Millisecond
	verdict, err := EvaluateCustomerSimulationMixedModal(scenario, results, checkpoints, tools, product, evidence)
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulationMixedModal preloaded control: %v", err)
	}
	if verdict.Pass || !familyCMechanicalFindingContains(verdict, "image_preloaded_before_prior_turn") || !familyCMechanicalFindingContains(verdict, "image_before_prior_turn") {
		t.Fatalf("preloaded-image verdict = %+v, want startup/order findings", verdict)
	}
}

func TestFamilyCMixedModalOraclePreservesUnsupportedProductGap(t *testing.T) {
	scenario := NewFamilyCScenario()
	results, checkpoints, tools, product := familyCPositiveEvidence(scenario)
	evidence := familyCPositiveMixedModal(scenario)
	evidence.Delivery = MixedModalDeliveryUnsupported
	evidence.Supported = false
	evidence.ImageObserved = false
	evidence.ImageSentAt = 0
	evidence.ObservedSHA256 = ""
	evidence.ProductGapCode = FamilyCMidSessionImageGapCode
	evidence.ProductGap = FamilyCMidSessionImageGap
	results[2].Confirmed = false
	results[2].Disposition = DispositionFailed
	results[2].OutcomeReason = FamilyCMidSessionImageGap
	checkpoints[2] = FilesystemCheckpoint{
		ID: "checkpoint-image", ActionID: FamilyCImageActionID, At: 1000 * time.Millisecond,
		Entries: []FilesystemCheckpointEntry{
			{Path: "mixed-modal/brief.md", Type: FileTypeFile, SHA256: sha256HexBytes([]byte(FamilyCTextBrief)), Size: int64(len(FamilyCTextBrief))},
			{Path: "mixed-modal/image-fact.txt", Type: FileTypeAbsent},
		},
	}
	product[2] = TranscriptEvent{
		ID: "product-turn-3", TurnID: "turn-3", Speaker: TranscriptProduct,
		Text: "I cannot apply the image-grounded request because the public mid-session image boundary is unsupported.",
		At:   1100 * time.Millisecond, Final: true,
	}
	verdict, err := EvaluateCustomerSimulationMixedModal(scenario, results, checkpoints, tools, product, evidence)
	if err != nil {
		t.Fatalf("EvaluateCustomerSimulationMixedModal unsupported path: %v", err)
	}
	if verdict.Pass || !familyCMechanicalFindingContains(verdict, "unsupported_mid_session_image") {
		t.Fatalf("unsupported image verdict = %+v, want precise BROKEN gap finding", verdict)
	}
	finding := familyCMechanicalFinding(verdict, "unsupported_mid_session_image")
	if !strings.Contains(finding.Message, FamilyCMidSessionImageGapCode) || !strings.Contains(finding.Message, "--audio-in -") {
		t.Fatalf("unsupported image finding = %+v, want exact public-boundary gap", finding)
	}
}

func TestFamilyCEvidenceBundleWritesMixedModalBoundaryArtifact(t *testing.T) {
	scenario := NewFamilyCScenario()
	results, checkpoints, tools, product := familyCPositiveEvidence(scenario)
	mixed := familyCPositiveMixedModal(scenario)
	mechanical, err := EvaluateCustomerSimulationMixedModal(scenario, results, checkpoints, tools, product, mixed)
	if err != nil {
		t.Fatalf("evaluate Family C bundle evidence: %v", err)
	}
	root := filepath.Join(t.TempDir(), "bundle")
	bundle, err := NewCustomerEvidenceBundle(root, scenario, "family-c-run")
	if err != nil {
		t.Fatalf("NewCustomerEvidenceBundle: %v", err)
	}
	bundle.Transcripts = PairedTranscripts{
		Customer: []TranscriptEvent{
			{ID: "customer-turn-1", TurnID: "turn-1", Speaker: TranscriptCustomer, Text: FamilyCSpokenScript()[0].Text, At: 100 * time.Millisecond, Final: true},
			{ID: "customer-turn-2", TurnID: "turn-2", Speaker: TranscriptCustomer, Text: FamilyCSpokenScript()[1].Text, At: 400 * time.Millisecond, Final: true},
			{ID: "customer-turn-3", TurnID: "turn-3", Speaker: TranscriptCustomer, Text: FamilyCSpokenScript()[2].Text, At: 750 * time.Millisecond, Final: true},
		},
		Product: product,
	}
	bundle.AudioTurnEvents = []AudioTurnEvent{
		{ID: "audio-in-1", TurnID: "turn-1", Direction: "input", Kind: "speech", At: 100 * time.Millisecond, Duration: 20 * time.Millisecond, Bytes: 960},
		{ID: "audio-in-2", TurnID: "turn-2", Direction: "input", Kind: "speech", At: 400 * time.Millisecond, Duration: 20 * time.Millisecond, Bytes: 960},
		{ID: "audio-in-3", TurnID: "turn-3", Direction: "input", Kind: "speech", At: 750 * time.Millisecond, Duration: 20 * time.Millisecond, Bytes: 960},
		{ID: "audio-out-1", TurnID: "turn-1", Direction: "output", Kind: "speech", At: 300 * time.Millisecond, Duration: 20 * time.Millisecond, Bytes: 960},
	}
	bundle.ToolObservations = tools
	bundle.FilesystemCheckpoints = checkpoints
	bundle.Process = ProcessFacts{PID: 123, ExitCode: 0, ExitClassification: "normal", ChildWaited: true, InputClosed: true, OutputClosed: true, StartedAt: 0, EndedAt: 2 * time.Second}
	bundle.MechanicalVerdict = &mechanical
	bundle.MixedModal = &mixed
	bundle.ValidatorInput = &ValidatorInput{
		Scenario: scenario, CustomerTranscript: bundle.Transcripts.Customer, ProductTranscript: product,
		AudioTurnEvents: bundle.AudioTurnEvents, ToolObservations: tools, FilesystemCheckpoints: checkpoints,
		Process: bundle.Process, Mechanical: mechanical, MixedModal: &mixed,
		EvidenceRefs: []string{"scenario.json", "transcripts/customer.jsonl", "transcripts/product.jsonl", "events/mixed-modal.json"},
	}
	bundle.ValidatorVerdict = &ValidatorVerdict{
		Verdict: ValidatorWorked, Summary: "The text-first conversation delivered the declared image and recorded its observed fact.",
		EvidenceRefs: []string{"transcripts/customer.jsonl", "transcripts/product.jsonl", "events/mixed-modal.json"},
	}
	productRecordDir := filepath.Join(t.TempDir(), "record-dir")
	if err := os.MkdirAll(productRecordDir, 0o700); err != nil {
		t.Fatalf("create product record directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(productRecordDir, "session.jsonl"), []byte("recorded\n"), 0o600); err != nil {
		t.Fatalf("write product record: %v", err)
	}
	if err := bundle.AddProductRecordDir(productRecordDir); err != nil {
		t.Fatalf("AddProductRecordDir: %v", err)
	}
	if err := bundle.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if _, err := VerifyCustomerEvidenceBundle(root); err != nil {
		t.Fatalf("VerifyCustomerEvidenceBundle: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "events", "mixed-modal.json"))
	if err != nil {
		t.Fatalf("read mixed-modal artifact: %v", err)
	}
	if !strings.Contains(string(data), FamilyCImageFixtureSHA256) {
		t.Fatalf("mixed-modal artifact = %s, want fixture digest", data)
	}
}

func familyCPositiveEvidence(scenario CustomerScenario) ([]ActionResult, []FilesystemCheckpoint, []ToolObservation, []TranscriptEvent) {
	return []ActionResult{
			{ActionID: FamilyCCreateActionID, TurnID: "turn-1", Confirmed: true, ConfirmedAt: 350 * time.Millisecond, Disposition: DispositionCompleted, EvidenceRefs: defaultActionEvidenceRefs(), CheckpointIDs: []string{"checkpoint-create"}, ToolObservationIDs: []string{"tool-create"}},
			{ActionID: FamilyCTextActionID, TurnID: "turn-2", Confirmed: true, ConfirmedAt: 650 * time.Millisecond, Disposition: DispositionCompleted, EvidenceRefs: defaultActionEvidenceRefs(), CheckpointIDs: []string{"checkpoint-text"}, ToolObservationIDs: []string{"tool-text"}},
			{ActionID: FamilyCImageActionID, TurnID: "turn-3", Confirmed: true, ConfirmedAt: 1100 * time.Millisecond, Disposition: DispositionCompleted, EvidenceRefs: defaultActionEvidenceRefs(), CheckpointIDs: []string{"checkpoint-image"}, ToolObservationIDs: []string{"tool-image"}},
		}, []FilesystemCheckpoint{
			familyCCheckpoint("checkpoint-create", FamilyCCreateActionID, 300*time.Millisecond, scenario.Actions[0].Oracle.Checkpoints),
			familyCCheckpoint("checkpoint-text", FamilyCTextActionID, 600*time.Millisecond, scenario.Actions[1].Oracle.Checkpoints),
			familyCCheckpoint("checkpoint-image", FamilyCImageActionID, time.Second, scenario.Actions[2].Oracle.Checkpoints),
		}, []ToolObservation{
			{ID: "tool-create", ActionID: FamilyCCreateActionID, TurnID: "turn-1", Tool: "write_file", Status: "completed", At: 200 * time.Millisecond, Duration: 50 * time.Millisecond, ResultSeen: true, Summary: "File written: mixed-modal/brief.md"},
			{ID: "tool-text", ActionID: FamilyCTextActionID, TurnID: "turn-2", Tool: "edit_file", Status: "completed", At: 500 * time.Millisecond, Duration: 50 * time.Millisecond, ResultSeen: true, Summary: "File edited: mixed-modal/brief.md"},
			{ID: "tool-image", ActionID: FamilyCImageActionID, TurnID: "turn-3", Tool: "write_file", Status: "completed", At: 800 * time.Millisecond, Duration: 50 * time.Millisecond, ResultSeen: true, Summary: "File written: mixed-modal/image-fact.txt"},
		}, []TranscriptEvent{
			{ID: "product-turn-1", TurnID: "turn-1", Speaker: TranscriptProduct, Text: "Created mixed-modal/brief.md.", At: 350 * time.Millisecond, Final: true},
			{ID: "product-turn-2", TurnID: "turn-2", Speaker: TranscriptProduct, Text: "Updated mixed-modal/brief.md with audience: engineers and a concise tone.", At: 650 * time.Millisecond, Final: true},
			{ID: "product-turn-3", TurnID: "turn-3", Speaker: TranscriptProduct, Text: "Recorded indigo (#4f46e5) in mixed-modal/image-fact.txt.", At: 1100 * time.Millisecond, Final: true},
		}
}

func familyCPositiveMixedModal(scenario CustomerScenario) MixedModalEvidence {
	return MixedModalEvidence{
		ImageEventID: FamilyCImageEventID, PriorActionID: FamilyCTextActionID, PriorTurnID: "turn-2", ImageTurnID: "turn-3",
		PriorActionCompletedAt: 650 * time.Millisecond, CustomerTurnStartedAt: 750 * time.Millisecond, ImageSentAt: 800 * time.Millisecond,
		ImageObserved: true, ExpectedSHA256: scenario.ImageEvents[0].SHA256, ObservedSHA256: scenario.ImageEvents[0].SHA256,
		Delivery: MixedModalDeliveryMidSession, Supported: true, EvidenceRefs: mixedModalEvidenceRefs(),
	}
}

func familyCCheckpoint(id, actionID string, at time.Duration, expectations []FilesystemExpectation) FilesystemCheckpoint {
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

func familyCMechanicalFindingContains(verdict MechanicalVerdict, code string) bool {
	for _, finding := range verdict.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func familyCMechanicalFinding(verdict MechanicalVerdict, code string) MechanicalFinding {
	for _, finding := range verdict.Findings {
		if finding.Code == code {
			return finding
		}
	}
	return MechanicalFinding{}
}
