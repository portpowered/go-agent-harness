package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const representativeScenario = `{
  "id": "greeting",
  "description": "portable greeting",
  "steps": [
    {"type": "send_text", "text": "hello"},
    {"type": "send_audio", "corpus_id": "greeting-audio"},
    {"type": "send_tool_result", "tool_call_id": "call-1", "tool_name": "weather", "result": {"ok": true}},
    {"type": "advance_to", "at": 10},
    {"type": "wait", "duration": 5},
    {"type": "close"}
  ],
  "expectations": [
    {"type": "text", "text": "ready"},
    {"type": "tool_result", "tool_call_id": "call-1", "result": {"ok": true}},
    {"type": "audio", "corpus_id": "greeting-audio"},
    {"type": "time", "at": 15},
    {"type": "close"}
  ]
}`

const textScenario = `{"id":"text","steps":[{"type":"send_text","text":"hello"},{"type":"close"}],"expectations":[{"type":"text","text":"reply"}]}`

type testCorpus struct {
	ids   map[string]bool
	calls []string
}

func (c *testCorpus) Lookup(id string) (bool, error) {
	c.calls = append(c.calls, id)
	return c.ids[id], nil
}

type hasCorpus map[string]bool

func (c hasCorpus) Has(id string) bool { return c[id] }

func TestLoadRepresentativeScenarioPreservesTypedOrder(t *testing.T) {
	corpus := &testCorpus{ids: map[string]bool{"greeting-audio": true}}
	scenario, err := Load(strings.NewReader(representativeScenario), corpus)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if scenario.ID != "greeting" || scenario.Description != "portable greeting" {
		t.Fatalf("identity: %#v", scenario)
	}
	wantKinds := []StepKind{StepSendText, StepSendAudio, StepSendToolResult, StepAdvanceTo, StepWait, StepClose}
	if len(scenario.Steps) != len(wantKinds) {
		t.Fatalf("step count: got %d, want %d", len(scenario.Steps), len(wantKinds))
	}
	for index, want := range wantKinds {
		if scenario.Steps[index].Kind != want || scenario.Steps[index].Type != want {
			t.Errorf("step %d kind: got %q/%q, want %q", index, scenario.Steps[index].Kind, scenario.Steps[index].Type, want)
		}
	}
	if got := scenario.Steps[0].Text; got != "hello" {
		t.Errorf("text payload: got %q", got)
	}
	if got := scenario.Steps[1].Corpus.CorpusID; got != "greeting-audio" {
		t.Errorf("corpus ID: got %q", got)
	}
	if got := scenario.Steps[2].ToolCallID; got != "call-1" {
		t.Errorf("tool call ID: got %q", got)
	}
	if got := string(scenario.Steps[2].ToolResult); got != `{"ok": true}` {
		t.Errorf("tool result: got %s", got)
	}
	if scenario.Steps[3].At != 10 || scenario.Steps[4].Duration != 5 {
		t.Errorf("logical values: got advance=%d wait=%d", scenario.Steps[3].At, scenario.Steps[4].Duration)
	}
	if len(scenario.Expectations) != 5 || scenario.Expectations[0].Text != "ready" || scenario.Expectations[1].ToolCallID != "call-1" || scenario.Expectations[3].At != 15 {
		t.Fatalf("expectations: %#v", scenario.Expectations)
	}
	if len(corpus.calls) != 1 || corpus.calls[0] != "greeting-audio" {
		t.Fatalf("lookup calls: %#v", corpus.calls)
	}

	got, err := json.Marshal(scenario)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"id":"greeting","description":"portable greeting","steps":[{"type":"send_text","text":"hello"},{"type":"send_audio","corpus_id":"greeting-audio"},{"type":"send_tool_result","tool_call_id":"call-1","tool_name":"weather","result":{"ok":true}},{"type":"advance_to","at":10},{"type":"wait","duration":5},{"type":"close"}],"expectations":[{"type":"text","text":"ready"},{"type":"tool_result","tool_call_id":"call-1","result":{"ok":true}},{"type":"audio","corpus_id":"greeting-audio"},{"type":"time","at":15},{"type":"close"}]}`
	if string(got) != want {
		t.Fatalf("normalized JSON:\n got  %s\n want %s", got, want)
	}
}

func TestLoadAliasesNestedPayloadAndLoaderAliases(t *testing.T) {
	input := `{"name":"alias-scenario","steps":[{"kind":"send_text","payload":{"text":"hello"}},{"kind":"send_audio","payload":{"corpus":{"id":"a"}}},{"kind":"close"}],"expected_behavior":[{"kind":"audio","corpus_id":"a"},{"kind":"close"}]}`
	scenario, err := LoadScenario(input, hasCorpus{"a": true})
	if err != nil {
		t.Fatalf("LoadScenario: %v", err)
	}
	if scenario.ID != "alias-scenario" || scenario.Steps[0].Text != "hello" || scenario.Steps[1].CorpusID != "a" {
		t.Fatalf("alias parse: %#v", scenario)
	}
	if err := scenario.Validate(hasCorpus{"a": true}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if _, err := Decode(bytes.NewBufferString(textScenario)); err != nil {
		t.Fatalf("Decode: %v", err)
	}
}

func TestLoadErrorsHaveIdentityAndStableLocation(t *testing.T) {
	valid := textScenario
	cases := []struct {
		name    string
		input   string
		lookup  any
		want    error
		loc     string
		message string
	}{
		{"malformed JSON", `{`, nil, ErrMalformed, "document", "unexpected"},
		{"trailing document", valid + ` {}`, nil, ErrMalformed, "document", "trailing"},
		{"top-level unknown field", strings.Replace(valid, `{"id"`, `{"extra":1,"id"`, 1), nil, ErrMalformed, "scenario.extra", "unknown field"},
		{"missing identity", `{"steps":[{"type":"close"}],"expectations":[{"type":"close"}]}`, nil, ErrMissingField, "scenario.id", "required"},
		{"missing steps", `{"id":"x","expectations":[{"type":"close"}]}`, nil, ErrMissingField, "steps", "required"},
		{"empty steps", `{"id":"x","steps":[],"expectations":[{"type":"close"}]}`, nil, ErrEmptyScenario, "steps", "at least"},
		{"missing expectations", `{"id":"x","steps":[{"type":"close"}]}`, nil, ErrMissingField, "expectations", "required"},
		{"empty expectations", `{"id":"x","steps":[{"type":"close"}],"expectations":[]}`, nil, ErrEmptyScenario, "expectations", "at least"},
		{"missing step discriminator", `{"id":"x","steps":[{"text":"x"}],"expectations":[{"type":"close"}]}`, nil, ErrMissingField, "steps[0].type", "discriminator"},
		{"unknown step variant", `{"id":"x","steps":[{"type":"mystery"},{"type":"close"}],"expectations":[{"type":"close"}]}`, nil, ErrUnknownVariant, "steps[0].type", "mystery"},
		{"missing text", `{"id":"x","steps":[{"type":"send_text"},{"type":"close"}],"expectations":[{"type":"close"}]}`, nil, ErrMissingField, "steps[0].text", "required"},
		{"missing corpus ID", `{"id":"x","steps":[{"type":"send_audio"},{"type":"close"}],"expectations":[{"type":"close"}]}`, hasCorpus{"a": true}, ErrMissingField, "steps[0].corpus_id", "required"},
		{"path audio rejected", `{"id":"x","steps":[{"type":"send_audio","path":"x.wav"},{"type":"close"}],"expectations":[{"type":"close"}]}`, hasCorpus{"x.wav": true}, ErrMalformed, "steps[0].path", "unknown field"},
		{"missing tool call ID", `{"id":"x","steps":[{"type":"send_tool_result","result":{}},{"type":"close"}],"expectations":[{"type":"close"}]}`, nil, ErrMissingField, "steps[0].tool_call_id", "required"},
		{"missing tool result", `{"id":"x","steps":[{"type":"send_tool_result","tool_call_id":"c"},{"type":"close"}],"expectations":[{"type":"close"}]}`, nil, ErrMissingField, "steps[0].result", "required"},
		{"missing advance time", `{"id":"x","steps":[{"type":"advance_to"},{"type":"close"}],"expectations":[{"type":"close"}]}`, nil, ErrMissingField, "steps[0].at", "logical"},
		{"missing wait duration", `{"id":"x","steps":[{"type":"wait"},{"type":"close"}],"expectations":[{"type":"close"}]}`, nil, ErrMissingField, "steps[0].duration", "duration"},
		{"mixed step payload", `{"id":"x","steps":[{"type":"send_text","text":"x","corpus_id":"a"},{"type":"close"}],"expectations":[{"type":"close"}]}`, nil, ErrMalformed, "steps[0].corpus_id", "unknown field"},
		{"close not terminal", `{"id":"x","steps":[{"type":"close"},{"type":"send_text","text":"x"}],"expectations":[{"type":"close"}]}`, nil, ErrContradictory, "steps[0]", "terminal"},
		{"missing close", `{"id":"x","steps":[{"type":"send_text","text":"x"}],"expectations":[{"type":"text","text":"x"}]}`, nil, ErrContradictory, "steps", "close"},
		{"multiple close", `{"id":"x","steps":[{"type":"close"},{"type":"close"}],"expectations":[{"type":"close"}]}`, nil, ErrContradictory, "steps[1]", "only one"},
		{"non-progressing time", `{"id":"x","steps":[{"type":"advance_to","at":0},{"type":"close"}],"expectations":[{"type":"close"}]}`, nil, ErrInvalidField, "steps[0].at", "positive"},
		{"unknown expectation", `{"id":"x","steps":[{"type":"close"}],"expectations":[{"type":"never"}]}`, nil, ErrUnknownVariant, "expectations[0].type", "never"},
		{"missing expectation text", `{"id":"x","steps":[{"type":"close"}],"expectations":[{"type":"text"}]}`, nil, ErrMissingField, "expectations[0].text", "required"},
		{"unsatisfiable tool expectation", `{"id":"x","steps":[{"type":"send_text","text":"x"},{"type":"close"}],"expectations":[{"type":"tool_result","tool_call_id":"missing","result":{}}]}`, nil, ErrUnsatisfiable, "expectations[0].tool_call_id", "no declared"},
		{"unsatisfiable time expectation", `{"id":"x","steps":[{"type":"close"}],"expectations":[{"type":"time","at":1}]}`, nil, ErrUnsatisfiable, "expectations[0].at", "unreachable"},
		{"bad expectation step", `{"id":"x","steps":[{"type":"close"}],"expectations":[{"type":"text","text":"x","step":4}]}`, nil, ErrUnsatisfiable, "expectations[0].step", "does not exist"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(test.input, test.lookup)
			if err == nil {
				t.Fatal("Load unexpectedly succeeded")
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("error identity: got %v, want errors.Is(..., %v)", err, test.want)
			}
			var scenarioErr *ScenarioError
			if !errors.As(err, &scenarioErr) {
				t.Fatalf("error type: %T", err)
			}
			if scenarioErr.Location != test.loc || !strings.Contains(scenarioErr.Message, test.message) {
				t.Fatalf("contract: category=%q location=%q message=%q", scenarioErr.Category, scenarioErr.Location, scenarioErr.Message)
			}
		})
	}
}

func TestUnknownCorpusIsReportedByLoadAndNamesID(t *testing.T) {
	corpus := &testCorpus{ids: map[string]bool{}}
	_, err := Load(`{"id":"x","steps":[{"type":"send_audio","corpus_id":"missing-audio"},{"type":"close"}],"expectations":[{"type":"audio","corpus_id":"missing-audio"}]}`, corpus)
	if !errors.Is(err, ErrUnknownCorpus) {
		t.Fatalf("identity: %v", err)
	}
	var scenarioErr *ScenarioError
	if !errors.As(err, &scenarioErr) || scenarioErr.CorpusID != "missing-audio" || scenarioErr.StepIndex != 0 || scenarioErr.Location != "steps[0].corpus_id" {
		t.Fatalf("unknown corpus details: %#v", scenarioErr)
	}
	if !strings.Contains(err.Error(), "missing-audio") {
		t.Fatalf("message does not name ID: %v", err)
	}
	if len(corpus.calls) != 1 {
		t.Fatalf("lookup calls: %#v", corpus.calls)
	}
}

func TestLookupAdaptersAndTypedValidation(t *testing.T) {
	if _, err := Load(textScenario, func(id string) bool { return id == "unused" }); err != nil {
		t.Fatalf("function lookup without audio: %v", err)
	}
	if _, err := Load(representativeScenario, map[string]struct{}{"greeting-audio": {}}); err != nil {
		t.Fatalf("map lookup: %v", err)
	}
	if _, err := Load(`{"id":"x","steps":[{"type":"send_audio","corpus_id":"a"},{"type":"close"}],"expectations":[{"type":"audio","corpus_id":"a"}]}`, func(string) (bool, error) { return true, nil }); err != nil {
		t.Fatalf("error-returning lookup: %v", err)
	}
	if _, err := Load(`{"id":"x","steps":[{"type":"send_audio","corpus_id":"a"},{"type":"close"}],"expectations":[{"type":"audio","corpus_id":"a"}]}`, func(string) error { return fmt.Errorf("missing") }); !errors.Is(err, ErrUnknownCorpus) {
		t.Fatalf("lookup error identity: %v", err)
	}

	valid := Scenario{
		ID: "typed",
		Steps: []Step{
			{Type: StepSendText, Kind: StepSendText, Text: "hello"},
			{Type: StepClose, Kind: StepClose},
		},
		Expectations: []ExpectedBehavior{{Type: ExpectText, Kind: ExpectText, Text: "reply"}},
	}
	if err := valid.Validate(); err != nil {
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
	unknown.Steps = []Step{{Type: StepKind("unknown")}, {Type: StepClose, Kind: StepClose}}
	if !errors.Is(unknown.Validate(), ErrUnknownVariant) {
		t.Fatalf("unknown typed variant was accepted")
	}
}

func TestStrictNestedExpectationAndExpectationOrdering(t *testing.T) {
	unknownPayload := `{"id":"x","steps":[{"type":"close"}],"expectations":[{"type":"close","payload":{"extra":true}}]}`
	if _, err := Load(unknownPayload); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unknown nested expectation field: %v", err)
	}
	ordered := `{"id":"x","steps":[{"type":"send_text","text":"x"},{"type":"send_text","text":"y"},{"type":"close"}],"expectations":[{"type":"text","text":"x","step":1},{"type":"text","text":"y","step":0}]}`
	if _, err := Load(ordered); !errors.Is(err, ErrContradictory) {
		t.Fatalf("expectation order: %v", err)
	}
	contradictory := `{"id":"x","steps":[{"type":"advance_to","at":3},{"type":"wait","duration":2},{"type":"advance_to","at":4},{"type":"close"}],"expectations":[{"type":"close"}]}`
	if _, err := Load(contradictory); !errors.Is(err, ErrContradictory) {
		t.Fatalf("logical order: %v", err)
	}
}

func TestErrorFormattingAndMarshalAliases(t *testing.T) {
	scenarioErr := makeError(CategoryInvalidField, "steps[2].text", "must not be empty")
	if scenarioErr.Error() != "invalid_field at steps[2].text: must not be empty" || scenarioErr.Kind != CategoryInvalidField {
		t.Fatalf("error formatting: %q %#v", scenarioErr.Error(), scenarioErr)
	}
	if (*ScenarioError)(nil).Error() != "<nil>" {
		t.Fatal("nil error formatting")
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

func TestAdditionalDeclarativeShapesAndLookupAdapters(t *testing.T) {
	input := `{"id":"all","steps":[{"kind":"send_text","payload":{"value":"hello"}},{"kind":"send_audio","payload":{"corpusID":"a"}},{"kind":"send_tool_result","payload":{"toolCallID":"c","toolName":"lookup","tool_result":{"ok":true}}},{"kind":"advance_to","payload":{"logicalTime":"7"}},{"kind":"wait","payload":{"duration":"1ms"}},{"kind":"close"}],"expected":[{"kind":"text","value":"x","count":1},{"kind":"transcript","value":"y"},{"kind":"contains","text":"z"},{"kind":"audio","corpusID":"a"},{"kind":"tool_call","toolName":"lookup"},{"kind":"tool_result","toolCallID":"c","result":{"ok":true}},{"kind":"event","event":"ready"},{"kind":"time","logicalTime":"1ms","step":0,"after_step":0,"before_step":5},{"kind":"close"}]}`
	if _, err := Load(json.RawMessage(input), map[string]struct{}{"a": {}}); err != nil {
		t.Fatalf("all declarative aliases: %v", err)
	}
	if _, err := Load(textScenario, func(string) (bool, error) { return true, nil }, nil); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("multiple lookups: %v", err)
	}
	if _, err := Load(42); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unsupported input: %v", err)
	}
	if _, err := Load(strings.NewReader("")); !errors.Is(err, ErrMalformed) {
		t.Fatalf("empty reader: %v", err)
	}
	for _, raw := range []string{"[]", "{} x"} {
		if _, err := Load(raw); !errors.Is(err, ErrMalformed) {
			t.Errorf("malformed %q: %v", raw, err)
		}
	}

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

func TestMalformedFieldTypesAndExpectationContracts(t *testing.T) {
	cases := []string{
		`{"id":"x","steps":[{"type":"send_text","text":4},{"type":"close"}],"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":[{"type":"send_audio","corpus_id":4},{"type":"close"}],"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":[{"type":"send_audio","corpus":{"path":"x"}},{"type":"close"}],"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":[{"type":"wait","duration":true},{"type":"close"}],"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":[{"type":"advance_to","at":1.2},{"type":"close"}],"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":[{"type":"close","payload":{"x":1}}],"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":[{"type":"close"}],"expectations":[{"type":"text","text":"x","payload":{"x":1}}]}`,
		`{"id":"x","steps":[{"type":"close"}],"expectations":[{"type":"text","text":"x","count":1.2}]}`,
		`{"id":"x","steps":[{"type":"close"}],"expectations":[{"type":"text","text":"x","step":-1}]}`,
		`{"id":"x","steps":[{"type":"close"}],"expectations":[{"type":"text","text":"x","after":1,"before":1}]}`,
	}
	for _, input := range cases {
		if _, err := Load(input); err == nil {
			t.Errorf("malformed input accepted: %s", input)
		}
	}
	validKinds := []string{
		`{"type":"tool_call","tool_name":"lookup"}`,
		`{"type":"event","event":"ready"}`,
		`{"type":"contains","text":"ok"}`,
	}
	for _, expectation := range validKinds {
		input := `{"id":"x","steps":[{"type":"close"}],"expectations":[` + expectation + `]}`
		if _, err := Load(input); err != nil {
			t.Errorf("valid expectation rejected: %v", err)
		}
	}
}

type methodCorpus struct{ ok bool }

func (c methodCorpus) Has(string) bool { return c.ok }

type resolveCorpus struct{ ok bool }

func (c resolveCorpus) Resolve(string) (bool, error) { return c.ok, nil }

type entryCorpus struct{}

func (entryCorpus) Lookup(string) (entryCorpus, error) { return entryCorpus{}, nil }

type pointerCorpus struct{}

func (pointerCorpus) Contains(string) *entryCorpus { return &entryCorpus{} }

type errorCorpus struct{}

func (errorCorpus) Exists(string) error { return nil }

type unusableCorpus struct{}

func (unusableCorpus) Lookup(int) bool { return true }

func TestExpectationContractErrorTable(t *testing.T) {
	base := func(expectation string) string {
		return `{"id":"x","steps":[{"type":"close"}],"expectations":[` + expectation + `]}`
	}
	cases := []struct {
		name  string
		input string
		want  error
	}{
		{"audio corpus missing", base(`{"type":"audio"}`), ErrMissingField},
		{"tool name missing", base(`{"type":"tool_call"}`), ErrMissingField},
		{"tool result ID missing", base(`{"type":"tool_result","result":{}}`), ErrMissingField},
		{"tool result value missing", base(`{"type":"tool_result","tool_call_id":"c"}`), ErrMissingField},
		{"time missing", base(`{"type":"time"}`), ErrMissingField},
		{"time zero", base(`{"type":"time","at":0}`), ErrInvalidField},
		{"event missing", base(`{"type":"event"}`), ErrMissingField},
		{"negative count", base(`{"type":"close","count":-1}`), ErrInvalidField},
		{"negative after", base(`{"type":"text","text":"x","after":-1}`), ErrInvalidField},
		{"negative before", base(`{"type":"text","text":"x","before":-1}`), ErrInvalidField},
		{"conflicting discriminator", `{"id":"x","steps":[{"type":"close"}],"expectations":[{"type":"text","kind":"event","text":"x"}]}`, ErrInvalidField},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Load(test.input); !errors.Is(err, test.want) {
				t.Fatalf("got %v, want errors.Is(..., %v)", err, test.want)
			}
		})
	}
}

func TestCorpusLookupMethodShapes(t *testing.T) {
	input := `{"id":"x","steps":[{"type":"send_audio","corpus_id":"a"},{"type":"close"}],"expectations":[{"type":"audio","corpus_id":"a"}]}`
	lookups := []any{methodCorpus{true}, resolveCorpus{true}, entryCorpus{}, pointerCorpus{}, errorCorpus{}}
	for _, lookup := range lookups {
		if _, err := Load(input, lookup); err != nil {
			t.Errorf("lookup %T: %v", lookup, err)
		}
	}
	for _, lookup := range []any{methodCorpus{false}, resolveCorpus{false}, unusableCorpus{}, map[int]bool{1: true}} {
		if _, err := Load(input, lookup); !errors.Is(err, ErrUnknownCorpus) {
			t.Errorf("unknown lookup %T: %v", lookup, err)
		}
	}
}

func TestErrorAndFieldEdgeCases(t *testing.T) {
	if makeError(CategoryInvalidField, "", "bad").Error() != "invalid_field: bad" {
		t.Fatal("empty error location was not formatted")
	}
	if (&ScenarioError{Category: ErrorCategory("other")}).Unwrap() != nil {
		t.Fatal("unknown category unexpectedly unwraps")
	}
	if _, err := Load([]byte(textScenario)); err != nil {
		t.Fatalf("byte input: %v", err)
	}
	inputs := []string{
		`{"id":"x","steps":[{"type":"send_text","text":"x","value":"y"},{"type":"close"}],"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":[{"type":"send_text","kind":"send_audio","text":"x"},{"type":"close"}],"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":[{"type":"send_audio","corpus":{"id":"a","path":"x"}},{"type":"close"}],"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":[{"type":"advance_to","at":"not-a-duration"},{"type":"close"}],"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":[{"type":"close"}],"expectations":[{"type":"text","text":"x","step":1.1}]}`,
	}
	for _, input := range inputs {
		if _, err := Load(input, map[string]struct{}{"a": {}}); err == nil {
			t.Errorf("edge input unexpectedly succeeded: %s", input)
		}
	}
	for _, input := range []string{
		`{"id":"x","steps":null,"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":{},"expectations":[{"type":"close"}]}`,
		`{"id":"x","steps":[{"type":"close"}],"expectations":null}`,
		`{"id":"x","steps":[{"type":"close"}],"expectations":{}}`,
	} {
		if _, err := Load(input); !errors.Is(err, ErrInvalidField) {
			t.Errorf("array shape accepted: %v", err)
		}
	}
}
