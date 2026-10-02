package probe

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type testCorpus struct {
	ids   map[string]bool
	calls []string
}

func (c *testCorpus) Has(id string) bool {
	c.calls = append(c.calls, id)
	return c.ids[id]
}

type hasCorpus map[string]bool

func (c hasCorpus) Has(id string) bool { return c[id] }

func closeStep() Step { return Step{Type: StepClose} }

func TestRepresentativeScenarioValidatesAndMarshalsInOrder(t *testing.T) {
	corpus := &testCorpus{ids: map[string]bool{"greeting-audio": true}}
	scenario := Scenario{
		ID: "greeting", Description: "portable greeting",
		Steps: []Step{
			{Type: StepSendText, Text: "hello"},
			{Type: StepSendAudio, CorpusID: "greeting-audio", Text: "hello from the microphone"},
			{Type: StepSendToolResult, ToolCallID: "call-1", ToolName: "weather", Result: json.RawMessage(`{"ok":true}`)},
			{Type: StepAdvanceTo, At: 10},
			{Type: StepWait, Duration: 5},
			closeStep(),
		},
		Expectations: []ExpectedBehavior{
			{Type: ExpectText, Text: "ready"},
			{Type: ExpectToolResult, ToolCallID: "call-1", Result: json.RawMessage(`{"ok":true}`)},
			{Type: ExpectAudio, CorpusID: "greeting-audio"},
			{Type: ExpectTime, At: 15, HasAt: true},
			{Type: ExpectClose},
		},
	}
	if err := scenario.Validate(corpus); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(corpus.calls) != 1 || corpus.calls[0] != "greeting-audio" {
		t.Fatalf("lookup calls: %#v", corpus.calls)
	}
	// probe.Scenario's JSON is the shape of the in-memory runner plan, not a
	// loadable document: probe.scenario.v2 is the only scenario format a
	// loader accepts.
	got, err := json.Marshal(scenario)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"id":"greeting","description":"portable greeting","steps":[{"type":"send_text","text":"hello"},{"type":"send_audio","corpus_id":"greeting-audio","text":"hello from the microphone"},{"type":"send_tool_result","tool_call_id":"call-1","tool_name":"weather","result":{"ok":true}},{"type":"advance_to","at":10},{"type":"wait","duration":5},{"type":"close"}],"expectations":[{"type":"text","text":"ready"},{"type":"tool_result","tool_call_id":"call-1","result":{"ok":true}},{"type":"audio","corpus_id":"greeting-audio"},{"type":"time","at":15},{"type":"close"}]}`
	if string(got) != want {
		t.Fatalf("normalized JSON:\n got  %s\n want %s", got, want)
	}
}

// TestScenarioValidationErrorsHaveIdentityAndStableLocation covers the
// timeline and satisfiability contract of the runner's execution plan.
func TestScenarioValidationErrorsHaveIdentityAndStableLocation(t *testing.T) {
	text := func(value string) Step { return Step{Type: StepSendText, Text: value} }
	cases := []struct {
		name     string
		scenario Scenario
		want     error
		loc      string
		message  string
	}{
		{"close not terminal", Scenario{ID: "x", Steps: []Step{closeStep(), text("x")}, Expectations: []ExpectedBehavior{{Type: ExpectClose}}}, ErrContradictory, "steps[0]", "terminal"},
		{"missing close", Scenario{ID: "x", Steps: []Step{text("x")}, Expectations: []ExpectedBehavior{{Type: ExpectText, Text: "x"}}}, ErrContradictory, "steps", "close"},
		{"multiple close", Scenario{ID: "x", Steps: []Step{closeStep(), closeStep()}, Expectations: []ExpectedBehavior{{Type: ExpectClose}}}, ErrContradictory, "steps[1]", "only one"},
		{"unsatisfiable tool expectation", Scenario{ID: "x", Steps: []Step{text("x"), closeStep()}, Expectations: []ExpectedBehavior{{Type: ExpectToolResult, ToolCallID: "missing", Result: json.RawMessage(`{}`)}}}, ErrUnsatisfiable, "expectations[0].tool_call_id", "no declared"},
		{"unsatisfiable time expectation", Scenario{ID: "x", Steps: []Step{closeStep()}, Expectations: []ExpectedBehavior{{Type: ExpectTime, At: 1, HasAt: true}}}, ErrUnsatisfiable, "expectations[0].at", "unreachable"},
		{"bad expectation step", Scenario{ID: "x", Steps: []Step{closeStep()}, Expectations: []ExpectedBehavior{{Type: ExpectText, Text: "x", StepIndex: 4, HasStep: true}}}, ErrUnsatisfiable, "expectations[0].step", "does not exist"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.scenario.Validate()
			if !errors.Is(err, test.want) {
				t.Fatalf("error identity: got %v, want errors.Is(..., %v)", err, test.want)
			}
			var scenarioErr *ScenarioError
			if !errors.As(err, &scenarioErr) || scenarioErr.Location != test.loc || !strings.Contains(scenarioErr.Message, test.message) {
				t.Fatalf("contract: %#v", scenarioErr)
			}
		})
	}
}

func TestScenarioValidationRejectsDisorderedExpectationsAndTime(t *testing.T) {
	ordered := Scenario{ID: "x", Steps: []Step{{Type: StepSendText, Text: "x"}, {Type: StepSendText, Text: "y"}, closeStep()},
		Expectations: []ExpectedBehavior{{Type: ExpectText, Text: "x", StepIndex: 1, HasStep: true}, {Type: ExpectText, Text: "y", StepIndex: 0, HasStep: true}}}
	if err := ordered.Validate(); !errors.Is(err, ErrContradictory) {
		t.Fatalf("expectation order: %v", err)
	}
	timeline := Scenario{ID: "x", Steps: []Step{{Type: StepAdvanceTo, At: 3}, {Type: StepWait, Duration: 2}, {Type: StepAdvanceTo, At: 4}, closeStep()},
		Expectations: []ExpectedBehavior{{Type: ExpectClose}}}
	if err := timeline.Validate(); !errors.Is(err, ErrContradictory) {
		t.Fatalf("logical order: %v", err)
	}
}

func TestUnknownCorpusIsReportedByValidateAndNamesID(t *testing.T) {
	corpus := &testCorpus{ids: map[string]bool{}}
	scenario := Scenario{ID: "x", Steps: []Step{{Type: StepSendAudio, CorpusID: "missing-audio"}, closeStep()},
		Expectations: []ExpectedBehavior{{Type: ExpectAudio, CorpusID: "missing-audio"}}}
	err := scenario.Validate(corpus)
	if !errors.Is(err, ErrUnknownCorpus) {
		t.Fatalf("identity: %v", err)
	}
	var scenarioErr *ScenarioError
	if !errors.As(err, &scenarioErr) || scenarioErr.CorpusID != "missing-audio" || scenarioErr.StepIndex != 0 || scenarioErr.Location != "steps[0].corpus_id" {
		t.Fatalf("unknown corpus details: %#v", scenarioErr)
	}
	if !strings.Contains(err.Error(), "missing-audio") || len(corpus.calls) != 1 {
		t.Fatalf("message does not name ID or lookup calls %#v: %v", corpus.calls, err)
	}
}

func TestLookupAdaptersAndTypedValidation(t *testing.T) {
	valid := Scenario{
		ID:           "typed",
		Steps:        []Step{{Type: StepSendText, Kind: StepSendText, Text: "hello"}, {Type: StepClose, Kind: StepClose}},
		Expectations: []ExpectedBehavior{{Type: ExpectText, Kind: ExpectText, Text: "reply"}},
	}
	if err := valid.Validate(hasCorpus{"unused": true}); err != nil {
		t.Fatalf("typed validation: %v", err)
	}
	if !valid.Valid() {
		t.Fatal("Valid returned false")
	}
	valid.Steps[0].CorpusID = "a"
	if !errors.Is(valid.Validate(), ErrInvalidField) {
		t.Fatalf("mixed typed payload was accepted")
	}
	unknown := valid
	unknown.Steps = []Step{{Type: StepKind(unknownLabel)}, {Type: StepClose, Kind: StepClose}}
	if !errors.Is(unknown.Validate(), ErrUnknownVariant) {
		t.Fatalf("unknown typed variant was accepted")
	}
	audio := Scenario{ID: "x", Steps: []Step{{Type: StepSendAudio, CorpusID: "a"}, closeStep()}, Expectations: []ExpectedBehavior{{Type: ExpectAudio, CorpusID: "a"}}}
	if !errors.Is(audio.Validate(hasCorpus{"a": false}), ErrUnknownCorpus) {
		t.Fatalf("unknown corpus identity: %v", audio.Validate(hasCorpus{"a": false}))
	}
}

func TestTypedPayloadsAreRejectedPerVariant(t *testing.T) {
	stepCases := []struct {
		name string
		step Step
		loc  string
	}{
		{"send text with audio", Step{Type: StepSendText, Text: "hello", CorpusID: "a"}, "steps[0].corpus_id"},
		{"send tool result with time", Step{Type: StepSendToolResult, ToolCallID: "call", Result: json.RawMessage(`{}`), At: 1}, "steps[0].at"},
		{"advance with text", Step{Type: StepAdvanceTo, At: 1, Text: "hello"}, "steps[0].text"},
		{"wait with call ID", Step{Type: StepWait, Duration: 1, ToolCallID: "call"}, "steps[0].tool_call_id"},
		{"close with duration", Step{Type: StepClose, Duration: 1}, "steps[0].duration"},
	}
	for _, test := range stepCases {
		t.Run(test.name, func(t *testing.T) {
			scenario := Scenario{ID: "x", Steps: []Step{test.step, {Type: StepClose}}, Expectations: []ExpectedBehavior{{Type: ExpectClose}}}
			assertTypedPayloadError(t, scenario.Validate(hasCorpus{"a": true}), test.loc)
		})
	}

	expectationCases := []struct {
		name        string
		expectation ExpectedBehavior
		loc         string
	}{
		{"text with audio", ExpectedBehavior{Type: ExpectText, Text: "ok", CorpusID: "a"}, "expectations[0].corpus_id"},
		{"transcript with tool", ExpectedBehavior{Type: ExpectTranscript, Text: "ok", ToolCallID: "c"}, "expectations[0].tool_call_id"},
		{"contains with result", ExpectedBehavior{Type: ExpectContains, Text: "ok", Result: json.RawMessage(`{}`)}, "expectations[0].result"},
		{"audio with text", ExpectedBehavior{Type: ExpectAudio, CorpusID: "a", Text: "ok"}, "expectations[0].text"},
		{"tool call with result", ExpectedBehavior{Type: ExpectToolCall, ToolName: "lookup", Result: json.RawMessage(`{}`)}, "expectations[0].result"},
		{"tool result with tool name", ExpectedBehavior{Type: ExpectToolResult, ToolCallID: "call", Result: json.RawMessage(`{}`), ToolName: "lookup"}, "expectations[0].tool_name"},
		{"close with text", ExpectedBehavior{Type: ExpectClose, Text: "unexpected"}, "expectations[0].text"},
		{"time with text", ExpectedBehavior{Type: ExpectTime, At: 1, HasAt: true, Text: "unexpected"}, "expectations[0].text"},
		{"event with audio", ExpectedBehavior{Type: ExpectEvent, Value: "ready", CorpusID: "a"}, "expectations[0].corpus_id"},
	}
	for _, test := range expectationCases {
		t.Run(test.name, func(t *testing.T) {
			scenario := Scenario{ID: "x", Steps: []Step{{Type: StepClose}}, Expectations: []ExpectedBehavior{test.expectation}}
			assertTypedPayloadError(t, scenario.Validate(), test.loc)
		})
	}
}

func TestTypedPayloadAliasesMustAgree(t *testing.T) {
	base := func(step Step) Scenario {
		return Scenario{ID: "x", Steps: []Step{step, {Type: StepClose}}, Expectations: []ExpectedBehavior{{Type: ExpectClose}}}
	}
	cases := []struct {
		name     string
		scenario Scenario
		location string
	}{
		{"audio corpus IDs", base(Step{Type: StepSendAudio, CorpusID: "a", Corpus: AudioCorpusReference{CorpusID: "b"}}), "steps[0].corpus_id"},
		{"tool results", base(Step{Type: StepSendToolResult, ToolCallID: "call", ToolResult: json.RawMessage(`{"a":1}`), Result: json.RawMessage(`{"b":2}`)}), "steps[0].result"},
		{"step logical time", base(Step{Type: StepAdvanceTo, At: 1, Time: 2}), "steps[0].at"},
		{"expected logical time", Scenario{ID: "x", Steps: []Step{{Type: StepClose}}, Expectations: []ExpectedBehavior{{Type: ExpectTime, At: 1, Time: 2, HasAt: true}}}, "expectations[0].at"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			assertTypedError(t, test.scenario.Validate(hasCorpus{"a": true}), test.location, "aliases disagree")
		})
	}
	if err := base(Step{Type: StepClose}).Validate(hasCorpus{}, hasCorpus{}); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("multiple typed corpus lookups: %v", err)
	}
}

func assertTypedPayloadError(t *testing.T, err error, location string) {
	t.Helper()
	assertTypedError(t, err, location, "unexpected payload")
}

func assertTypedError(t *testing.T, err error, location string, message string) {
	t.Helper()
	if !errors.Is(err, ErrInvalidField) {
		t.Fatalf("identity: got %v", err)
	}
	var scenarioErr *ScenarioError
	if !errors.As(err, &scenarioErr) || scenarioErr.Category != CategoryInvalidField || scenarioErr.Location != location || !strings.Contains(scenarioErr.Message, message) {
		t.Fatalf("contract: %#v", scenarioErr)
	}
}

func TestErrorFormattingAndMarshalAliases(t *testing.T) {
	scenarioErr := makeError(CategoryInvalidField, "steps[2].text", "must not be empty")
	if scenarioErr.Error() != "invalid_field at steps[2].text: must not be empty" || scenarioErr.Kind != CategoryInvalidField {
		t.Fatalf("error formatting: %q %#v", scenarioErr.Error(), scenarioErr)
	}
	if (*ScenarioError)(nil).Error() != nilErrorText {
		t.Fatal("nil error formatting")
	}
	if makeError(CategoryInvalidField, "", "bad").Error() != "invalid_field: bad" {
		t.Fatal("empty error location was not formatted")
	}
	if (&ScenarioError{Category: ErrorCategory("other")}).Unwrap() != nil {
		t.Fatal("unknown category unexpectedly unwraps")
	}
	ref, marshalErr := json.Marshal(AudioCorpusReference{CorpusID: "a"})
	if marshalErr != nil || string(ref) != `{"corpus_id":"a"}` {
		t.Fatalf("reference marshal: %s %v", ref, marshalErr)
	}
	step, marshalErr := json.Marshal(Step{Type: StepSendText, Text: "hello"})
	if marshalErr != nil || string(step) != `{"type":"send_text","text":"hello"}` {
		t.Fatalf("step marshal: %s %v", step, marshalErr)
	}
	closeExpectation, marshalErr := json.Marshal(ExpectedBehavior{Type: ExpectClose, Kind: ExpectClose})
	if marshalErr != nil || string(closeExpectation) != `{"type":"close"}` {
		t.Fatalf("expectation marshal: %s %v", closeExpectation, marshalErr)
	}
}

func TestTypedAliasFieldsValidate(t *testing.T) {
	typed := Scenario{
		ID: "typed-all",
		Steps: []Step{
			{Type: StepSendText, Text: "hello"},
			{Type: StepSendAudio, Corpus: AudioCorpusReference{CorpusID: "a"}},
			{Type: StepSendToolResult, ToolCallID: "c", Result: json.RawMessage(`{"ok":true}`)},
			{Type: StepAdvanceTo, At: 1},
			{Type: StepWait, Duration: 1},
			{Type: StepClose},
		},
		ExpectedBehavior: []ExpectedBehavior{{Type: ExpectText, Text: "reply"}},
	}
	if err := typed.Validate(hasCorpus{"a": true}); err != nil {
		t.Fatalf("typed aliases: %v", err)
	}
	if got := (Scenario{ID: "x", Steps: typed.Steps, Expected: typed.Expectations}).Validate(); got == nil {
		t.Fatal("expected audio lookup to be required")
	}
}

func TestTypedValidationErrorTable(t *testing.T) {
	base := func(step Step) Scenario {
		return Scenario{ID: "x", Steps: []Step{step, {Type: StepClose}}, Expectations: []ExpectedBehavior{{Type: ExpectText, Text: "ok"}}}
	}
	expectation := func(value ExpectedBehavior) Scenario {
		return Scenario{ID: "x", Steps: []Step{{Type: StepClose}}, Expectations: []ExpectedBehavior{value}}
	}
	cases := []struct {
		name     string
		scenario Scenario
		want     error
	}{
		{"missing identity", Scenario{Steps: []Step{{Type: StepClose}}, Expectations: []ExpectedBehavior{{Type: ExpectClose}}}, ErrMissingField},
		{"empty steps", Scenario{ID: "x", Expectations: []ExpectedBehavior{{Type: ExpectClose}}}, ErrEmptyScenario},
		{"empty expectations", Scenario{ID: "x", Steps: []Step{{Type: StepClose}}}, ErrEmptyScenario},
		{"text required", base(Step{Type: StepSendText}), ErrMissingField},
		{"audio required", base(Step{Type: StepSendAudio}), ErrMissingField},
		{"tool ID required", base(Step{Type: StepSendToolResult, Result: json.RawMessage(`{}`)}), ErrMissingField},
		{"tool result required", base(Step{Type: StepSendToolResult, ToolCallID: "c"}), ErrMissingField},
		{"advance positive", base(Step{Type: StepAdvanceTo}), ErrInvalidField},
		{"wait positive", base(Step{Type: StepWait}), ErrInvalidField},
		{"close payload", Scenario{ID: "x", Steps: []Step{{Type: StepClose, Text: "bad"}}, Expectations: []ExpectedBehavior{{Type: ExpectClose}}}, ErrInvalidField},
		{"audio corpus missing", expectation(ExpectedBehavior{Type: ExpectAudio}), ErrMissingField},
		{"tool name missing", expectation(ExpectedBehavior{Type: ExpectToolCall}), ErrMissingField},
		{"tool result ID missing", expectation(ExpectedBehavior{Type: ExpectToolResult, Result: json.RawMessage(`{}`)}), ErrMissingField},
		{"tool result value missing", expectation(ExpectedBehavior{Type: ExpectToolResult, ToolCallID: "c"}), ErrMissingField},
		{"time missing", expectation(ExpectedBehavior{Type: ExpectTime}), ErrMissingField},
		{"event missing", expectation(ExpectedBehavior{Type: ExpectEvent}), ErrMissingField},
		{"negative count", expectation(ExpectedBehavior{Type: ExpectClose, Count: -1}), ErrInvalidField},
		{"negative after", expectation(ExpectedBehavior{Type: ExpectText, Text: "x", HasAfter: true, AfterStep: -1}), ErrInvalidField},
		{"negative before", expectation(ExpectedBehavior{Type: ExpectText, Text: "x", HasBefore: true, BeforeStep: -1}), ErrInvalidField},
		{"conflicting discriminator", expectation(ExpectedBehavior{Type: ExpectText, Kind: ExpectEvent, Text: "x"}), ErrContradictory},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := test.scenario.Validate(hasCorpus{"a": true}); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want errors.Is(..., %v)", err, test.want)
			}
		})
	}

	badAfter := Scenario{ID: "x", Steps: []Step{{Type: StepClose}}, Expectations: []ExpectedBehavior{{Type: ExpectText, Text: "x", HasAfter: true, AfterStep: 1}}}
	if !errors.Is(badAfter.Validate(), ErrUnsatisfiable) {
		t.Fatalf("bad after: %v", badAfter.Validate())
	}
	badBefore := Scenario{ID: "x", Steps: []Step{{Type: StepClose}}, Expectations: []ExpectedBehavior{{Type: ExpectText, Text: "x", HasBefore: true, BeforeStep: 2}}}
	if !errors.Is(badBefore.Validate(), ErrUnsatisfiable) {
		t.Fatalf("bad before: %v", badBefore.Validate())
	}
	badAudio := Scenario{ID: "x", Steps: []Step{{Type: StepSendAudio, CorpusID: "other"}, {Type: StepClose}}, Expectations: []ExpectedBehavior{{Type: ExpectAudio, CorpusID: "missing"}}}
	if !errors.Is(badAudio.Validate(hasCorpus{"other": true}), ErrUnsatisfiable) {
		t.Fatalf("bad audio expectation: %v", badAudio.Validate(hasCorpus{"other": true}))
	}
	overflow := Scenario{ID: "x", Steps: []Step{{Type: StepAdvanceTo, At: LogicalTime(mathMaxInt64())}, {Type: StepWait, Duration: 1}, {Type: StepClose}}, Expectations: []ExpectedBehavior{{Type: ExpectText, Text: "x"}}}
	if !errors.Is(overflow.Validate(), ErrInvalidField) {
		t.Fatalf("overflow: %v", overflow.Validate())
	}
}

func mathMaxInt64() LogicalTime { return LogicalTime(9223372036854775807) }
