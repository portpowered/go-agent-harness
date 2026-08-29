package probe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCustomerScenarioRoundTripAndStrictValidation(t *testing.T) {
	scenario := validCustomerScenario()
	data, err := json.Marshal(scenario)
	if err != nil {
		t.Fatalf("marshal scenario: %v", err)
	}
	parsed, err := ParseCustomerScenario(data)
	if err != nil {
		t.Fatalf("ParseCustomerScenario: %v", err)
	}
	if parsed.ID != scenario.ID || parsed.Family != ScenarioFamilyA || len(parsed.Actions) != 2 {
		t.Fatalf("parsed scenario = %+v, want stable identity, family, and ordered actions", parsed)
	}
	if parsed.Actions[0].ID != "create" || parsed.Actions[1].ID != "summarize" {
		t.Fatalf("action order = %q/%q, want create/summarize", parsed.Actions[0].ID, parsed.Actions[1].ID)
	}

	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("decode scenario object: %v", err)
	}
	object["unexpected"] = true
	unknown, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("marshal unknown scenario: %v", err)
	}
	if _, err := ParseCustomerScenario(unknown); err == nil || !errors.Is(err, ErrInvalidCustomerScenario) {
		t.Fatalf("unknown scenario field error = %v, want invalid-scenario error", err)
	}

	duplicate := scenario
	duplicate.Actions = append([]ActionIntent(nil), scenario.Actions...)
	duplicate.Actions[1].ID = duplicate.Actions[0].ID
	if err := duplicate.Validate(); !errors.Is(err, ErrDuplicateActionIntent) {
		t.Fatalf("duplicate action error = %v, want duplicate-action error", err)
	}
	invalidFamily := scenario
	invalidFamily.Family = ScenarioFamily("Z")
	if err := invalidFamily.Validate(); !errors.Is(err, ErrInvalidScenarioFamily) {
		t.Fatalf("invalid family error = %v, want invalid-family error", err)
	}
}

func TestCustomerScenarioRejectsMalformedDeclarations(t *testing.T) {
	tests := []struct {
		name string
		edit func(*CustomerScenario)
		want error
	}{
		{"schema version", func(s *CustomerScenario) { s.SchemaVersion = 0 }, ErrInvalidCustomerScenario},
		{"missing id", func(s *CustomerScenario) { s.ID = "" }, ErrInvalidCustomerScenario},
		{"missing persona", func(s *CustomerScenario) { s.Persona = "" }, ErrInvalidCustomerScenario},
		{"missing goal", func(s *CustomerScenario) { s.Goal = "" }, ErrInvalidCustomerScenario},
		{"missing wording freedom", func(s *CustomerScenario) { s.WordingFreedom = "" }, ErrInvalidCustomerScenario},
		{"missing text seed", func(s *CustomerScenario) { s.TextSeed = "" }, ErrInvalidCustomerScenario},
		{"missing sandbox name", func(s *CustomerScenario) { s.Sandbox.Name = "" }, ErrInvalidCustomerScenario},
		{"unsafe sandbox root", func(s *CustomerScenario) { s.Sandbox.Root = "../outside" }, ErrUnsafeEvidenceArtifactPath},
		{"non-fresh sandbox", func(s *CustomerScenario) { s.Sandbox.Fresh = false }, ErrInvalidCustomerScenario},
		{"bad interruption kind", func(s *CustomerScenario) { s.Interruption.Kind = "other" }, ErrInvalidCustomerScenario},
		{"interruption missing action", func(s *CustomerScenario) {
			s.Interruption = InterruptionTrigger{Kind: InterruptionDuringSpeech, Description: "interrupt"}
		}, ErrInvalidCustomerScenario},
		{"interruption missing description", func(s *CustomerScenario) {
			s.Interruption = InterruptionTrigger{Kind: InterruptionDuringSpeech, ActionID: "create", BeforeTerminal: true}
		}, ErrInvalidCustomerScenario},
		{"interruption after terminal", func(s *CustomerScenario) {
			s.Interruption = InterruptionTrigger{Kind: InterruptionDuringSpeech, ActionID: "create", Description: "interrupt", BeforeTerminal: false}
		}, ErrInvalidCustomerScenario},
		{"interruption unknown action", func(s *CustomerScenario) {
			s.Interruption = InterruptionTrigger{Kind: InterruptionDuringSpeech, ActionID: "unknown", Description: "interrupt", BeforeTerminal: true}
		}, ErrInvalidCustomerScenario},
		{"zero patience", func(s *CustomerScenario) { s.Patience.Reprompt = 0 }, ErrInvalidCustomerScenario},
		{"negative reprompts", func(s *CustomerScenario) { s.Patience.MaxReprompts = -1 }, ErrInvalidCustomerScenario},
		{"unordered patience", func(s *CustomerScenario) { s.Patience.ListenBeforeFollowUp = 4 * time.Second }, ErrInvalidCustomerScenario},
		{"bad termination", func(s *CustomerScenario) { s.Termination = "timeout" }, ErrInvalidCustomerScenario},
		{"zero deadline", func(s *CustomerScenario) { s.Deadline = 0 }, ErrInvalidCustomerScenario},
		{"empty image id", func(s *CustomerScenario) {
			s.ImageEvents = []ScenarioImageEvent{{Path: "fixture.png", SHA256: sha256Hex([]byte("image"))}}
		}, ErrInvalidCustomerScenario},
		{"duplicate image id", func(s *CustomerScenario) {
			image := ScenarioImageEvent{ID: "image", Path: "fixture.png", SHA256: sha256Hex([]byte("image"))}
			s.ImageEvents = []ScenarioImageEvent{image, image}
		}, ErrInvalidCustomerScenario},
		{"unsafe image path", func(s *CustomerScenario) {
			s.ImageEvents = []ScenarioImageEvent{{ID: "image", Path: "../fixture.png", SHA256: sha256Hex([]byte("image"))}}
		}, ErrUnsafeEvidenceArtifactPath},
		{"unhashed image", func(s *CustomerScenario) { s.ImageEvents = []ScenarioImageEvent{{ID: "image", Path: "fixture.png"}} }, ErrUnhashedEvidenceArtifact},
		{"unknown image ordering action", func(s *CustomerScenario) {
			s.ImageEvents = []ScenarioImageEvent{{ID: "image", Path: "fixture.png", SHA256: sha256Hex([]byte("image")), AfterActionID: "unknown"}}
		}, ErrInvalidCustomerScenario},
		{"no actions", func(s *CustomerScenario) { s.Actions = nil }, ErrInvalidCustomerScenario},
		{"empty action id", func(s *CustomerScenario) { s.Actions[0].ID = "" }, ErrInvalidCustomerScenario},
		{"empty action wording", func(s *CustomerScenario) { s.Actions[0].Intent, s.Actions[0].Description = "", "" }, ErrInvalidCustomerScenario},
		{"no allowed disposition", func(s *CustomerScenario) { s.Actions[0].AllowedDispositions = nil }, ErrInvalidCustomerScenario},
		{"bad allowed disposition", func(s *CustomerScenario) { s.Actions[0].AllowedDispositions = []TerminalDisposition{"unknown"} }, ErrInvalidCustomerScenario},
		{"duplicate allowed disposition", func(s *CustomerScenario) {
			s.Actions[0].AllowedDispositions = []TerminalDisposition{DispositionCompleted, DispositionCompleted}
		}, ErrInvalidCustomerScenario},
		{"bad side effect policy", func(s *CustomerScenario) { s.Actions[0].PartialSideEffectPolicy = "unknown" }, ErrInvalidCustomerScenario},
		{"missing side effect rule", func(s *CustomerScenario) { s.Actions[0].SideEffectRule = "" }, ErrInvalidCustomerScenario},
		{"empty action oracle", func(s *CustomerScenario) { s.Actions[0].Oracle = ActionOracle{} }, ErrInvalidCustomerScenario},
		{"unsafe oracle path", func(s *CustomerScenario) { s.Actions[0].Oracle.Checkpoints[0].Path = "../README.md" }, ErrUnsafeEvidenceArtifactPath},
		{"bad oracle type", func(s *CustomerScenario) { s.Actions[0].Oracle.Checkpoints[0].Type = "socket" }, ErrInvalidCustomerScenario},
		{"unhashed oracle file", func(s *CustomerScenario) { s.Actions[0].Oracle.Checkpoints[0].SHA256 = "" }, ErrUnhashedEvidenceArtifact},
		{"absent oracle hash", func(s *CustomerScenario) {
			s.Actions[0].Oracle.Checkpoints[0] = FilesystemExpectation{Path: "project/README.md", Type: FileTypeAbsent, SHA256: sha256Hex([]byte("wrong"))}
		}, ErrInvalidCustomerScenario},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := validCustomerScenario()
			test.edit(&scenario)
			if err := scenario.Validate(); !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestWriteCustomerScenarioUsesTheValidatedVersionedShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := WriteCustomerScenario(path, validCustomerScenario()); err != nil {
		t.Fatalf("WriteCustomerScenario: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read scenario: %v", err)
	}
	parsed, err := ParseCustomerScenario(data)
	if err != nil || parsed.ID != "family-a-project" {
		t.Fatalf("written scenario parse = %+v, %v", parsed, err)
	}
}

func TestCustomerEvidenceBundleFinalizesPairedEvidenceAndVerifiesHashes(t *testing.T) {
	scenario := validCustomerScenario()
	root := filepath.Join(t.TempDir(), "bundle")
	bundle, err := NewCustomerEvidenceBundle(root, scenario, "run-001", "sk-contract-secret")
	if err != nil {
		t.Fatalf("NewCustomerEvidenceBundle: %v", err)
	}
	bundle.Transcripts = PairedTranscripts{
		Customer: []TranscriptEvent{{ID: "customer-1", TurnID: "turn-1", Speaker: TranscriptCustomer, Text: "create the project", At: 10 * time.Millisecond, Final: true}},
		Product:  []TranscriptEvent{{ID: "product-1", TurnID: "turn-1", Speaker: TranscriptProduct, Text: "The project is ready.", At: 20 * time.Millisecond, Final: true}},
	}
	bundle.AudioTurnEvents = []AudioTurnEvent{
		{ID: "audio-1", TurnID: "turn-1", Direction: "input", Kind: "speech", At: 5 * time.Millisecond, Duration: 10 * time.Millisecond, Bytes: 320},
		{ID: "audio-2", TurnID: "turn-1", Direction: "output", Kind: "speech", At: 20 * time.Millisecond, Duration: 15 * time.Millisecond, Bytes: 480},
	}
	bundle.ToolObservations = []ToolObservation{{
		ID: "tool-1", ActionID: "create", TurnID: "turn-1", Tool: "workspace.write", Status: "completed", At: 12 * time.Millisecond, Duration: 3 * time.Millisecond, ResultSeen: true, Summary: "created project files",
	}}
	checkpointHash := sha256Hex([]byte("project contents"))
	bundle.FilesystemCheckpoints = []FilesystemCheckpoint{{
		ID: "checkpoint-1", ActionID: "create", At: 18 * time.Millisecond,
		Entries: []FilesystemCheckpointEntry{
			{Path: "project", Type: FileTypeDirectory, Size: 0, SHA256: sha256Hex([]byte("directory snapshot"))},
			{Path: "project/README.md", Type: FileTypeFile, Size: int64(len("project contents")), SHA256: checkpointHash},
			{Path: "project/missing.txt", Type: FileTypeAbsent},
		},
	}}
	bundle.Process = ProcessFacts{
		PID: 123, ExitCode: 0, ExitClassification: "normal", ChildWaited: true,
		InputClosed: true, OutputClosed: true, StartedAt: 0, EndedAt: 40 * time.Millisecond,
	}
	mechanical := MechanicalVerdict{
		Pass: true, Summary: "all requested actions have truthful side effects",
		ActionResults: []ActionResult{
			{ActionID: "create", TurnID: "turn-1", Confirmed: true, Disposition: DispositionCompleted, EvidenceRefs: []string{"filesystem-checkpoints.jsonl"}, CheckpointIDs: []string{"checkpoint-1"}, ToolObservationIDs: []string{"tool-1"}},
			{ActionID: "summarize", TurnID: "turn-2", Confirmed: true, Disposition: DispositionCompleted, EvidenceRefs: []string{"transcripts/product.jsonl"}},
		},
	}
	bundle.MechanicalVerdict = &mechanical
	bundle.ValidatorInput = &ValidatorInput{
		Scenario:              scenario,
		CustomerTranscript:    bundle.Transcripts.Customer,
		ProductTranscript:     bundle.Transcripts.Product,
		AudioTurnEvents:       bundle.AudioTurnEvents,
		ToolObservations:      bundle.ToolObservations,
		FilesystemCheckpoints: bundle.FilesystemCheckpoints,
		Process:               bundle.Process,
		Mechanical:            mechanical,
		EvidenceRefs:          []string{"scenario.json", "transcripts/customer.jsonl", "transcripts/product.jsonl", "filesystem-checkpoints.jsonl"},
	}
	bundle.ValidatorVerdict = &ValidatorVerdict{
		Verdict:      ValidatorWorked,
		Summary:      "The customer requests were completed in order and the final response matches the observed workspace.",
		EvidenceRefs: []string{"transcripts/customer.jsonl", "transcripts/product.jsonl", "filesystem-checkpoints.jsonl"},
	}
	if err := bundle.AddArtifactBytes("notes/empty.txt", ArtifactKindScenario, nil, false); err != nil {
		t.Fatalf("add explicit empty artifact: %v", err)
	}
	if err := bundle.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	manifest, err := VerifyCustomerEvidenceBundle(root)
	if err != nil {
		t.Fatalf("VerifyCustomerEvidenceBundle: %v", err)
	}
	if !manifest.Finalized || manifest.ValidatorVerdict != ValidatorWorked || !manifest.MechanicalPass {
		t.Fatalf("manifest = %+v, want finalized WORKED/pass manifest", manifest)
	}
	if len(manifest.Artifacts) < 12 {
		t.Fatalf("artifact count = %d, want canonical paired-evidence artifacts", len(manifest.Artifacts))
	}
	for _, artifact := range manifest.Artifacts {
		if filepath.IsAbs(artifact.Path) || strings.HasPrefix(filepath.ToSlash(artifact.Path), "../") {
			t.Errorf("artifact path is not relative: %+v", artifact)
		}
		if artifact.State != ArtifactStateAvailable || artifact.SHA256 == "" {
			t.Errorf("artifact is not hash-verified: %+v", artifact)
		}
	}
	var empty ArtifactEntry
	for _, artifact := range manifest.Artifacts {
		if artifact.Path == "notes/empty.txt" {
			empty = artifact
		}
	}
	if empty.State != ArtifactStateAvailable || empty.Size != 0 || empty.SHA256 != sha256Hex(nil) {
		t.Fatalf("empty artifact = %+v, want available zero-byte artifact with empty hash", empty)
	}

	if err := os.WriteFile(filepath.Join(root, "transcripts", "product.jsonl"), []byte("tampered\n"), 0o600); err != nil {
		t.Fatalf("tamper product transcript: %v", err)
	}
	if _, err := VerifyCustomerEvidenceBundle(root); !errors.Is(err, ErrArtifactHashMismatch) {
		t.Fatalf("tampered bundle error = %v, want hash mismatch", err)
	}
}

func TestCustomerEvidenceValidationRejectsMissingDispositionAndEvidence(t *testing.T) {
	scenario := validCustomerScenario()
	mechanical := MechanicalVerdict{
		Pass: true, Summary: "claimed pass",
		ActionResults: []ActionResult{
			{ActionID: "create", Confirmed: true, EvidenceRefs: []string{"filesystem-checkpoints.jsonl"}},
			{ActionID: "summarize", Disposition: DispositionCompleted, EvidenceRefs: []string{"transcripts/product.jsonl"}},
		},
	}
	if err := mechanical.validate(scenario, "mechanical_verdict"); !errors.Is(err, ErrConfirmationWithoutDisposition) {
		t.Fatalf("confirmation without disposition error = %v, want typed error", err)
	}

	manifest := CustomerEvidenceManifest{
		SchemaVersion: CustomerEvidenceSchemaVersion, RunID: "run", ScenarioID: scenario.ID,
		Finalized: true, FinalizedAt: time.Now().UTC(), MechanicalPass: true, ValidatorVerdict: ValidatorWorked,
		Artifacts: []ArtifactEntry{{Path: "scenario.json", Kind: ArtifactKindScenario, Required: true, State: ArtifactStateAvailable, Size: 1}},
	}
	if err := manifest.Validate(); !errors.Is(err, ErrUnhashedEvidenceArtifact) {
		t.Fatalf("unhashed manifest error = %v, want typed hash error", err)
	}

	bundle, err := NewCustomerEvidenceBundle(filepath.Join(t.TempDir(), "bundle"), scenario, "run-002")
	if err != nil {
		t.Fatalf("NewCustomerEvidenceBundle: %v", err)
	}
	if err := bundle.Finalize(); !errors.Is(err, ErrMissingEvidence) {
		t.Fatalf("incomplete Finalize error = %v, want missing-evidence error", err)
	}
	if _, err := os.Stat(filepath.Join(bundle.root, "manifest.json")); err != nil {
		t.Fatalf("incomplete bundle did not write final manifest: %v", err)
	}
}

func TestCustomerEvidenceRejectsCredentialMaterialAndUnsafePaths(t *testing.T) {
	scenario := validCustomerScenario()
	bundle, err := NewCustomerEvidenceBundle(filepath.Join(t.TempDir(), "bundle"), scenario, "run-003", "contract-secret")
	if err != nil {
		t.Fatalf("NewCustomerEvidenceBundle: %v", err)
	}
	if err := bundle.AddArtifactBytes("safe.txt", ArtifactKindScenario, []byte("contract-secret"), false); !errors.Is(err, ErrCredentialInEvidence) {
		t.Fatalf("credential artifact error = %v, want credential error", err)
	}
	if err := bundle.AddArtifactBytes("../escape.txt", ArtifactKindScenario, []byte("safe"), false); !errors.Is(err, ErrUnsafeEvidenceArtifactPath) {
		t.Fatalf("unsafe artifact error = %v, want unsafe-path error", err)
	}
	if err := bundle.AddArtifactBytes("safe.txt", ArtifactKind(""), []byte("safe"), false); !errors.Is(err, ErrInvalidCustomerEvidence) {
		t.Fatalf("invalid kind error = %v, want invalid-evidence error", err)
	}
}

func TestCustomerEvidenceRecordValidationRejectsMalformedFacts(t *testing.T) {
	transcript := TranscriptEvent{ID: "event", TurnID: "turn", Speaker: TranscriptCustomer, At: time.Second}
	for _, test := range []struct {
		name string
		edit func(*TranscriptEvent)
	}{
		{"missing id", func(e *TranscriptEvent) { e.ID = "" }},
		{"missing turn", func(e *TranscriptEvent) { e.TurnID = "" }},
		{"wrong speaker", func(e *TranscriptEvent) { e.Speaker = TranscriptProduct }},
		{"negative timestamp", func(e *TranscriptEvent) { e.At = -time.Millisecond }},
	} {
		t.Run("transcript/"+test.name, func(t *testing.T) {
			event := transcript
			test.edit(&event)
			if err := (PairedTranscripts{Customer: []TranscriptEvent{event}}).validate(); err == nil {
				t.Fatal("malformed transcript unexpectedly validated")
			}
		})
	}
	if err := (PairedTranscripts{Customer: []TranscriptEvent{transcript, transcript}}).validate(); err == nil {
		t.Fatal("duplicate transcript unexpectedly validated")
	}
	late := transcript
	late.ID, late.At = "late", time.Millisecond
	if err := (PairedTranscripts{Customer: []TranscriptEvent{transcript, late}}).validate(); err == nil {
		t.Fatal("non-monotonic transcript unexpectedly validated")
	}

	audio := AudioTurnEvent{ID: "audio", TurnID: "turn", Direction: "input", Kind: "speech", At: time.Second, Duration: time.Second, Bytes: 2}
	for _, test := range []struct {
		name string
		edit func(*AudioTurnEvent)
	}{
		{"bad direction", func(e *AudioTurnEvent) { e.Direction = "sideways" }},
		{"missing kind", func(e *AudioTurnEvent) { e.Kind = "" }},
		{"negative duration", func(e *AudioTurnEvent) { e.Duration = -time.Millisecond }},
		{"negative bytes", func(e *AudioTurnEvent) { e.Bytes = -1 }},
	} {
		t.Run("audio/"+test.name, func(t *testing.T) {
			event := audio
			test.edit(&event)
			if err := event.validate("audio"); err == nil {
				t.Fatal("malformed audio event unexpectedly validated")
			}
		})
	}

	tool := ToolObservation{ID: "tool", ActionID: "create", TurnID: "turn", Tool: "write", Status: "completed", At: time.Second}
	for _, status := range []string{"", "running", "done"} {
		tool.Status = status
		if err := tool.validate("tool"); err == nil {
			t.Fatalf("tool status %q unexpectedly validated", status)
		}
	}
	tool.Status, tool.ID = "completed", ""
	if err := tool.validate("tool"); err == nil {
		t.Fatal("tool without ID unexpectedly validated")
	}

	checkpoint := FilesystemCheckpoint{ID: "checkpoint", ActionID: "create", At: time.Second, Entries: []FilesystemCheckpointEntry{{Path: "file", Type: FileTypeFile, Size: 1, SHA256: sha256Hex([]byte("x"))}}}
	for _, test := range []struct {
		name string
		edit func(*FilesystemCheckpoint)
	}{
		{"missing id", func(c *FilesystemCheckpoint) { c.ID = "" }},
		{"missing entries", func(c *FilesystemCheckpoint) { c.Entries = nil }},
		{"unsafe path", func(c *FilesystemCheckpoint) { c.Entries[0].Path = "../file" }},
		{"duplicate path", func(c *FilesystemCheckpoint) { c.Entries = append(c.Entries, c.Entries[0]) }},
		{"bad type", func(c *FilesystemCheckpoint) { c.Entries[0].Type = "socket" }},
		{"negative size", func(c *FilesystemCheckpoint) { c.Entries[0].Size = -1 }},
		{"unhashed present", func(c *FilesystemCheckpoint) { c.Entries[0].SHA256 = "" }},
		{"bad absent fact", func(c *FilesystemCheckpoint) {
			c.Entries[0] = FilesystemCheckpointEntry{Path: "file", Type: FileTypeAbsent, Size: 1}
		}},
	} {
		t.Run("checkpoint/"+test.name, func(t *testing.T) {
			value := checkpoint
			value.Entries = append([]FilesystemCheckpointEntry(nil), checkpoint.Entries...)
			test.edit(&value)
			if err := value.validate("checkpoint"); err == nil {
				t.Fatal("malformed checkpoint unexpectedly validated")
			}
		})
	}

	process := ProcessFacts{PID: 1, ExitCode: 0, ExitClassification: "normal", StartedAt: time.Second, EndedAt: 2 * time.Second}
	for _, test := range []struct {
		name string
		edit func(*ProcessFacts)
	}{
		{"bad classification", func(p *ProcessFacts) { p.ExitClassification = "unknown" }},
		{"bad pid", func(p *ProcessFacts) { p.PID = -2 }},
		{"signal without value", func(p *ProcessFacts) { p.SignalSent = true }},
		{"sigint without signal", func(p *ProcessFacts) { p.ExitClassification = "sigint" }},
		{"reversed timestamps", func(p *ProcessFacts) { p.EndedAt = 0 }},
	} {
		t.Run("process/"+test.name, func(t *testing.T) {
			value := process
			test.edit(&value)
			if err := value.validate("process"); err == nil {
				t.Fatal("malformed process facts unexpectedly validated")
			}
		})
	}
}

func TestCustomerEvidenceValidatorVerdictsAndMechanicalFailuresAreStructured(t *testing.T) {
	broken := ValidatorVerdict{Verdict: ValidatorBroken, FirstFailingTurn: "turn-2", Behavior: "The correction was ignored.", Violation: "replacement action was not completed", EvidenceRefs: []string{"mechanical-verdict.json"}, CustomerImpact: "The customer received the wrong result."}
	if err := broken.Validate(); err != nil {
		t.Fatalf("valid BROKEN verdict rejected: %v", err)
	}
	for _, test := range []struct {
		name string
		edit func(*ValidatorVerdict)
	}{
		{"unknown verdict", func(v *ValidatorVerdict) { v.Verdict = "MAYBE" }},
		{"worked without summary", func(v *ValidatorVerdict) { v.Verdict, v.Summary = ValidatorWorked, "" }},
		{"worked without evidence", func(v *ValidatorVerdict) { v.Verdict, v.Summary, v.EvidenceRefs = ValidatorWorked, "worked", nil }},
		{"broken without turn", func(v *ValidatorVerdict) { v.FirstFailingTurn = "" }},
		{"broken without behavior", func(v *ValidatorVerdict) { v.Behavior = "" }},
		{"broken without violation", func(v *ValidatorVerdict) { v.Violation = "" }},
		{"broken without impact", func(v *ValidatorVerdict) { v.CustomerImpact = "" }},
		{"broken without evidence", func(v *ValidatorVerdict) { v.EvidenceRefs = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := broken
			test.edit(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("malformed validator verdict unexpectedly validated")
			}
		})
	}

	scenario := validCustomerScenario()
	mechanical := MechanicalVerdict{Summary: "failure is explicit", ActionResults: []ActionResult{
		{ActionID: "create", Disposition: DispositionFailed, OutcomeReason: "tool failed", EvidenceRefs: []string{"process.json"}},
		{ActionID: "summarize", Disposition: DispositionCancelled, OutcomeReason: "session cancelled", EvidenceRefs: []string{"process.json"}},
	}, Findings: []MechanicalFinding{{Code: "action_failed", Message: "create failed", EvidenceRefs: []string{"process.json"}}}}
	if err := mechanical.validate(scenario, "mechanical"); err != nil {
		t.Fatalf("structured mechanical failure rejected: %v", err)
	}
	for _, test := range []struct {
		name string
		edit func(*MechanicalVerdict)
	}{
		{"unknown action", func(v *MechanicalVerdict) { v.ActionResults[0].ActionID = "unknown" }},
		{"duplicate action", func(v *MechanicalVerdict) { v.ActionResults[1].ActionID = "create" }},
		{"failed without reason", func(v *MechanicalVerdict) { v.ActionResults[0].OutcomeReason = "" }},
		{"finding without code", func(v *MechanicalVerdict) { v.Findings[0].Code = "" }},
		{"finding without evidence", func(v *MechanicalVerdict) { v.Findings[0].EvidenceRefs = nil }},
	} {
		t.Run("mechanical/"+test.name, func(t *testing.T) {
			value := mechanical
			value.ActionResults = append([]ActionResult(nil), mechanical.ActionResults...)
			value.Findings = append([]MechanicalFinding(nil), mechanical.Findings...)
			test.edit(&value)
			if err := value.validate(scenario, "mechanical"); err == nil {
				t.Fatal("malformed mechanical verdict unexpectedly validated")
			}
		})
	}
}

func TestCustomerEvidenceRecordDirectoryAndManifestParsing(t *testing.T) {
	scenario := validCustomerScenario()
	bundle, err := NewCustomerEvidenceBundle(filepath.Join(t.TempDir(), "bundle"), scenario, "run-records")
	if err != nil {
		t.Fatalf("NewCustomerEvidenceBundle: %v", err)
	}
	source := filepath.Join(t.TempDir(), "record-dir")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatalf("create record directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "session.json"), []byte(`{"event":"done"}`), 0o600); err != nil {
		t.Fatalf("write record artifact: %v", err)
	}
	if err := bundle.AddProductRecordDir(source); err != nil {
		t.Fatalf("AddProductRecordDir: %v", err)
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].Path != "product-record-dir/nested/session.json" {
		t.Fatalf("record artifacts = %+v, want one relative copied artifact", bundle.Artifacts)
	}
	if _, err := os.Stat(filepath.Join(bundle.root, "product-record-dir", "nested", "session.json")); err != nil {
		t.Fatalf("copied record artifact missing: %v", err)
	}
	if err := bundle.WriteArtifact("notes/alias.txt", ArtifactKindScenario, []byte("alias"), false); err != nil {
		t.Fatalf("WriteArtifact alias: %v", err)
	}
	if err := bundle.RegisterArtifact("missing.txt", ArtifactKindScenario, false); err != nil {
		t.Fatalf("RegisterArtifact missing: %v", err)
	}
	if got := findArtifact(bundle.Artifacts, "missing.txt"); got.State != ArtifactStateMissing || got.Size != -1 {
		t.Fatalf("missing artifact = %+v, want explicit missing state", got)
	}
	if err := os.Mkdir(filepath.Join(bundle.root, "directory-artifact"), 0o700); err != nil {
		t.Fatalf("create directory artifact: %v", err)
	}
	if err := bundle.RegisterArtifact("directory-artifact", ArtifactKindScenario, false); err != nil {
		t.Fatalf("RegisterArtifact directory: %v", err)
	}
	if got := findArtifact(bundle.Artifacts, "directory-artifact"); got.State != ArtifactStateFailed {
		t.Fatalf("directory artifact = %+v, want failed non-file state", got)
	}

	manifest := minimalCustomerEvidenceManifest(scenario.ID)
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal minimal manifest: %v", err)
	}
	parsed, err := ParseCustomerEvidenceManifest(data)
	if err != nil || parsed.RunID != manifest.RunID {
		t.Fatalf("ParseCustomerEvidenceManifest = %+v, %v", parsed, err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("decode manifest object: %v", err)
	}
	object["private_prompt"] = "must not be accepted"
	unknown, err := json.Marshal(object)
	if err != nil {
		t.Fatalf("marshal unknown manifest: %v", err)
	}
	if _, err := ParseCustomerEvidenceManifest(unknown); !errors.Is(err, ErrInvalidCustomerEvidence) {
		t.Fatalf("unknown manifest field error = %v, want invalid-evidence error", err)
	}
	manifest.ValidationError = "incomplete evidence"
	if err := manifest.Validate(); !errors.Is(err, ErrInvalidCustomerEvidence) {
		t.Fatalf("manifest validation-error state = %v, want invalid-evidence error", err)
	}
}

func findArtifact(entries []ArtifactEntry, path string) ArtifactEntry {
	for _, entry := range entries {
		if entry.Path == path {
			return entry
		}
	}
	return ArtifactEntry{}
}

func minimalCustomerEvidenceManifest(scenarioID string) CustomerEvidenceManifest {
	kinds := []ArtifactKind{
		ArtifactKindScenario,
		ArtifactKindCustomerTranscript,
		ArtifactKindProductTranscript,
		ArtifactKindAudioTurnEvents,
		ArtifactKindProductRecordDir,
		ArtifactKindToolObservations,
		ArtifactKindFilesystemCheckpoints,
		ArtifactKindProcessFacts,
		ArtifactKindMechanicalVerdict,
		ArtifactKindValidatorInput,
		ArtifactKindValidatorVerdict,
	}
	artifacts := make([]ArtifactEntry, 0, len(kinds)+1)
	for index, kind := range kinds {
		artifacts = append(artifacts, ArtifactEntry{
			Path: filepath.ToSlash(filepath.Join("artifacts", fmt.Sprintf("%02d.json", index))),
			Kind: kind, Required: true, State: ArtifactStateAvailable, Size: 0, SHA256: sha256Hex(nil),
		})
	}
	artifacts = append(artifacts, ArtifactEntry{Path: "optional.txt", Kind: ArtifactKindScenario, State: ArtifactStateMissing, Size: -1, Reason: "not applicable"})
	return CustomerEvidenceManifest{
		SchemaVersion: CustomerEvidenceSchemaVersion,
		RunID:         "minimal-run", ScenarioID: scenarioID, Finalized: true, FinalizedAt: time.Now().UTC(),
		Artifacts: artifacts, ValidatorVerdict: ValidatorBroken,
	}
}

func validCustomerScenario() CustomerScenario {
	return CustomerScenario{
		SchemaVersion:  CustomerScenarioSchemaVersion,
		ID:             "family-a-project",
		Name:           "Iterative project build",
		Family:         ScenarioFamilyA,
		Persona:        "A patient but exacting project collaborator",
		Goal:           "Build and summarize a small project in the sandbox",
		WordingFreedom: "Use natural conversational wording while preserving the requested paths and facts.",
		TextSeed:       "The project starts empty and should be built incrementally.",
		ImageEvents:    []ScenarioImageEvent{},
		Actions: []ActionIntent{
			{
				ID: "create", Intent: "Create the project and its README.",
				AllowedDispositions:     []TerminalDisposition{DispositionCompleted, DispositionFailed, DispositionCancelled},
				PartialSideEffectPolicy: PartialSideEffectsPreserve,
				SideEffectRule:          "Preserve any created files and report their exact state if interrupted.",
				Oracle:                  ActionOracle{Description: "Project directory and README exist with the expected content.", RequireConfirmation: true, Checkpoints: []FilesystemExpectation{{Path: "project/README.md", Type: FileTypeFile, SHA256: sha256Hex([]byte("project contents"))}}},
			},
			{
				ID: "summarize", Description: "State the actual final project contents.",
				AllowedDispositions:     []TerminalDisposition{DispositionCompleted, DispositionFailed, DispositionCancelled},
				PartialSideEffectPolicy: PartialSideEffectsForbid,
				SideEffectRule:          "Make no filesystem change; preserve the prior checkpoint for comparison.",
				Oracle:                  ActionOracle{Description: "Spoken summary agrees with the final checkpoint.", RequireConfirmation: true},
			},
		},
		Sandbox:      SandboxSpec{Name: "fresh-project-sandbox", Root: ".", Fresh: true},
		Interruption: InterruptionTrigger{Kind: InterruptionNone},
		Patience: PatienceThresholds{
			ListenBeforeFollowUp: 500 * time.Millisecond,
			ResponseStart:        time.Second,
			InProgressWork:       2 * time.Second,
			Reprompt:             3 * time.Second,
			AbsoluteDeadAir:      10 * time.Second,
			MaxReprompts:         2,
		},
		Termination: TerminationNatural,
		Deadline:    30 * time.Second,
	}
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
