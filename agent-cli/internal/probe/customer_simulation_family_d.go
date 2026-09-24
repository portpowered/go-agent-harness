package probe

import (
	"fmt"
	"strings"
	"time"
)

const (
	FamilyDScenarioSIGINTID  = "family-d-sigint"
	FamilyDScenarioNaturalID = "family-d-natural"
	FamilyDActionID          = "active-customer-request"
	FamilyDActiveTurnID      = "turn-1"
	FamilyDActiveResponseID  = "response-family-d"
	FamilyDResponseText      = "The request is complete, and I am ready to stop."
)

// TerminationEvidence is the explicit ledger for Family D's two process
// endings. ProcessFacts carries the low-level lifecycle facts; this record
// preserves the customer-visible response boundary and satisfaction decision
// that caused the run to end.
type TerminationEvidence struct {
	Method                  TerminationMethod `json:"method"`
	ActiveActionID          string            `json:"active_action_id"`
	ActiveTurnID            string            `json:"active_turn_id"`
	ActiveResponseID        string            `json:"active_response_id"`
	ActiveResponseStatus    string            `json:"active_response_status"`
	ActiveResponseStartedAt time.Duration     `json:"active_response_started_at"`
	ActiveResponseEndedAt   time.Duration     `json:"active_response_ended_at"`
	SatisfactionDeclared    bool              `json:"satisfaction_declared"`
	SatisfactionAt          time.Duration     `json:"satisfaction_at,omitempty"`
	SignalSent              bool              `json:"signal_sent"`
	Signal                  string            `json:"signal,omitempty"`
	SignalAt                time.Duration     `json:"signal_at,omitempty"`
	OutstandingToolIDs      []string          `json:"outstanding_tool_ids,omitempty"`
	UnresolvedActionIDs     []string          `json:"unresolved_action_ids,omitempty"`
	Process                 ProcessFacts      `json:"process"`
	EvidenceRefs            []string          `json:"evidence_refs"`
}

func (e TerminationEvidence) Validate(scenario CustomerScenario) error {
	if err := e.validateShape(scenario); err != nil {
		return err
	}
	if err := e.validateSignal(); err != nil {
		return err
	}
	if err := e.validateEnding(); err != nil {
		return err
	}
	if err := e.validateIdentityAndProcess(); err != nil {
		return err
	}
	if len(e.EvidenceRefs) == 0 {
		return contractFieldError(ErrMissingEvidence, "termination.evidence_refs", "must not be empty")
	}
	if err := validateUniqueNonEmptyStrings("termination.outstanding_tool_ids", e.OutstandingToolIDs); err != nil {
		return err
	}
	return validateUniqueNonEmptyStrings("termination.unresolved_action_ids", e.UnresolvedActionIDs)
}

const (
	terminationStatusCompleted   = "completed"
	terminationStatusCancelled   = "cancelled"
	terminationStatusInterrupted = "interrupted"
	terminationStatusIncomplete  = "incomplete"
)

func (e TerminationEvidence) validateShape(scenario CustomerScenario) error {
	if err := scenario.Validate(); err != nil {
		return err
	}
	if scenario.Family != ScenarioFamilyD {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination", "requires a Family D scenario")
	}
	if e.Method != scenario.Termination {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.method", "does not match the scenario termination method")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"active_action_id", e.ActiveActionID},
		{"active_turn_id", e.ActiveTurnID},
		{"active_response_id", e.ActiveResponseID},
		{"active_response_status", e.ActiveResponseStatus},
	} {
		if strings.TrimSpace(field.value) == "" {
			return contractFieldError(ErrInvalidCustomerEvidence, "termination."+field.name, "must not be empty")
		}
	}
	switch e.ActiveResponseStatus {
	case terminationStatusCompleted, terminationStatusCancelled, terminationStatusInterrupted, terminationStatusIncomplete:
	default:
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_response_status", fmt.Sprintf("%q is invalid", e.ActiveResponseStatus))
	}
	if e.ActiveResponseStartedAt < 0 || e.ActiveResponseEndedAt < e.ActiveResponseStartedAt || e.SatisfactionAt < 0 || e.SignalAt < 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination", "timestamps must be non-negative and ordered")
	}
	if e.ActiveResponseStartedAt == e.ActiveResponseEndedAt && e.ActiveResponseStatus != terminationStatusIncomplete {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_response_ended_at", "must follow response start")
	}
	return nil
}

func (e TerminationEvidence) validateSignal() error {
	if !e.SignalSent {
		if e.Signal != "" || e.SignalAt != 0 {
			return contractFieldError(ErrInvalidCustomerEvidence, "termination.signal", "must be empty when no signal was sent")
		}
		return nil
	}
	if e.Method != TerminationSIGINT {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.signal_sent", "only SIGINT runs may send a signal")
	}
	if e.Signal != duplexSIGINTName {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.signal", "must be SIGINT")
	}
	if e.SignalAt < e.ActiveResponseStartedAt || e.SignalAt > e.ActiveResponseEndedAt {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.signal_at", "must fall within the active response interval")
	}
	return nil
}

// validateEnding checks the response status and satisfaction decision
// against the selected termination method.
func (e TerminationEvidence) validateEnding() error {
	if e.Method == TerminationSIGINT {
		return e.validateSIGINTEnding()
	}
	if e.SignalSent {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.signal_sent", "natural completion must not send SIGINT")
	}
	if e.ActiveResponseStatus == terminationStatusIncomplete {
		if e.SatisfactionDeclared || e.SatisfactionAt != 0 {
			return contractFieldError(ErrInvalidCustomerEvidence, "termination.satisfaction_declared", "an incomplete response cannot declare satisfaction")
		}
		return nil
	}
	if !e.SatisfactionDeclared || e.SatisfactionAt < e.ActiveResponseEndedAt {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.satisfaction_declared", "natural completion needs a satisfaction decision after the response")
	}
	if e.ActiveResponseStatus != terminationStatusCompleted {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_response_status", "natural completion needs a completed response")
	}
	return nil
}

func (e TerminationEvidence) validateSIGINTEnding() error {
	if e.SatisfactionDeclared {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.satisfaction_declared", "SIGINT must not be reported as natural satisfaction")
	}
	if e.SignalSent {
		if e.ActiveResponseStatus != terminationStatusCancelled && e.ActiveResponseStatus != terminationStatusInterrupted {
			return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_response_status", "SIGINT must interrupt the active response")
		}
		return nil
	}
	if e.ActiveResponseStatus != terminationStatusIncomplete {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_response_status", "an unrecorded SIGINT must leave the response incomplete")
	}
	return nil
}

func (e TerminationEvidence) validateIdentityAndProcess() error {
	if e.ActiveActionID != FamilyDActionID {
		return contractFieldError(ErrUnknownActionIntent, "termination.active_action_id", e.ActiveActionID)
	}
	if e.ActiveTurnID != FamilyDActiveTurnID {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_turn_id", "must identify the active Family D turn")
	}
	if e.ActiveResponseID != FamilyDActiveResponseID {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_response_id", "must identify the active Family D response")
	}
	if err := e.Process.validate("termination.process"); err != nil {
		return err
	}
	if e.Process.SignalSent != e.SignalSent || e.Process.Signal != e.Signal {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.process", "process signal facts must match termination evidence")
	}
	if e.Process.SignalAt != e.SignalAt {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.process.signal_at", "process signal timestamp must match termination evidence")
	}
	if e.SignalSent && e.SignalAt == 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.signal_at", "sent SIGINT needs a positive timestamp")
	}
	if !e.SatisfactionDeclared && e.SatisfactionAt != 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.satisfaction_at", "must be zero when satisfaction was not declared")
	}
	return nil
}

// FamilyDSpokenScript returns the one natural customer utterance used by
// either selectable termination run. SIGINT is scheduled from observed
// product output, not smuggled into the spoken words.
func FamilyDSpokenScript() []CustomerScriptTurn {
	return []CustomerScriptTurn{{
		ActionID: FamilyDActionID,
		Text:     "Please begin this request and keep working while I listen; I will tell you when I am satisfied.",
	}}
}

// NewFamilyDScenario declares one of the two selectable termination shapes.
// The action intentionally has no side effect: its absent checkpoint proves a
// cancellation did not leave a hidden artifact, while the natural run still
// needs a truthful completed response.
func NewFamilyDScenario(method TerminationMethod) CustomerScenario {
	id := FamilyDScenarioNaturalID
	interruption := InterruptionTrigger{Kind: InterruptionNone}
	requireConfirmation := true
	requiredText := []string{"request is complete", "ready to stop"}
	if method == TerminationSIGINT {
		id = FamilyDScenarioSIGINTID
		interruption = InterruptionTrigger{
			Kind: InterruptionDuringOutput, ActionID: FamilyDActionID,
			Description:    "Send SIGINT after product audio starts and before the active response reaches its terminal event.",
			BeforeTerminal: true,
		}
		requireConfirmation = false
		requiredText = nil
	}
	return CustomerScenario{
		SchemaVersion:  CustomerScenarioSchemaVersion,
		ID:             id,
		Name:           "Customer termination " + string(method),
		Family:         ScenarioFamilyD,
		Persona:        "A listener who either becomes satisfied or stops the session",
		Goal:           "End an active conversational response with an explicit, clean lifecycle",
		WordingFreedom: "Use natural wording while preserving the active-response boundary and the selected termination method.",
		TextSeed:       "The request has no filesystem side effect. For SIGINT, interrupt the active response; for natural completion, wait for the completed response and declare satisfaction.",
		ImageEvents:    []ScenarioImageEvent{},
		Actions: []ActionIntent{{
			ID: FamilyDActionID, Intent: "Handle the active customer request until the selected termination boundary.",
			AllowedDispositions:     []TerminalDisposition{DispositionCompleted, DispositionFailed, DispositionCancelled},
			PartialSideEffectPolicy: PartialSideEffectsForbid,
			SideEffectRule:          "Do not create a filesystem artifact; cancellation preserves the empty sandbox and natural completion preserves the empty sandbox.",
			Oracle: ActionOracle{
				Description:         "termination/marker.txt remains absent, and the product response matches the selected termination shape.",
				RequireConfirmation: requireConfirmation,
				RequiredText:        requiredText,
				Checkpoints:         []FilesystemExpectation{{Path: "termination/marker.txt", Type: FileTypeAbsent}},
			},
		}},
		Sandbox:      SandboxSpec{Name: "fresh-family-d-sandbox", Root: ".", Fresh: true},
		Interruption: interruption,
		Patience: PatienceThresholds{
			ListenBeforeFollowUp: 250 * time.Millisecond, ResponseStart: time.Second, InProgressWork: 2 * time.Second,
			Reprompt: 3 * time.Second, AbsoluteDeadAir: 10 * time.Second, MaxReprompts: 1,
		},
		Termination: method,
		Deadline:    30 * time.Second,
	}
}

func terminationEvidenceRefs() []string {
	return []string{
		"events/termination.json",
		"process.json",
		"transcripts/product.jsonl",
		"tool-observations.jsonl",
	}
}

// FamilyDTerminationEvidenceRefs returns the canonical artifact references
// used by action and validator records for either termination shape.
func FamilyDTerminationEvidenceRefs() []string {
	return terminationEvidenceRefs()
}

// EvaluateCustomerSimulationTermination applies the ordinary action oracle
// and then checks the selected process ending. It treats an explicitly
// cancelled active action as valid only for SIGINT; all other unresolved,
// orphaned, un-reaped, or misclassified endings remain BROKEN findings.
func EvaluateCustomerSimulationTermination(
	scenario CustomerScenario,
	actionResults []ActionResult,
	checkpoints []FilesystemCheckpoint,
	toolObservations []ToolObservation,
	productTranscript []TranscriptEvent,
	evidence TerminationEvidence,
) (MechanicalVerdict, error) {
	if err := scenario.Validate(); err != nil {
		return MechanicalVerdict{}, err
	}
	if err := evidence.Validate(scenario); err != nil {
		return MechanicalVerdict{}, err
	}
	mechanical, err := EvaluateCustomerSimulation(scenario, actionResults, checkpoints, toolObservations, productTranscript)
	if err != nil {
		return mechanical, err
	}

	set := terminationFindingSet{
		oracleFindingSet: oracleFindingSet{findings: make([]MechanicalFinding, 0, len(mechanical.Findings)+terminationFindingCapacity), evidenceRefs: terminationEvidenceRefs},
		scenario:         scenario,
		evidence:         evidence,
	}
	for _, finding := range mechanical.Findings {
		if scenario.Termination == TerminationSIGINT && finding.ActionID == FamilyDActionID && finding.Code == "action_not_completed" {
			if terminationActionCancelled(mechanical.ActionResults) {
				continue
			}
		}
		set.findings = append(set.findings, finding)
	}
	set.addResponseFindings(mechanical.ActionResults)
	set.addProcessFindings()
	set.addUnresolvedFindings(toolObservations)
	if strings.TrimSpace(transcriptTextForTurn(productTranscript, evidence.ActiveTurnID)) == "" {
		set.add("active_response_missing", FamilyDActionID, evidence.ActiveTurnID, "no product transcript was recorded for the active response")
	}

	findings := set.findings
	mechanical.Findings = findings
	mechanical.Pass = len(findings) == 0
	mechanical.Summary = mechanicalSummary(len(findings), len(scenario.Actions))
	if err := mechanical.validate(scenario, "mechanical_verdict"); err != nil {
		return mechanical, err
	}
	return mechanical, nil
}

const terminationFindingCapacity = 8

// terminationFindingSet collects Family D findings in evaluation order.
type terminationFindingSet struct {
	oracleFindingSet
	scenario CustomerScenario
	evidence TerminationEvidence
}

func (s *terminationFindingSet) addResponseFindings(results []ActionResult) {
	scenario, e := s.scenario, s.evidence
	sigint := scenario.Termination == TerminationSIGINT
	want := DispositionCompleted
	if sigint {
		want = DispositionCancelled
	}
	result, resolved := findActionResult(results, FamilyDActionID)
	s.addChecks([]findingCheck{
		{e.Method != scenario.Termination, "termination_method_mismatch", FamilyDActionID, e.ActiveTurnID, fmt.Sprintf("evidence method is %q, scenario requires %q", e.Method, scenario.Termination)},
		{!resolved, "termination_action_unresolved", FamilyDActionID, e.ActiveTurnID, "the active action has no terminal disposition"},
		{resolved && result.Disposition != want, "unexpected_termination_disposition", FamilyDActionID, result.TurnID, fmt.Sprintf("selected %q termination requires action disposition %q, got %q", scenario.Termination, want, result.Disposition)},
		{e.ActiveActionID != FamilyDActionID, "active_action_mismatch", e.ActiveActionID, e.ActiveTurnID, fmt.Sprintf("termination evidence names active action %q", e.ActiveActionID)},
		{e.ActiveResponseStartedAt >= e.ActiveResponseEndedAt, "active_response_not_observed", FamilyDActionID, e.ActiveTurnID, "active response has no positive observable interval"},
		{sigint && (!e.SignalSent || e.Signal != duplexSIGINTName), "sigint_not_recorded", FamilyDActionID, e.ActiveTurnID, "SIGINT termination did not record a sent SIGINT"},
		{sigint && (e.SignalAt < e.ActiveResponseStartedAt || e.SignalAt > e.ActiveResponseEndedAt), "sigint_outside_active_response", FamilyDActionID, e.ActiveTurnID, "SIGINT did not occur inside the active response interval"},
		{sigint && e.ActiveResponseStatus != terminationStatusCancelled && e.ActiveResponseStatus != terminationStatusInterrupted, "sigint_response_not_interrupted", FamilyDActionID, e.ActiveTurnID, fmt.Sprintf("active response ended with status %q", e.ActiveResponseStatus)},
		{!sigint && (e.SignalSent || e.Signal != "" || e.SignalAt != 0), "natural_completion_signalled", FamilyDActionID, e.ActiveTurnID, "natural completion recorded a signal"},
		{!sigint && (e.ActiveResponseStatus != terminationStatusCompleted || !e.SatisfactionDeclared), "natural_completion_not_satisfied", FamilyDActionID, e.ActiveTurnID, "natural completion lacks a completed response and satisfaction declaration"},
	})
}

func (s *terminationFindingSet) addProcessFindings() {
	termination, turnID := s.scenario.Termination, s.evidence.ActiveTurnID
	process := s.evidence.Process
	wantClassification := "normal"
	if termination == TerminationSIGINT {
		wantClassification = "sigint"
	}
	s.addChecks([]findingCheck{
		{process.ExitClassification != wantClassification, "exit_classification_mismatch", FamilyDActionID, turnID, fmt.Sprintf("process exit classification is %q, want %q", process.ExitClassification, wantClassification)},
		{termination == TerminationSIGINT && !process.SignalSent, "sigint_signal_missing", FamilyDActionID, turnID, "process facts do not show SIGINT was sent"},
		{termination == TerminationNatural && process.SignalSent, "natural_completion_signal", FamilyDActionID, turnID, "process facts show a signal during natural completion"},
		{!process.ChildWaited, "child_not_reaped", FamilyDActionID, turnID, "the shipped child was not reaped"},
		{process.WaitCount != 1, "child_reap_count", FamilyDActionID, turnID, fmt.Sprintf("child wait was invoked %d times, want exactly once", process.WaitCount)},
		{process.DescendantsAlive, "orphan_process", FamilyDActionID, turnID, "a child descendant remained alive after termination"},
		{!process.InputClosed || !process.OutputClosed, "stream_not_closed", FamilyDActionID, turnID, fmt.Sprintf("PCM boundaries closed input=%t output=%t", process.InputClosed, process.OutputClosed)},
		{termination == TerminationNatural && !process.InputFinished, "natural_input_incomplete", FamilyDActionID, turnID, "natural completion ended before the incremental input stream finished"},
	})
}

func (s *terminationFindingSet) addUnresolvedFindings(toolObservations []ToolObservation) {
	turnID := s.evidence.ActiveTurnID
	for _, toolID := range s.evidence.OutstandingToolIDs {
		s.add("unresolved_tool", FamilyDActionID, turnID, fmt.Sprintf("tool %q remained outstanding at termination", toolID))
	}
	for _, actionID := range s.evidence.UnresolvedActionIDs {
		s.add("unresolved_action", actionID, turnID, "an action remained unresolved at termination")
	}
	for _, observation := range toolObservations {
		if observation.Status == "started" || !observation.ResultSeen {
			s.add("unresolved_tool", observation.ActionID, observation.TurnID, fmt.Sprintf("tool observation %q has status=%q result_seen=%t", observation.ID, observation.Status, observation.ResultSeen))
		}
	}
}

func terminationActionCancelled(results []ActionResult) bool {
	result, ok := findActionResult(results, FamilyDActionID)
	return ok && result.Disposition == DispositionCancelled
}
