package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

const (
	CustomerSimulationValidatorRequestVersion = "customer-simulation-validator.v1"
	DefaultCustomerSimulationValidatorTimeout = 45 * time.Second
)

var (
	ErrValidatorUnavailable       = errors.New("customer simulation validator is unavailable")
	ErrValidatorMalformedResponse = errors.New("customer simulation validator returned a malformed response")
	ErrValidatorTimeout           = errors.New("customer simulation validator timed out")
	ErrValidatorCancelled         = errors.New("customer simulation validator was cancelled")
	ErrValidatorInputInvalid      = errors.New("customer simulation validator input is invalid")
	ErrValidatorFinalization      = errors.New("customer simulation validator requires finalized evidence")
	ErrValidatorEvidenceMismatch  = errors.New("customer simulation validator evidence does not match finalized artifacts")
)

// CustomerSimulationValidatorAgent is the intentionally small seam between
// the evidence runner and an independent post-run model. Implementations
// return the raw JSON response so the runner, rather than the model adapter,
// owns strict schema validation and fail-closed behavior.
type CustomerSimulationValidatorAgent interface {
	ValidateCustomerSimulation(context.Context, CustomerSimulationValidatorRequest) ([]byte, error)
}

// CustomerSimulationValidatorAgentFunc adapts a function to the validator
// agent interface. It is useful for hermetic fake-validator tests.
type CustomerSimulationValidatorAgentFunc func(context.Context, CustomerSimulationValidatorRequest) ([]byte, error)

func (f CustomerSimulationValidatorAgentFunc) ValidateCustomerSimulation(ctx context.Context, request CustomerSimulationValidatorRequest) ([]byte, error) {
	if f == nil {
		return nil, ErrValidatorUnavailable
	}
	return f(ctx, request)
}

type CustomerSimulationValidatorCriterion struct {
	ID          string `json:"id"`
	Requirement string `json:"requirement"`
}

type CustomerSimulationValidatorRubric struct {
	Version  string                                 `json:"version"`
	Criteria []CustomerSimulationValidatorCriterion `json:"criteria"`
}

// DefaultCustomerSimulationValidatorRubric is the explicit review rubric
// sent with every validator request. The model may summarize evidence in its
// own words, but it cannot omit any of these customer-facing concerns.
func DefaultCustomerSimulationValidatorRubric() CustomerSimulationValidatorRubric {
	return CustomerSimulationValidatorRubric{
		Version: CustomerSimulationValidatorRequestVersion,
		Criteria: []CustomerSimulationValidatorCriterion{
			{ID: "iterative_context", Requirement: "Judge whether the product kept the customer's context across ordered turns and built on prior work."},
			{ID: "truthfulness", Requirement: "Judge whether confirmations and the final summary agree with tool results and observed side effects, without claiming absent work."},
			{ID: "correction_interruption", Requirement: "Judge whether a mid-stream correction was heard, the original action has an explicit disposition, and replacement work is independently complete."},
			{ID: "mixed_modal_grounding", Requirement: "When image evidence applies, judge whether the later spoken task is grounded in the actual fixture and supported boundary; an unsupported product seam remains BROKEN."},
			{ID: "patience_dead_air", Requirement: "Judge whether listening, progress, bounded re-prompting, dead air, and timeout behavior match the declared patience policy."},
			{ID: "cancellation_cleanup", Requirement: "Judge whether cancellation or natural completion closed both streams, reaped the child, and left no tool or descendant process alive."},
			{ID: "unresolved_work", Requirement: "Judge whether any action, tool continuation, unresolved request, or required evidence remained outstanding at termination."},
			{ID: "wrong_reason_success", Requirement: "Reject success for the wrong reason, including a plausible final state that hides an incorrect intermediate history or missing causal evidence."},
		},
	}
}

func (r CustomerSimulationValidatorRubric) Validate() error {
	if r.Version != CustomerSimulationValidatorRequestVersion {
		return contractFieldError(ErrInvalidValidatorVerdict, "validator_request.rubric.version", "must identify the supported validator rubric")
	}
	if len(r.Criteria) == 0 {
		return contractFieldError(ErrMissingEvidence, "validator_request.rubric.criteria", "must not be empty")
	}
	seen := make(map[string]struct{}, len(r.Criteria))
	for index, criterion := range r.Criteria {
		field := fmt.Sprintf("validator_request.rubric.criteria[%d]", index)
		if strings.TrimSpace(criterion.ID) == "" || strings.TrimSpace(criterion.Requirement) == "" {
			return contractFieldError(ErrInvalidValidatorVerdict, field, "id and requirement must not be empty")
		}
		if _, exists := seen[criterion.ID]; exists {
			return contractFieldError(ErrInvalidValidatorVerdict, field+".id", "must be unique")
		}
		seen[criterion.ID] = struct{}{}
	}
	return nil
}

// CustomerSimulationValidatorRequest is the complete, relative-path
// evidence view given to the independent validator. Manifest hashes let a
// gateway-backed validator see artifact integrity without receiving the
// private bundle root or raw credentials.
type CustomerSimulationValidatorRequest struct {
	Version  string                            `json:"version"`
	Rubric   CustomerSimulationValidatorRubric `json:"rubric"`
	Input    ValidatorInput                    `json:"input"`
	Manifest *CustomerEvidenceManifest         `json:"manifest,omitempty"`
}

func (r CustomerSimulationValidatorRequest) Validate() error {
	if r.Version != CustomerSimulationValidatorRequestVersion {
		return contractFieldError(ErrInvalidValidatorVerdict, "validator_request.version", "must identify the supported validator request")
	}
	if err := r.Rubric.Validate(); err != nil {
		return err
	}
	if err := r.Input.Validate(); err != nil {
		return err
	}
	if r.Manifest != nil {
		if err := r.Manifest.Validate(); err != nil {
			return err
		}
		if r.Manifest.ScenarioID != r.Input.Scenario.ID {
			return contractFieldError(ErrInvalidCustomerEvidence, "validator_request.manifest.scenario_id", "must identify the input scenario")
		}
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal validator request for credential check: %w", err)
	}
	if credentialPattern.Match(encoded) {
		return ErrCredentialInEvidence
	}
	return nil
}

// Validate exposes the same strict input contract used by bundle validation
// to callers that construct an independent validator request directly.
func (i ValidatorInput) Validate() error {
	if err := i.Scenario.Validate(); err != nil {
		return err
	}
	return i.validate(i.Scenario, "validator_input")
}

type CustomerSimulationValidatorStatus string

const (
	ValidatorStatusWorked                 CustomerSimulationValidatorStatus = "worked"
	ValidatorStatusBroken                 CustomerSimulationValidatorStatus = "broken"
	ValidatorStatusUnavailable            CustomerSimulationValidatorStatus = "unavailable"
	ValidatorStatusMalformed              CustomerSimulationValidatorStatus = "malformed"
	ValidatorStatusTimeout                CustomerSimulationValidatorStatus = "timeout"
	ValidatorStatusCancelled              CustomerSimulationValidatorStatus = "cancelled"
	ValidatorStatusInputInvalid           CustomerSimulationValidatorStatus = "input_invalid"
	ValidatorStatusMissingEvidence        CustomerSimulationValidatorStatus = "missing_evidence"
	ValidatorStatusMechanicalDisagreement CustomerSimulationValidatorStatus = "mechanical_disagreement"
)

// CustomerSimulationValidatorResult keeps the parsed model judgment and the
// effective judgment separately. This matters when a model says WORKED while
// the mechanical oracle fails: both facts remain reviewable, but only the
// effective BROKEN verdict can be persisted as acceptance evidence.
type CustomerSimulationValidatorResult struct {
	Status       CustomerSimulationValidatorStatus `json:"status"`
	Accepted     bool                              `json:"accepted"`
	Mechanical   MechanicalVerdict                 `json:"mechanical"`
	Verdict      ValidatorVerdict                  `json:"verdict"`
	AgentVerdict *ValidatorVerdict                 `json:"agent_verdict,omitempty"`
	Error        string                            `json:"error,omitempty"`
	RawResponse  []byte                            `json:"-"`
}

func (r CustomerSimulationValidatorResult) Pass() bool {
	return r.Accepted && r.Status == ValidatorStatusWorked && r.Verdict.Verdict == ValidatorWorked
}

type CustomerSimulationValidatorRunner struct {
	Agent   CustomerSimulationValidatorAgent
	Timeout time.Duration
}

// RunCustomerSimulationValidator evaluates already-collected typed evidence.
// For a persisted run, prefer RunFinalizedCustomerSimulationValidator so the
// runner verifies the on-disk manifest and canonical artifacts before the
// agent is invoked.
func RunCustomerSimulationValidator(ctx context.Context, input ValidatorInput, agent CustomerSimulationValidatorAgent, timeout time.Duration) (CustomerSimulationValidatorResult, error) {
	return (CustomerSimulationValidatorRunner{Agent: agent, Timeout: timeout}).Run(ctx, input)
}

func (r CustomerSimulationValidatorRunner) Run(ctx context.Context, input ValidatorInput) (CustomerSimulationValidatorResult, error) {
	return r.run(ctx, input, nil, nil)
}

func (r CustomerSimulationValidatorRunner) run(ctx context.Context, input ValidatorInput, manifest *CustomerEvidenceManifest, allowedRefs map[string]struct{}) (CustomerSimulationValidatorResult, error) {
	baseResult := CustomerSimulationValidatorResult{Mechanical: input.Mechanical}
	if err := input.Validate(); err != nil {
		return customerSimulationValidatorFailure(baseResult, input, ValidatorStatusInputInvalid, fmt.Errorf("%w: %v", ErrValidatorInputInvalid, err), allowedRefs)
	}
	if allowedRefs == nil {
		allowedRefs = evidenceReferenceSet(input.EvidenceRefs)
	}
	request := CustomerSimulationValidatorRequest{
		Version:  CustomerSimulationValidatorRequestVersion,
		Rubric:   DefaultCustomerSimulationValidatorRubric(),
		Input:    input,
		Manifest: manifest,
	}
	if err := request.Validate(); err != nil {
		return customerSimulationValidatorFailure(baseResult, input, ValidatorStatusInputInvalid, fmt.Errorf("%w: %v", ErrValidatorInputInvalid, err), allowedRefs)
	}
	if r.Agent == nil {
		return customerSimulationValidatorFailure(baseResult, input, ValidatorStatusUnavailable, ErrValidatorUnavailable, allowedRefs)
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultCustomerSimulationValidatorTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The validator is a post-run actor. Strip product/simulator cancellation
	// and impose a fresh finite budget so a cancelled child cannot cancel the
	// independent evidence judgment or leave it unbounded.
	validatorContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	raw, err := r.Agent.ValidateCustomerSimulation(validatorContext, request)
	baseResult.RawResponse = append([]byte(nil), raw...)
	if err != nil {
		status := ValidatorStatusUnavailable
		kind := ErrValidatorUnavailable
		if errors.Is(err, ErrValidatorMalformedResponse) {
			status, kind = ValidatorStatusMalformed, ErrValidatorMalformedResponse
		} else if errors.Is(validatorContext.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrValidatorTimeout) {
			status, kind = ValidatorStatusTimeout, ErrValidatorTimeout
		} else if errors.Is(validatorContext.Err(), context.Canceled) || errors.Is(err, context.Canceled) || errors.Is(err, ErrValidatorCancelled) {
			status, kind = ValidatorStatusCancelled, ErrValidatorCancelled
		}
		return customerSimulationValidatorFailure(baseResult, input, status, fmt.Errorf("%w: %v", kind, err), allowedRefs)
	}
	if err := validatorContext.Err(); err != nil {
		kind := ErrValidatorCancelled
		status := ValidatorStatusCancelled
		if errors.Is(err, context.DeadlineExceeded) {
			kind, status = ErrValidatorTimeout, ValidatorStatusTimeout
		}
		return customerSimulationValidatorFailure(baseResult, input, status, kind, allowedRefs)
	}

	verdict, err := ParseCustomerSimulationValidatorVerdict(raw)
	if err != nil {
		return customerSimulationValidatorFailure(baseResult, input, ValidatorStatusMalformed, fmt.Errorf("%w: %v", ErrValidatorMalformedResponse, err), allowedRefs)
	}
	baseResult.AgentVerdict = &verdict
	if err := validateValidatorEvidenceRefs(verdict.EvidenceRefs, allowedRefs); err != nil {
		return customerSimulationValidatorFailure(baseResult, input, ValidatorStatusMalformed, fmt.Errorf("%w: %v", ErrValidatorMalformedResponse, err), allowedRefs)
	}
	if verdict.Verdict == ValidatorBroken {
		baseResult.Status = ValidatorStatusBroken
		baseResult.Verdict = verdict
		return baseResult, nil
	}

	if err := validateMechanicalWorkedClaim(input); err != nil {
		return customerSimulationValidatorFailure(baseResult, input, ValidatorStatusMechanicalDisagreement, err, allowedRefs)
	}
	if err := validateWorkedValidatorEvidence(input); err != nil {
		return customerSimulationValidatorFailure(baseResult, input, ValidatorStatusMissingEvidence, err, allowedRefs)
	}
	baseResult.Status = ValidatorStatusWorked
	baseResult.Accepted = true
	baseResult.Verdict = verdict
	return baseResult, nil
}

func validateMechanicalWorkedClaim(input ValidatorInput) error {
	if !input.Mechanical.Pass || len(input.Mechanical.Findings) != 0 {
		return fmt.Errorf("%w: WORKED requires a passing mechanical verdict without findings", ErrValidatorMechanicalDisagreement)
	}
	actions := make(map[string]ActionIntent, len(input.Scenario.Actions))
	for _, action := range input.Scenario.Actions {
		actions[action.ID] = action
	}
	for _, result := range input.Mechanical.ActionResults {
		if result.Disposition == DispositionFailed {
			return fmt.Errorf("%w: action %q has a failed terminal disposition", ErrValidatorMechanicalDisagreement, result.ActionID)
		}
		action := actions[result.ActionID]
		if action.Oracle.RequireConfirmation && !result.Confirmed {
			return fmt.Errorf("%w: action %q has no truthful customer-visible confirmation", ErrValidatorMechanicalDisagreement, result.ActionID)
		}
	}
	return nil
}

func validateWorkedValidatorEvidence(input ValidatorInput) error {
	for _, evidence := range []struct {
		field string
		count int
	}{
		{field: "customer_transcript", count: len(input.CustomerTranscript)},
		{field: "product_transcript", count: len(input.ProductTranscript)},
		{field: "audio_turn_events", count: len(input.AudioTurnEvents)},
		{field: "filesystem_checkpoints", count: len(input.FilesystemCheckpoints)},
		{field: "evidence_refs", count: len(input.EvidenceRefs)},
	} {
		if evidence.count == 0 {
			return contractFieldError(ErrMissingEvidence, "validator_input."+evidence.field, "WORKED requires this evidence")
		}
	}
	if !input.Process.ChildWaited || input.Process.WaitCount != 1 {
		return contractFieldError(ErrMissingEvidence, "validator_input.process", "WORKED requires exactly one reaped child")
	}
	if input.Process.DescendantsAlive || !input.Process.InputClosed || !input.Process.OutputClosed {
		return contractFieldError(ErrMissingEvidence, "validator_input.process", "WORKED requires closed streams and no live descendants")
	}
	if input.Process.ExitClassification == "timeout" || input.Process.ExitClassification == "failed" || input.Process.ExitClassification == "cancelled" {
		return contractFieldError(ErrMissingEvidence, "validator_input.process.exit_classification", "WORKED cannot use an unsuccessful process classification")
	}
	for _, result := range input.Mechanical.ActionResults {
		if len(result.EvidenceRefs) == 0 {
			return contractFieldError(ErrMissingEvidence, "validator_input.mechanical.action_results", "every action needs evidence references")
		}
	}
	return nil
}

func customerSimulationValidatorFailure(result CustomerSimulationValidatorResult, input ValidatorInput, status CustomerSimulationValidatorStatus, cause error, allowedRefs map[string]struct{}) (CustomerSimulationValidatorResult, error) {
	result.Status = status
	result.Accepted = false
	result.Error = safeValidatorFailureDetail(cause)
	refs := validatorFallbackEvidenceRefs(input, allowedRefs)
	turn := firstValidatorFailureTurn(input)
	result.Verdict = ValidatorVerdict{
		Verdict:          ValidatorBroken,
		FirstFailingTurn: turn,
		Behavior:         "The independent post-run validator did not produce an acceptable WORKED judgment.",
		Violation:        result.Error,
		EvidenceRefs:     refs,
		CustomerImpact:   "The conversation cannot be accepted without a trustworthy independent judgment.",
	}
	if cause == nil {
		return result, nil
	}
	return result, cause
}

func firstValidatorFailureTurn(input ValidatorInput) string {
	for _, finding := range input.Mechanical.Findings {
		if strings.TrimSpace(finding.TurnID) != "" {
			return finding.TurnID
		}
	}
	for _, result := range input.Mechanical.ActionResults {
		if strings.TrimSpace(result.TurnID) != "" {
			return result.TurnID
		}
	}
	for _, event := range input.CustomerTranscript {
		if strings.TrimSpace(event.TurnID) != "" {
			return event.TurnID
		}
	}
	return "validator"
}

func validatorFallbackEvidenceRefs(input ValidatorInput, allowedRefs map[string]struct{}) []string {
	refs := make([]string, 0, len(input.EvidenceRefs))
	seen := map[string]struct{}{}
	for _, ref := range input.EvidenceRefs {
		if strings.TrimSpace(ref) == "" {
			continue
		}
		if allowedRefs != nil {
			if _, ok := allowedRefs[ref]; !ok {
				continue
			}
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	if len(refs) > 0 {
		return refs
	}
	for _, ref := range []string{"mechanical-verdict.json", "process.json", "scenario.json"} {
		if allowedRefs == nil {
			return []string{ref}
		}
		if _, ok := allowedRefs[ref]; ok {
			return []string{ref}
		}
	}
	return []string{"scenario.json"}
}

func evidenceReferenceSet(refs []string) map[string]struct{} {
	set := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref) != "" {
			set[ref] = struct{}{}
		}
	}
	return set
}

func validateValidatorEvidenceRefs(refs []string, allowed map[string]struct{}) error {
	if len(refs) == 0 {
		return contractFieldError(ErrMissingEvidence, "validator_verdict.evidence_refs", "must not be empty")
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref) == "" {
			return contractFieldError(ErrMissingEvidence, "validator_verdict.evidence_refs", "references must not be empty")
		}
		if _, duplicate := seen[ref]; duplicate {
			return contractFieldError(ErrInvalidValidatorVerdict, "validator_verdict.evidence_refs", "references must be unique")
		}
		seen[ref] = struct{}{}
		if allowed != nil {
			if _, available := allowed[ref]; !available {
				return contractFieldError(ErrMissingEvidence, "validator_verdict.evidence_refs", fmt.Sprintf("reference %q was not supplied to the validator", ref))
			}
		}
	}
	return nil
}

func safeValidatorFailureDetail(err error) string {
	if err == nil {
		return "validator failed without a diagnostic"
	}
	detail := strings.TrimSpace(err.Error())
	if detail == "" {
		return "validator failed without a diagnostic"
	}
	if credentialPattern.MatchString(detail) {
		return "validator failure contained credential-like material; diagnostic redacted"
	}
	const maxDetail = 512
	if len(detail) > maxDetail {
		return detail[:maxDetail] + "..."
	}
	return detail
}

func ParseCustomerSimulationValidatorVerdict(data []byte) (ValidatorVerdict, error) {
	var verdict ValidatorVerdict
	if err := decodeStrictJSON(data, &verdict); err != nil {
		return ValidatorVerdict{}, fmt.Errorf("decode validator verdict: %w", err)
	}
	if err := verdict.Validate(); err != nil {
		return ValidatorVerdict{}, err
	}
	return verdict, nil
}

const customerSimulationValidatorSystemPrompt = `You are an independent post-run customer-satisfaction validator. Inspect only the supplied scenario and evidence. Do not infer unobserved work, repair missing evidence, or treat a plausible final state as proof of every intermediate action.

Return exactly one JSON object and no markdown. The verdict field must be exactly WORKED or BROKEN. WORKED requires a whole-conversation summary and evidence_refs. BROKEN requires first_failing_turn, behavior, violation, evidence_refs, and customer_impact. Cite only the relative evidence paths supplied in the request. Use BROKEN for any mechanical failure, missing or unreadable evidence, unresolved action/tool, incorrect confirmation, ignored correction, unsupported mixed-modal boundary, dead air/timeout, or cleanup problem.`

// GatewayCustomerSimulationValidator adapts the repository's provider-neutral
// stateless gateway to the independent post-run validator seam. It has no
// product-session state and is invoked only by the post-finalization runner.
type GatewayCustomerSimulationValidator struct {
	Gateway     gateway.Gateway
	Model       string
	MaxTokens   *int
	Temperature *float64
}

func (a GatewayCustomerSimulationValidator) ValidateCustomerSimulation(ctx context.Context, request CustomerSimulationValidatorRequest) ([]byte, error) {
	if a.Gateway == nil {
		return nil, ErrValidatorUnavailable
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal validator request: %w", err)
	}
	response, err := a.Gateway.Infer(ctx, gateway.InferenceRequest{
		Messages: []models.Message{
			models.NewTextMessage(models.RoleSystem, customerSimulationValidatorSystemPrompt),
			models.NewTextMessage(models.RoleUser, string(payload)),
		},
		Model:       a.Model,
		MaxTokens:   a.MaxTokens,
		Temperature: a.Temperature,
	})
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(response.Message.TextContent())
	if text == "" {
		return nil, ErrValidatorMalformedResponse
	}
	return []byte(text), nil
}

// RunFinalizedCustomerSimulationValidator verifies the persisted evidence
// bundle and reconstructs the validator input from canonical artifacts before
// invoking the independent agent. This is the post-child boundary: a caller
// cannot accidentally ask the validator to judge a still-running or
// partially-written product session.
func RunFinalizedCustomerSimulationValidator(ctx context.Context, root string, agent CustomerSimulationValidatorAgent, timeout time.Duration) (CustomerSimulationValidatorResult, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		result := CustomerSimulationValidatorResult{Status: ValidatorStatusInputInvalid, Verdict: validatorFinalizationFailureVerdict("validator", err), Error: safeValidatorFailureDetail(fmt.Errorf("%w: %v", ErrValidatorFinalization, err))}
		return result, fmt.Errorf("%w: %v", ErrValidatorFinalization, err)
	}
	manifest, err := VerifyCustomerEvidenceBundle(absRoot)
	if err != nil {
		result := CustomerSimulationValidatorResult{Status: ValidatorStatusInputInvalid, Verdict: validatorFinalizationFailureVerdict("validator", err), Error: safeValidatorFailureDetail(fmt.Errorf("%w: %v", ErrValidatorFinalization, err))}
		return result, fmt.Errorf("%w: %v", ErrValidatorFinalization, err)
	}
	input, err := readFinalizedCustomerSimulationValidatorInput(absRoot, manifest)
	if err != nil {
		result := CustomerSimulationValidatorResult{Status: ValidatorStatusInputInvalid, Verdict: validatorFinalizationFailureVerdict(firstValidatorFailureTurn(input), err), Mechanical: input.Mechanical, Error: safeValidatorFailureDetail(fmt.Errorf("%w: %v", ErrValidatorEvidenceMismatch, err))}
		return result, fmt.Errorf("%w: %v", ErrValidatorEvidenceMismatch, err)
	}
	if !input.Process.ChildWaited || input.Process.WaitCount != 1 {
		result := CustomerSimulationValidatorResult{Status: ValidatorStatusInputInvalid, Mechanical: input.Mechanical, Verdict: validatorFinalizationFailureVerdict(firstValidatorFailureTurn(input), errors.New("the child process was not reaped exactly once")), Error: ErrValidatorFinalization.Error()}
		return result, ErrValidatorFinalization
	}
	allowedRefs := availableArtifactPaths(manifest.Artifacts)
	return (CustomerSimulationValidatorRunner{Agent: agent, Timeout: timeout}).run(ctx, input, &manifest, allowedRefs)
}

func validatorFinalizationFailureVerdict(turn string, cause error) ValidatorVerdict {
	if strings.TrimSpace(turn) == "" {
		turn = "validator"
	}
	detail := safeValidatorFailureDetail(cause)
	return ValidatorVerdict{
		Verdict:          ValidatorBroken,
		FirstFailingTurn: turn,
		Behavior:         "The evidence bundle was not ready for independent validation.",
		Violation:        detail,
		EvidenceRefs:     []string{"scenario.json"},
		CustomerImpact:   "The conversation cannot be accepted because its finalized evidence is unavailable or inconsistent.",
	}
}

// FinalizeWithValidator performs the required two-phase lifecycle. The first
// finalization creates a hash-verified BROKEN placeholder so the independent
// agent can only start after the child evidence is durable. The second
// finalization replaces that placeholder with the effective model judgment;
// validator failures remain persisted as structured BROKEN evidence.
func (b *CustomerEvidenceBundle) FinalizeWithValidator(ctx context.Context, agent CustomerSimulationValidatorAgent, timeout time.Duration) (CustomerSimulationValidatorResult, error) {
	if b == nil {
		return CustomerSimulationValidatorResult{Status: ValidatorStatusInputInvalid, Verdict: validatorFinalizationFailureVerdict("validator", errors.New("bundle is nil"))}, ErrValidatorFinalization
	}
	if b.MechanicalVerdict == nil || b.ValidatorInput == nil {
		return CustomerSimulationValidatorResult{Status: ValidatorStatusInputInvalid, Mechanical: valueOrZeroMechanical(b.MechanicalVerdict), Verdict: validatorFinalizationFailureVerdict("validator", ErrMissingEvidence)}, ErrMissingEvidence
	}
	if b.ValidatorVerdict == nil {
		placeholder := ValidatorVerdict{
			Verdict:          ValidatorBroken,
			FirstFailingTurn: "validator",
			Behavior:         "Independent validation is pending.",
			Violation:        "the validator has not run yet",
			EvidenceRefs:     validatorFallbackEvidenceRefs(*b.ValidatorInput, nil),
			CustomerImpact:   "The run is not accepted until an independent validator judgment is recorded.",
		}
		b.ValidatorVerdict = &placeholder
	}
	if err := b.Finalize(); err != nil {
		return CustomerSimulationValidatorResult{Status: ValidatorStatusInputInvalid, Mechanical: *b.MechanicalVerdict, Verdict: *b.ValidatorVerdict, Error: safeValidatorFailureDetail(err)}, err
	}
	result, validatorErr := RunFinalizedCustomerSimulationValidator(ctx, b.Root(), agent, timeout)
	b.ValidatorVerdict = &result.Verdict
	finalizeErr := b.Finalize()
	return result, errors.Join(validatorErr, finalizeErr)
}

func valueOrZeroMechanical(verdict *MechanicalVerdict) MechanicalVerdict {
	if verdict == nil {
		return MechanicalVerdict{}
	}
	return *verdict
}

func readFinalizedCustomerSimulationValidatorInput(root string, manifest CustomerEvidenceManifest) (ValidatorInput, error) {
	scenario, err := readCustomerSimulationJSONArtifact[CustomerScenario](root, "scenario.json")
	if err != nil {
		return ValidatorInput{}, err
	}
	if scenario.ID != manifest.ScenarioID {
		return ValidatorInput{}, fmt.Errorf("scenario artifact identifies %q, manifest identifies %q", scenario.ID, manifest.ScenarioID)
	}
	declared, err := readCustomerSimulationJSONArtifact[ValidatorInput](root, "validator-input.json")
	if err != nil {
		return ValidatorInput{}, err
	}
	if err := declared.Validate(); err != nil {
		return ValidatorInput{}, err
	}
	customer, err := readCustomerSimulationJSONLines[TranscriptEvent](root, "transcripts/customer.jsonl")
	if err != nil {
		return ValidatorInput{}, err
	}
	product, err := readCustomerSimulationJSONLines[TranscriptEvent](root, "transcripts/product.jsonl")
	if err != nil {
		return ValidatorInput{}, err
	}
	audio, err := readCustomerSimulationJSONLines[AudioTurnEvent](root, "events/audio-turn-events.jsonl")
	if err != nil {
		return ValidatorInput{}, err
	}
	tools, err := readCustomerSimulationJSONLines[ToolObservation](root, "tool-observations.jsonl")
	if err != nil {
		return ValidatorInput{}, err
	}
	checkpoints, err := readCustomerSimulationJSONLines[FilesystemCheckpoint](root, "filesystem-checkpoints.jsonl")
	if err != nil {
		return ValidatorInput{}, err
	}
	process, err := readCustomerSimulationJSONArtifact[ProcessFacts](root, "process.json")
	if err != nil {
		return ValidatorInput{}, err
	}
	mechanical, err := readCustomerSimulationJSONArtifact[MechanicalVerdict](root, "mechanical-verdict.json")
	if err != nil {
		return ValidatorInput{}, err
	}
	authoritative := ValidatorInput{
		Scenario:              scenario,
		CustomerTranscript:    customer,
		ProductTranscript:     product,
		AudioTurnEvents:       audio,
		ToolObservations:      tools,
		FilesystemCheckpoints: checkpoints,
		Process:               process,
		Mechanical:            mechanical,
		EvidenceRefs:          append([]string(nil), declared.EvidenceRefs...),
	}
	if scenario.Family == ScenarioFamilyC {
		mixed, readErr := readCustomerSimulationJSONArtifact[MixedModalEvidence](root, "events/mixed-modal.json")
		if readErr != nil {
			return ValidatorInput{}, readErr
		}
		authoritative.MixedModal = &mixed
	}
	if scenario.Family == ScenarioFamilyD {
		termination, readErr := readCustomerSimulationJSONArtifact[TerminationEvidence](root, "events/termination.json")
		if readErr != nil {
			return ValidatorInput{}, readErr
		}
		authoritative.Termination = &termination
	}
	if scenario.Family == ScenarioFamilyE {
		patience, readErr := readCustomerSimulationJSONArtifact[PatienceEvidence](root, FamilyEPatienceEventPath)
		if readErr != nil {
			return ValidatorInput{}, readErr
		}
		authoritative.Patience = &patience
	}
	if !equivalentValidatorInputs(declared, authoritative) {
		return ValidatorInput{}, ErrValidatorEvidenceMismatch
	}
	if err := authoritative.Validate(); err != nil {
		return ValidatorInput{}, err
	}
	return authoritative, nil
}

func equivalentValidatorInputs(left, right ValidatorInput) bool {
	left = normalizeValidatorInput(left)
	right = normalizeValidatorInput(right)
	return reflect.DeepEqual(left, right)
}

func normalizeValidatorInput(input ValidatorInput) ValidatorInput {
	if len(input.CustomerTranscript) == 0 {
		input.CustomerTranscript = nil
	}
	if len(input.ProductTranscript) == 0 {
		input.ProductTranscript = nil
	}
	if len(input.AudioTurnEvents) == 0 {
		input.AudioTurnEvents = nil
	}
	if len(input.ToolObservations) == 0 {
		input.ToolObservations = nil
	}
	if len(input.FilesystemCheckpoints) == 0 {
		input.FilesystemCheckpoints = nil
	}
	if len(input.EvidenceRefs) == 0 {
		input.EvidenceRefs = nil
	}
	return input
}

func readCustomerSimulationJSONArtifact[T any](root, relative string) (T, error) {
	var value T
	path, err := safeEvidencePath(root, relative)
	if err != nil {
		return value, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	if err := decodeStrictJSON(data, &value); err != nil {
		return value, fmt.Errorf("decode %q: %w", relative, err)
	}
	return value, nil
}

func readCustomerSimulationJSONLines[T any](root, relative string) ([]T, error) {
	path, err := safeEvidencePath(root, relative)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	lines := bytes.Split(data, []byte{'\n'})
	values := make([]T, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var value T
		if err := decodeStrictJSON(line, &value); err != nil {
			return nil, fmt.Errorf("decode line in %q: %w", relative, err)
		}
		values = append(values, value)
	}
	return values, nil
}
