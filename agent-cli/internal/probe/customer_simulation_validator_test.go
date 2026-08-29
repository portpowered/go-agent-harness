package probe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
)

const validatorTestEvidenceRef = "transcripts/product.jsonl"

func TestCustomerSimulationValidatorAcceptsWorkedWithCompleteIndependentRequest(t *testing.T) {
	input := validatorTestInput(t)
	var called atomic.Bool
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), validatorTestContextKey{}, "product-context"))
	cancel()
	agent := CustomerSimulationValidatorAgentFunc(func(ctx context.Context, request CustomerSimulationValidatorRequest) ([]byte, error) {
		called.Store(true)
		if ctx.Err() != nil {
			t.Fatalf("validator context = %v, want independent context after product cancellation", ctx.Err())
		}
		if request.Version != CustomerSimulationValidatorRequestVersion {
			t.Errorf("request version = %q, want %q", request.Version, CustomerSimulationValidatorRequestVersion)
		}
		if request.Manifest != nil {
			t.Error("direct validator request unexpectedly contains a manifest")
		}
		if request.Input.Scenario.ID != input.Scenario.ID || len(request.Input.CustomerTranscript) != 1 || len(request.Input.ProductTranscript) != 1 || len(request.Input.ToolObservations) != 1 || len(request.Input.FilesystemCheckpoints) != 1 {
			t.Fatalf("validator input = %+v, want complete paired evidence", request.Input)
		}
		if len(request.Rubric.Criteria) != 8 {
			t.Fatalf("rubric criteria = %d, want all eight customer-facing checks", len(request.Rubric.Criteria))
		}
		return validatorWorkedJSON(), nil
	})

	result, err := RunCustomerSimulationValidator(parent, input, agent, time.Second)
	if err != nil {
		t.Fatalf("RunCustomerSimulationValidator: %v", err)
	}
	if !called.Load() || !result.Pass() || result.Status != ValidatorStatusWorked || result.Verdict.Verdict != ValidatorWorked {
		t.Fatalf("result = %+v, want accepted WORKED result", result)
	}
	if result.AgentVerdict == nil || result.AgentVerdict.Verdict != ValidatorWorked {
		t.Fatalf("agent verdict = %+v, want parsed WORKED judgment", result.AgentVerdict)
	}
}

func TestCustomerSimulationValidatorPreservesSubstantiveBrokenJudgment(t *testing.T) {
	input := validatorTestInput(t)
	agent := CustomerSimulationValidatorAgentFunc(func(context.Context, CustomerSimulationValidatorRequest) ([]byte, error) {
		return []byte(`{"verdict":"BROKEN","first_failing_turn":"turn-2","behavior":"The final summary omitted the observed README state.","violation":"the summary did not match the filesystem checkpoint","evidence_refs":["filesystem-checkpoints.jsonl","transcripts/product.jsonl"],"customer_impact":"The customer cannot trust the reported project state."}`), nil
	})

	result, err := RunCustomerSimulationValidator(context.Background(), input, agent, time.Second)
	if err != nil {
		t.Fatalf("RunCustomerSimulationValidator: %v", err)
	}
	if result.Pass() || result.Status != ValidatorStatusBroken || result.Verdict.Verdict != ValidatorBroken {
		t.Fatalf("result = %+v, want substantive non-success BROKEN result", result)
	}
	if result.Verdict.FirstFailingTurn != "turn-2" || result.Verdict.CustomerImpact == "" {
		t.Fatalf("broken verdict = %+v, want turn, diagnosis, and customer impact", result.Verdict)
	}
	if result.AgentVerdict == nil || result.AgentVerdict.Verdict != ValidatorBroken {
		t.Fatalf("agent verdict = %+v, want preserved parsed BROKEN judgment", result.AgentVerdict)
	}
}

func TestCustomerSimulationValidatorFailsClosedAndPreservesAttemptedJudgment(t *testing.T) {
	tests := []struct {
		name        string
		input       func(ValidatorInput) ValidatorInput
		agent       CustomerSimulationValidatorAgent
		timeout     time.Duration
		wantStatus  CustomerSimulationValidatorStatus
		wantError   error
		wantAttempt bool
	}{
		{
			name: "mechanical disagreement",
			input: func(input ValidatorInput) ValidatorInput {
				input.Mechanical.Pass = false
				input.Mechanical.Findings = []MechanicalFinding{{Code: "wrong_state", TurnID: "turn-1", Message: "checkpoint mismatch", EvidenceRefs: []string{validatorTestEvidenceRef}}}
				return input
			},
			agent: CustomerSimulationValidatorAgentFunc(func(context.Context, CustomerSimulationValidatorRequest) ([]byte, error) {
				return validatorWorkedJSON(), nil
			}),
			wantStatus:  ValidatorStatusMechanicalDisagreement,
			wantError:   ErrValidatorMechanicalDisagreement,
			wantAttempt: true,
		},
		{
			name: "mechanical pass with failed action",
			input: func(input ValidatorInput) ValidatorInput {
				input.Mechanical.ActionResults[0].Disposition = DispositionFailed
				input.Mechanical.ActionResults[0].OutcomeReason = "the tool failed"
				return input
			},
			agent: CustomerSimulationValidatorAgentFunc(func(context.Context, CustomerSimulationValidatorRequest) ([]byte, error) {
				return validatorWorkedJSON(), nil
			}),
			wantStatus:  ValidatorStatusMechanicalDisagreement,
			wantError:   ErrValidatorMechanicalDisagreement,
			wantAttempt: true,
		},
		{
			name: "missing worked evidence",
			input: func(input ValidatorInput) ValidatorInput {
				input.ProductTranscript = nil
				return input
			},
			agent: CustomerSimulationValidatorAgentFunc(func(context.Context, CustomerSimulationValidatorRequest) ([]byte, error) {
				return validatorWorkedJSON(), nil
			}),
			wantStatus:  ValidatorStatusMissingEvidence,
			wantError:   ErrMissingEvidence,
			wantAttempt: true,
		},
		{
			name:  "malformed json",
			input: func(input ValidatorInput) ValidatorInput { return input },
			agent: CustomerSimulationValidatorAgentFunc(func(context.Context, CustomerSimulationValidatorRequest) ([]byte, error) {
				return []byte("```json\n{\"verdict\":\"WORKED\"}\n```"), nil
			}),
			wantStatus: ValidatorStatusMalformed,
			wantError:  ErrValidatorMalformedResponse,
		},
		{
			name:  "unlisted evidence reference",
			input: func(input ValidatorInput) ValidatorInput { return input },
			agent: CustomerSimulationValidatorAgentFunc(func(context.Context, CustomerSimulationValidatorRequest) ([]byte, error) {
				return []byte(`{"verdict":"BROKEN","first_failing_turn":"turn-1","behavior":"The run failed.","violation":"the supplied evidence is insufficient","evidence_refs":["private-not-supplied.json"],"customer_impact":"The customer cannot verify the result."}`), nil
			}),
			wantStatus:  ValidatorStatusMalformed,
			wantError:   ErrValidatorMalformedResponse,
			wantAttempt: true,
		},
		{
			name:  "timeout",
			input: func(input ValidatorInput) ValidatorInput { return input },
			agent: CustomerSimulationValidatorAgentFunc(func(ctx context.Context, _ CustomerSimulationValidatorRequest) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}),
			timeout:    5 * time.Millisecond,
			wantStatus: ValidatorStatusTimeout,
			wantError:  ErrValidatorTimeout,
		},
		{
			name:        "unavailable",
			input:       func(input ValidatorInput) ValidatorInput { return input },
			wantStatus:  ValidatorStatusUnavailable,
			wantError:   ErrValidatorUnavailable,
			wantAttempt: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := test.input(validatorTestInput(t))
			result, err := RunCustomerSimulationValidator(context.Background(), input, test.agent, test.timeout)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
			if result.Status != test.wantStatus || result.Pass() || result.Verdict.Verdict != ValidatorBroken {
				t.Fatalf("result = %+v, want fail-closed %s/BROKEN", result, test.wantStatus)
			}
			if test.wantAttempt && result.AgentVerdict == nil {
				t.Fatal("agent verdict was not preserved")
			}
			if !test.wantAttempt && result.AgentVerdict != nil {
				t.Fatalf("agent verdict = %+v, want no attempted judgment", result.AgentVerdict)
			}
			if result.Verdict.FirstFailingTurn == "" || len(result.Verdict.EvidenceRefs) == 0 || result.Error == "" {
				t.Fatalf("failure verdict = %+v, want structured diagnostic and evidence", result.Verdict)
			}
		})
	}
}

func TestCustomerSimulationValidatorRejectsInvalidInputBeforeAgent(t *testing.T) {
	input := validatorTestInput(t)
	input.Scenario.ID = ""
	var called atomic.Bool
	result, err := RunCustomerSimulationValidator(context.Background(), input, CustomerSimulationValidatorAgentFunc(func(context.Context, CustomerSimulationValidatorRequest) ([]byte, error) {
		called.Store(true)
		return validatorWorkedJSON(), nil
	}), time.Second)
	if !errors.Is(err, ErrValidatorInputInvalid) || result.Status != ValidatorStatusInputInvalid || called.Load() {
		t.Fatalf("result = %+v, error = %v, called = %t; want input-invalid without agent call", result, err, called.Load())
	}
}

func TestFinalizeWithValidatorPersistsOnlyPostFinalizationJudgment(t *testing.T) {
	input := validatorTestInput(t)
	bundle := newValidatorTestBundle(t, input)
	var called atomic.Bool
	agent := CustomerSimulationValidatorAgentFunc(func(ctx context.Context, request CustomerSimulationValidatorRequest) ([]byte, error) {
		called.Store(true)
		if ctx.Err() != nil {
			t.Fatalf("finalized validator context = %v, want active bounded context", ctx.Err())
		}
		if request.Manifest == nil || !request.Manifest.Finalized || request.Manifest.ValidatorVerdict != ValidatorBroken {
			t.Fatalf("manifest at validator call = %+v, want finalized placeholder BROKEN", request.Manifest)
		}
		if request.Input.Process.WaitCount != 1 || !request.Input.Process.ChildWaited {
			t.Fatalf("validator process input = %+v, want exactly one reaped child", request.Input.Process)
		}
		for _, artifact := range request.Manifest.Artifacts {
			if artifact.Required && artifact.State != ArtifactStateAvailable {
				t.Errorf("required artifact = %+v, want available before agent invocation", artifact)
			}
		}
		return validatorWorkedJSON(), nil
	})

	result, err := bundle.FinalizeWithValidator(context.Background(), agent, time.Second)
	if err != nil {
		t.Fatalf("FinalizeWithValidator: %v", err)
	}
	if !called.Load() || !result.Pass() {
		t.Fatalf("called = %t, result = %+v, want finalized accepted WORKED result", called.Load(), result)
	}
	manifest, err := VerifyCustomerEvidenceBundle(bundle.Root())
	if err != nil {
		t.Fatalf("VerifyCustomerEvidenceBundle: %v", err)
	}
	if manifest.ValidatorVerdict != ValidatorWorked || !manifest.MechanicalPass {
		t.Fatalf("manifest = %+v, want final WORKED with mechanical pass", manifest)
	}
	verdict, err := readCustomerSimulationJSONArtifact[ValidatorVerdict](bundle.Root(), "validator-verdict.json")
	if err != nil {
		t.Fatalf("read persisted validator verdict: %v", err)
	}
	if verdict.Verdict != ValidatorWorked || verdict.Summary == "" {
		t.Fatalf("persisted verdict = %+v, want accepted WORKED judgment", verdict)
	}
}

func TestFinalizeWithValidatorDoesNotInvokeAgentBeforeChildIsReaped(t *testing.T) {
	input := validatorTestInput(t)
	input.Process.ChildWaited = false
	input.Process.WaitCount = 0
	bundle := newValidatorTestBundle(t, input)
	var called atomic.Bool
	result, err := bundle.FinalizeWithValidator(context.Background(), CustomerSimulationValidatorAgentFunc(func(context.Context, CustomerSimulationValidatorRequest) ([]byte, error) {
		called.Store(true)
		return validatorWorkedJSON(), nil
	}), time.Second)
	if !errors.Is(err, ErrValidatorFinalization) || called.Load() || result.Status != ValidatorStatusInputInvalid {
		t.Fatalf("result = %+v, error = %v, called = %t; want finalized precondition failure without invocation", result, err, called.Load())
	}
	manifest, verifyErr := VerifyCustomerEvidenceBundle(bundle.Root())
	if verifyErr != nil {
		t.Fatalf("VerifyCustomerEvidenceBundle after precondition failure: %v", verifyErr)
	}
	if manifest.ValidatorVerdict != ValidatorBroken {
		t.Fatalf("manifest = %+v, want persisted BROKEN judgment", manifest)
	}
}

func TestParseCustomerSimulationValidatorVerdictIsStrict(t *testing.T) {
	valid := validatorWorkedJSON()
	if _, err := ParseCustomerSimulationValidatorVerdict(valid); err != nil {
		t.Fatalf("valid verdict rejected: %v", err)
	}
	for _, data := range [][]byte{
		[]byte(`{"verdict":"MAYBE","summary":"no","evidence_refs":["scenario.json"]}`),
		[]byte(`{"verdict":"WORKED","summary":"yes","evidence_refs":["scenario.json"],"extra":true}`),
		[]byte(`{"verdict":"BROKEN","first_failing_turn":"turn-1","behavior":"failed","violation":"failed","evidence_refs":[],"customer_impact":"impact"}`),
	} {
		if _, err := ParseCustomerSimulationValidatorVerdict(data); err == nil {
			t.Fatalf("malformed validator response %s unexpectedly parsed", data)
		}
	}
}

func validatorWorkedJSON() []byte {
	return []byte(fmt.Sprintf(`{"verdict":"WORKED","summary":"The ordered requests were completed with truthful confirmations and clean termination.","evidence_refs":[%q,%q]}`, "scenario.json", validatorTestEvidenceRef))
}

type validatorTestContextKey struct{}

func validatorTestInput(t *testing.T) ValidatorInput {
	t.Helper()
	scenario := validCustomerScenario()
	return ValidatorInput{
		Scenario: scenario,
		CustomerTranscript: []TranscriptEvent{{
			ID: "customer-1", TurnID: "turn-1", Speaker: TranscriptCustomer,
			Text: "Please create the project and then summarize it.", At: 10 * time.Millisecond, Final: true,
		}},
		ProductTranscript: []TranscriptEvent{{
			ID: "product-1", TurnID: "turn-1", Speaker: TranscriptProduct,
			Text: "The project is ready and its observed contents are recorded.", At: 200 * time.Millisecond, Final: true,
		}},
		AudioTurnEvents: []AudioTurnEvent{
			{ID: "audio-in-1", TurnID: "turn-1", Direction: "input", Kind: "speech", At: 10 * time.Millisecond, Duration: 20 * time.Millisecond, Bytes: 640},
			{ID: "audio-out-1", TurnID: "turn-1", Direction: "output", Kind: "speech", At: 200 * time.Millisecond, Duration: 20 * time.Millisecond, Bytes: 640},
		},
		ToolObservations: []ToolObservation{{
			ID: "tool-1", ActionID: "create", TurnID: "turn-1", Tool: "exec", Status: "completed",
			At: 50 * time.Millisecond, Duration: 50 * time.Millisecond, ResultSeen: true, Summary: "created project",
		}},
		FilesystemCheckpoints: []FilesystemCheckpoint{{
			ID: "checkpoint-1", ActionID: "create", At: 150 * time.Millisecond,
			Entries: []FilesystemCheckpointEntry{{Path: "project/README.md", Type: FileTypeFile, SHA256: sha256Hex([]byte("project contents")), Size: int64(len("project contents"))}},
		}},
		Process: ProcessFacts{
			PID: 123, ExitCode: 0, ExitClassification: "normal", ChildWaited: true, WaitCount: 1,
			InputClosed: true, InputFinished: true, OutputClosed: true, StartedAt: 0, EndedAt: time.Second,
		},
		Mechanical: MechanicalVerdict{
			Pass: true, Summary: "all requested actions have truthful side effects",
			ActionResults: []ActionResult{
				{ActionID: "create", TurnID: "turn-1", Confirmed: true, ConfirmedAt: 200 * time.Millisecond, Disposition: DispositionCompleted, EvidenceRefs: []string{validatorTestEvidenceRef}, CheckpointIDs: []string{"checkpoint-1"}, ToolObservationIDs: []string{"tool-1"}},
				{ActionID: "summarize", TurnID: "turn-2", Confirmed: true, ConfirmedAt: 300 * time.Millisecond, Disposition: DispositionCompleted, EvidenceRefs: []string{validatorTestEvidenceRef}},
			},
		},
		EvidenceRefs: []string{
			"scenario.json", "transcripts/customer.jsonl", "transcripts/product.jsonl", "events/audio-turn-events.jsonl",
			"tool-observations.jsonl", "filesystem-checkpoints.jsonl", "process.json", "mechanical-verdict.json",
		},
	}
}

func newValidatorTestBundle(t *testing.T, input ValidatorInput) *CustomerEvidenceBundle {
	t.Helper()
	bundle, err := NewCustomerEvidenceBundle(filepath.Join(t.TempDir(), "bundle"), input.Scenario, "run-validator-test", "hermetic-key")
	if err != nil {
		t.Fatalf("NewCustomerEvidenceBundle: %v", err)
	}
	bundle.Transcripts = PairedTranscripts{Customer: input.CustomerTranscript, Product: input.ProductTranscript}
	bundle.AudioTurnEvents = input.AudioTurnEvents
	bundle.ToolObservations = input.ToolObservations
	bundle.FilesystemCheckpoints = input.FilesystemCheckpoints
	bundle.Process = input.Process
	bundle.MechanicalVerdict = &input.Mechanical
	bundle.ValidatorInput = &input
	recordDir := filepath.Join(t.TempDir(), "record-dir")
	if err := os.MkdirAll(recordDir, 0o700); err != nil {
		t.Fatalf("create record directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recordDir, "session.jsonl"), []byte("validator evidence\n"), 0o600); err != nil {
		t.Fatalf("write record artifact: %v", err)
	}
	if err := bundle.AddProductRecordDir(recordDir); err != nil {
		t.Fatalf("AddProductRecordDir: %v", err)
	}
	return bundle
}

func TestCustomerSimulationValidatorRubricHasStableCriteria(t *testing.T) {
	rubric := DefaultCustomerSimulationValidatorRubric()
	if err := rubric.Validate(); err != nil {
		t.Fatalf("DefaultCustomerSimulationValidatorRubric validation: %v", err)
	}
	ids := make([]string, 0, len(rubric.Criteria))
	for _, criterion := range rubric.Criteria {
		ids = append(ids, criterion.ID)
	}
	got := strings.Join(ids, ",")
	want := "iterative_context,truthfulness,correction_interruption,mixed_modal_grounding,patience_dead_air,cancellation_cleanup,unresolved_work,wrong_reason_success"
	if got != want {
		t.Fatalf("rubric IDs = %q, want %q", got, want)
	}
}

func TestGatewayCustomerSimulationValidatorUsesIndependentStatelessRequest(t *testing.T) {
	input := validatorTestInput(t)
	stub := &validatorGatewayStub{response: string(validatorWorkedJSON())}
	adapter := GatewayCustomerSimulationValidator{Gateway: stub, Model: "validator-model"}
	request := CustomerSimulationValidatorRequest{
		Version: CustomerSimulationValidatorRequestVersion,
		Rubric:  DefaultCustomerSimulationValidatorRubric(),
		Input:   input,
	}

	raw, err := adapter.ValidateCustomerSimulation(context.Background(), request)
	if err != nil {
		t.Fatalf("ValidateCustomerSimulation: %v", err)
	}
	if string(raw) != string(validatorWorkedJSON()) {
		t.Fatalf("raw validator response = %q, want %q", raw, validatorWorkedJSON())
	}
	if stub.request.Model != "validator-model" || len(stub.request.Messages) != 2 || len(stub.request.Tools) != 0 {
		t.Fatalf("gateway request = %+v, want one stateless system/user request without tools", stub.request)
	}
	if stub.request.Messages[0].Role != messages.RoleSystem || !strings.Contains(stub.request.Messages[0].TextContent(), "independent post-run") {
		t.Fatalf("system prompt = %+v, want independent post-run validator instructions", stub.request.Messages[0])
	}
	var decoded CustomerSimulationValidatorRequest
	if err := decodeStrictJSON([]byte(stub.request.Messages[1].TextContent()), &decoded); err != nil {
		t.Fatalf("decode gateway validator payload: %v", err)
	}
	if decoded.Input.Scenario.ID != input.Scenario.ID || len(decoded.Rubric.Criteria) != 8 {
		t.Fatalf("decoded gateway payload = %+v, want complete input and rubric", decoded)
	}
}

type validatorGatewayStub struct {
	response string
	request  gateway.InferenceRequest
}

func (s *validatorGatewayStub) Infer(_ context.Context, request gateway.InferenceRequest) (gateway.InferenceResponse, error) {
	s.request = request
	return gateway.InferenceResponse{Message: messages.NewTextMessage(messages.RoleAssistant, s.response)}, nil
}

func (*validatorGatewayStub) InferStream(context.Context, gateway.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return nil, errors.New("validator gateway stub does not stream")
}

func (*validatorGatewayStub) Interact(context.Context, gateway.InteractionRequest) (<-chan gateway.InteractionEvent, error) {
	return nil, errors.New("validator gateway stub does not interact")
}

var _ gateway.Gateway = (*validatorGatewayStub)(nil)
