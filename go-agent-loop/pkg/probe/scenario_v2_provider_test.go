package probe

import (
	"errors"
	"strings"
	"testing"
)

const providerOnlyScenarioV2 = `{
  "schema_version": "probe.scenario.v2",
  "id": "provider-plan",
  "name": "provider plan",
  "description": "every provider step and expectation",
  "steps": [
    {"type": "send_text", "text": "hello"},
    {"type": "send_audio", "corpus_id": "greeting-audio", "text": "hello from the microphone"},
    {"type": "sleep_fake", "duration_ms": 5},
    {"type": "close"}
  ],
  "expectations": [
    {"type": "transcript_contains", "text": "ready"},
    {"type": "response_canceled"},
    {"type": "frame_count", "equals": 9},
    {"type": "terminal_reason", "value": "disconnect"},
    {"type": "terminal_provenance", "value": "provider"},
    {"type": "output_state", "value": "partial"},
    {"type": "buffer_disposition", "value": "committed"},
    {"type": "audio_energy"},
    {"type": "tool_called", "name": "lookup"},
    {"type": "tool_result_delivered", "tool_call_id": "call-1"},
    {"type": "tool_result_discarded", "tool_call_id": "call-2"},
    {"type": "no_orphaned_tool_result"}
  ]
}`

func TestScenarioV2ProviderScenarioCompilesEveryProviderVariant(t *testing.T) {
	corpus := hasCorpus{"greeting-audio": true}
	document, err := LoadScenarioV2(providerOnlyScenarioV2, "", corpus)
	if err != nil {
		t.Fatalf("LoadScenarioV2: %v", err)
	}
	if !document.ProviderOnly() {
		t.Fatal("provider-only document was not recognized")
	}
	plan, err := document.ProviderScenario(corpus)
	if err != nil {
		t.Fatalf("ProviderScenario: %v", err)
	}
	if plan.ID != "provider-plan" || plan.Name != "provider plan" || plan.Description != "every provider step and expectation" {
		t.Fatalf("plan identity = %#v", plan)
	}
	wantSteps := []Step{
		{Type: StepSendText, Kind: StepSendText, Text: "hello"},
		{Type: StepSendAudio, Kind: StepSendAudio, CorpusID: "greeting-audio", Text: "hello from the microphone", Corpus: AudioCorpusReference{ID: "greeting-audio", CorpusID: "greeting-audio"}},
		{Type: StepWait, Kind: StepWait, Duration: 5},
		{Type: StepClose, Kind: StepClose},
	}
	if len(plan.Steps) != len(wantSteps) {
		t.Fatalf("plan steps = %#v", plan.Steps)
	}
	for index, want := range wantSteps {
		got := plan.Steps[index]
		if got.Type != want.Type || got.Kind != want.Kind || got.Text != want.Text || got.CorpusID != want.CorpusID || got.Corpus != want.Corpus || got.Duration != want.Duration {
			t.Fatalf("plan step %d = %#v, want %#v", index, got, want)
		}
	}
	wantExpectations := []ExpectedBehavior{
		{Type: ExpectTranscriptContains, Text: "ready"},
		{Type: ExpectResponseCancel},
		{Type: ExpectFrameCount, Count: 9},
		{Type: ExpectTerminalReason, Value: "disconnect"},
		{Type: ExpectTerminalProvenance, Value: "provider"},
		{Type: ExpectOutputState, Value: "partial"},
		{Type: ExpectBufferDisposition, Value: "committed"},
		{Type: ExpectAudioEnergy},
		{Type: ExpectToolCalled, ToolName: "lookup"},
		{Type: ExpectToolResultDelivered, ToolCallID: "call-1"},
		{Type: ExpectToolResultDiscarded, ToolCallID: "call-2"},
		{Type: ExpectNoOrphanedToolResult},
	}
	if len(plan.Expectations) != len(wantExpectations) || len(plan.Expected) != len(wantExpectations) || len(plan.ExpectedBehavior) != len(wantExpectations) {
		t.Fatalf("plan expectations = %#v", plan.Expectations)
	}
	for index, want := range wantExpectations {
		got := plan.Expectations[index]
		if got.Type != want.Type || got.Kind != want.Type || got.Text != want.Text || got.Value != want.Value || got.Count != want.Count || got.ToolName != want.ToolName || got.ToolCallID != want.ToolCallID {
			t.Fatalf("plan expectation %d = %#v, want %#v", index, got, want)
		}
	}
}

func TestScenarioV2ProviderPlanEvaluatesTerminalExpectations(t *testing.T) {
	document, err := LoadScenarioV2(`{"schema_version":"probe.scenario.v2","id":"terminal","steps":[{"type":"close"}],"expectations":[
		{"type":"terminal_reason","value":"disconnect"},
		{"type":"terminal_provenance","value":"provider"},
		{"type":"output_state","value":"partial"}]}`, "")
	if err != nil {
		t.Fatalf("LoadScenarioV2: %v", err)
	}
	plan, err := document.ProviderScenario()
	if err != nil {
		t.Fatalf("ProviderScenario: %v", err)
	}
	results := EvaluateScenario(plan, ObservationSnapshot{TerminalReason: "disconnect", TerminalProvenance: "provider", OutputState: "partial"})
	if len(results) != 3 {
		t.Fatalf("expectation result count = %d, want 3", len(results))
	}
	for index, result := range results {
		if !result.Passed || result.Err != nil {
			t.Fatalf("terminal expectation %d failed: %#v", index, result)
		}
	}
}

func TestScenarioV2BrowserDocumentsAreNotProviderOnly(t *testing.T) {
	cases := map[string]ScenarioV2{
		"provider fixture": {ProviderFixture: "provider.jsonl", Steps: []ScenarioV2Step{{Type: ScenarioV2StepClose}}},
		"browser fixture":  {BrowserFixture: "browser.json", Steps: []ScenarioV2Step{{Type: ScenarioV2StepClose}}},
		"browser step":     {Steps: []ScenarioV2Step{{Type: ScenarioV2StepWebMCPWaitReady}, {Type: ScenarioV2StepClose}}},
		"browser expectation": {Steps: []ScenarioV2Step{{Type: ScenarioV2StepClose}},
			Expectations: []ScenarioV2Expectation{{Type: ScenarioV2ExpectationNoPendingInvocations}}},
	}
	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			if document.ProviderOnly() {
				t.Fatal("browser document reported provider-only")
			}
			if _, err := document.ProviderScenario(); !errors.Is(err, ErrInvalidScenarioV2) || !strings.Contains(err.Error(), "browser-aware executor") {
				t.Fatalf("ProviderScenario error = %v", err)
			}
		})
	}
}

func TestScenarioV2ProviderRunnerExpectationsRejectBrowserDocuments(t *testing.T) {
	browser := `{"schema_version":"probe.scenario.v2","id":"mixed","steps":[{"type":"webmcp_wait_ready"},{"type":"close"}],"expectations":[{"type":"frame_count","equals":1}]}`
	if _, err := LoadScenarioV2(browser, ""); !errors.Is(err, ErrInvalidScenarioV2) || !strings.Contains(err.Error(), "provider-only document") {
		t.Fatalf("browser document with a provider-runner expectation: %v", err)
	}
	typed := ScenarioV2{SchemaVersion: ScenarioV2Version, ID: "typed",
		Steps:        []ScenarioV2Step{{Type: ScenarioV2StepWebMCPWaitReady}, {Type: ScenarioV2StepClose}},
		Expectations: []ScenarioV2Expectation{{Type: ScenarioV2ExpectationAudioEnergy}}}
	if err := typed.Validate(); !errors.Is(err, ErrInvalidScenarioV2) {
		t.Fatalf("typed browser document with a provider-runner expectation: %v", err)
	}
	shared := `{"schema_version":"probe.scenario.v2","id":"shared","steps":[{"type":"webmcp_wait_ready"},{"type":"close"}],"expectations":[{"type":"transcript_contains","text":"x"},{"type":"response_canceled"}]}`
	if _, err := LoadScenarioV2(shared, ""); err != nil {
		t.Fatalf("shared expectations rejected in a browser document: %v", err)
	}
}

func TestScenarioV2ProviderExpectationFieldContracts(t *testing.T) {
	base := `{"schema_version":"probe.scenario.v2","id":"x","steps":[{"type":"close"}],"expectations":[%s]}`
	cases := map[string]string{
		"frame count requires equals":    `{"type":"frame_count"}`,
		"frame count rejects negatives":  `{"type":"frame_count","equals":-1}`,
		"terminal reason requires value": `{"type":"terminal_reason"}`,
		"terminal reason value string":   `{"type":"terminal_reason","value":3}`,
		"output state value non-empty":   `{"type":"output_state","value":" "}`,
		"tool called requires name":      `{"type":"tool_called"}`,
		"delivered requires call ID":     `{"type":"tool_result_delivered"}`,
		"discarded rejects unknown":      `{"type":"tool_result_discarded","tool_call_id":"c","text":"x"}`,
		"audio energy takes no payload":  `{"type":"audio_energy","value":"x"}`,
	}
	for name, expectation := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadScenarioV2(strings.Replace(base, "%s", expectation, 1), ""); !errors.Is(err, ErrInvalidScenarioV2) {
				t.Fatalf("invalid expectation accepted: %v", err)
			}
		})
	}
	plan, err := (ScenarioV2{SchemaVersion: ScenarioV2Version, ID: "x", Steps: []ScenarioV2Step{{Type: ScenarioV2StepClose}},
		Expectations: []ScenarioV2Expectation{{Type: ScenarioV2ExpectationTerminalReason, Value: []byte(`3`)}}}).ProviderScenario()
	if err == nil {
		t.Fatalf("non-string typed value compiled: %#v", plan)
	}
}
