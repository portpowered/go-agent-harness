package probe

import (
	"fmt"
	"slices"
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

// Audio turn directions relative to the shipped child.
const (
	audioDirectionInput  = "input"
	audioDirectionOutput = "output"
)

func (e AudioTurnEvent) validate(field string) error {
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.TurnID) == "" || strings.TrimSpace(e.Kind) == "" {
		return contractFieldError(ErrInvalidCustomerEvidence, field, "id, turn_id, and kind must not be empty")
	}
	if e.Direction != audioDirectionInput && e.Direction != audioDirectionOutput {
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
	case toolStatusStarted, toolStatusCompleted, toolStatusFailed, toolStatusCancelled:
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
		if err := entry.validateFact(item); err != nil {
			return err
		}
	}
	return nil
}

// validateFact checks one entry's type-specific size and hash facts.
func (entry FilesystemCheckpointEntry) validateFact(item string) error {
	if !entry.Type.valid() {
		return contractFieldError(ErrInvalidCustomerEvidence, item+".type", fmt.Sprintf("%q is invalid", entry.Type))
	}
	if entry.Type == FileTypeAbsent {
		if entry.SHA256 != "" || entry.Size != 0 {
			return contractFieldError(ErrInvalidCustomerEvidence, item, "absent facts must have zero size and no hash")
		}
		return nil
	}
	if entry.Size < 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, item+".size", "must not be negative")
	}
	return validateSHA256(item+".sha256", entry.SHA256, true)
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
	case duplexExitNormal, duplexExitSIGINT, duplexExitCancelled, duplexExitTimeout, duplexExitFailed:
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
		if err := result.validateAgainst(action, item); err != nil {
			return err
		}
	}
	return validateMechanicalFindings(field, v.Findings)
}

func validateMechanicalFindings(field string, findings []MechanicalFinding) error {
	for i, finding := range findings {
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

// validateAgainst checks one mechanical action result against the scenario
// action it reports on.
func (result ActionResult) validateAgainst(action ActionIntent, item string) error {
	if result.Confirmed && result.Disposition == "" {
		return contractFieldError(ErrConfirmationWithoutDisposition, item+".disposition", "confirmed action must have a terminal disposition")
	}
	if result.ConfirmedAt < 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, item+".confirmed_at", "must not be negative")
	}
	if !result.Disposition.valid() {
		return contractFieldError(ErrInvalidCustomerEvidence, item+".disposition", fmt.Sprintf("%q is invalid", result.Disposition))
	}
	if !slices.Contains(action.AllowedDispositions, result.Disposition) {
		return contractFieldError(ErrInvalidCustomerEvidence, item+".disposition", "disposition is not allowed by the scenario")
	}
	if len(result.EvidenceRefs) == 0 {
		return contractFieldError(ErrMissingEvidence, item+".evidence_refs", "must not be empty")
	}
	if (result.Disposition == DispositionFailed || result.Disposition == DispositionCancelled) && strings.TrimSpace(result.OutcomeReason) == "" {
		return contractFieldError(ErrInvalidCustomerEvidence, item+".outcome_reason", "must explain the terminal outcome")
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
	if err := validateObservedFacts(field+".", i.AudioTurnEvents, i.ToolObservations, i.FilesystemCheckpoints); err != nil {
		return err
	}
	if err := i.Process.validate(field + ".process"); err != nil {
		return err
	}
	if err := i.Mechanical.validate(scenario, field+".mechanical"); err != nil {
		return err
	}
	if slot, ok := familyEvidenceSlotFor(scenario.Family); ok {
		evidence := selectFamilyEvidence(scenario.Family, i.MixedModal, i.Termination, i.Patience)
		if !evidence.present {
			return contractFieldError(ErrMissingEvidence, field+"."+slot.field, fmt.Sprintf("Family %s validator input requires %s", scenario.Family, slot.noun))
		}
		if err := evidence.validate(scenario); err != nil {
			return err
		}
	}
	if len(i.EvidenceRefs) == 0 {
		return contractFieldError(ErrMissingEvidence, field+".evidence_refs", "must not be empty")
	}
	return nil
}

func validateUniqueNonEmptyStrings(field string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			return contractFieldError(ErrInvalidCustomerEvidence, fmt.Sprintf("%s[%d]", field, index), "must not be empty")
		}
		if _, ok := seen[value]; ok {
			return contractFieldError(ErrInvalidCustomerEvidence, field, "values must be unique")
		}
		seen[value] = struct{}{}
	}
	return nil
}
