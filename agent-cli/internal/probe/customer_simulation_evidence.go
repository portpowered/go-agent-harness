package probe

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// familyEvidenceSlot names the family-specific evidence a Family C, D, or E
// scenario must carry.
type familyEvidenceSlot struct {
	field string
	noun  string
	kind  ArtifactKind
}

func familyEvidenceSlotFor(family ScenarioFamily) (familyEvidenceSlot, bool) {
	if family == ScenarioFamilyC {
		return familyEvidenceSlot{field: "mixed_modal", noun: "mixed-modal evidence", kind: ArtifactKindMixedModalEvidence}, true
	}
	if family == ScenarioFamilyD {
		return familyEvidenceSlot{field: "termination", noun: "termination evidence", kind: ArtifactKindTerminationEvidence}, true
	}
	if family == ScenarioFamilyE {
		return familyEvidenceSlot{field: "patience", noun: "patience evidence", kind: ArtifactKindPatienceEvidence}, true
	}
	return familyEvidenceSlot{}, false
}

// familyEvidence is the family-specific evidence value selected for a
// scenario family, if present.
type familyEvidence struct {
	present  bool
	validate func(CustomerScenario) error
	refs     []string
}

func selectFamilyEvidence(family ScenarioFamily, mixed *MixedModalEvidence, termination *TerminationEvidence, patience *PatienceEvidence) familyEvidence {
	if family == ScenarioFamilyC && mixed != nil {
		return familyEvidence{present: true, validate: mixed.Validate, refs: mixed.EvidenceRefs}
	}
	if family == ScenarioFamilyD && termination != nil {
		return familyEvidence{present: true, validate: termination.Validate, refs: termination.EvidenceRefs}
	}
	if family == ScenarioFamilyE && patience != nil {
		return familyEvidence{present: true, validate: patience.Validate, refs: patience.EvidenceRefs}
	}
	return familyEvidence{}
}

type ValidatorVerdictKind string

const (
	ValidatorWorked ValidatorVerdictKind = "WORKED"
	ValidatorBroken ValidatorVerdictKind = "BROKEN"
	VerdictWorked                        = ValidatorWorked
	VerdictBroken                        = ValidatorBroken
)

type ValidatorVerdict struct {
	Verdict          ValidatorVerdictKind `json:"verdict"`
	Summary          string               `json:"summary,omitempty"`
	FirstFailingTurn string               `json:"first_failing_turn,omitempty"`
	Behavior         string               `json:"behavior,omitempty"`
	Violation        string               `json:"violation,omitempty"`
	EvidenceRefs     []string             `json:"evidence_refs"`
	CustomerImpact   string               `json:"customer_impact,omitempty"`
}

func (v ValidatorVerdict) Validate() error {
	switch v.Verdict {
	case ValidatorWorked:
		if strings.TrimSpace(v.Summary) == "" {
			return contractFieldError(ErrInvalidValidatorVerdict, "validator_verdict.summary", "WORKED requires a summary")
		}
		if len(v.EvidenceRefs) == 0 {
			return contractFieldError(ErrMissingEvidence, "validator_verdict.evidence_refs", "WORKED requires evidence references")
		}
	case ValidatorBroken:
		for _, field := range []struct{ name, value string }{{"first_failing_turn", v.FirstFailingTurn}, {"behavior", v.Behavior}, {"violation", v.Violation}, {"customer_impact", v.CustomerImpact}} {
			if strings.TrimSpace(field.value) == "" {
				return contractFieldError(ErrInvalidValidatorVerdict, "validator_verdict."+field.name, "BROKEN requires this diagnosis")
			}
		}
		if len(v.EvidenceRefs) == 0 {
			return contractFieldError(ErrMissingEvidence, "validator_verdict.evidence_refs", "BROKEN requires evidence references")
		}
	default:
		return contractFieldError(ErrInvalidValidatorVerdict, "validator_verdict.verdict", fmt.Sprintf("%q must be WORKED or BROKEN", v.Verdict))
	}
	return nil
}

type ArtifactKind string

const (
	ArtifactKindScenario              ArtifactKind = "scenario"
	ArtifactKindCustomerTranscript    ArtifactKind = "customer_transcript"
	ArtifactKindProductTranscript     ArtifactKind = "product_transcript"
	ArtifactKindAudioTurnEvents       ArtifactKind = "audio_turn_events"
	ArtifactKindProductRecordDir      ArtifactKind = "product_record_dir"
	ArtifactKindToolObservations      ArtifactKind = "tool_observations"
	ArtifactKindFilesystemCheckpoints ArtifactKind = "filesystem_checkpoints"
	ArtifactKindProcessFacts          ArtifactKind = "process_facts"
	ArtifactKindMechanicalVerdict     ArtifactKind = "mechanical_verdict"
	ArtifactKindValidatorInput        ArtifactKind = "validator_input"
	ArtifactKindValidatorVerdict      ArtifactKind = "validator_verdict"
	ArtifactKindCorrectionEvidence    ArtifactKind = "correction_evidence"
	ArtifactKindMixedModalEvidence    ArtifactKind = "mixed_modal_evidence"
	ArtifactKindTerminationEvidence   ArtifactKind = "termination_evidence"
	ArtifactKindPatienceEvidence      ArtifactKind = "patience_evidence"
)

func (k ArtifactKind) valid() bool {
	switch k {
	case ArtifactKindScenario, ArtifactKindCustomerTranscript, ArtifactKindProductTranscript, ArtifactKindAudioTurnEvents, ArtifactKindProductRecordDir, ArtifactKindToolObservations, ArtifactKindFilesystemCheckpoints, ArtifactKindProcessFacts, ArtifactKindMechanicalVerdict, ArtifactKindValidatorInput, ArtifactKindValidatorVerdict, ArtifactKindCorrectionEvidence, ArtifactKindMixedModalEvidence, ArtifactKindTerminationEvidence, ArtifactKindPatienceEvidence:
		return true
	}
	return false
}

type ArtifactState string

const (
	ArtifactStateAvailable ArtifactState = "available"
	ArtifactStateMissing   ArtifactState = "missing"
	ArtifactStateFailed    ArtifactState = "failed"
	ArtifactPresent                      = ArtifactStateAvailable
	ArtifactMissing                      = ArtifactStateMissing
	ArtifactFailed                       = ArtifactStateFailed
)

type ArtifactEntry struct {
	Path     string        `json:"path"`
	Kind     ArtifactKind  `json:"kind"`
	Required bool          `json:"required"`
	State    ArtifactState `json:"state"`
	Size     int64         `json:"size"`
	SHA256   string        `json:"sha256,omitempty"`
	Reason   string        `json:"reason,omitempty"`
}

func (a ArtifactEntry) validate(field string) error {
	if err := validateArtifactPath(a.Path); err != nil {
		return err
	}
	if !a.Kind.valid() {
		return contractFieldError(ErrInvalidCustomerEvidence, field+".kind", fmt.Sprintf("%q is invalid", a.Kind))
	}
	switch a.State {
	case ArtifactStateAvailable:
		if a.Size < 0 {
			return contractFieldError(ErrInvalidCustomerEvidence, field+".size", "must not be negative")
		}
		if err := validateSHA256(field+".sha256", a.SHA256, true); err != nil {
			return errors.Join(err, ErrUnhashedEvidenceArtifact)
		}
		if a.Reason != "" {
			return contractFieldError(ErrInvalidCustomerEvidence, field+".reason", "available evidence cannot have a reason")
		}
	case ArtifactStateMissing, ArtifactStateFailed:
		if a.SHA256 != "" {
			return contractFieldError(ErrUnhashedEvidenceArtifact, field+".sha256", "unavailable evidence cannot have a hash")
		}
		if strings.TrimSpace(a.Reason) == "" {
			return contractFieldError(ErrInvalidCustomerEvidence, field+".reason", "unavailable evidence must have a reason")
		}
	default:
		return contractFieldError(ErrInvalidCustomerEvidence, field+".state", fmt.Sprintf("%q is invalid", a.State))
	}
	return nil
}

type CustomerEvidenceManifest struct {
	SchemaVersion    int                  `json:"schema_version"`
	RunID            string               `json:"run_id"`
	ScenarioID       string               `json:"scenario_id"`
	Finalized        bool                 `json:"finalized"`
	FinalizedAt      time.Time            `json:"finalized_at"`
	Artifacts        []ArtifactEntry      `json:"artifacts"`
	MechanicalPass   bool                 `json:"mechanical_pass"`
	ValidatorVerdict ValidatorVerdictKind `json:"validator_verdict"`
	ValidationError  string               `json:"validation_error,omitempty"`
}

func (m CustomerEvidenceManifest) Validate() error {
	if m.SchemaVersion != CustomerEvidenceSchemaVersion {
		return contractFieldError(ErrInvalidCustomerEvidence, "schema_version", "must be 1")
	}
	if strings.TrimSpace(m.RunID) == "" || strings.TrimSpace(m.ScenarioID) == "" {
		return contractFieldError(ErrInvalidCustomerEvidence, "manifest", "run_id and scenario_id must not be empty")
	}
	if !m.Finalized || m.FinalizedAt.IsZero() {
		return contractFieldError(ErrInvalidCustomerEvidence, "finalized", "finalized bundles need finalized=true and finalized_at")
	}
	if m.ValidationError != "" {
		return contractFieldError(ErrInvalidCustomerEvidence, "validation_error", "manifest records invalid evidence")
	}
	if err := validateArtifactEntries(m.Artifacts, true); err != nil {
		return err
	}
	if err := validateRequiredArtifactKinds(m.Artifacts); err != nil {
		return err
	}
	if m.ValidatorVerdict != ValidatorWorked && m.ValidatorVerdict != ValidatorBroken {
		return contractFieldError(ErrInvalidValidatorVerdict, "validator_verdict", "must be WORKED or BROKEN")
	}
	if m.ValidatorVerdict == ValidatorWorked && !m.MechanicalPass {
		return contractFieldError(ErrValidatorMechanicalDisagreement, "validator_verdict", "WORKED requires mechanical_pass")
	}
	return nil
}

type CustomerEvidenceBundle struct {
	SchemaVersion         int                    `json:"schema_version"`
	RunID                 string                 `json:"run_id"`
	Scenario              CustomerScenario       `json:"scenario"`
	Transcripts           PairedTranscripts      `json:"transcripts"`
	AudioTurnEvents       []AudioTurnEvent       `json:"audio_turn_events"`
	ToolObservations      []ToolObservation      `json:"tool_observations"`
	FilesystemCheckpoints []FilesystemCheckpoint `json:"filesystem_checkpoints"`
	Process               ProcessFacts           `json:"process"`
	MechanicalVerdict     *MechanicalVerdict     `json:"mechanical_verdict"`
	ValidatorInput        *ValidatorInput        `json:"validator_input"`
	ValidatorVerdict      *ValidatorVerdict      `json:"validator_verdict"`
	MixedModal            *MixedModalEvidence    `json:"mixed_modal,omitempty"`
	Termination           *TerminationEvidence   `json:"termination,omitempty"`
	Patience              *PatienceEvidence      `json:"patience,omitempty"`
	Artifacts             []ArtifactEntry        `json:"artifacts"`
	Finalized             bool                   `json:"finalized"`
	FinalizedAt           time.Time              `json:"finalized_at"`

	root               string
	secrets            []string
	productRecordAdded bool
}

func NewCustomerEvidenceBundle(root string, scenario CustomerScenario, runID string, secrets ...string) (*CustomerEvidenceBundle, error) {
	if err := scenario.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(runID) == "" {
		return nil, contractFieldError(ErrInvalidCustomerEvidence, "run_id", "must not be empty")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Lstat(absRoot); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, contractFieldError(ErrInvalidCustomerEvidence, "root", "must be a non-symlink directory")
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if err := os.MkdirAll(absRoot, 0o700); err != nil {
			return nil, err
		}
	} else {
		return nil, statErr
	}
	cleanSecrets := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if strings.TrimSpace(secret) != "" {
			cleanSecrets = append(cleanSecrets, secret)
		}
	}
	return &CustomerEvidenceBundle{SchemaVersion: CustomerEvidenceSchemaVersion, RunID: runID, Scenario: scenario, Artifacts: []ArtifactEntry{}, root: absRoot, secrets: cleanSecrets}, nil
}

// Root returns the absolute directory containing this evidence bundle.
func (b *CustomerEvidenceBundle) Root() string {
	if b == nil {
		return ""
	}
	return b.root
}

func (b CustomerEvidenceBundle) Validate() error {
	if b.SchemaVersion != CustomerEvidenceSchemaVersion {
		return contractFieldError(ErrInvalidCustomerEvidence, "schema_version", "must be 1")
	}
	if strings.TrimSpace(b.RunID) == "" {
		return contractFieldError(ErrInvalidCustomerEvidence, "run_id", "must not be empty")
	}
	if err := b.Scenario.Validate(); err != nil {
		return err
	}
	if !b.Finalized || b.FinalizedAt.IsZero() {
		return contractFieldError(ErrInvalidCustomerEvidence, "finalized", "must be true with a finalization timestamp")
	}
	if err := b.Transcripts.validate(); err != nil {
		return err
	}
	if err := validateObservedFacts("", b.AudioTurnEvents, b.ToolObservations, b.FilesystemCheckpoints); err != nil {
		return err
	}
	if b.MechanicalVerdict == nil || b.ValidatorInput == nil || b.ValidatorVerdict == nil {
		return contractFieldError(ErrMissingEvidence, "bundle", "mechanical verdict, validator input, and validator verdict are required")
	}
	if err := b.validateFamilyEvidence(); err != nil {
		return err
	}
	if err := b.Process.validate("process"); err != nil {
		return err
	}
	if err := b.MechanicalVerdict.validate(b.Scenario, "mechanical_verdict"); err != nil {
		return err
	}
	// Validator input validation also enforces its own family evidence.
	if err := b.ValidatorInput.validate(b.Scenario, "validator_input"); err != nil {
		return err
	}
	if err := b.ValidatorVerdict.Validate(); err != nil {
		return err
	}
	if b.ValidatorVerdict.Verdict == ValidatorWorked && !b.MechanicalVerdict.Pass {
		return contractFieldError(ErrValidatorMechanicalDisagreement, "validator_verdict.verdict", "WORKED requires a passing mechanical verdict")
	}
	if err := validateArtifactEntries(b.Artifacts, true); err != nil {
		return err
	}
	if err := validateRequiredArtifactKinds(b.Artifacts); err != nil {
		return err
	}
	if b.MechanicalVerdict.Pass && (len(b.Transcripts.Customer) == 0 || len(b.Transcripts.Product) == 0 || len(b.AudioTurnEvents) == 0 || len(b.FilesystemCheckpoints) == 0) {
		return contractFieldError(ErrMissingEvidence, "bundle", "a passing run needs paired transcripts, audio/turn events, and checkpoints")
	}
	return b.validateEvidenceRefs(availableArtifactPaths(b.Artifacts))
}

func (b CustomerEvidenceBundle) validateFamilyEvidence() error {
	slot, ok := familyEvidenceSlotFor(b.Scenario.Family)
	if !ok {
		return nil
	}
	evidence := selectFamilyEvidence(b.Scenario.Family, b.MixedModal, b.Termination, b.Patience)
	if !evidence.present {
		return contractFieldError(ErrMissingEvidence, slot.field, fmt.Sprintf("Family %s bundles require %s", b.Scenario.Family, slot.noun))
	}
	if err := evidence.validate(b.Scenario); err != nil {
		return err
	}
	if !hasArtifactKind(b.Artifacts, slot.kind) {
		return contractFieldError(ErrMissingEvidence, "artifacts", fmt.Sprintf("Family %s bundles require a hash-verified %s artifact", b.Scenario.Family, slot.noun))
	}
	return nil
}

// validateEvidenceRefs requires every verdict and family evidence reference to
// name an available artifact. It runs after all structural validation.
func (b CustomerEvidenceBundle) validateEvidenceRefs(available map[string]struct{}) error {
	for i, result := range b.MechanicalVerdict.ActionResults {
		if !allEvidenceRefsAvailable(result.EvidenceRefs, available) {
			return contractFieldError(ErrMissingEvidence, fmt.Sprintf("mechanical_verdict.action_results[%d].evidence_refs", i), unavailableEvidenceMessage)
		}
	}
	for i, finding := range b.MechanicalVerdict.Findings {
		if !allEvidenceRefsAvailable(finding.EvidenceRefs, available) {
			return contractFieldError(ErrMissingEvidence, fmt.Sprintf("mechanical_verdict.findings[%d].evidence_refs", i), unavailableEvidenceMessage)
		}
	}
	if !allEvidenceRefsAvailable(b.ValidatorInput.EvidenceRefs, available) {
		return contractFieldError(ErrMissingEvidence, "validator_input.evidence_refs", unavailableEvidenceMessage)
	}
	if slot, ok := familyEvidenceSlotFor(b.Scenario.Family); ok {
		if !allEvidenceRefsAvailable(selectFamilyEvidence(b.Scenario.Family, b.MixedModal, b.Termination, b.Patience).refs, available) {
			return contractFieldError(ErrMissingEvidence, slot.field+".evidence_refs", unavailableEvidenceMessage)
		}
		input := b.ValidatorInput
		if !allEvidenceRefsAvailable(selectFamilyEvidence(b.Scenario.Family, input.MixedModal, input.Termination, input.Patience).refs, available) {
			return contractFieldError(ErrMissingEvidence, "validator_input."+slot.field+".evidence_refs", unavailableEvidenceMessage)
		}
	}
	if !allEvidenceRefsAvailable(b.ValidatorVerdict.EvidenceRefs, available) {
		return contractFieldError(ErrMissingEvidence, "validator_verdict.evidence_refs", unavailableEvidenceMessage)
	}
	return nil
}

const unavailableEvidenceMessage = "references unavailable evidence"

func (b CustomerEvidenceBundle) Manifest() CustomerEvidenceManifest {
	artifacts := append([]ArtifactEntry(nil), b.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	m := CustomerEvidenceManifest{SchemaVersion: CustomerEvidenceSchemaVersion, RunID: b.RunID, ScenarioID: b.Scenario.ID, Finalized: b.Finalized, FinalizedAt: b.FinalizedAt, Artifacts: artifacts}
	if b.MechanicalVerdict != nil {
		m.MechanicalPass = b.MechanicalVerdict.Pass
	}
	if b.ValidatorVerdict != nil {
		m.ValidatorVerdict = b.ValidatorVerdict.Verdict
	}
	return m
}

func (b *CustomerEvidenceBundle) RegisterArtifact(path string, kind ArtifactKind, required bool) error {
	if b == nil {
		return contractFieldError(ErrInvalidCustomerEvidence, "bundle", "must not be nil")
	}
	if err := validateArtifactPath(path); err != nil {
		return err
	}
	if !kind.valid() {
		return contractFieldError(ErrInvalidCustomerEvidence, "artifact.kind", fmt.Sprintf("%q is invalid", kind))
	}
	absolute, err := b.resolve(path)
	if err != nil {
		return err
	}
	// An unreadable artifact is recorded evidence, not a registration failure.
	data, state, reason := readRegularArtifact(absolute)
	if state != ArtifactStateAvailable {
		b.upsertArtifact(ArtifactEntry{Path: path, Kind: kind, Required: required, State: state, Size: -1, Reason: reason})
		return nil
	}
	if err := b.checkCredentialFree(data); err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	b.upsertArtifact(ArtifactEntry{Path: path, Kind: kind, Required: required, State: ArtifactStateAvailable, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])})
	return nil
}

func (b *CustomerEvidenceBundle) AddArtifactBytes(path string, kind ArtifactKind, data []byte, required bool) error {
	if b == nil {
		return contractFieldError(ErrInvalidCustomerEvidence, "bundle", "must not be nil")
	}
	if err := validateArtifactPath(path); err != nil {
		return err
	}
	if !kind.valid() {
		return contractFieldError(ErrInvalidCustomerEvidence, "artifact.kind", fmt.Sprintf("%q is invalid", kind))
	}
	if err := b.checkCredentialFree(data); err != nil {
		return err
	}
	absolute, err := b.resolve(path)
	if err != nil {
		return err
	}
	if err := writePrivateFile(absolute, ".evidence-*.tmp", data); err != nil {
		return err
	}
	return b.RegisterArtifact(path, kind, required)
}
func (b *CustomerEvidenceBundle) WriteArtifact(path string, kind ArtifactKind, data []byte, required bool) error {
	return b.AddArtifactBytes(path, kind, data, required)
}
func (b *CustomerEvidenceBundle) RecordMissingArtifact(path string, kind ArtifactKind, required bool, reason string) error {
	if b == nil {
		return contractFieldError(ErrInvalidCustomerEvidence, "bundle", "must not be nil")
	}
	if err := validateArtifactPath(path); err != nil {
		return err
	}
	if !kind.valid() {
		return contractFieldError(ErrInvalidCustomerEvidence, "artifact.kind", "is invalid")
	}
	if strings.TrimSpace(reason) == "" {
		return contractFieldError(ErrInvalidCustomerEvidence, "artifact.reason", "must not be empty")
	}
	b.upsertArtifact(ArtifactEntry{Path: path, Kind: kind, Required: required, State: ArtifactStateMissing, Size: -1, Reason: reason})
	return nil
}

func (b *CustomerEvidenceBundle) AddProductRecordDir(source string) error {
	if b == nil {
		return contractFieldError(ErrInvalidCustomerEvidence, "bundle", "must not be nil")
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	info, err := os.Lstat(absSource)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMissingEvidence, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return contractFieldError(ErrInvalidCustomerEvidence, "product_record_dir", "must be a non-symlink directory")
	}
	b.productRecordAdded = true
	return filepath.Walk(absSource, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular product artifact", ErrInvalidCustomerEvidence)
		}
		rel, err := filepath.Rel(absSource, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return b.AddArtifactBytes(filepath.ToSlash(filepath.Join("product-record-dir", rel)), ArtifactKindProductRecordDir, data, true)
	})
}

func (b *CustomerEvidenceBundle) Finalize() error {
	if b == nil {
		return contractFieldError(ErrInvalidCustomerEvidence, "bundle", "must not be nil")
	}
	if b.root == "" {
		return contractFieldError(ErrInvalidCustomerEvidence, "root", "must not be empty")
	}
	var writeErrors []error
	add := func(err error) {
		if err != nil {
			writeErrors = append(writeErrors, err)
		}
	}
	add(b.writeJSONArtifact("scenario.json", ArtifactKindScenario, b.Scenario, true))
	add(b.writeJSONLinesArtifact("transcripts/customer.jsonl", ArtifactKindCustomerTranscript, b.Transcripts.Customer, true))
	add(b.writeJSONLinesArtifact("transcripts/product.jsonl", ArtifactKindProductTranscript, b.Transcripts.Product, true))
	add(b.writeJSONLinesArtifact("events/audio-turn-events.jsonl", ArtifactKindAudioTurnEvents, b.AudioTurnEvents, true))
	add(b.writeJSONLinesArtifact("tool-observations.jsonl", ArtifactKindToolObservations, b.ToolObservations, true))
	add(b.writeJSONLinesArtifact("filesystem-checkpoints.jsonl", ArtifactKindFilesystemCheckpoints, b.FilesystemCheckpoints, true))
	add(b.writeJSONArtifact("process.json", ArtifactKindProcessFacts, b.Process, true))
	if b.Scenario.Family == ScenarioFamilyC {
		if b.MixedModal == nil {
			add(b.RecordMissingArtifact("events/mixed-modal.json", ArtifactKindMixedModalEvidence, true, "mixed-modal boundary evidence was not produced"))
		} else {
			add(b.writeJSONArtifact("events/mixed-modal.json", ArtifactKindMixedModalEvidence, b.MixedModal, true))
		}
	}
	if b.Scenario.Family == ScenarioFamilyD {
		if b.Termination == nil {
			add(b.RecordMissingArtifact("events/termination.json", ArtifactKindTerminationEvidence, true, "termination evidence was not produced"))
		} else {
			add(b.writeJSONArtifact("events/termination.json", ArtifactKindTerminationEvidence, b.Termination, true))
		}
	}
	if b.Scenario.Family == ScenarioFamilyE {
		if b.Patience == nil {
			add(b.RecordMissingArtifact(FamilyEPatienceEventPath, ArtifactKindPatienceEvidence, true, "patience timing evidence was not produced"))
		} else {
			add(b.writeJSONArtifact(FamilyEPatienceEventPath, ArtifactKindPatienceEvidence, b.Patience, true))
		}
	}
	if b.MechanicalVerdict == nil {
		add(b.RecordMissingArtifact("mechanical-verdict.json", ArtifactKindMechanicalVerdict, true, "mechanical verdict was not produced"))
	} else {
		add(b.writeJSONArtifact("mechanical-verdict.json", ArtifactKindMechanicalVerdict, b.MechanicalVerdict, true))
	}
	if b.ValidatorInput == nil {
		add(b.RecordMissingArtifact("validator-input.json", ArtifactKindValidatorInput, true, "validator input was not produced"))
	} else {
		add(b.writeJSONArtifact("validator-input.json", ArtifactKindValidatorInput, b.ValidatorInput, true))
	}
	if b.ValidatorVerdict == nil {
		add(b.RecordMissingArtifact("validator-verdict.json", ArtifactKindValidatorVerdict, true, "validator verdict was not produced"))
	} else {
		add(b.writeJSONArtifact("validator-verdict.json", ArtifactKindValidatorVerdict, b.ValidatorVerdict, true))
	}
	if !hasArtifactKind(b.Artifacts, ArtifactKindProductRecordDir) {
		add(b.writeJSONArtifact("product-record-dir/index.json", ArtifactKindProductRecordDir, struct {
			SourceRegistered bool     `json:"source_registered"`
			Files            []string `json:"files"`
		}{b.productRecordAdded, productRecordPaths(b.Artifacts)}, true))
	}
	b.Finalized = true
	b.FinalizedAt = time.Now().UTC()
	validationErr := b.Validate()
	manifest := b.Manifest()
	if validationErr != nil {
		manifest.ValidationError = validationErr.Error()
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		add(err)
	} else {
		add(writePrivateFile(filepath.Join(b.root, "manifest.json"), ".manifest-*.tmp", append(data, '\n')))
	}
	if validationErr != nil {
		add(validationErr)
	}
	return errors.Join(writeErrors...)
}
func (b *CustomerEvidenceBundle) writeJSONArtifact(path string, kind ArtifactKind, value any, required bool) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return b.AddArtifactBytes(path, kind, append(data, '\n'), required)
}
func (b *CustomerEvidenceBundle) writeJSONLinesArtifact(path string, kind ArtifactKind, value any, required bool) error {
	data, err := jsonLines(value)
	if err != nil {
		return err
	}
	return b.AddArtifactBytes(path, kind, data, required)
}
func jsonLines(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(encoded) == "null" {
		return nil, nil
	}
	var lines []json.RawMessage
	if err := json.Unmarshal(encoded, &lines); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	for _, line := range lines {
		output.Write(line)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}
func (b *CustomerEvidenceBundle) resolve(relative string) (string, error) {
	if err := validateArtifactPath(relative); err != nil {
		return "", err
	}
	root, err := filepath.Abs(b.root)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrUnsafeEvidenceArtifactPath
	}
	return path, nil
}
func (b *CustomerEvidenceBundle) checkCredentialFree(data []byte) error {
	for _, secret := range b.secrets {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			return fmt.Errorf("%w: configured secret found", ErrCredentialInEvidence)
		}
	}
	if credentialPattern.Match(data) {
		return ErrCredentialInEvidence
	}
	return nil
}
func (b *CustomerEvidenceBundle) upsertArtifact(entry ArtifactEntry) {
	for i := range b.Artifacts {
		if b.Artifacts[i].Path == entry.Path {
			b.Artifacts[i] = entry
			return
		}
	}
	b.Artifacts = append(b.Artifacts, entry)
}

var credentialPattern = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|authorization|bearer|password)\s*[:=]\s*[^\s,}"']+|\bsk-[a-z0-9_-]{8,}\b`)

func validateArtifactEntries(entries []ArtifactEntry, requireComplete bool) error {
	if len(entries) == 0 {
		return contractFieldError(ErrMissingEvidence, "artifacts", "must not be empty")
	}
	seen := map[string]struct{}{}
	for i, entry := range entries {
		field := fmt.Sprintf("artifacts[%d]", i)
		if err := entry.validate(field); err != nil {
			return err
		}
		if _, ok := seen[entry.Path]; ok {
			return contractFieldError(ErrDuplicateEvidenceArtifact, field+".path", entry.Path)
		}
		seen[entry.Path] = struct{}{}
		if requireComplete && entry.Required && entry.State != ArtifactStateAvailable {
			return contractFieldError(ErrMissingEvidence, field, fmt.Sprintf("required artifact %q is %s", entry.Path, entry.State))
		}
	}
	return nil
}
func validateRequiredArtifactKinds(entries []ArtifactEntry) error {
	seen := map[ArtifactKind]bool{}
	for _, entry := range entries {
		if entry.Required && entry.State == ArtifactStateAvailable {
			seen[entry.Kind] = true
		}
	}
	required := []ArtifactKind{ArtifactKindScenario, ArtifactKindCustomerTranscript, ArtifactKindProductTranscript, ArtifactKindAudioTurnEvents, ArtifactKindProductRecordDir, ArtifactKindToolObservations, ArtifactKindFilesystemCheckpoints, ArtifactKindProcessFacts, ArtifactKindMechanicalVerdict, ArtifactKindValidatorInput, ArtifactKindValidatorVerdict}
	for _, kind := range required {
		if !seen[kind] {
			return contractFieldError(ErrMissingEvidence, "artifacts", fmt.Sprintf("required artifact kind %q is unavailable", kind))
		}
	}
	return nil
}
func availableArtifactPaths(entries []ArtifactEntry) map[string]struct{} {
	paths := map[string]struct{}{}
	for _, entry := range entries {
		if entry.State == ArtifactStateAvailable {
			paths[entry.Path] = struct{}{}
		}
	}
	return paths
}
func allEvidenceRefsAvailable(refs []string, available map[string]struct{}) bool {
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		if _, ok := available[ref]; !ok {
			return false
		}
	}
	return true
}
func hasArtifactKind(entries []ArtifactEntry, kind ArtifactKind) bool {
	for _, entry := range entries {
		if entry.Kind == kind && entry.State == ArtifactStateAvailable {
			return true
		}
	}
	return false
}
func productRecordPaths(entries []ArtifactEntry) []string {
	var paths []string
	for _, entry := range entries {
		if entry.Kind == ArtifactKindProductRecordDir && entry.State == ArtifactStateAvailable {
			paths = append(paths, entry.Path)
		}
	}
	sort.Strings(paths)
	return paths
}

func ParseCustomerEvidenceManifest(data []byte) (CustomerEvidenceManifest, error) {
	var manifest CustomerEvidenceManifest
	if err := decodeStrictJSON(data, &manifest); err != nil {
		return CustomerEvidenceManifest{}, fmt.Errorf("%w: decode manifest: %v", ErrInvalidCustomerEvidence, err)
	}
	if err := manifest.Validate(); err != nil {
		return CustomerEvidenceManifest{}, err
	}
	return manifest, nil
}
func ReadCustomerEvidenceManifest(root string) (CustomerEvidenceManifest, error) {
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return CustomerEvidenceManifest{}, err
	}
	return ParseCustomerEvidenceManifest(data)
}
func VerifyCustomerEvidenceManifest(root string, manifest CustomerEvidenceManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	for i, entry := range manifest.Artifacts {
		path, err := safeEvidencePath(absRoot, entry.Path)
		if err != nil {
			return err
		}
		info, statErr := os.Lstat(path)
		if entry.State != ArtifactStateAvailable {
			if entry.Required {
				return contractFieldError(ErrMissingEvidence, fmt.Sprintf("artifacts[%d]", i), "required artifact is unavailable")
			}
			continue
		}
		if statErr != nil {
			return fmt.Errorf("%w: %v", ErrMissingEvidence, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: artifact is not regular", ErrArtifactHashMismatch)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrArtifactHashMismatch, err)
		}
		if int64(len(data)) != entry.Size {
			return fmt.Errorf("%w: size mismatch for %q", ErrArtifactHashMismatch, entry.Path)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != entry.SHA256 {
			return fmt.Errorf("%w: hash mismatch for %q", ErrArtifactHashMismatch, entry.Path)
		}
	}
	return nil
}
func VerifyCustomerEvidenceBundle(root string) (CustomerEvidenceManifest, error) {
	manifest, err := ReadCustomerEvidenceManifest(root)
	if err != nil {
		return CustomerEvidenceManifest{}, err
	}
	if err := VerifyCustomerEvidenceManifest(root, manifest); err != nil {
		return CustomerEvidenceManifest{}, err
	}
	return manifest, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("contains more than one JSON value")
		}
		return err
	}
	return nil
}
func validateArtifactPath(path string) error {
	if err := validateRelativePath("artifact.path", path, false); err != nil {
		return err
	}
	return nil
}
func safeEvidencePath(root, relative string) (string, error) {
	if err := validateArtifactPath(relative); err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrUnsafeEvidenceArtifactPath
	}
	return path, nil
}

// removeTemporaryEvidenceFile is deferred after an atomic write. Once the
// rename succeeds the temporary name no longer exists, so a removal failure is
// expected and must not turn a successful write into an error.
// readRegularArtifact reads a regular, non-symlink artifact file and reports
// the artifact state with a reason when it cannot be used.
func readRegularArtifact(absolute string) ([]byte, ArtifactState, string) {
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ArtifactStateMissing, "artifact was not produced"
	}
	if err != nil {
		return nil, ArtifactStateFailed, err.Error()
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, ArtifactStateFailed, "artifact is not a regular file"
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, ArtifactStateFailed, err.Error()
	}
	return data, ArtifactStateAvailable, ""
}

func removeTemporaryEvidenceFile(name string) {
	if err := os.Remove(name); err != nil {
		return
	}
}

func writePrivateFile(path, temporaryPattern string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), temporaryPattern)
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer removeTemporaryEvidenceFile(name)
	if _, err := temporary.Write(data); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Chmod(0o600); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func addCustomerSimulationProductRecord(bundle *CustomerEvidenceBundle, recordRoot string) error {
	if bundle == nil {
		return ErrMissingEvidence
	}
	err := bundle.AddProductRecordDir(recordRoot)
	if err == nil && customerSimulationProductRecordFileCount(bundle) > 0 {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("%w: product record directory contained no files", ErrMissingEvidence)
	}
	// A failed child may not have created a record directory. Preserve a
	// hash-verified, explicit absence marker so the bundle remains readable and
	// the mechanical/validator verdict can report missing product evidence.
	if markerErr := bundle.AddArtifactBytes("product-record-dir/index.json", ArtifactKindProductRecordDir, []byte(`{"source_registered":false,"files":[],"reason":"product record directory was unavailable"}`+"\n"), true); markerErr != nil {
		return errors.Join(err, markerErr)
	}
	return fmt.Errorf("%w: product record directory was unavailable", ErrMissingEvidence)
}

func customerSimulationProductRecordFileCount(bundle *CustomerEvidenceBundle) int {
	if bundle == nil {
		return 0
	}
	count := 0
	for _, artifact := range bundle.Artifacts {
		if artifact.Kind == ArtifactKindProductRecordDir && strings.HasPrefix(artifact.Path, "product-record-dir/") && artifact.Path != "product-record-dir/index.json" && artifact.State == ArtifactStateAvailable {
			count++
		}
	}
	return count
}

// validateObservedFacts validates the audio, tool, and filesystem facts shared
// by evidence bundles and validator input. prefix is empty or ends in ".".
func validateObservedFacts(prefix string, audio []AudioTurnEvent, tools []ToolObservation, checkpoints []FilesystemCheckpoint) error {
	for n, event := range audio {
		if err := event.validate(fmt.Sprintf("%saudio_turn_events[%d]", prefix, n)); err != nil {
			return err
		}
	}
	toolIDs := map[string]struct{}{}
	for n, observation := range tools {
		if err := observation.validate(fmt.Sprintf("%stool_observations[%d]", prefix, n)); err != nil {
			return err
		}
		if _, ok := toolIDs[observation.ID]; ok {
			return contractFieldError(ErrInvalidCustomerEvidence, prefix+"tool_observations", "IDs must be unique")
		}
		toolIDs[observation.ID] = struct{}{}
	}
	checkpointIDs := map[string]struct{}{}
	for n, checkpoint := range checkpoints {
		if err := checkpoint.validate(fmt.Sprintf("%sfilesystem_checkpoints[%d]", prefix, n)); err != nil {
			return err
		}
		if _, ok := checkpointIDs[checkpoint.ID]; ok {
			return contractFieldError(ErrInvalidCustomerEvidence, prefix+"filesystem_checkpoints", "IDs must be unique")
		}
		checkpointIDs[checkpoint.ID] = struct{}{}
	}
	return nil
}
