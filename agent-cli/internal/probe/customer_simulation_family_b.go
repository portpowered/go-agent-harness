package probe

import "time"

const (
	FamilyBScenarioID = "family-b-corrected-release-note"

	FamilyBOriginalReleaseNote    = "# Aurora Release Draft\n\nStatus: draft.\n"
	FamilyBReplacementReleaseNote = "# Aurora Release Note\n\nStatus: final.\n"

	familyBOriginalReleaseNoteHash    = "c9777d19f569b6c5c9d500d31b707ae1fcee2d1b33a9f797a17917691c3c4769"
	familyBReplacementReleaseNoteHash = "1d13b7646c0630d0775f09d289d2171b5f44767acc03163930fba5bfe67caafc"
)

const (
	FamilyBOriginalActionID    = "create-draft-release-note"
	FamilyBReplacementActionID = "create-final-release-note"
)

// CorrectionEvidence records the process-boundary facts that make a
// correction reviewable. The action disposition and the response disposition
// are deliberately separate: a tool may have completed while the assistant's
// explanation was interrupted by the correction.
type CorrectionEvidence struct {
	OriginalActionID    string `json:"original_action_id"`
	ReplacementActionID string `json:"replacement_action_id"`
	OriginalTurnID      string `json:"original_turn_id"`
	CorrectionTurnID    string `json:"correction_turn_id"`
	OriginalResponseID  string `json:"original_response_id"`

	OriginalResponseStartedAt    time.Duration `json:"original_response_started_at"`
	CorrectionStartedAt          time.Duration `json:"correction_started_at"`
	CancellationSentAt           time.Duration `json:"cancellation_sent_at"`
	OriginalResponseEndedAt      time.Duration `json:"original_response_ended_at"`
	ReplacementResponseStartedAt time.Duration `json:"replacement_response_started_at"`
	ReplacementResponseEndedAt   time.Duration `json:"replacement_response_ended_at"`

	OriginalResponseStatus    string `json:"original_response_status"`
	ReplacementResponseStatus string `json:"replacement_response_status"`

	OutstandingToolIDs  []string      `json:"outstanding_tool_ids,omitempty"`
	UnresolvedActionIDs []string      `json:"unresolved_action_ids,omitempty"`
	Process             *ProcessFacts `json:"process,omitempty"`
}

// Validate checks identity and shape. Ordering and terminal semantics are
// evaluated as mechanical findings so a malformed correction still produces
// an action/turn-specific BROKEN verdict when the rest of the run is readable.
func (e CorrectionEvidence) Validate(scenario CustomerScenario) error {
	if err := scenario.Validate(); err != nil {
		return err
	}
	if scenario.Family != ScenarioFamilyB {
		return contractFieldError(ErrInvalidCustomerEvidence, "correction", "requires a Family B scenario")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"original_action_id", e.OriginalActionID},
		{"replacement_action_id", e.ReplacementActionID},
		{"original_turn_id", e.OriginalTurnID},
		{"correction_turn_id", e.CorrectionTurnID},
		{"original_response_id", e.OriginalResponseID},
		{"original_response_status", e.OriginalResponseStatus},
		{"replacement_response_status", e.ReplacementResponseStatus},
	} {
		if field.value == "" {
			return contractFieldError(ErrInvalidCustomerEvidence, "correction."+field.name, "must not be empty")
		}
	}
	if e.OriginalActionID == e.ReplacementActionID {
		return contractFieldError(ErrInvalidCustomerEvidence, "correction.replacement_action_id", "must identify a distinct action")
	}
	if e.OriginalTurnID == e.CorrectionTurnID {
		return contractFieldError(ErrInvalidCustomerEvidence, "correction.correction_turn_id", "must identify a distinct turn")
	}
	for _, field := range []struct {
		name  string
		value time.Duration
	}{
		{"original_response_started_at", e.OriginalResponseStartedAt},
		{"correction_started_at", e.CorrectionStartedAt},
		{"cancellation_sent_at", e.CancellationSentAt},
		{"original_response_ended_at", e.OriginalResponseEndedAt},
		{"replacement_response_started_at", e.ReplacementResponseStartedAt},
		{"replacement_response_ended_at", e.ReplacementResponseEndedAt},
	} {
		if field.value < 0 {
			return contractFieldError(ErrInvalidCustomerEvidence, "correction."+field.name, "must not be negative")
		}
	}
	if e.OriginalResponseEndedAt < e.OriginalResponseStartedAt {
		return contractFieldError(ErrInvalidCustomerEvidence, "correction.original_response_ended_at", "must not precede response start")
	}
	if e.ReplacementResponseEndedAt < e.ReplacementResponseStartedAt {
		return contractFieldError(ErrInvalidCustomerEvidence, "correction.replacement_response_ended_at", "must not precede response start")
	}
	if e.Process != nil {
		if err := e.Process.validate("correction.process"); err != nil {
			return err
		}
	}
	return nil
}

// FamilyBSpokenScript returns natural customer wording for the original
// request and its correction. The correction is a new utterance on the same
// continuously open PCM stream, not a hidden text bridge.
func FamilyBSpokenScript() []CustomerScriptTurn {
	return []CustomerScriptTurn{
		{
			ActionID: FamilyBOriginalActionID,
			Text:     "Please start a draft Aurora release note in draft/brief.md and tell me when it is ready.",
		},
		{
			ActionID: FamilyBReplacementActionID,
			Text:     "Actually, I meant the final release note: keep any draft work you already finished, and create final/brief.md instead.",
		},
	}
}

// NewFamilyBScenario declares a correction whose original side effects are
// preserved and explicitly reported. Keeping the draft makes the positive
// path exercise the important distinction between a completed tool action
// and an interrupted assistant response.
func NewFamilyBScenario() CustomerScenario {
	allDispositions := []TerminalDisposition{DispositionCompleted, DispositionFailed, DispositionCancelled}
	return CustomerScenario{
		SchemaVersion:  CustomerScenarioSchemaVersion,
		ID:             FamilyBScenarioID,
		Name:           "Corrected Aurora release note",
		Family:         ScenarioFamilyB,
		Persona:        "A precise collaborator who corrects an active request",
		Goal:           "Preserve any completed draft work while honoring the corrected final-note request",
		WordingFreedom: "Use natural wording, but make the correction explicit and preserve the declared action order, paths, and side-effect policy.",
		TextSeed:       "The first request may already have written a draft when the customer changes the destination. Do not hide or silently discard that state.",
		ImageEvents:    []ScenarioImageEvent{},
		Actions: []ActionIntent{
			{
				ID:                      FamilyBOriginalActionID,
				Intent:                  "Create the original draft release note at draft/brief.md.",
				AllowedDispositions:     append([]TerminalDisposition(nil), allDispositions...),
				PartialSideEffectPolicy: PartialSideEffectsPreserve,
				SideEffectRule:          "Preserve any draft bytes already written when the correction interrupts the explanation, and report that original state explicitly.",
				Oracle: ActionOracle{
					Description:         "The original draft file exists with its exact content and the replacement path is still absent at the original checkpoint.",
					RequireConfirmation: true,
					RequiredText:        []string{"created draft/brief.md"},
					Checkpoints: []FilesystemExpectation{
						{Path: "draft/brief.md", Type: FileTypeFile, SHA256: familyBOriginalReleaseNoteHash, Content: FamilyBOriginalReleaseNote},
						{Path: "final/brief.md", Type: FileTypeAbsent},
					},
				},
			},
			{
				ID:                      FamilyBReplacementActionID,
				Intent:                  "Create the corrected final release note at final/brief.md.",
				AllowedDispositions:     append([]TerminalDisposition(nil), allDispositions...),
				PartialSideEffectPolicy: PartialSideEffectsPreserve,
				SideEffectRule:          "Complete and independently verify final/brief.md; the original draft must not be mistaken for the replacement.",
				Oracle: ActionOracle{
					Description:         "The corrected final file exists with its exact content while the preserved original draft remains observable.",
					RequireConfirmation: true,
					RequiredText:        []string{"created final/brief.md", "corrected"},
					Checkpoints: []FilesystemExpectation{
						{Path: "draft/brief.md", Type: FileTypeFile, SHA256: familyBOriginalReleaseNoteHash, Content: FamilyBOriginalReleaseNote},
						{Path: "final/brief.md", Type: FileTypeFile, SHA256: familyBReplacementReleaseNoteHash, Content: FamilyBReplacementReleaseNote},
					},
				},
			},
		},
		Sandbox: SandboxSpec{Name: "fresh-family-b-sandbox", Root: ".", Fresh: true},
		Interruption: InterruptionTrigger{
			Kind:           InterruptionDuringOutput,
			ActionID:       FamilyBOriginalActionID,
			Description:    "Begin the correction after the original confirmation audio starts but before that response reaches its terminal event.",
			BeforeTerminal: true,
		},
		Patience: PatienceThresholds{
			ListenBeforeFollowUp: 250 * time.Millisecond,
			ResponseStart:        time.Second,
			InProgressWork:       2 * time.Second,
			Reprompt:             3 * time.Second,
			AbsoluteDeadAir:      10 * time.Second,
			MaxReprompts:         1,
		},
		Termination: TerminationNatural,
		Deadline:    30 * time.Second,
	}
}
