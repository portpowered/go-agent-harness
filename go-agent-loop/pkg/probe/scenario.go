// Package probe defines a validated, transport-neutral probe scenario.
package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type StepKind string

const (
	StepSendText           StepKind = "send_text"
	StepSendAudio          StepKind = "send_audio"
	StepSendToolResult     StepKind = "send_tool_result"
	StepAdvanceTo          StepKind = "advance_to"
	StepWait               StepKind = "wait"
	StepClose              StepKind = "close"
	StepKindSendText                = StepSendText
	StepKindSendAudio               = StepSendAudio
	StepKindSendToolResult          = StepSendToolResult
	StepKindAdvanceTo               = StepAdvanceTo
	StepKindWait                    = StepWait
	StepKindClose                   = StepClose
)

type StepType = StepKind
type LogicalTime int64

type AudioCorpusReference struct {
	ID       string `json:"-"`
	CorpusID string `json:"corpus_id"`
}
type CorpusReference = AudioCorpusReference

// These payload names are public vocabulary for callers that want to build a
// scenario in Go; Step keeps the same data in serializable scalar fields.
type SendText struct{ Text string }
type SendAudio struct {
	Corpus   AudioCorpusReference
	CorpusID string
}
type SendToolResult struct {
	ToolCallID, ToolName string
	Result               json.RawMessage
}
type AdvanceTo struct{ At, Time LogicalTime }
type Wait struct{ Duration LogicalTime }
type Close struct{}

type Step struct {
	Type       StepKind             `json:"type"`
	Kind       StepKind             `json:"-"`
	CorpusID   string               `json:"corpus_id,omitempty"`
	Text       string               `json:"text,omitempty"`
	Corpus     AudioCorpusReference `json:"-"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	ToolName   string               `json:"tool_name,omitempty"`
	ToolResult json.RawMessage      `json:"-"`
	Result     json.RawMessage      `json:"result,omitempty"`
	At         LogicalTime          `json:"at,omitempty"`
	Time       LogicalTime          `json:"-"`
	Duration   LogicalTime          `json:"duration,omitempty"`
}

type ExpectationKind string

const (
	ExpectText       ExpectationKind = "text"
	ExpectAudio      ExpectationKind = "audio"
	ExpectToolCall   ExpectationKind = "tool_call"
	ExpectToolResult ExpectationKind = "tool_result"
	ExpectClose      ExpectationKind = "close"
	ExpectTime       ExpectationKind = "time"
	ExpectEvent      ExpectationKind = "event"
	ExpectContains   ExpectationKind = "contains"
	ExpectTranscript ExpectationKind = "transcript"
)

type ExpectedBehavior struct {
	Type       ExpectationKind `json:"type"`
	Kind       ExpectationKind `json:"-"`
	Text       string          `json:"text,omitempty"`
	Value      string          `json:"value,omitempty"`
	CorpusID   string          `json:"corpus_id,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	At         LogicalTime     `json:"at,omitempty"`
	Time       LogicalTime     `json:"-"`
	Count      int             `json:"count,omitempty"`
	StepIndex  int             `json:"-"`
	Step       int             `json:"-"`
	HasStep    bool            `json:"-"`
	AfterStep  int             `json:"-"`
	BeforeStep int             `json:"-"`
	HasAfter   bool            `json:"-"`
	HasBefore  bool            `json:"-"`
	HasAt      bool            `json:"-"`
}
type Expectation = ExpectedBehavior
type ExpectedKind = ExpectationKind

type Scenario struct {
	ID               string             `json:"id"`
	Name             string             `json:"name,omitempty"`
	Description      string             `json:"description,omitempty"`
	Steps            []Step             `json:"steps"`
	Expectations     []ExpectedBehavior `json:"expectations"`
	Expected         []ExpectedBehavior `json:"-"`
	ExpectedBehavior []ExpectedBehavior `json:"-"`
}

type ErrorCategory string

const (
	CategoryMalformed      ErrorCategory = "malformed"
	CategoryEmpty          ErrorCategory = "empty"
	CategoryMissingField   ErrorCategory = "missing_field"
	CategoryInvalidField   ErrorCategory = "invalid_field"
	CategoryUnknownVariant ErrorCategory = "unknown_variant"
	CategoryUnknownCorpus  ErrorCategory = "unknown_corpus"
	CategoryContradictory  ErrorCategory = "contradictory"
	CategoryUnsatisfiable  ErrorCategory = "unsatisfiable"
)

type ErrorKind = ErrorCategory

var (
	ErrMalformed         = errors.New("malformed scenario")
	ErrEmptyScenario     = errors.New("empty scenario")
	ErrMissingField      = errors.New("missing scenario field")
	ErrInvalidField      = errors.New("invalid scenario field")
	ErrUnknownVariant    = errors.New("unknown scenario variant")
	ErrUnknownCorpus     = errors.New("unknown audio corpus")
	ErrContradictory     = errors.New("contradictory scenario")
	ErrUnsatisfiable     = errors.New("unsatisfiable expectation")
	ErrMalformedScenario = ErrMalformed
	ErrUnknownCorpusID   = ErrUnknownCorpus
)

type ScenarioError struct {
	Category    ErrorCategory
	Kind        ErrorCategory
	Location    string
	Message     string
	CorpusID    string
	StepIndex   int
	Expectation int
}

func makeError(category ErrorCategory, location, format string, args ...any) *ScenarioError {
	return &ScenarioError{Category: category, Kind: category, Location: location,
		Message: fmt.Sprintf(format, args...), StepIndex: -1, Expectation: -1}
}
func (e *ScenarioError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Location == "" {
		return fmt.Sprintf("%s: %s", e.Category, e.Message)
	}
	return fmt.Sprintf("%s at %s: %s", e.Category, e.Location, e.Message)
}
func (e *ScenarioError) Unwrap() error {
	switch e.Category {
	case CategoryMalformed:
		return ErrMalformed
	case CategoryEmpty:
		return ErrEmptyScenario
	case CategoryMissingField:
		return ErrMissingField
	case CategoryInvalidField:
		return ErrInvalidField
	case CategoryUnknownVariant:
		return ErrUnknownVariant
	case CategoryUnknownCorpus:
		return ErrUnknownCorpus
	case CategoryContradictory:
		return ErrContradictory
	case CategoryUnsatisfiable:
		return ErrUnsatisfiable
	default:
		return nil
	}
}

// CorpusLookup is the read-only corpus identity lookup used by the loader.
// The audiofixture package is not part of the current baseline, so this is the
// narrow seam that the eventual audiofixture implementation must satisfy.
type CorpusLookup interface{ Has(string) bool }

func Load(input any, lookups ...CorpusLookup) (Scenario, error) {
	if len(lookups) > 1 {
		return Scenario{}, makeError(CategoryInvalidField, "corpus_lookup", "only one corpus lookup is permitted")
	}
	data, err := readInput(input)
	if err != nil {
		return Scenario{}, makeError(CategoryMalformed, "document", "%v", err)
	}
	value, err := decodeObject(data)
	if err != nil {
		return Scenario{}, err
	}
	scenario, err := parseScenario(value)
	if err != nil {
		return Scenario{}, err
	}
	var lookup CorpusLookup
	if len(lookups) == 1 {
		lookup = lookups[0]
	}
	if err := scenario.validate(lookup); err != nil {
		return Scenario{}, err
	}
	return scenario, nil
}
func LoadScenario(input any, lookups ...CorpusLookup) (Scenario, error) {
	return Load(input, lookups...)
}
func Decode(input any, lookups ...CorpusLookup) (Scenario, error) { return Load(input, lookups...) }
func (s Scenario) Validate(lookups ...CorpusLookup) error {
	if len(lookups) > 1 {
		return makeError(CategoryInvalidField, "corpus_lookup", "only one corpus lookup is permitted")
	}
	var lookup CorpusLookup
	if len(lookups) == 1 {
		lookup = lookups[0]
	}
	return s.validate(lookup)
}
func (s Scenario) Valid() bool { return s.Validate() == nil }
func (s Scenario) expectedValues() []ExpectedBehavior {
	if s.Expectations != nil {
		return s.Expectations
	}
	if s.ExpectedBehavior != nil {
		return s.ExpectedBehavior
	}
	return s.Expected
}

type object map[string]json.RawMessage

func readInput(input any) ([]byte, error) {
	switch value := input.(type) {
	case []byte:
		return value, nil
	case json.RawMessage:
		return value, nil
	case string:
		return []byte(value), nil
	case io.Reader:
		return io.ReadAll(value)
	default:
		return nil, fmt.Errorf("unsupported input %T", input)
	}
}
func decodeObject(data []byte) (object, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return nil, makeError(CategoryMalformed, "document", "%v", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err == nil {
		return nil, makeError(CategoryMalformed, "document", "trailing JSON document")
	} else if err != io.EOF {
		return nil, makeError(CategoryMalformed, "document", "%v", err)
	}
	var value object
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, makeError(CategoryMalformed, "document", "scenario must be a JSON object")
	}
	return value, nil
}
func unknown(value object, allowed map[string]bool, location string) error {
	keys := make([]string, 0)
	for key := range value {
		if !allowed[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) != 0 {
		return makeError(CategoryMalformed, location+"."+keys[0], "unknown field %q", keys[0])
	}
	return nil
}
func field(value object, location string, names ...string) (json.RawMessage, string, bool, error) {
	found := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := value[name]; ok {
			found = append(found, name)
		}
	}
	if len(found) > 1 {
		return nil, "", false, makeError(CategoryInvalidField, location, "fields %s are mutually exclusive", strings.Join(found, ", "))
	}
	if len(found) == 0 {
		return nil, "", false, nil
	}
	return value[found[0]], found[0], true, nil
}
func stringValue(raw json.RawMessage, location string) (string, error) {
	var value string
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
		return "", makeError(CategoryInvalidField, location, "must be a string")
	}
	return value, nil
}
func requiredString(value object, location string, names ...string) (string, error) {
	raw, key, ok, err := field(value, location, names...)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", makeError(CategoryMissingField, location+"."+names[0], "required field is missing")
	}
	text, err := stringValue(raw, location+"."+key)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", makeError(CategoryInvalidField, location+"."+key, "must not be empty")
	}
	return text, nil
}
func optionalString(value object, location string, names ...string) (string, error) {
	raw, key, ok, err := field(value, location, names...)
	if err != nil || !ok {
		return "", err
	}
	return stringValue(raw, location+"."+key)
}

var scenarioFields = map[string]bool{"id": true, "name": true, "description": true, "steps": true, "expectations": true, "expected_behavior": true, "expected": true}

func parseScenario(value object) (Scenario, error) {
	if err := unknown(value, scenarioFields, "scenario"); err != nil {
		return Scenario{}, err
	}
	id, err := optionalString(value, "scenario", "id")
	if err != nil {
		return Scenario{}, err
	}
	name, err := optionalString(value, "scenario", "name")
	if err != nil {
		return Scenario{}, err
	}
	if strings.TrimSpace(id) == "" {
		id = name
	}
	if strings.TrimSpace(id) == "" {
		return Scenario{}, makeError(CategoryMissingField, "scenario.id", "required field is missing")
	}
	description, err := optionalString(value, "scenario", "description")
	if err != nil {
		return Scenario{}, err
	}
	rawSteps, _, ok, err := field(value, "scenario", "steps")
	if err != nil {
		return Scenario{}, err
	}
	if !ok {
		return Scenario{}, makeError(CategoryMissingField, "steps", "required field is missing")
	}
	var stepsRaw []json.RawMessage
	if json.Unmarshal(rawSteps, &stepsRaw) != nil || stepsRaw == nil {
		return Scenario{}, makeError(CategoryInvalidField, "steps", "must be a JSON array")
	}
	if len(stepsRaw) == 0 {
		return Scenario{}, makeError(CategoryEmpty, "steps", "scenario must contain at least one step")
	}
	steps := make([]Step, len(stepsRaw))
	for index, raw := range stepsRaw {
		steps[index], err = parseStep(raw, index)
		if err != nil {
			return Scenario{}, err
		}
	}
	rawExpected, _, ok, err := field(value, "scenario", "expectations", "expected_behavior", "expected")
	if err != nil {
		return Scenario{}, err
	}
	if !ok {
		return Scenario{}, makeError(CategoryMissingField, "expectations", "required field is missing")
	}
	var expectedRaw []json.RawMessage
	if json.Unmarshal(rawExpected, &expectedRaw) != nil || expectedRaw == nil {
		return Scenario{}, makeError(CategoryInvalidField, "expectations", "must be a JSON array")
	}
	if len(expectedRaw) == 0 {
		return Scenario{}, makeError(CategoryEmpty, "expectations", "at least one expected behavior is required")
	}
	expectations := make([]ExpectedBehavior, len(expectedRaw))
	for index, raw := range expectedRaw {
		expectations[index], err = parseExpectation(raw, index)
		if err != nil {
			return Scenario{}, err
		}
	}
	return Scenario{ID: id, Name: name, Description: description, Steps: steps, Expectations: expectations, Expected: expectations, ExpectedBehavior: expectations}, nil
}

var stepFields = map[string]bool{"type": true, "kind": true, "payload": true, "text": true, "value": true, "corpus_id": true, "corpusID": true, "corpus": true, "tool_call_id": true, "toolCallID": true, "tool_name": true, "toolName": true, "result": true, "tool_result": true, "at": true, "time": true, "logical_time": true, "logicalTime": true, "duration": true}

func stepKind(value string) (StepKind, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "send_text":
		return StepSendText, true
	case "send_audio":
		return StepSendAudio, true
	case "send_tool_result":
		return StepSendToolResult, true
	case "advance_to":
		return StepAdvanceTo, true
	case "wait":
		return StepWait, true
	case "close":
		return StepClose, true
	default:
		return "", false
	}
}
func payload(value object, location string) (object, error) {
	if raw, ok := value["payload"]; ok {
		if len(value) != 2 {
			return nil, makeError(CategoryInvalidField, location, "payload cannot be mixed with fields")
		}
		var nested object
		if json.Unmarshal(raw, &nested) != nil || nested == nil {
			return nil, makeError(CategoryInvalidField, location+".payload", "must be a JSON object")
		}
		return nested, nil
	}
	result := make(object, len(value))
	for key, raw := range value {
		if key != "type" && key != "kind" {
			result[key] = raw
		}
	}
	return result, nil
}
func parseStep(raw json.RawMessage, index int) (Step, error) {
	location := fmt.Sprintf("steps[%d]", index)
	var value object
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return Step{}, makeError(CategoryMalformed, location, "step must be a JSON object")
	}
	if err := unknown(value, stepFields, location); err != nil {
		return Step{}, err
	}
	discriminator, key, ok, err := field(value, location, "type", "kind")
	if err != nil {
		return Step{}, err
	}
	if !ok {
		return Step{}, makeError(CategoryMissingField, location+".type", "step discriminator is required")
	}
	name, err := stringValue(discriminator, location+"."+key)
	if err != nil {
		return Step{}, err
	}
	kind, ok := stepKind(name)
	if !ok {
		return Step{}, makeError(CategoryUnknownVariant, location+"."+key, "unknown step variant %q", name)
	}
	fields, err := payload(value, location)
	if err != nil {
		return Step{}, err
	}
	allowed := map[StepKind]map[string]bool{
		StepSendText:       {"text": true, "value": true},
		StepSendAudio:      {"corpus_id": true, "corpusID": true, "corpus": true, "text": true},
		StepSendToolResult: {"tool_call_id": true, "toolCallID": true, "tool_name": true, "toolName": true, "result": true, "tool_result": true, "value": true},
		StepAdvanceTo:      {"at": true, "time": true, "logical_time": true, "logicalTime": true},
		StepWait:           {"duration": true}, StepClose: {},
	}
	if err := unknown(fields, allowed[kind], location); err != nil {
		return Step{}, err
	}
	step := Step{Type: kind, Kind: kind}
	switch kind {
	case StepSendText:
		step.Text, err = requiredString(fields, location, "text", "value")
		if err != nil {
			return Step{}, err
		}
	case StepSendAudio:
		step.CorpusID, err = corpusID(fields, location)
		if err != nil {
			return Step{}, err
		}
		step.Text, err = optionalString(fields, location, "text")
		if err != nil {
			return Step{}, err
		}
		step.Corpus = AudioCorpusReference{ID: step.CorpusID, CorpusID: step.CorpusID}
	case StepSendToolResult:
		step.ToolCallID, err = requiredString(fields, location, "tool_call_id", "toolCallID")
		if err != nil {
			return Step{}, err
		}
		result, _, ok, err := field(fields, location, "result", "tool_result", "value")
		if err != nil {
			return Step{}, err
		}
		if !ok {
			return Step{}, makeError(CategoryMissingField, location+".result", "required field is missing")
		}
		step.ToolName, err = optionalString(fields, location, "tool_name", "toolName")
		if err != nil {
			return Step{}, err
		}
		step.ToolResult, step.Result = append(json.RawMessage(nil), result...), append(json.RawMessage(nil), result...)
	case StepAdvanceTo:
		step.At, _, ok, err = logicalField(fields, location, "at", "time", "logical_time", "logicalTime")
		if err != nil {
			return Step{}, err
		}
		if !ok {
			return Step{}, makeError(CategoryMissingField, location+".at", "required logical-time field is missing")
		}
		step.Time = step.At
	case StepWait:
		step.Duration, _, ok, err = logicalField(fields, location, "duration")
		if err != nil {
			return Step{}, err
		}
		if !ok {
			return Step{}, makeError(CategoryMissingField, location+".duration", "required duration field is missing")
		}
	case StepClose:
		// The empty allowed set above rejects every close payload.
	}
	return step, nil
}
func corpusID(value object, location string) (string, error) {
	if raw, key, ok, err := field(value, location, "corpus_id", "corpusID"); err != nil {
		return "", err
	} else if ok {
		return requiredValue(raw, location+"."+key)
	}
	raw, ok := value["corpus"]
	if !ok {
		return "", makeError(CategoryMissingField, location+".corpus_id", "required corpus ID is missing")
	}
	var nested object
	if json.Unmarshal(raw, &nested) != nil || nested == nil {
		return "", makeError(CategoryInvalidField, location+".corpus", "must be an object containing corpus_id")
	}
	if err := unknown(nested, map[string]bool{"id": true, "corpus_id": true}, location+".corpus"); err != nil {
		return "", err
	}
	return requiredString(nested, location+".corpus", "corpus_id", "id")
}
func requiredValue(raw json.RawMessage, location string) (string, error) {
	value, err := stringValue(raw, location)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", makeError(CategoryInvalidField, location, "must not be empty")
	}
	return value, nil
}
func logicalField(value object, location string, names ...string) (LogicalTime, string, bool, error) {
	raw, key, ok, err := field(value, location, names...)
	if err != nil || !ok {
		return 0, "", false, err
	}
	tick, err := parseLogical(raw, location+"."+key)
	return tick, key, true, err
}
func parseLogical(raw json.RawMessage, location string) (LogicalTime, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return 0, makeError(CategoryInvalidField, location, "must be an integer tick or duration string")
	}
	switch value := value.(type) {
	case json.Number:
		integer, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return 0, makeError(CategoryInvalidField, location, "must be an integer tick")
		}
		return LogicalTime(integer), nil
	case string:
		if integer, err := strconv.ParseInt(value, 10, 64); err == nil {
			return LogicalTime(integer), nil
		}
		duration, err := time.ParseDuration(value)
		if err != nil {
			return 0, makeError(CategoryInvalidField, location, "must be an integer tick or duration string")
		}
		return LogicalTime(duration), nil
	default:
		return 0, makeError(CategoryInvalidField, location, "must be an integer tick or duration string")
	}
}

var expectationFields = map[string]bool{"type": true, "kind": true, "payload": true, "text": true, "value": true, "message": true, "event": true, "corpus_id": true, "corpusID": true, "tool_call_id": true, "toolCallID": true, "tool_name": true, "toolName": true, "name": true, "result": true, "at": true, "time": true, "logical_time": true, "logicalTime": true, "count": true, "step": true, "step_index": true, "after": true, "after_step": true, "before": true, "before_step": true}

var expectationModifiers = map[string]bool{"count": true, "step": true, "step_index": true, "after": true, "after_step": true, "before": true, "before_step": true}

func expectationKind(value string) (ExpectationKind, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "text", "text_output", "assistant_text":
		return ExpectText, true
	case "transcript", "transcript_contains":
		return ExpectTranscript, true
	case "audio", "audio_output":
		return ExpectAudio, true
	case "tool_call", "tool":
		return ExpectToolCall, true
	case "tool_result":
		return ExpectToolResult, true
	case "close", "closed", "terminal", "session_close":
		return ExpectClose, true
	case "time", "advance_to", "wait":
		return ExpectTime, true
	case "event":
		return ExpectEvent, true
	case "contains":
		return ExpectContains, true
	case "terminal_reason", "terminal-reason":
		return ExpectTerminalReason, true
	case "terminal_provenance", "terminal-provenance":
		return ExpectTerminalProvenance, true
	case "output_state", "output-state", "terminal_output_state", "terminal-output-state":
		return ExpectOutputState, true
	default:
		return "", false
	}
}
func parseExpectation(raw json.RawMessage, index int) (ExpectedBehavior, error) {
	location := fmt.Sprintf("expectations[%d]", index)
	var value object
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return ExpectedBehavior{}, makeError(CategoryMalformed, location, "expected behavior must be a JSON object")
	}
	if err := unknown(value, expectationFields, location); err != nil {
		return ExpectedBehavior{}, err
	}
	discriminator, key, ok, err := field(value, location, "type", "kind")
	if err != nil {
		return ExpectedBehavior{}, err
	}
	if !ok {
		return ExpectedBehavior{}, makeError(CategoryMissingField, location+".type", "expectation discriminator is required")
	}
	name, err := stringValue(discriminator, location+"."+key)
	if err != nil {
		return ExpectedBehavior{}, err
	}
	kind, ok := expectationKind(name)
	if !ok {
		return ExpectedBehavior{}, makeError(CategoryUnknownVariant, location+"."+key, "unknown expectation variant %q", name)
	}
	fields, err := payload(value, location)
	if err != nil {
		return ExpectedBehavior{}, err
	}
	if err := unknown(fields, expectationFieldsByKind[kind], location); err != nil {
		return ExpectedBehavior{}, err
	}
	expectation := ExpectedBehavior{Type: kind, Kind: kind, StepIndex: -1, Step: -1}
	if raw, _, ok, err := field(fields, location, "text", "value", "message", "event"); err != nil {
		return expectation, err
	} else if ok {
		text, textErr := stringValue(raw, location+".value")
		err = textErr
		if err != nil {
			return expectation, err
		}
		if kind == ExpectText || kind == ExpectTranscript || kind == ExpectContains {
			expectation.Text = text
		} else {
			expectation.Value = text
		}
	}
	if raw, _, ok, err := field(fields, location, "corpus_id", "corpusID"); err != nil {
		return expectation, err
	} else if ok {
		expectation.CorpusID, err = requiredValue(raw, location+".corpus_id")
		if err != nil {
			return expectation, err
		}
	}
	if raw, _, ok, err := field(fields, location, "tool_call_id", "toolCallID"); err != nil {
		return expectation, err
	} else if ok {
		expectation.ToolCallID, err = requiredValue(raw, location+".tool_call_id")
		if err != nil {
			return expectation, err
		}
	}
	if raw, _, ok, err := field(fields, location, "tool_name", "toolName", "name"); err != nil {
		return expectation, err
	} else if ok {
		expectation.ToolName, err = requiredValue(raw, location+".tool_name")
		if err != nil {
			return expectation, err
		}
	}
	if raw, _, ok, err := field(fields, location, "result"); err != nil {
		return expectation, err
	} else if ok {
		expectation.Result = append(json.RawMessage(nil), raw...)
	}
	if raw, _, ok, err := field(fields, location, "at", "time", "logical_time", "logicalTime"); err != nil {
		return expectation, err
	} else if ok {
		expectation.At, err = parseLogical(raw, location+".at")
		if err != nil {
			return expectation, err
		}
		expectation.Time, expectation.HasAt = expectation.At, true
	}
	if raw, _, ok, err := field(fields, location, "count"); err != nil {
		return expectation, err
	} else if ok {
		expectation.Count, err = integer(raw, location+".count")
		if err != nil {
			return expectation, err
		}
	}
	if raw, _, ok, err := field(fields, location, "step", "step_index"); err != nil {
		return expectation, err
	} else if ok {
		expectation.StepIndex, err = integer(raw, location+".step")
		if err != nil {
			return expectation, err
		}
		expectation.Step, expectation.HasStep = expectation.StepIndex, true
	}
	if raw, _, ok, err := field(fields, location, "after", "after_step"); err != nil {
		return expectation, err
	} else if ok {
		expectation.AfterStep, err = integer(raw, location+".after")
		if err != nil {
			return expectation, err
		}
		expectation.HasAfter = true
	}
	if raw, _, ok, err := field(fields, location, "before", "before_step"); err != nil {
		return expectation, err
	} else if ok {
		expectation.BeforeStep, err = integer(raw, location+".before")
		if err != nil {
			return expectation, err
		}
		expectation.HasBefore = true
	}
	if err := validateExpectationFields(expectation, location); err != nil {
		return ExpectedBehavior{}, err
	}
	return expectation, nil
}
func integer(raw json.RawMessage, location string) (int, error) {
	var value int64
	if json.Unmarshal(raw, &value) != nil || value > math.MaxInt || value < math.MinInt {
		return 0, makeError(CategoryInvalidField, location, "must be an integer")
	}
	return int(value), nil
}

func expectationTimeValue(value ExpectedBehavior, location string) (LogicalTime, bool, error) {
	if value.At != 0 && value.Time != 0 && value.At != value.Time {
		return 0, false, makeError(CategoryInvalidField, location+".at", "at and time aliases disagree")
	}
	if value.HasAt {
		if value.At != 0 {
			return value.At, true, nil
		}
		return value.Time, true, nil
	}
	if value.At != 0 {
		return value.At, true, nil
	}
	if value.Time != 0 {
		return value.Time, true, nil
	}
	return 0, false, nil
}

var typedExpectationFieldsByKind = map[ExpectationKind]map[string]bool{
	ExpectText:       {"text": true},
	ExpectTranscript: {"text": true},
	ExpectContains:   {"text": true},
	ExpectAudio:      {"corpus_id": true},
	ExpectToolCall:   {"tool_call_id": true, "tool_name": true},
	ExpectToolResult: {"tool_call_id": true, "result": true}, ExpectToolResultDelivered: {"tool_call_id": true},
	ExpectToolResultDiscarded: {"tool_call_id": true}, ExpectNoOrphanedToolResult: {}, ExpectClose: {},
	ExpectTime:  {"at": true},
	ExpectEvent: {"value": true},
}

func rejectTypedExpectationFields(value ExpectedBehavior, location string, hasAt bool) error {
	allowed := typedExpectationFieldsByKind[value.Kind]
	fields := []struct {
		name      string
		populated bool
	}{
		{"text", value.Text != ""},
		{"value", value.Value != ""},
		{"corpus_id", value.CorpusID != ""},
		{"tool_call_id", value.ToolCallID != ""},
		{"tool_name", value.ToolName != ""},
		{"result", len(value.Result) != 0},
		{"at", hasAt},
	}
	for _, field := range fields {
		if field.populated && !allowed[field.name] {
			return makeError(CategoryInvalidField, location+"."+field.name, "unexpected payload for %s expectation", value.Kind)
		}
	}
	return nil
}

func validateExpectationFields(value ExpectedBehavior, location string) error {
	at, hasAt, err := expectationTimeValue(value, location)
	if err != nil {
		return err
	}
	if value.HasStep && value.StepIndex < 0 {
		return makeError(CategoryInvalidField, location+".step", "must not be negative")
	}
	if value.HasAfter && value.AfterStep < 0 {
		return makeError(CategoryInvalidField, location+".after", "must not be negative")
	}
	if value.HasBefore && value.BeforeStep < 0 {
		return makeError(CategoryInvalidField, location+".before", "must not be negative")
	}
	if value.HasAfter && value.HasBefore && value.AfterStep >= value.BeforeStep {
		return makeError(CategoryContradictory, location, "after step must precede before step")
	}
	if value.Count < 0 {
		return makeError(CategoryInvalidField, location+".count", "must not be negative")
	}
	if err := rejectTypedExpectationFields(value, location, hasAt); err != nil {
		return err
	}
	switch value.Kind {
	case ExpectText, ExpectTranscript, ExpectContains:
		if strings.TrimSpace(value.Text) == "" {
			return makeError(CategoryMissingField, location+".text", "expected text is required")
		}
	case ExpectAudio:
		if value.CorpusID == "" {
			return makeError(CategoryMissingField, location+".corpus_id", "expected corpus ID is required")
		}
	case ExpectToolCall:
		if value.ToolName == "" && value.ToolCallID == "" {
			return makeError(CategoryMissingField, location+".tool_name", "tool name or call ID is required")
		}
	case ExpectToolResult:
		if value.ToolCallID == "" {
			return makeError(CategoryMissingField, location+".tool_call_id", "tool call ID is required")
		}
		if len(value.Result) == 0 {
			return makeError(CategoryMissingField, location+".result", "expected result is required")
		}
	case ExpectTime:
		if !hasAt {
			return makeError(CategoryMissingField, location+".at", "expected logical time is required")
		}
		if at <= 0 {
			return makeError(CategoryInvalidField, location+".at", "must be positive")
		}
	case ExpectEvent:
		if value.Value == "" {
			return makeError(CategoryMissingField, location+".event", "event name is required")
		}
	}
	return nil
}

func (s Scenario) validate(lookup CorpusLookup) error {
	if strings.TrimSpace(s.ID) == "" && strings.TrimSpace(s.Name) == "" {
		return makeError(CategoryMissingField, "scenario.id", "required field is missing")
	}
	if len(s.Steps) == 0 {
		return makeError(CategoryEmpty, "steps", "scenario must contain at least one step")
	}
	expectations := s.expectedValues()
	if len(expectations) == 0 {
		return makeError(CategoryEmpty, "expectations", "at least one expected behavior is required")
	}
	logical, closeIndex := LogicalTime(0), -1
	for index, step := range s.Steps {
		kind := step.Kind
		if kind == "" {
			kind = step.Type
		}
		if step.Kind != "" && step.Type != "" && step.Kind != step.Type {
			return makeError(CategoryContradictory, fmt.Sprintf("steps[%d].type", index), "type and kind disagree")
		}
		if _, ok := stepKind(string(kind)); !ok {
			return makeError(CategoryUnknownVariant, fmt.Sprintf("steps[%d].type", index), "unknown step variant %q", kind)
		}
		if err := validateStep(step, kind, index); err != nil {
			return err
		}
		switch kind {
		case StepSendAudio:
			id, _ := stepCorpusID(step, fmt.Sprintf("steps[%d]", index))
			if lookup == nil {
				return makeError(CategoryInvalidField, fmt.Sprintf("steps[%d].corpus_id", index), "audio corpus lookup is required")
			}
			if !lookupCorpus(lookup, id) {
				e := makeError(CategoryUnknownCorpus, fmt.Sprintf("steps[%d].corpus_id", index), "unknown audio corpus ID %q", id)
				e.CorpusID, e.StepIndex = id, index
				return e
			}
		case StepAdvanceTo:
			at := step.At
			if at == 0 {
				at = step.Time
			}
			if at <= logical {
				return makeError(CategoryContradictory, fmt.Sprintf("steps[%d].at", index), "logical time must progress beyond %d", logical)
			}
			logical = at
		case StepWait:
			if logical > LogicalTime(math.MaxInt64)-step.Duration {
				return makeError(CategoryInvalidField, fmt.Sprintf("steps[%d].duration", index), "logical time overflows")
			}
			logical += step.Duration
		case StepClose:
			if closeIndex >= 0 {
				return makeError(CategoryContradictory, fmt.Sprintf("steps[%d]", index), "scenario may contain only one close")
			}
			closeIndex = index
		}
	}
	if closeIndex < 0 {
		return makeError(CategoryContradictory, "steps", "scenario must end with close")
	}
	if closeIndex != len(s.Steps)-1 {
		return makeError(CategoryContradictory, fmt.Sprintf("steps[%d]", closeIndex), "close must be the terminal step")
	}
	for index, expectation := range expectations {
		if expectation.Kind == "" {
			expectation.Kind = expectation.Type
		}
		if expectation.Type != "" && expectation.Kind != "" && expectation.Type != expectation.Kind {
			return makeError(CategoryContradictory, fmt.Sprintf("expectations[%d].type", index), "type and kind disagree")
		}
		if !expectation.HasAt && expectation.At == 0 && expectation.Time != 0 {
			expectation.At, expectation.HasAt = expectation.Time, true
		}
		location := fmt.Sprintf("expectations[%d]", index)
		if err := validateExpectationFields(expectation, location); err != nil {
			return err
		}
		if err := satisfiable(expectation, index, expectations, s.Steps, closeIndex, logical); err != nil {
			return err
		}
	}
	return nil
}

func stepCorpusID(step Step, location string) (string, error) {
	id := ""
	for _, candidate := range []string{step.CorpusID, step.Corpus.CorpusID, step.Corpus.ID} {
		if candidate == "" {
			continue
		}
		if strings.TrimSpace(candidate) == "" {
			return "", makeError(CategoryInvalidField, location+".corpus_id", "must not be empty")
		}
		if id != "" && id != candidate {
			return "", makeError(CategoryInvalidField, location+".corpus_id", "corpus ID aliases disagree")
		}
		id = candidate
	}
	return id, nil
}

func stepHasCorpus(step Step) bool {
	return step.CorpusID != "" || step.Corpus.CorpusID != "" || step.Corpus.ID != ""
}

func stepResult(step Step, location string) (json.RawMessage, error) {
	if len(step.ToolResult) != 0 && len(step.Result) != 0 && !bytes.Equal(step.ToolResult, step.Result) {
		return nil, makeError(CategoryInvalidField, location+".result", "result aliases disagree")
	}
	if len(step.ToolResult) != 0 {
		return step.ToolResult, nil
	}
	return step.Result, nil
}

func stepLogicalTime(step Step, location string) (LogicalTime, error) {
	if step.At != 0 && step.Time != 0 && step.At != step.Time {
		return 0, makeError(CategoryInvalidField, location+".at", "at and time aliases disagree")
	}
	if step.At != 0 {
		return step.At, nil
	}
	return step.Time, nil
}

func rejectStepFields(location string, kind StepKind, fields ...struct {
	name      string
	populated bool
}) error {
	for _, field := range fields {
		if field.populated {
			return unexpectedStepField(location, field.name, kind)
		}
	}
	return nil
}

func validateStep(step Step, kind StepKind, index int) error {
	location := fmt.Sprintf("steps[%d]", index)
	switch kind {
	case StepSendText:
		if strings.TrimSpace(step.Text) == "" {
			return makeError(CategoryMissingField, location+".text", "required field is missing")
		}
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"corpus_id", stepHasCorpus(step)},
			struct {
				name      string
				populated bool
			}{"tool_call_id", step.ToolCallID != ""},
			struct {
				name      string
				populated bool
			}{"tool_name", step.ToolName != ""},
			struct {
				name      string
				populated bool
			}{"result", len(step.ToolResult) != 0 || len(step.Result) != 0},
			struct {
				name      string
				populated bool
			}{"at", step.At != 0 || step.Time != 0},
			struct {
				name      string
				populated bool
			}{"duration", step.Duration != 0},
		); err != nil {
			return err
		}
	case StepSendAudio:
		id, err := stepCorpusID(step, location)
		if err != nil {
			return err
		}
		if strings.TrimSpace(id) == "" {
			return makeError(CategoryMissingField, location+".corpus_id", "required field is missing")
		}
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"tool_call_id", step.ToolCallID != ""},
			struct {
				name      string
				populated bool
			}{"tool_name", step.ToolName != ""},
			struct {
				name      string
				populated bool
			}{"result", len(step.ToolResult) != 0 || len(step.Result) != 0},
			struct {
				name      string
				populated bool
			}{"at", step.At != 0 || step.Time != 0},
			struct {
				name      string
				populated bool
			}{"duration", step.Duration != 0},
		); err != nil {
			return err
		}
	case StepSendToolResult:
		if strings.TrimSpace(step.ToolCallID) == "" {
			return makeError(CategoryMissingField, location+".tool_call_id", "required field is missing")
		}
		if lenResult, err := stepResult(step, location); err != nil {
			return err
		} else if len(lenResult) == 0 {
			return makeError(CategoryMissingField, location+".result", "required field is missing")
		}
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"text", step.Text != ""},
			struct {
				name      string
				populated bool
			}{"corpus_id", stepHasCorpus(step)},
			struct {
				name      string
				populated bool
			}{"at", step.At != 0 || step.Time != 0},
			struct {
				name      string
				populated bool
			}{"duration", step.Duration != 0},
		); err != nil {
			return err
		}
	case StepAdvanceTo:
		at, err := stepLogicalTime(step, location)
		if err != nil {
			return err
		}
		if at <= 0 {
			return makeError(CategoryInvalidField, location+".at", "must be positive")
		}
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"text", step.Text != ""},
			struct {
				name      string
				populated bool
			}{"corpus_id", stepHasCorpus(step)},
			struct {
				name      string
				populated bool
			}{"tool_call_id", step.ToolCallID != ""},
			struct {
				name      string
				populated bool
			}{"tool_name", step.ToolName != ""},
			struct {
				name      string
				populated bool
			}{"result", len(step.ToolResult) != 0 || len(step.Result) != 0},
			struct {
				name      string
				populated bool
			}{"duration", step.Duration != 0},
		); err != nil {
			return err
		}
	case StepWait:
		if step.Duration <= 0 {
			return makeError(CategoryInvalidField, location+".duration", "must be positive")
		}
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"text", step.Text != ""},
			struct {
				name      string
				populated bool
			}{"corpus_id", stepHasCorpus(step)},
			struct {
				name      string
				populated bool
			}{"tool_call_id", step.ToolCallID != ""},
			struct {
				name      string
				populated bool
			}{"tool_name", step.ToolName != ""},
			struct {
				name      string
				populated bool
			}{"result", len(step.ToolResult) != 0 || len(step.Result) != 0},
			struct {
				name      string
				populated bool
			}{"at", step.At != 0 || step.Time != 0},
		); err != nil {
			return err
		}
	case StepClose:
		if err := rejectStepFields(location, kind,
			struct {
				name      string
				populated bool
			}{"text", step.Text != ""},
			struct {
				name      string
				populated bool
			}{"corpus_id", stepHasCorpus(step)},
			struct {
				name      string
				populated bool
			}{"tool_call_id", step.ToolCallID != ""},
			struct {
				name      string
				populated bool
			}{"tool_name", step.ToolName != ""},
			struct {
				name      string
				populated bool
			}{"result", len(step.ToolResult) != 0 || len(step.Result) != 0},
			struct {
				name      string
				populated bool
			}{"at", step.At != 0 || step.Time != 0},
			struct {
				name      string
				populated bool
			}{"duration", step.Duration != 0},
		); err != nil {
			return err
		}
	}
	return nil
}

func unexpectedStepField(location string, field string, kind StepKind) error {
	return makeError(CategoryInvalidField, location+"."+field, "unexpected payload for %s", kind)
}
func satisfiable(value ExpectedBehavior, index int, all []ExpectedBehavior, steps []Step, closeIndex int, finalTime LogicalTime) error {
	location := fmt.Sprintf("expectations[%d]", index)
	if value.HasStep {
		if value.StepIndex < 0 || value.StepIndex >= len(steps) {
			return makeError(CategoryUnsatisfiable, location+".step", "referenced step %d does not exist", value.StepIndex)
		}
		if value.StepIndex >= closeIndex && (value.Kind != ExpectClose || value.StepIndex != closeIndex) {
			return makeError(CategoryUnsatisfiable, location+".step", "expectation refers to a terminal step incompatibly")
		}
	}
	if value.HasAfter && (value.AfterStep >= len(steps) || value.AfterStep >= closeIndex) {
		return makeError(CategoryUnsatisfiable, location+".after", "after step %d cannot occur before terminal close", value.AfterStep)
	}
	if value.HasBefore && (value.BeforeStep >= len(steps) || value.BeforeStep > closeIndex) {
		return makeError(CategoryUnsatisfiable, location+".before", "before step %d is outside the scenario", value.BeforeStep)
	}
	if index > 0 && value.HasStep && all[index-1].HasStep && value.StepIndex < all[index-1].StepIndex {
		return makeError(CategoryContradictory, location+".step", "expectations must preserve step order")
	}
	switch value.Kind {
	case ExpectToolResult:
		for _, step := range steps {
			kind := step.Kind
			if kind == "" {
				kind = step.Type
			}
			if kind == StepSendToolResult && step.ToolCallID == value.ToolCallID {
				return nil
			}
		}
		return makeError(CategoryUnsatisfiable, location+".tool_call_id", "no declared tool result can satisfy this expectation")
	case ExpectAudio:
		for _, step := range steps {
			kind := step.Kind
			if kind == "" {
				kind = step.Type
			}
			id := step.CorpusID
			if id == "" {
				id = step.Corpus.CorpusID
			}
			if kind == StepSendAudio && id == value.CorpusID {
				return nil
			}
		}
		return makeError(CategoryUnsatisfiable, location+".corpus_id", "no declared audio input uses corpus ID %q", value.CorpusID)
	case ExpectClose:
		if index != len(all)-1 {
			return makeError(CategoryContradictory, location, "close expectation must be last")
		}
	case ExpectTime:
		if value.At > finalTime {
			return makeError(CategoryUnsatisfiable, location+".at", "logical time %d is unreachable; scenario ends at %d", value.At, finalTime)
		}
	}
	return nil
}

func lookupCorpus(lookup CorpusLookup, id string) bool {
	return lookup != nil && lookup.Has(id)
}
