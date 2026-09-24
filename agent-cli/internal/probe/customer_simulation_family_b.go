package probe

import (
	"fmt"
	"strings"
	"time"
)

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
	CancellationEventRecorded    bool          `json:"cancellation_event_recorded"`
	CancellationResponseID       string        `json:"cancellation_response_id,omitempty"`

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

func customerSimulationRecordedInputStart(facts customerSimulationRecordingFacts, index int) time.Duration {
	if index < 0 || index >= len(facts.inputSpeechStarts) {
		return 0
	}
	return facts.inputSpeechStarts[index]
}

// EvaluateCustomerSimulationCorrection adds the Family B correction ledger
// to the ordinary action/tool/filesystem oracle. It permits an explicitly
// cancelled original action only when the response was actually interrupted;
// a replacement still has to complete against its own tool and filesystem
// evidence.
func EvaluateCustomerSimulationCorrection(
	scenario CustomerScenario,
	actionResults []ActionResult,
	checkpoints []FilesystemCheckpoint,
	toolObservations []ToolObservation,
	productTranscript []TranscriptEvent,
	correction CorrectionEvidence,
) (MechanicalVerdict, error) {
	if err := scenario.Validate(); err != nil {
		return MechanicalVerdict{}, err
	}
	if err := correction.Validate(scenario); err != nil {
		return MechanicalVerdict{}, err
	}

	mechanical, err := EvaluateCustomerSimulation(scenario, actionResults, checkpoints, toolObservations, productTranscript)
	if err != nil {
		return mechanical, err
	}

	set := correctionFindingSet{
		oracleFindingSet: oracleFindingSet{findings: append([]MechanicalFinding(nil), mechanical.Findings...)},
		correction:       correction,
	}
	replacementAction := set.addActionIdentityFindings(scenario)

	resultByID := make(map[string]ActionResult, len(actionResults))
	for _, result := range actionResults {
		resultByID[result.ActionID] = result
	}
	originalResult, originalResultObserved := resultByID[correction.OriginalActionID]
	replacementResult, replacementResultObserved := resultByID[correction.ReplacementActionID]

	// Generic evaluation deliberately treats every non-completed action as a
	// failure. Family B is the one scenario where cancellation is an intended
	// terminal disposition, but only with a matching provider cancellation.
	if originalResultObserved && originalResult.Disposition == DispositionCancelled && isCorrectionCancelledStatus(correction.OriginalResponseStatus) {
		set.dropOriginalNotCompleted()
	}

	set.addResultFindings(originalResult, originalResultObserved, replacementAction, replacementResult, replacementResultObserved)
	set.addResponseBoundaryFindings()
	set.addTranscriptFindings(productTranscript)
	set.addUnresolvedFindings(toolObservations)
	set.addProcessFindings()

	findings := set.findings
	mechanical.Findings = findings
	mechanical.Pass = len(findings) == 0
	mechanical.Summary = mechanicalSummary(len(findings), len(scenario.Actions))
	if err := mechanical.validate(scenario, "mechanical_verdict"); err != nil {
		return mechanical, err
	}
	return mechanical, nil
}

// correctionFindingSet extends the ordinary oracle findings with the Family B
// correction ledger checks.
type correctionFindingSet struct {
	oracleFindingSet
	correction CorrectionEvidence
}

// addActionIdentityFindings checks the correction's action identities against
// the scenario and returns the declared replacement action.
func (s *correctionFindingSet) addActionIdentityFindings(scenario CustomerScenario) ActionIntent {
	c := s.correction
	actions := make(map[string]ActionIntent, len(scenario.Actions))
	actionOrder := make(map[string]int, len(scenario.Actions))
	for index, action := range scenario.Actions {
		actions[action.ID] = action
		actionOrder[action.ID] = index
	}
	_, originalKnown := actions[c.OriginalActionID]
	replacementAction, replacementKnown := actions[c.ReplacementActionID]
	s.addChecks([]findingCheck{
		{!originalKnown, "unknown_original_action", c.OriginalActionID, c.OriginalTurnID, "correction names an undeclared original action"},
		{!replacementKnown, "unknown_replacement_action", c.ReplacementActionID, c.CorrectionTurnID, "correction names an undeclared replacement action"},
		{originalKnown && replacementKnown && actionOrder[c.OriginalActionID] >= actionOrder[c.ReplacementActionID], "correction_action_order", c.OriginalActionID, c.OriginalTurnID, "replacement action must follow the original action"},
		{scenario.Interruption.Kind != InterruptionDuringOutput, "interruption_trigger_mismatch", c.OriginalActionID, c.OriginalTurnID, fmt.Sprintf("Family B requires during_output interruption, got %q", scenario.Interruption.Kind)},
		{scenario.Interruption.ActionID != c.OriginalActionID, "interruption_action_mismatch", c.OriginalActionID, c.OriginalTurnID, fmt.Sprintf("scenario interruption targets %q", scenario.Interruption.ActionID)},
	})
	return replacementAction
}

func (s *correctionFindingSet) dropOriginalNotCompleted() {
	filtered := s.findings[:0]
	for _, finding := range s.findings {
		if finding.Code == "action_not_completed" && finding.ActionID == s.correction.OriginalActionID {
			continue
		}
		filtered = append(filtered, finding)
	}
	s.findings = filtered
}

func (s *correctionFindingSet) addResultFindings(original ActionResult, originalObserved bool, replacementAction ActionIntent, replacement ActionResult, replacementObserved bool) {
	c := s.correction
	s.addChecks([]findingCheck{
		{!originalObserved, "original_action_unresolved", c.OriginalActionID, c.OriginalTurnID, "the original action has no terminal disposition"},
		{originalObserved && original.TurnID != c.OriginalTurnID, "original_turn_mismatch", c.OriginalActionID, original.TurnID, fmt.Sprintf("correction ledger names turn %q", c.OriginalTurnID)},
		{!replacementObserved, "replacement_not_verified", c.ReplacementActionID, c.CorrectionTurnID, "the replacement action has no independently recorded terminal result"},
	})
	if !replacementObserved {
		return
	}
	s.addChecks([]findingCheck{
		{replacement.TurnID != c.CorrectionTurnID, "replacement_turn_mismatch", c.ReplacementActionID, replacement.TurnID, fmt.Sprintf("correction ledger names turn %q", c.CorrectionTurnID)},
		{replacement.Disposition != DispositionCompleted, "replacement_not_completed", c.ReplacementActionID, replacement.TurnID, fmt.Sprintf("replacement ended with %q", replacement.Disposition)},
		{len(replacement.CheckpointIDs) == 0, "replacement_not_verified", c.ReplacementActionID, replacement.TurnID, "replacement completion has no filesystem checkpoint"},
		{len(replacement.ToolObservationIDs) == 0 && replacementAction.PartialSideEffectPolicy != PartialSideEffectsForbid, "replacement_not_independent", c.ReplacementActionID, replacement.TurnID, "replacement completion has no tool evidence distinct from the original work"},
	})
}

func (s *correctionFindingSet) addResponseBoundaryFindings() {
	c := s.correction
	cancelResponseMissing := c.CancellationEventRecorded && strings.TrimSpace(c.CancellationResponseID) == ""
	s.addChecks([]findingCheck{
		{!isCorrectionCancelledStatus(c.OriginalResponseStatus), "correction_ignored", c.OriginalActionID, c.OriginalTurnID, fmt.Sprintf("original response ended with status %q instead of cancelled", c.OriginalResponseStatus)},
		{!isCorrectionCompletedStatus(c.ReplacementResponseStatus), "replacement_response_incomplete", c.ReplacementActionID, c.CorrectionTurnID, fmt.Sprintf("replacement response ended with status %q", c.ReplacementResponseStatus)},
		{!c.CancellationEventRecorded, "cancellation_event_missing", c.OriginalActionID, c.CorrectionTurnID, "the copied product recording has no outbound RESPONSE.CANCEL event"},
		{cancelResponseMissing, "cancellation_response_missing", c.OriginalActionID, c.CorrectionTurnID, "the recorded RESPONSE.CANCEL event is not associated with an original response"},
		{c.CancellationEventRecorded && !cancelResponseMissing && c.CancellationResponseID != c.OriginalResponseID, "cancellation_response_mismatch", c.OriginalActionID, c.CorrectionTurnID, fmt.Sprintf("recorded RESPONSE.CANCEL targets %q, original response is %q", c.CancellationResponseID, c.OriginalResponseID)},
		{c.OriginalResponseStartedAt >= c.CorrectionStartedAt, "correction_not_after_output_start", c.OriginalActionID, c.OriginalTurnID, "correction speech did not begin after original output started"},
		{c.CorrectionStartedAt >= c.OriginalResponseEndedAt, "correction_after_response", c.OriginalActionID, c.CorrectionTurnID, "correction speech began after the original response had already ended"},
		{c.CancellationSentAt < c.OriginalResponseStartedAt || c.CancellationSentAt >= c.CorrectionStartedAt, "cancellation_boundary_missing", c.OriginalActionID, c.CorrectionTurnID, "response cancellation was not observed between original output start and correction speech"},
		{c.ReplacementResponseStartedAt < c.CorrectionStartedAt, "replacement_started_before_correction", c.ReplacementActionID, c.CorrectionTurnID, "replacement response started before the correction utterance"},
		{c.ReplacementResponseEndedAt <= c.ReplacementResponseStartedAt, "replacement_response_unfinished", c.ReplacementActionID, c.CorrectionTurnID, "replacement response has no positive completed interval"},
	})
}

func (s *correctionFindingSet) addTranscriptFindings(productTranscript []TranscriptEvent) {
	c := s.correction
	productTurns := map[string]struct{}{}
	for _, event := range productTranscript {
		if strings.TrimSpace(event.Text) != "" {
			productTurns[event.TurnID] = struct{}{}
		}
	}
	_, originalConfirmed := productTurns[c.OriginalTurnID]
	_, correctionConfirmed := productTurns[c.CorrectionTurnID]
	s.addChecks([]findingCheck{
		{!originalConfirmed, "original_confirmation_missing", c.OriginalActionID, c.OriginalTurnID, "no product transcript evidence was recorded for the original action"},
		{!correctionConfirmed, "correction_confirmation_missing", c.ReplacementActionID, c.CorrectionTurnID, "no product transcript evidence was recorded for the corrected request"},
	})
}

func (s *correctionFindingSet) addUnresolvedFindings(toolObservations []ToolObservation) {
	c := s.correction
	for _, toolID := range c.OutstandingToolIDs {
		if strings.TrimSpace(toolID) == "" {
			s.add("unresolved_tool", c.OriginalActionID, c.OriginalTurnID, "an outstanding tool ledger entry has an empty ID")
			continue
		}
		s.add("unresolved_tool", c.OriginalActionID, c.OriginalTurnID, fmt.Sprintf("tool %q was still outstanding at session termination", toolID))
	}
	for _, actionID := range c.UnresolvedActionIDs {
		s.add("unresolved_action", actionID, c.OriginalTurnID, "an action remained unresolved at session termination")
	}
	for _, observation := range toolObservations {
		if (observation.ActionID == c.OriginalActionID || observation.ActionID == c.ReplacementActionID) && (observation.Status == "started" || !observation.ResultSeen) {
			s.add("unresolved_tool", observation.ActionID, observation.TurnID, fmt.Sprintf("tool observation %q has status=%q result_seen=%t", observation.ID, observation.Status, observation.ResultSeen))
		}
	}
}

func (s *correctionFindingSet) addProcessFindings() {
	c := s.correction
	process := c.Process
	if process == nil {
		return
	}
	s.addChecks([]findingCheck{
		{process.DescendantsAlive, "orphan_process", c.ReplacementActionID, c.CorrectionTurnID, "a descendant process remained alive after the corrected run"},
		{!process.ChildWaited, "child_not_reaped", c.ReplacementActionID, c.CorrectionTurnID, "the shipped child was not reaped"},
		{!process.InputClosed || !process.OutputClosed, "stream_not_closed", c.ReplacementActionID, c.CorrectionTurnID, fmt.Sprintf("process streams closed input=%t output=%t", process.InputClosed, process.OutputClosed)},
		{process.ExitClassification != "normal", "unclean_process_termination", c.ReplacementActionID, c.CorrectionTurnID, fmt.Sprintf("corrected run exit classification was %q", process.ExitClassification)},
	})
}

func isCorrectionCancelledStatus(status string) bool {
	return status == "cancelled" || status == "canceled"
}

func isCorrectionCompletedStatus(status string) bool {
	return status == "completed"
}
