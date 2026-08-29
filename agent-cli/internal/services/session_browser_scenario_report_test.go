package services

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

func TestComputeBrowserConversationInputJSONValidityRetainsEveryInvokeObservation(t *testing.T) {
	valid := " \n{\"count\":90071992547409931234567890}\t"
	measurement := ComputeBrowserConversationInputJSONValidity([]BrowserConversationBrokerCall{
		{Sequence: 1, Operation: BrowserConversationListTools, InputJSON: `{}`},
		{Sequence: 2, StepID: "first", Operation: BrowserConversationInvoke, ToolRef: "ref-1", ToolName: "write", State: webmcp.InvocationDispatched, InputJSON: valid},
		{Sequence: 3, StepID: "first", Operation: BrowserConversationInvoke, ToolRef: "ref-1", ToolName: "write", State: webmcp.InvocationCompleted, Terminal: true, InputJSON: valid},
		{Sequence: 4, StepID: "bad", Operation: BrowserConversationInvoke, InputJSON: `[]`, State: webmcp.InvocationError, Terminal: true},
		{Sequence: 5, StepID: "trailing", Operation: BrowserConversationInvoke, InputJSON: `{"ok":true} trailing`, State: webmcp.InvocationError, Terminal: true},
	})

	if measurement.ValidObjectStrings != 2 || measurement.TotalAttempts != 4 {
		t.Fatalf("measurement = %+v, want 2/4", measurement)
	}
	if want := 50.0; measurement.Percentage != want {
		t.Fatalf("percentage = %v, want %v", measurement.Percentage, want)
	}
	if len(measurement.Attempts) != 4 || measurement.Attempts[0].InputJSON != valid || !measurement.Attempts[0].ValidObject || measurement.Attempts[2].ValidObject {
		t.Fatalf("attempts = %+v, want exact raw values and object classifications", measurement.Attempts)
	}
}

func TestBrowserConversationRunDerivesValidityInImmutableSnapshots(t *testing.T) {
	run, err := NewBrowserConversationRun(validBrowserConversationScenario())
	if err != nil {
		t.Fatalf("new run: %v", err)
	}
	if err := run.ObserveBrokerCall(BrowserConversationBrokerCall{
		StepID: "inspect", Operation: BrowserConversationInvoke, InputJSON: `{"value":1}`, State: webmcp.InvocationCompleted, Terminal: true,
	}); err != nil {
		t.Fatalf("valid call: %v", err)
	}
	if err := run.ObserveBrokerCall(BrowserConversationBrokerCall{
		StepID: "inspect", Operation: BrowserConversationInvoke, InputJSON: `not-json`, State: webmcp.InvocationError, Terminal: true,
	}); err != nil {
		t.Fatalf("invalid call: %v", err)
	}

	first := run.Snapshot()
	if first.InputJSONValidity.ValidObjectStrings != 1 || first.InputJSONValidity.TotalAttempts != 2 {
		t.Fatalf("snapshot validity = %+v, want 1/2", first.InputJSONValidity)
	}
	first.InputJSONValidity.Attempts[0].InputJSON = "mutated"
	second := run.Snapshot()
	if second.InputJSONValidity.Attempts[0].InputJSON != `{"value":1}` {
		t.Fatalf("snapshot validity shares mutable attempt: %+v", second.InputJSONValidity)
	}
}

func TestBrowserConversationReportSanitizesMetadataButPreservesSafeRawInput(t *testing.T) {
	run, err := NewBrowserConversationRun(validBrowserConversationScenario())
	if err != nil {
		t.Fatalf("new run: %v", err)
	}
	input := " {\"value\":1} \n"
	if err := run.ObserveBrokerCall(BrowserConversationBrokerCall{
		StepID: "inspect", Operation: BrowserConversationInvoke, InputJSON: input, State: webmcp.InvocationCompleted, Terminal: true,
	}); err != nil {
		t.Fatalf("broker call: %v", err)
	}
	result, err := run.Finalize()
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	report, err := NewBrowserConversationReport(result, BrowserConversationReportMetadata{
		Command:       "agent session --api-key=sk-live-never-publish",
		Configuration: "api_key: sk-live-never-publish",
		Provider:      "openai",
		Model:         "pinned-model",
	})
	if err != nil {
		t.Fatalf("new report: %v", err)
	}
	if report.Metadata.Command != "[redacted]" || report.Metadata.Configuration != "[redacted]" {
		t.Fatalf("metadata = %+v, want credential-shaped values redacted", report.Metadata)
	}
	if report.Evidence.BrokerCalls[0].InputJSON != input {
		t.Fatalf("safe input_json = %q, want exact %q", report.Evidence.BrokerCalls[0].InputJSON, input)
	}
	if report.Evidence.InputJSONValidity.Attempts[0].InputJSON != input || report.Evidence.InputJSONValidity.Percentage != 100 {
		t.Fatalf("report validity = %+v, want exact 1/1 evidence", report.Evidence.InputJSONValidity)
	}
	encoded, err := MarshalBrowserConversationReport(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if strings.Contains(string(encoded), "sk-live-never-publish") {
		t.Fatalf("report leaked credential-shaped metadata: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"claim_grounding"`) || !strings.Contains(string(encoded), `"input_json_validity"`) {
		t.Fatalf("report omitted fixed rubric or validity: %s", encoded)
	}
	var decoded BrowserConversationReport
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode report: %v", err)
	}
}

func TestBrowserConversationReportPreservesValidityWhenCredentialInputIsRedacted(t *testing.T) {
	secretInput := `{"token":"sk-live-never-publish"}`
	result := BrowserConversationResult{
		ScenarioID:   "scenario",
		ScenarioName: "name",
		BrokerCalls:  []BrowserConversationBrokerCall{{Sequence: 1, Operation: BrowserConversationInvoke, InputJSON: secretInput, State: webmcp.InvocationCompleted, Terminal: true}},
	}
	report, err := NewBrowserConversationReport(result, BrowserConversationReportMetadata{})
	if err != nil {
		t.Fatalf("new report: %v", err)
	}
	if report.Evidence.BrokerCalls[0].InputJSON == secretInput || strings.Contains(report.Evidence.BrokerCalls[0].InputJSON, "sk-live-never-publish") {
		t.Fatalf("report leaked credential input_json: %q", report.Evidence.BrokerCalls[0].InputJSON)
	}
	if report.Evidence.InputJSONValidity.ValidObjectStrings != 1 || report.Evidence.InputJSONValidity.TotalAttempts != 1 || report.Evidence.InputJSONValidity.Percentage != 100 || !report.Evidence.InputJSONValidity.Attempts[0].ValidObject {
		t.Fatalf("redacted validity = %+v, want preserved valid 1/1 measurement", report.Evidence.InputJSONValidity)
	}
}

func TestBrowserConversationValidatorInputIncludesFixedRubricAndSanitizedEvidence(t *testing.T) {
	result := BrowserConversationResult{ScenarioID: "scenario", ScenarioName: "name", BrokerCalls: []BrowserConversationBrokerCall{
		{Sequence: 1, Operation: BrowserConversationInvoke, InputJSON: `{"value":true}`, State: webmcp.InvocationCompleted, Terminal: true},
	}}
	input, err := NewBrowserConversationValidatorInput(result)
	if err != nil {
		t.Fatalf("new validator input: %v", err)
	}
	if input.Version != BrowserConversationValidatorInputVersion || len(input.Rubric) != 8 || !input.Evidence.Finalized {
		t.Fatalf("validator input = %+v, want version/rubric/finalized evidence", input)
	}
	if input.Evidence.InputJSONValidity.ValidObjectStrings != 1 {
		t.Fatalf("validator input validity = %+v, want one valid object", input.Evidence.InputJSONValidity)
	}
}

func TestBrowserConversationCommandValidatorReadsBoundedStructuredVerdict(t *testing.T) {
	if os.Getenv("WEBMCP_BROWSER_VALIDATOR_HELPER") == "1" {
		var input BrowserConversationValidatorInput
		if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
			os.Exit(2)
		}
		if input.Version != BrowserConversationValidatorInputVersion || len(input.Rubric) != 8 || input.Evidence.InputJSONValidity.TotalAttempts != 1 {
			os.Exit(3)
		}
		verdict := BrowserConversationValidatorVerdict{
			Version: BrowserConversationValidatorVersion,
			Status:  BrowserConversationValidatorPass,
			Passed:  true,
		}
		for _, name := range input.Rubric {
			verdict.Checks = append(verdict.Checks, BrowserConversationValidatorCheck{Name: name, Passed: true})
		}
		_ = json.NewEncoder(os.Stdout).Encode(verdict)
		os.Exit(0)
	}

	result := BrowserConversationResult{ScenarioID: "scenario", ScenarioName: "name", BrokerCalls: []BrowserConversationBrokerCall{
		{Sequence: 1, Operation: BrowserConversationInvoke, InputJSON: `{"value":true}`, State: webmcp.InvocationCompleted, Terminal: true},
	}}
	validator, err := NewBrowserConversationCommandValidator([]string{os.Args[0], "-test.run=^TestBrowserConversationCommandValidatorReadsBoundedStructuredVerdict$"}, time.Second)
	if err != nil {
		t.Fatalf("new command validator: %v", err)
	}
	validator.Env = []string{"WEBMCP_BROWSER_VALIDATOR_HELPER=1"}
	verdict, err := validator.ValidateBrowserConversation(result)
	if err != nil {
		t.Fatalf("validate browser conversation: %v", err)
	}
	if verdict.Status != BrowserConversationValidatorPass || !verdict.Passed || len(verdict.Checks) != 8 {
		t.Fatalf("verdict = %+v, want complete pass", verdict)
	}
}

func TestBrowserConversationCommandValidatorRejectsUnboundedOrCredentialCommand(t *testing.T) {
	if _, err := NewBrowserConversationCommandValidator(nil, time.Second); err == nil {
		t.Fatal("empty validator command was accepted")
	}
	if _, err := NewBrowserConversationCommandValidator([]string{"validator"}, 0); err == nil {
		t.Fatal("zero validator timeout was accepted")
	}
	if _, err := NewBrowserConversationCommandValidator([]string{"validator", "--api-key", "secret"}, time.Second); err == nil {
		t.Fatal("credential-shaped validator command was accepted")
	}
	if browserConversationJSONStringObject(`{"x":1} trailing`) {
		t.Fatal("trailing JSON was accepted")
	}
	if browserConversationJSONStringObject(`null`) || browserConversationJSONStringObject(`[]`) {
		t.Fatal("non-object JSON was accepted")
	}
	if browserConversationJSONStringObject(`{"x":1}`) != true {
		t.Fatal("object JSON was rejected")
	}
}
