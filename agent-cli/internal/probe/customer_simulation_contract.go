package probe

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	CustomerScenarioSchemaVersion = 1
	CustomerEvidenceSchemaVersion = 1
)

var (
	ErrInvalidCustomerScenario         = errors.New("invalid customer simulation scenario")
	ErrInvalidCustomerEvidence         = errors.New("invalid customer simulation evidence")
	ErrInvalidScenarioFamily           = errors.New("invalid customer simulation scenario family")
	ErrDuplicateActionIntent           = errors.New("customer simulation scenario contains duplicate action IDs")
	ErrUnknownActionIntent             = errors.New("customer simulation evidence refers to an unknown action")
	ErrDuplicateEvidenceArtifact       = errors.New("customer simulation evidence contains duplicate artifacts")
	ErrUnsafeEvidenceArtifactPath      = errors.New("customer simulation evidence artifact path is unsafe")
	ErrMissingEvidence                 = errors.New("customer simulation evidence is missing required evidence")
	ErrUnhashedEvidenceArtifact        = errors.New("customer simulation evidence artifact is not hash verified")
	ErrArtifactHashMismatch            = errors.New("customer simulation evidence artifact hash mismatch")
	ErrConfirmationWithoutDisposition  = errors.New("customer simulation confirmation has no terminal disposition")
	ErrInvalidValidatorVerdict         = errors.New("invalid customer simulation validator verdict")
	ErrValidatorMechanicalDisagreement = errors.New("customer simulation validator disagrees with mechanical verdict")
	ErrCredentialInEvidence            = errors.New("customer simulation evidence contains credential material")
)

// CustomerContractValidationError carries a stable error category and the
// exact contract field that failed validation.
type CustomerContractValidationError struct {
	Kind           error
	Field, Problem string
}

func (e *CustomerContractValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := fmt.Sprintf("customer simulation contract field %q", e.Field)
	if e.Problem != "" {
		message += ": " + e.Problem
	}
	if e.Kind != nil {
		message += ": " + e.Kind.Error()
	}
	return message
}
func (e *CustomerContractValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}
func contractFieldError(kind error, field, problem string) error {
	return &CustomerContractValidationError{Kind: kind, Field: field, Problem: problem}
}

type ScenarioFamily string

const (
	ScenarioFamilyA ScenarioFamily = "A"
	ScenarioFamilyB ScenarioFamily = "B"
	ScenarioFamilyC ScenarioFamily = "C"
	ScenarioFamilyD ScenarioFamily = "D"
	ScenarioFamilyE ScenarioFamily = "E"
	FamilyA                        = ScenarioFamilyA
	FamilyB                        = ScenarioFamilyB
	FamilyC                        = ScenarioFamilyC
	FamilyD                        = ScenarioFamilyD
	FamilyE                        = ScenarioFamilyE
)

func (f ScenarioFamily) valid() bool {
	switch f {
	case ScenarioFamilyA, ScenarioFamilyB, ScenarioFamilyC, ScenarioFamilyD, ScenarioFamilyE:
		return true
	default:
		return false
	}
}

type TerminalDisposition string

const (
	DispositionCompleted TerminalDisposition = "completed"
	DispositionFailed    TerminalDisposition = "failed"
	DispositionCancelled TerminalDisposition = "cancelled"
	ActionCompleted                          = DispositionCompleted
	ActionFailed                             = DispositionFailed
	ActionCancelled                          = DispositionCancelled
)

func (d TerminalDisposition) valid() bool {
	return d == DispositionCompleted || d == DispositionFailed || d == DispositionCancelled
}

type PartialSideEffectPolicy string

const (
	PartialSideEffectsPreserve PartialSideEffectPolicy = "preserve"
	PartialSideEffectsRollback PartialSideEffectPolicy = "rollback"
	PartialSideEffectsForbid   PartialSideEffectPolicy = "forbid"
	SideEffectsPreserve                                = PartialSideEffectsPreserve
	SideEffectsRollback                                = PartialSideEffectsRollback
	SideEffectsForbid                                  = PartialSideEffectsForbid
)

func (p PartialSideEffectPolicy) valid() bool {
	return p == PartialSideEffectsPreserve || p == PartialSideEffectsRollback || p == PartialSideEffectsForbid
}

type FileType string

const (
	FileTypeFile      FileType = "file"
	FileTypeDirectory FileType = "directory"
	FileTypeSymlink   FileType = "symlink"
	FileTypeAbsent    FileType = "absent"
)

func (t FileType) valid() bool {
	return t == FileTypeFile || t == FileTypeDirectory || t == FileTypeSymlink || t == FileTypeAbsent
}

type InterruptionTriggerKind string

const (
	InterruptionNone         InterruptionTriggerKind = "none"
	InterruptionDuringSpeech InterruptionTriggerKind = "during_speech"
	InterruptionDuringTool   InterruptionTriggerKind = "during_tool"
	InterruptionDuringOutput InterruptionTriggerKind = "during_output"
)

func (k InterruptionTriggerKind) valid() bool {
	return k == InterruptionNone || k == InterruptionDuringSpeech || k == InterruptionDuringTool || k == InterruptionDuringOutput
}

type TerminationMethod string

const (
	TerminationSIGINT  TerminationMethod = "sigint"
	TerminationNatural TerminationMethod = "natural"
	TerminationCtrlC                     = TerminationSIGINT
)

func (m TerminationMethod) valid() bool { return m == TerminationSIGINT || m == TerminationNatural }

type CustomerScenario struct {
	SchemaVersion  int                  `json:"schema_version"`
	ID             string               `json:"id"`
	Name           string               `json:"name,omitempty"`
	Family         ScenarioFamily       `json:"family"`
	Persona        string               `json:"persona"`
	Goal           string               `json:"goal"`
	WordingFreedom string               `json:"wording_freedom"`
	TextSeed       string               `json:"text_seed"`
	ImageEvents    []ScenarioImageEvent `json:"image_events"`
	Actions        []ActionIntent       `json:"actions"`
	Sandbox        SandboxSpec          `json:"sandbox"`
	Interruption   InterruptionTrigger  `json:"interruption_trigger"`
	Patience       PatienceThresholds   `json:"patience"`
	Termination    TerminationMethod    `json:"termination"`
	Deadline       time.Duration        `json:"deadline"`
}

type ScenarioImageEvent struct {
	ID            string `json:"id"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	Text          string `json:"text,omitempty"`
	AfterActionID string `json:"after_action_id,omitempty"`
}
type ImageEvent = ScenarioImageEvent

type SandboxSpec struct {
	Name  string `json:"name"`
	Root  string `json:"root"`
	Fresh bool   `json:"fresh"`
}

type InterruptionTrigger struct {
	Kind           InterruptionTriggerKind `json:"kind"`
	ActionID       string                  `json:"action_id,omitempty"`
	Description    string                  `json:"description,omitempty"`
	BeforeTerminal bool                    `json:"before_terminal"`
}

type PatienceThresholds struct {
	ListenBeforeFollowUp time.Duration `json:"listen_before_follow_up"`
	ResponseStart        time.Duration `json:"response_start"`
	InProgressWork       time.Duration `json:"in_progress_work"`
	Reprompt             time.Duration `json:"reprompt"`
	AbsoluteDeadAir      time.Duration `json:"absolute_dead_air"`
	MaxReprompts         int           `json:"max_reprompts"`
}

type ActionIntent struct {
	ID                      string                  `json:"id"`
	Intent                  string                  `json:"intent,omitempty"`
	Description             string                  `json:"description,omitempty"`
	AllowedDispositions     []TerminalDisposition   `json:"allowed_dispositions"`
	PartialSideEffectPolicy PartialSideEffectPolicy `json:"partial_side_effect_policy"`
	SideEffectRule          string                  `json:"side_effect_rule"`
	Oracle                  ActionOracle            `json:"oracle"`
}

type ActionOracle struct {
	Description         string                  `json:"description"`
	Checkpoints         []FilesystemExpectation `json:"checkpoints"`
	RequireConfirmation bool                    `json:"require_confirmation"`
}

type FilesystemExpectation struct {
	Path    string   `json:"path"`
	Type    FileType `json:"type"`
	SHA256  string   `json:"sha256,omitempty"`
	Content string   `json:"content,omitempty"`
}

func (s CustomerScenario) Validate() error {
	if s.SchemaVersion != CustomerScenarioSchemaVersion {
		return contractFieldError(ErrInvalidCustomerScenario, "schema_version", "must be 1")
	}
	if strings.TrimSpace(s.ID) == "" {
		return contractFieldError(ErrInvalidCustomerScenario, "id", "must not be empty")
	}
	if !s.Family.valid() {
		return contractFieldError(ErrInvalidScenarioFamily, "family", fmt.Sprintf("%q is not A, B, C, D, or E", s.Family))
	}
	for _, field := range []struct{ name, value string }{{"persona", s.Persona}, {"goal", s.Goal}, {"wording_freedom", s.WordingFreedom}, {"text_seed", s.TextSeed}} {
		if strings.TrimSpace(field.value) == "" {
			return contractFieldError(ErrInvalidCustomerScenario, field.name, "must not be empty")
		}
	}
	if err := validateSandbox(s.Sandbox); err != nil {
		return err
	}
	if err := validateInterruption(s.Interruption, s.Actions); err != nil {
		return err
	}
	if err := validatePatience(s.Patience); err != nil {
		return err
	}
	if !s.Termination.valid() {
		return contractFieldError(ErrInvalidCustomerScenario, "termination", "must be sigint or natural")
	}
	if s.Deadline <= 0 {
		return contractFieldError(ErrInvalidCustomerScenario, "deadline", "must be positive")
	}

	images := map[string]struct{}{}
	for i, image := range s.ImageEvents {
		field := fmt.Sprintf("image_events[%d]", i)
		if strings.TrimSpace(image.ID) == "" {
			return contractFieldError(ErrInvalidCustomerScenario, field+".id", "must not be empty")
		}
		if _, ok := images[image.ID]; ok {
			return contractFieldError(ErrInvalidCustomerScenario, field+".id", "must be unique")
		}
		images[image.ID] = struct{}{}
		if err := validateRelativePath(field+".path", image.Path, false); err != nil {
			return err
		}
		if err := validateSHA256(field+".sha256", image.SHA256, true); err != nil {
			return err
		}
	}
	if len(s.Actions) == 0 {
		return contractFieldError(ErrInvalidCustomerScenario, "actions", "must contain at least one ordered action")
	}
	seen := map[string]struct{}{}
	for i, action := range s.Actions {
		field := fmt.Sprintf("actions[%d]", i)
		if strings.TrimSpace(action.ID) == "" {
			return contractFieldError(ErrInvalidCustomerScenario, field+".id", "must not be empty")
		}
		if _, ok := seen[action.ID]; ok {
			return contractFieldError(ErrDuplicateActionIntent, field+".id", fmt.Sprintf("duplicate action %q", action.ID))
		}
		seen[action.ID] = struct{}{}
		if strings.TrimSpace(action.Intent) == "" && strings.TrimSpace(action.Description) == "" {
			return contractFieldError(ErrInvalidCustomerScenario, field, "intent or description must not be empty")
		}
		if len(action.AllowedDispositions) == 0 {
			return contractFieldError(ErrInvalidCustomerScenario, field+".allowed_dispositions", "must not be empty")
		}
		dispositions := map[TerminalDisposition]struct{}{}
		for j, disposition := range action.AllowedDispositions {
			if !disposition.valid() {
				return contractFieldError(ErrInvalidCustomerScenario, fmt.Sprintf("%s.allowed_dispositions[%d]", field, j), fmt.Sprintf("%q is invalid", disposition))
			}
			if _, ok := dispositions[disposition]; ok {
				return contractFieldError(ErrInvalidCustomerScenario, field+".allowed_dispositions", "must be unique")
			}
			dispositions[disposition] = struct{}{}
		}
		if !action.PartialSideEffectPolicy.valid() {
			return contractFieldError(ErrInvalidCustomerScenario, field+".partial_side_effect_policy", "must be preserve, rollback, or forbid")
		}
		if strings.TrimSpace(action.SideEffectRule) == "" {
			return contractFieldError(ErrInvalidCustomerScenario, field+".side_effect_rule", "must describe observable cleanup or preservation")
		}
		if err := action.Oracle.validate(field + ".oracle"); err != nil {
			return err
		}
	}
	actionIDs := make(map[string]struct{}, len(s.Actions))
	for _, action := range s.Actions {
		actionIDs[action.ID] = struct{}{}
	}
	for i, image := range s.ImageEvents {
		if image.AfterActionID != "" {
			if _, ok := actionIDs[image.AfterActionID]; !ok {
				return contractFieldError(ErrInvalidCustomerScenario, fmt.Sprintf("image_events[%d].after_action_id", i), "must identify a declared action")
			}
		}
	}
	return nil
}

func (o ActionOracle) validate(field string) error {
	if strings.TrimSpace(o.Description) == "" && len(o.Checkpoints) == 0 && !o.RequireConfirmation {
		return contractFieldError(ErrInvalidCustomerScenario, field, "must declare an observable check")
	}
	for i, checkpoint := range o.Checkpoints {
		if err := checkpoint.validate(fmt.Sprintf("%s.checkpoints[%d]", field, i)); err != nil {
			return err
		}
	}
	return nil
}
func (e FilesystemExpectation) validate(field string) error {
	if err := validateRelativePath(field+".path", e.Path, false); err != nil {
		return err
	}
	if !e.Type.valid() {
		return contractFieldError(ErrInvalidCustomerScenario, field+".type", fmt.Sprintf("%q is invalid", e.Type))
	}
	if e.Type == FileTypeAbsent {
		if e.SHA256 != "" || e.Content != "" {
			return contractFieldError(ErrInvalidCustomerScenario, field, "absent paths cannot declare content or a hash")
		}
		return nil
	}
	return validateSHA256(field+".sha256", e.SHA256, true)
}
func validateSandbox(s SandboxSpec) error {
	if strings.TrimSpace(s.Name) == "" {
		return contractFieldError(ErrInvalidCustomerScenario, "sandbox.name", "must not be empty")
	}
	if err := validateRelativePath("sandbox.root", s.Root, true); err != nil {
		return err
	}
	if !s.Fresh {
		return contractFieldError(ErrInvalidCustomerScenario, "sandbox.fresh", "must be true for an isolated run")
	}
	return nil
}
func validateInterruption(t InterruptionTrigger, actions []ActionIntent) error {
	if !t.Kind.valid() {
		return contractFieldError(ErrInvalidCustomerScenario, "interruption_trigger.kind", fmt.Sprintf("%q is invalid", t.Kind))
	}
	if t.Kind == InterruptionNone {
		return nil
	}
	if strings.TrimSpace(t.ActionID) == "" || strings.TrimSpace(t.Description) == "" {
		return contractFieldError(ErrInvalidCustomerScenario, "interruption_trigger", "action_id and description are required")
	}
	for _, action := range actions {
		if action.ID == t.ActionID {
			if !t.BeforeTerminal {
				return contractFieldError(ErrInvalidCustomerScenario, "interruption_trigger.before_terminal", "must be true")
			}
			return nil
		}
	}
	return contractFieldError(ErrInvalidCustomerScenario, "interruption_trigger.action_id", fmt.Sprintf("unknown action %q", t.ActionID))
}
func validatePatience(p PatienceThresholds) error {
	for _, field := range []struct {
		name  string
		value time.Duration
	}{{"listen_before_follow_up", p.ListenBeforeFollowUp}, {"response_start", p.ResponseStart}, {"in_progress_work", p.InProgressWork}, {"reprompt", p.Reprompt}, {"absolute_dead_air", p.AbsoluteDeadAir}} {
		if field.value <= 0 {
			return contractFieldError(ErrInvalidCustomerScenario, "patience."+field.name, "must be positive")
		}
	}
	if p.MaxReprompts < 0 {
		return contractFieldError(ErrInvalidCustomerScenario, "patience.max_reprompts", "must not be negative")
	}
	if p.ListenBeforeFollowUp > p.Reprompt || p.Reprompt > p.AbsoluteDeadAir {
		return contractFieldError(ErrInvalidCustomerScenario, "patience", "thresholds must be ordered")
	}
	return nil
}

func ParseCustomerScenario(data []byte) (CustomerScenario, error) {
	var scenario CustomerScenario
	if err := decodeStrictJSON(data, &scenario); err != nil {
		return CustomerScenario{}, fmt.Errorf("%w: decode scenario: %v", ErrInvalidCustomerScenario, err)
	}
	if err := scenario.Validate(); err != nil {
		return CustomerScenario{}, err
	}
	return scenario, nil
}
func WriteCustomerScenario(path string, scenario CustomerScenario) error {
	if err := scenario.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(scenario, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func validateRelativePath(field, raw string, allowDot bool) error {
	if strings.TrimSpace(raw) == "" || filepath.IsAbs(raw) || strings.Contains(raw, "\\") || strings.Contains(raw, "\x00") {
		return contractFieldError(ErrUnsafeEvidenceArtifactPath, field, "must be a relative slash-separated path")
	}
	clean := filepath.Clean(filepath.FromSlash(raw))
	for _, part := range strings.Split(filepath.ToSlash(raw), "/") {
		if part == ".." {
			return contractFieldError(ErrUnsafeEvidenceArtifactPath, field, "parent path components are not allowed")
		}
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || (!allowDot && clean == ".") {
		return contractFieldError(ErrUnsafeEvidenceArtifactPath, field, "must stay below the declared root")
	}
	return nil
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validateSHA256(field, value string, required bool) error {
	if value == "" && !required {
		return nil
	}
	if !sha256Pattern.MatchString(value) {
		return contractFieldError(ErrUnhashedEvidenceArtifact, field, "must be a lowercase SHA-256 hex digest")
	}
	return nil
}
