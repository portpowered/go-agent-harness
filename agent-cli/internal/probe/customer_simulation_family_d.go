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
	if e.ActiveResponseStatus != "completed" && e.ActiveResponseStatus != "cancelled" && e.ActiveResponseStatus != "interrupted" && e.ActiveResponseStatus != "incomplete" {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_response_status", fmt.Sprintf("%q is invalid", e.ActiveResponseStatus))
	}
	if e.ActiveResponseStartedAt < 0 || e.ActiveResponseEndedAt < e.ActiveResponseStartedAt || e.SatisfactionAt < 0 || e.SignalAt < 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination", "timestamps must be non-negative and ordered")
	}
	if e.ActiveResponseStartedAt == e.ActiveResponseEndedAt && e.ActiveResponseStatus != "incomplete" {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_response_ended_at", "must follow response start")
	}
	if e.SignalSent {
		if e.Method != TerminationSIGINT {
			return contractFieldError(ErrInvalidCustomerEvidence, "termination.signal_sent", "only SIGINT runs may send a signal")
		}
		if e.Signal != duplexSIGINTName {
			return contractFieldError(ErrInvalidCustomerEvidence, "termination.signal", "must be SIGINT")
		}
		if e.SignalAt < e.ActiveResponseStartedAt || e.SignalAt > e.ActiveResponseEndedAt {
			return contractFieldError(ErrInvalidCustomerEvidence, "termination.signal_at", "must fall within the active response interval")
		}
	} else if e.Signal != "" || e.SignalAt != 0 {
		return contractFieldError(ErrInvalidCustomerEvidence, "termination.signal", "must be empty when no signal was sent")
	}
	if e.Method == TerminationSIGINT {
		if e.SatisfactionDeclared {
			return contractFieldError(ErrInvalidCustomerEvidence, "termination.satisfaction_declared", "SIGINT must not be reported as natural satisfaction")
		}
		if e.SignalSent {
			if e.ActiveResponseStatus != "cancelled" && e.ActiveResponseStatus != "interrupted" {
				return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_response_status", "SIGINT must interrupt the active response")
			}
		} else if e.ActiveResponseStatus != "incomplete" {
			return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_response_status", "an unrecorded SIGINT must leave the response incomplete")
		}
	} else {
		if e.SignalSent {
			return contractFieldError(ErrInvalidCustomerEvidence, "termination.signal_sent", "natural completion must not send SIGINT")
		}
		if e.ActiveResponseStatus == "incomplete" {
			if e.SatisfactionDeclared || e.SatisfactionAt != 0 {
				return contractFieldError(ErrInvalidCustomerEvidence, "termination.satisfaction_declared", "an incomplete response cannot declare satisfaction")
			}
		} else {
			if !e.SatisfactionDeclared || e.SatisfactionAt < e.ActiveResponseEndedAt {
				return contractFieldError(ErrInvalidCustomerEvidence, "termination.satisfaction_declared", "natural completion needs a satisfaction decision after the response")
			}
			if e.ActiveResponseStatus != "completed" {
				return contractFieldError(ErrInvalidCustomerEvidence, "termination.active_response_status", "natural completion needs a completed response")
			}
		}
	}
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
	if len(e.EvidenceRefs) == 0 {
		return contractFieldError(ErrMissingEvidence, "termination.evidence_refs", "must not be empty")
	}
	if err := validateUniqueNonEmptyStrings("termination.outstanding_tool_ids", e.OutstandingToolIDs); err != nil {
		return err
	}
	return validateUniqueNonEmptyStrings("termination.unresolved_action_ids", e.UnresolvedActionIDs)
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

// ProcessFactsFromDuplexResult keeps the runner's lifecycle fields and the
// evidence schema in lockstep for both termination shapes.
func ProcessFactsFromDuplexResult(result DuplexRunResult) ProcessFacts {
	return ProcessFacts{
		PID:                result.PID,
		ExitCode:           result.ExitCode,
		ExitClassification: result.ExitClassification,
		Signal:             result.Signal,
		SignalSent:         result.SignalSent,
		SignalAt:           result.SignalAt,
		ChildWaited:        result.ChildWaited,
		WaitCount:          result.WaitCount,
		DescendantsAlive:   result.DescendantsAlive,
		InputClosed:        result.InputClosed,
		InputFinished:      result.InputFinished,
		OutputClosed:       result.StdoutClosed && result.StderrClosed,
		StartedAt:          0,
		EndedAt:            result.Duration,
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

	findings := make([]MechanicalFinding, 0, len(mechanical.Findings)+8)
	for _, finding := range mechanical.Findings {
		if scenario.Termination == TerminationSIGINT && finding.ActionID == FamilyDActionID && finding.Code == "action_not_completed" {
			if terminationActionCancelled(mechanical.ActionResults) {
				continue
			}
		}
		findings = append(findings, finding)
	}
	addFinding := func(code, actionID, turnID, message string) {
		findings = append(findings, MechanicalFinding{
			Code: code, ActionID: actionID, TurnID: turnID, Message: message,
			EvidenceRefs: terminationEvidenceRefs(),
		})
	}

	if evidence.Method != scenario.Termination {
		addFinding("termination_method_mismatch", FamilyDActionID, evidence.ActiveTurnID, fmt.Sprintf("evidence method is %q, scenario requires %q", evidence.Method, scenario.Termination))
	}
	if result, ok := findActionResult(mechanical.ActionResults, FamilyDActionID); !ok {
		addFinding("termination_action_unresolved", FamilyDActionID, evidence.ActiveTurnID, "the active action has no terminal disposition")
	} else {
		want := DispositionCompleted
		if scenario.Termination == TerminationSIGINT {
			want = DispositionCancelled
		}
		if result.Disposition != want {
			addFinding("unexpected_termination_disposition", FamilyDActionID, result.TurnID, fmt.Sprintf("selected %q termination requires action disposition %q, got %q", scenario.Termination, want, result.Disposition))
		}
	}
	if evidence.ActiveActionID != FamilyDActionID {
		addFinding("active_action_mismatch", evidence.ActiveActionID, evidence.ActiveTurnID, fmt.Sprintf("termination evidence names active action %q", evidence.ActiveActionID))
	}
	if evidence.ActiveResponseStartedAt >= evidence.ActiveResponseEndedAt {
		addFinding("active_response_not_observed", FamilyDActionID, evidence.ActiveTurnID, "active response has no positive observable interval")
	}
	if scenario.Termination == TerminationSIGINT {
		if !evidence.SignalSent || evidence.Signal != duplexSIGINTName {
			addFinding("sigint_not_recorded", FamilyDActionID, evidence.ActiveTurnID, "SIGINT termination did not record a sent SIGINT")
		}
		if evidence.SignalAt < evidence.ActiveResponseStartedAt || evidence.SignalAt > evidence.ActiveResponseEndedAt {
			addFinding("sigint_outside_active_response", FamilyDActionID, evidence.ActiveTurnID, "SIGINT did not occur inside the active response interval")
		}
		if evidence.ActiveResponseStatus != "cancelled" && evidence.ActiveResponseStatus != "interrupted" {
			addFinding("sigint_response_not_interrupted", FamilyDActionID, evidence.ActiveTurnID, fmt.Sprintf("active response ended with status %q", evidence.ActiveResponseStatus))
		}
	} else {
		if evidence.SignalSent || evidence.Signal != "" || evidence.SignalAt != 0 {
			addFinding("natural_completion_signalled", FamilyDActionID, evidence.ActiveTurnID, "natural completion recorded a signal")
		}
		if evidence.ActiveResponseStatus != "completed" || !evidence.SatisfactionDeclared {
			addFinding("natural_completion_not_satisfied", FamilyDActionID, evidence.ActiveTurnID, "natural completion lacks a completed response and satisfaction declaration")
		}
	}

	process := evidence.Process
	wantClassification := "normal"
	if scenario.Termination == TerminationSIGINT {
		wantClassification = "sigint"
	}
	if process.ExitClassification != wantClassification {
		addFinding("exit_classification_mismatch", FamilyDActionID, evidence.ActiveTurnID, fmt.Sprintf("process exit classification is %q, want %q", process.ExitClassification, wantClassification))
	}
	if scenario.Termination == TerminationSIGINT && !process.SignalSent {
		addFinding("sigint_signal_missing", FamilyDActionID, evidence.ActiveTurnID, "process facts do not show SIGINT was sent")
	}
	if scenario.Termination == TerminationNatural && process.SignalSent {
		addFinding("natural_completion_signal", FamilyDActionID, evidence.ActiveTurnID, "process facts show a signal during natural completion")
	}
	if !process.ChildWaited {
		addFinding("child_not_reaped", FamilyDActionID, evidence.ActiveTurnID, "the shipped child was not reaped")
	}
	if process.WaitCount != 1 {
		addFinding("child_reap_count", FamilyDActionID, evidence.ActiveTurnID, fmt.Sprintf("child wait was invoked %d times, want exactly once", process.WaitCount))
	}
	if process.DescendantsAlive {
		addFinding("orphan_process", FamilyDActionID, evidence.ActiveTurnID, "a child descendant remained alive after termination")
	}
	if !process.InputClosed || !process.OutputClosed {
		addFinding("stream_not_closed", FamilyDActionID, evidence.ActiveTurnID, fmt.Sprintf("PCM boundaries closed input=%t output=%t", process.InputClosed, process.OutputClosed))
	}
	if scenario.Termination == TerminationNatural && !process.InputFinished {
		addFinding("natural_input_incomplete", FamilyDActionID, evidence.ActiveTurnID, "natural completion ended before the incremental input stream finished")
	}
	for _, toolID := range evidence.OutstandingToolIDs {
		addFinding("unresolved_tool", FamilyDActionID, evidence.ActiveTurnID, fmt.Sprintf("tool %q remained outstanding at termination", toolID))
	}
	for _, actionID := range evidence.UnresolvedActionIDs {
		addFinding("unresolved_action", actionID, evidence.ActiveTurnID, "an action remained unresolved at termination")
	}
	for _, observation := range toolObservations {
		if observation.Status == "started" || !observation.ResultSeen {
			addFinding("unresolved_tool", observation.ActionID, observation.TurnID, fmt.Sprintf("tool observation %q has status=%q result_seen=%t", observation.ID, observation.Status, observation.ResultSeen))
		}
	}
	if strings.TrimSpace(transcriptTextForTurn(productTranscript, evidence.ActiveTurnID)) == "" {
		addFinding("active_response_missing", FamilyDActionID, evidence.ActiveTurnID, "no product transcript was recorded for the active response")
	}

	mechanical.Findings = findings
	mechanical.Pass = len(findings) == 0
	mechanical.Summary = mechanicalSummary(len(findings), len(scenario.Actions))
	if err := mechanical.validate(scenario, "mechanical_verdict"); err != nil {
		return mechanical, err
	}
	return mechanical, nil
}

func terminationActionCancelled(results []ActionResult) bool {
	result, ok := findActionResult(results, FamilyDActionID)
	return ok && result.Disposition == DispositionCancelled
}

func findActionResult(results []ActionResult, actionID string) (ActionResult, bool) {
	for _, result := range results {
		if result.ActionID == actionID {
			return result, true
		}
	}
	return ActionResult{}, false
}
