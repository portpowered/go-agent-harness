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

type TranscriptSpeaker string

const (
	TranscriptCustomer TranscriptSpeaker = "customer"
	TranscriptProduct  TranscriptSpeaker = "product"
)

type TranscriptEvent struct {
	ID      string            `json:"id"`
	TurnID  string            `json:"turn_id"`
	Speaker TranscriptSpeaker `json:"speaker"`
	Text    string            `json:"text"`
	At      time.Duration     `json:"at"`
	Final   bool              `json:"final"`
}

type PairedTranscripts struct {
	Customer []TranscriptEvent `json:"customer"`
	Product  []TranscriptEvent `json:"product"`
}

func (p PairedTranscripts) validate() error {
	if err := validateTranscriptEvents("transcripts.customer", p.Customer, TranscriptCustomer); err != nil {
		return err
	}
	return validateTranscriptEvents("transcripts.product", p.Product, TranscriptProduct)
}
func validateTranscriptEvents(field string, events []TranscriptEvent, speaker TranscriptSpeaker) error {
	seen := map[string]struct{}{}
	var previous time.Duration
	for i, event := range events {
		item := fmt.Sprintf("%s[%d]", field, i)
		if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.TurnID) == "" {
			return contractFieldError(ErrInvalidCustomerEvidence, item, "id and turn_id must not be empty")
		}
		if _, ok := seen[event.ID]; ok {
			return contractFieldError(ErrInvalidCustomerEvidence, item+".id", "must be unique")
		}
		seen[event.ID] = struct{}{}
		if event.Speaker != speaker {
			return contractFieldError(ErrInvalidCustomerEvidence, item+".speaker", fmt.Sprintf("must be %q", speaker))
		}
		if event.At < 0 || (i > 0 && event.At < previous) {
			return contractFieldError(ErrInvalidCustomerEvidence, item+".at", "timestamps must be non-negative and monotonic")
		}
		previous = event.At
	}
	return nil
}

type AudioTurnEvent struct {
	ID        string        `json:"id"`
	TurnID    string        `json:"turn_id"`
	Direction string        `json:"direction"`
	Kind      string        `json:"kind"`
	At        time.Duration `json:"at"`
	Duration  time.Duration `json:"duration"`
	Bytes     int           `json:"bytes"`
}

func (e AudioTurnEvent) validate(field string) error {
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.TurnID) == "" || strings.TrimSpace(e.Kind) == "" {
		return contractFieldError(ErrInvalidCustomerEvidence, field, "id, turn_id, and kind must not be empty")
	}
	if e.Direction != "input" && e.Direction != "output" {
		return contractFieldError(ErrInvalidCustomerEvidence, field+".direction", "must be input or output")
	}
	if e.At < 0 || e.Duration < 0 || e.Bytes < 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, field, "at, duration, and bytes must not be negative")
	}
	return nil
}

type ToolObservation struct {
	ID         string        `json:"id"`
	ActionID   string        `json:"action_id"`
	TurnID     string        `json:"turn_id"`
	Tool       string        `json:"tool"`
	Status     string        `json:"status"`
	At         time.Duration `json:"at"`
	Duration   time.Duration `json:"duration"`
	ResultSeen bool          `json:"result_seen"`
	Summary    string        `json:"summary,omitempty"`
}

func (o ToolObservation) validate(field string) error {
	if strings.TrimSpace(o.ID) == "" || strings.TrimSpace(o.ActionID) == "" || strings.TrimSpace(o.TurnID) == "" || strings.TrimSpace(o.Tool) == "" {
		return contractFieldError(ErrInvalidCustomerEvidence, field, "id, action_id, turn_id, and tool must not be empty")
	}
	switch o.Status {
	case "started", "completed", "failed", "cancelled":
	default:
		return contractFieldError(ErrInvalidCustomerEvidence, field+".status", fmt.Sprintf("%q is invalid", o.Status))
	}
	if o.At < 0 || o.Duration < 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, field, "at and duration must not be negative")
	}
	return nil
}

type FilesystemCheckpoint struct {
	ID       string                      `json:"id"`
	ActionID string                      `json:"action_id"`
	At       time.Duration               `json:"at"`
	Entries  []FilesystemCheckpointEntry `json:"entries"`
}
type FilesystemCheckpointEntry struct {
	Path   string   `json:"path"`
	Type   FileType `json:"type"`
	SHA256 string   `json:"sha256,omitempty"`
	Size   int64    `json:"size"`
	Target string   `json:"target,omitempty"`
}
type FilesystemObservation = FilesystemCheckpointEntry

func (c FilesystemCheckpoint) validate(field string) error {
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.ActionID) == "" {
		return contractFieldError(ErrInvalidCustomerEvidence, field, "id and action_id must not be empty")
	}
	if c.At < 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, field+".at", "must not be negative")
	}
	if len(c.Entries) == 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, field+".entries", "must not be empty")
	}
	seen := map[string]struct{}{}
	for i, entry := range c.Entries {
		item := fmt.Sprintf("%s.entries[%d]", field, i)
		if err := validateRelativePath(item+".path", entry.Path, false); err != nil {
			return err
		}
		if _, ok := seen[entry.Path]; ok {
			return contractFieldError(ErrInvalidCustomerEvidence, item+".path", "must be unique")
		}
		seen[entry.Path] = struct{}{}
		if !entry.Type.valid() {
			return contractFieldError(ErrInvalidCustomerEvidence, item+".type", fmt.Sprintf("%q is invalid", entry.Type))
		}
		if entry.Type == FileTypeAbsent {
			if entry.SHA256 != "" || entry.Size != 0 {
				return contractFieldError(ErrInvalidCustomerEvidence, item, "absent facts must have zero size and no hash")
			}
			continue
		}
		if entry.Size < 0 {
			return contractFieldError(ErrInvalidCustomerEvidence, item+".size", "must not be negative")
		}
		if err := validateSHA256(item+".sha256", entry.SHA256, true); err != nil {
			return err
		}
	}
	return nil
}

type ProcessFacts struct {
	PID                int           `json:"pid"`
	ExitCode           int           `json:"exit_code"`
	ExitClassification string        `json:"exit_classification"`
	Signal             string        `json:"signal,omitempty"`
	SignalSent         bool          `json:"signal_sent"`
	SignalAt           time.Duration `json:"signal_at,omitempty"`
	ChildWaited        bool          `json:"child_waited"`
	WaitCount          int           `json:"wait_count"`
	DescendantsAlive   bool          `json:"descendants_alive"`
	InputClosed        bool          `json:"input_closed"`
	InputFinished      bool          `json:"input_finished"`
	OutputClosed       bool          `json:"output_closed"`
	StartedAt          time.Duration `json:"started_at"`
	EndedAt            time.Duration `json:"ended_at"`
}

func (p ProcessFacts) validate(field string) error {
	if p.PID < -1 {
		return contractFieldError(ErrInvalidCustomerEvidence, field+".pid", "must be -1 or greater")
	}
	switch p.ExitClassification {
	case "normal", "sigint", "cancelled", "timeout", "failed":
	default:
		return contractFieldError(ErrInvalidCustomerEvidence, field+".exit_classification", fmt.Sprintf("%q is invalid", p.ExitClassification))
	}
	if p.SignalSent && strings.TrimSpace(p.Signal) == "" {
		return contractFieldError(ErrInvalidCustomerEvidence, field+".signal", "must be present when signal_sent is true")
	}
	if p.ExitClassification == "sigint" && !p.SignalSent {
		return contractFieldError(ErrInvalidCustomerEvidence, field+".signal_sent", "must be true for sigint classification")
	}
	if p.SignalAt < 0 || p.WaitCount < 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, field, "signal_at and wait_count must not be negative")
	}
	if p.SignalSent && p.SignalAt > p.EndedAt {
		return contractFieldError(ErrInvalidCustomerEvidence, field+".signal_at", "must not follow process end")
	}
	if p.StartedAt < 0 || p.EndedAt < p.StartedAt {
		return contractFieldError(ErrInvalidCustomerEvidence, field, "timestamps must be non-negative and ordered")
	}
	return nil
}

type ActionResult struct {
	ActionID           string              `json:"action_id"`
	TurnID             string              `json:"turn_id,omitempty"`
	Confirmed          bool                `json:"confirmed"`
	ConfirmedAt        time.Duration       `json:"confirmed_at,omitempty"`
	Disposition        TerminalDisposition `json:"disposition"`
	OutcomeReason      string              `json:"outcome_reason,omitempty"`
	EvidenceRefs       []string            `json:"evidence_refs"`
	CheckpointIDs      []string            `json:"checkpoint_ids,omitempty"`
	ToolObservationIDs []string            `json:"tool_observation_ids,omitempty"`
}
type MechanicalFinding struct {
	Code         string   `json:"code"`
	ActionID     string   `json:"action_id,omitempty"`
	TurnID       string   `json:"turn_id,omitempty"`
	Message      string   `json:"message"`
	EvidenceRefs []string `json:"evidence_refs"`
}
type MechanicalVerdict struct {
	Pass          bool                `json:"pass"`
	Summary       string              `json:"summary"`
	ActionResults []ActionResult      `json:"action_results"`
	Findings      []MechanicalFinding `json:"findings"`
}

func (v MechanicalVerdict) validate(scenario CustomerScenario, field string) error {
	if strings.TrimSpace(v.Summary) == "" {
		return contractFieldError(ErrInvalidCustomerEvidence, field+".summary", "must not be empty")
	}
	if len(v.ActionResults) != len(scenario.Actions) {
		return contractFieldError(ErrMissingEvidence, field+".action_results", "must cover every declared action")
	}
	actions := map[string]ActionIntent{}
	for _, action := range scenario.Actions {
		actions[action.ID] = action
	}
	seen := map[string]struct{}{}
	for i, result := range v.ActionResults {
		item := fmt.Sprintf("%s.action_results[%d]", field, i)
		action, ok := actions[result.ActionID]
		if !ok {
			return contractFieldError(ErrUnknownActionIntent, item+".action_id", result.ActionID)
		}
		if _, ok := seen[result.ActionID]; ok {
			return contractFieldError(ErrDuplicateActionIntent, item+".action_id", "must be unique")
		}
		seen[result.ActionID] = struct{}{}
		if result.Confirmed && result.Disposition == "" {
			return contractFieldError(ErrConfirmationWithoutDisposition, item+".disposition", "confirmed action must have a terminal disposition")
		}
		if result.ConfirmedAt < 0 {
			return contractFieldError(ErrInvalidCustomerEvidence, item+".confirmed_at", "must not be negative")
		}
		if !result.Disposition.valid() {
			return contractFieldError(ErrInvalidCustomerEvidence, item+".disposition", fmt.Sprintf("%q is invalid", result.Disposition))
		}
		allowed := false
		for _, candidate := range action.AllowedDispositions {
			if result.Disposition == candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return contractFieldError(ErrInvalidCustomerEvidence, item+".disposition", "disposition is not allowed by the scenario")
		}
		if len(result.EvidenceRefs) == 0 {
			return contractFieldError(ErrMissingEvidence, item+".evidence_refs", "must not be empty")
		}
		if (result.Disposition == DispositionFailed || result.Disposition == DispositionCancelled) && strings.TrimSpace(result.OutcomeReason) == "" {
			return contractFieldError(ErrInvalidCustomerEvidence, item+".outcome_reason", "must explain the terminal outcome")
		}
	}
	for i, finding := range v.Findings {
		item := fmt.Sprintf("%s.findings[%d]", field, i)
		if strings.TrimSpace(finding.Code) == "" || strings.TrimSpace(finding.Message) == "" {
			return contractFieldError(ErrInvalidCustomerEvidence, item, "code and message must not be empty")
		}
		if len(finding.EvidenceRefs) == 0 {
			return contractFieldError(ErrMissingEvidence, item+".evidence_refs", "must not be empty")
		}
	}
	return nil
}

type ValidatorInput struct {
	Scenario              CustomerScenario       `json:"scenario"`
	CustomerTranscript    []TranscriptEvent      `json:"customer_transcript"`
	ProductTranscript     []TranscriptEvent      `json:"product_transcript"`
	AudioTurnEvents       []AudioTurnEvent       `json:"audio_turn_events"`
	ToolObservations      []ToolObservation      `json:"tool_observations"`
	FilesystemCheckpoints []FilesystemCheckpoint `json:"filesystem_checkpoints"`
	Process               ProcessFacts           `json:"process"`
	Mechanical            MechanicalVerdict      `json:"mechanical"`
	MixedModal            *MixedModalEvidence    `json:"mixed_modal,omitempty"`
	Termination           *TerminationEvidence   `json:"termination,omitempty"`
	Patience              *PatienceEvidence      `json:"patience,omitempty"`
	EvidenceRefs          []string               `json:"evidence_refs"`
}

func (i ValidatorInput) validate(scenario CustomerScenario, field string) error {
	if i.Scenario.ID != scenario.ID || i.Scenario.SchemaVersion != scenario.SchemaVersion {
		return contractFieldError(ErrInvalidCustomerEvidence, field+".scenario", "must identify the same scenario")
	}
	if err := (PairedTranscripts{Customer: i.CustomerTranscript, Product: i.ProductTranscript}).validate(); err != nil {
		return err
	}
	for n, event := range i.AudioTurnEvents {
		if err := event.validate(fmt.Sprintf("%s.audio_turn_events[%d]", field, n)); err != nil {
			return err
		}
	}
	tools := map[string]struct{}{}
	for n, observation := range i.ToolObservations {
		if err := observation.validate(fmt.Sprintf("%s.tool_observations[%d]", field, n)); err != nil {
			return err
		}
		if _, ok := tools[observation.ID]; ok {
			return contractFieldError(ErrInvalidCustomerEvidence, field+".tool_observations", "IDs must be unique")
		}
		tools[observation.ID] = struct{}{}
	}
	checkpoints := map[string]struct{}{}
	for n, checkpoint := range i.FilesystemCheckpoints {
		if err := checkpoint.validate(fmt.Sprintf("%s.filesystem_checkpoints[%d]", field, n)); err != nil {
			return err
		}
		if _, ok := checkpoints[checkpoint.ID]; ok {
			return contractFieldError(ErrInvalidCustomerEvidence, field+".filesystem_checkpoints", "IDs must be unique")
		}
		checkpoints[checkpoint.ID] = struct{}{}
	}
	if err := i.Process.validate(field + ".process"); err != nil {
		return err
	}
	if err := i.Mechanical.validate(scenario, field+".mechanical"); err != nil {
		return err
	}
	if scenario.Family == ScenarioFamilyC {
		if i.MixedModal == nil {
			return contractFieldError(ErrMissingEvidence, field+".mixed_modal", "Family C validator input requires mixed-modal evidence")
		}
		if err := i.MixedModal.Validate(scenario); err != nil {
			return err
		}
	}
	if scenario.Family == ScenarioFamilyD {
		if i.Termination == nil {
			return contractFieldError(ErrMissingEvidence, field+".termination", "Family D validator input requires termination evidence")
		}
		if err := i.Termination.Validate(scenario); err != nil {
			return err
		}
	}
	if scenario.Family == ScenarioFamilyE {
		if i.Patience == nil {
			return contractFieldError(ErrMissingEvidence, field+".patience", "Family E validator input requires patience evidence")
		}
		if err := i.Patience.Validate(scenario); err != nil {
			return err
		}
	}
	if len(i.EvidenceRefs) == 0 {
		return contractFieldError(ErrMissingEvidence, field+".evidence_refs", "must not be empty")
	}
	return nil
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
	for i, event := range b.AudioTurnEvents {
		if err := event.validate(fmt.Sprintf("audio_turn_events[%d]", i)); err != nil {
			return err
		}
	}
	tools := map[string]struct{}{}
	for i, observation := range b.ToolObservations {
		if err := observation.validate(fmt.Sprintf("tool_observations[%d]", i)); err != nil {
			return err
		}
		if _, ok := tools[observation.ID]; ok {
			return contractFieldError(ErrInvalidCustomerEvidence, "tool_observations", "IDs must be unique")
		}
		tools[observation.ID] = struct{}{}
	}
	checkpoints := map[string]struct{}{}
	for i, checkpoint := range b.FilesystemCheckpoints {
		if err := checkpoint.validate(fmt.Sprintf("filesystem_checkpoints[%d]", i)); err != nil {
			return err
		}
		if _, ok := checkpoints[checkpoint.ID]; ok {
			return contractFieldError(ErrInvalidCustomerEvidence, "filesystem_checkpoints", "IDs must be unique")
		}
		checkpoints[checkpoint.ID] = struct{}{}
	}
	if b.MechanicalVerdict == nil || b.ValidatorInput == nil || b.ValidatorVerdict == nil {
		return contractFieldError(ErrMissingEvidence, "bundle", "mechanical verdict, validator input, and validator verdict are required")
	}
	if b.Scenario.Family == ScenarioFamilyC {
		if b.MixedModal == nil {
			return contractFieldError(ErrMissingEvidence, "mixed_modal", "Family C bundles require mixed-modal evidence")
		}
		if err := b.MixedModal.Validate(b.Scenario); err != nil {
			return err
		}
		if !hasArtifactKind(b.Artifacts, ArtifactKindMixedModalEvidence) {
			return contractFieldError(ErrMissingEvidence, "artifacts", "Family C bundles require a hash-verified mixed-modal evidence artifact")
		}
	}
	if b.Scenario.Family == ScenarioFamilyD {
		if b.Termination == nil {
			return contractFieldError(ErrMissingEvidence, "termination", "Family D bundles require termination evidence")
		}
		if err := b.Termination.Validate(b.Scenario); err != nil {
			return err
		}
		if !hasArtifactKind(b.Artifacts, ArtifactKindTerminationEvidence) {
			return contractFieldError(ErrMissingEvidence, "artifacts", "Family D bundles require a hash-verified termination evidence artifact")
		}
	}
	if b.Scenario.Family == ScenarioFamilyE {
		if b.Patience == nil {
			return contractFieldError(ErrMissingEvidence, "patience", "Family E bundles require patience evidence")
		}
		if err := b.Patience.Validate(b.Scenario); err != nil {
			return err
		}
		if !hasArtifactKind(b.Artifacts, ArtifactKindPatienceEvidence) {
			return contractFieldError(ErrMissingEvidence, "artifacts", "Family E bundles require a hash-verified patience evidence artifact")
		}
	}
	if err := b.Process.validate("process"); err != nil {
		return err
	}
	if err := b.MechanicalVerdict.validate(b.Scenario, "mechanical_verdict"); err != nil {
		return err
	}
	if err := b.ValidatorInput.validate(b.Scenario, "validator_input"); err != nil {
		return err
	}
	if b.Scenario.Family == ScenarioFamilyC {
		if b.ValidatorInput.MixedModal == nil {
			return contractFieldError(ErrMissingEvidence, "validator_input.mixed_modal", "Family C validator input requires mixed-modal evidence")
		}
		if err := b.ValidatorInput.MixedModal.Validate(b.Scenario); err != nil {
			return err
		}
	}
	if b.Scenario.Family == ScenarioFamilyD {
		if b.ValidatorInput.Termination == nil {
			return contractFieldError(ErrMissingEvidence, "validator_input.termination", "Family D validator input requires termination evidence")
		}
		if err := b.ValidatorInput.Termination.Validate(b.Scenario); err != nil {
			return err
		}
	}
	if b.Scenario.Family == ScenarioFamilyE {
		if b.ValidatorInput.Patience == nil {
			return contractFieldError(ErrMissingEvidence, "validator_input.patience", "Family E validator input requires patience evidence")
		}
		if err := b.ValidatorInput.Patience.Validate(b.Scenario); err != nil {
			return err
		}
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
	available := availableArtifactPaths(b.Artifacts)
	if b.MechanicalVerdict.Pass && (len(b.Transcripts.Customer) == 0 || len(b.Transcripts.Product) == 0 || len(b.AudioTurnEvents) == 0 || len(b.FilesystemCheckpoints) == 0) {
		return contractFieldError(ErrMissingEvidence, "bundle", "a passing run needs paired transcripts, audio/turn events, and checkpoints")
	}
	for i, result := range b.MechanicalVerdict.ActionResults {
		if !allEvidenceRefsAvailable(result.EvidenceRefs, available) {
			return contractFieldError(ErrMissingEvidence, fmt.Sprintf("mechanical_verdict.action_results[%d].evidence_refs", i), "references unavailable evidence")
		}
	}
	for i, finding := range b.MechanicalVerdict.Findings {
		if !allEvidenceRefsAvailable(finding.EvidenceRefs, available) {
			return contractFieldError(ErrMissingEvidence, fmt.Sprintf("mechanical_verdict.findings[%d].evidence_refs", i), "references unavailable evidence")
		}
	}
	if !allEvidenceRefsAvailable(b.ValidatorInput.EvidenceRefs, available) {
		return contractFieldError(ErrMissingEvidence, "validator_input.evidence_refs", "references unavailable evidence")
	}
	if b.Scenario.Family == ScenarioFamilyC {
		if !allEvidenceRefsAvailable(b.MixedModal.EvidenceRefs, available) {
			return contractFieldError(ErrMissingEvidence, "mixed_modal.evidence_refs", "references unavailable evidence")
		}
		if !allEvidenceRefsAvailable(b.ValidatorInput.MixedModal.EvidenceRefs, available) {
			return contractFieldError(ErrMissingEvidence, "validator_input.mixed_modal.evidence_refs", "references unavailable evidence")
		}
	}
	if b.Scenario.Family == ScenarioFamilyD {
		if !allEvidenceRefsAvailable(b.Termination.EvidenceRefs, available) {
			return contractFieldError(ErrMissingEvidence, "termination.evidence_refs", "references unavailable evidence")
		}
		if !allEvidenceRefsAvailable(b.ValidatorInput.Termination.EvidenceRefs, available) {
			return contractFieldError(ErrMissingEvidence, "validator_input.termination.evidence_refs", "references unavailable evidence")
		}
	}
	if b.Scenario.Family == ScenarioFamilyE {
		if !allEvidenceRefsAvailable(b.Patience.EvidenceRefs, available) {
			return contractFieldError(ErrMissingEvidence, "patience.evidence_refs", "references unavailable evidence")
		}
		if !allEvidenceRefsAvailable(b.ValidatorInput.Patience.EvidenceRefs, available) {
			return contractFieldError(ErrMissingEvidence, "validator_input.patience.evidence_refs", "references unavailable evidence")
		}
	}
	if !allEvidenceRefsAvailable(b.ValidatorVerdict.EvidenceRefs, available) {
		return contractFieldError(ErrMissingEvidence, "validator_verdict.evidence_refs", "references unavailable evidence")
	}
	return nil
}

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
	info, statErr := os.Lstat(absolute)
	if errors.Is(statErr, os.ErrNotExist) {
		b.upsertArtifact(ArtifactEntry{Path: path, Kind: kind, Required: required, State: ArtifactStateMissing, Size: -1, Reason: "artifact was not produced"})
		return nil
	}
	if statErr != nil {
		b.upsertArtifact(ArtifactEntry{Path: path, Kind: kind, Required: required, State: ArtifactStateFailed, Size: -1, Reason: statErr.Error()})
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		b.upsertArtifact(ArtifactEntry{Path: path, Kind: kind, Required: required, State: ArtifactStateFailed, Size: -1, Reason: "artifact is not a regular file"})
		return nil
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		b.upsertArtifact(ArtifactEntry{Path: path, Kind: kind, Required: required, State: ArtifactStateFailed, Size: -1, Reason: err.Error()})
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
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(absolute), ".evidence-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, absolute); err != nil {
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
		add(writePrivateFile(filepath.Join(b.root, "manifest.json"), append(data, '\n')))
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
func writePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".manifest-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
