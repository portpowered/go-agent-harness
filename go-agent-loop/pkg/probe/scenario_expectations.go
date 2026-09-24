package probe

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

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
	kind, err := parseExpectationKind(value, location)
	if err != nil {
		return ExpectedBehavior{}, err
	}
	fields, err := payload(value, location)
	if err != nil {
		return ExpectedBehavior{}, err
	}
	if err := unknown(fields, expectationFieldsByKind[kind], location); err != nil {
		return ExpectedBehavior{}, err
	}
	expectation := ExpectedBehavior{Type: kind, Kind: kind, StepIndex: -1, Step: -1}
	for _, decoder := range expectationFieldDecoders() {
		raw, _, ok, err := field(fields, location, decoder.aliases...)
		if err != nil {
			return expectation, err
		}
		if !ok {
			continue
		}
		if err := decoder.decode(&expectation, raw, location); err != nil {
			return expectation, err
		}
	}
	if err := validateExpectationFields(expectation, location); err != nil {
		return ExpectedBehavior{}, err
	}
	return expectation, nil
}

func parseExpectationKind(value object, location string) (ExpectationKind, error) {
	discriminator, key, ok, err := field(value, location, "type", "kind")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", makeError(CategoryMissingField, location+".type", "expectation discriminator is required")
	}
	name, err := stringValue(discriminator, location+"."+key)
	if err != nil {
		return "", err
	}
	kind, ok := expectationKind(name)
	if !ok {
		return "", makeError(CategoryUnknownVariant, location+"."+key, "unknown expectation variant %q", name)
	}
	return kind, nil
}

// expectationFieldDecoder applies one optional payload field, located by any
// of its aliases, to the expectation being decoded.
type expectationFieldDecoder struct {
	aliases []string
	decode  func(expectation *ExpectedBehavior, raw json.RawMessage, location string) error
}

// expectationFieldDecoders lists payload fields in their decoding order.
func expectationFieldDecoders() []expectationFieldDecoder {
	return []expectationFieldDecoder{
		{[]string{"text", "value", "message", "event"}, decodeExpectationText},
		{[]string{"corpus_id", "corpusID"}, func(e *ExpectedBehavior, raw json.RawMessage, location string) (err error) {
			e.CorpusID, err = requiredValue(raw, location+".corpus_id")
			return err
		}},
		{[]string{"tool_call_id", "toolCallID"}, func(e *ExpectedBehavior, raw json.RawMessage, location string) (err error) {
			e.ToolCallID, err = requiredValue(raw, location+".tool_call_id")
			return err
		}},
		{[]string{"tool_name", "toolName", "name"}, func(e *ExpectedBehavior, raw json.RawMessage, location string) (err error) {
			e.ToolName, err = requiredValue(raw, location+".tool_name")
			return err
		}},
		{[]string{"result"}, func(e *ExpectedBehavior, raw json.RawMessage, _ string) error {
			e.Result = append(json.RawMessage(nil), raw...)
			return nil
		}},
		{[]string{"at", "time", "logical_time", "logicalTime"}, decodeExpectationAt},
		{[]string{"count"}, func(e *ExpectedBehavior, raw json.RawMessage, location string) (err error) {
			e.Count, err = integer(raw, location+".count")
			return err
		}},
		{[]string{"step", "step_index"}, decodeExpectationStep},
		{[]string{"after", "after_step"}, decodeExpectationAfter},
		{[]string{"before", "before_step"}, decodeExpectationBefore},
	}
}

func decodeExpectationText(e *ExpectedBehavior, raw json.RawMessage, location string) error {
	text, err := stringValue(raw, location+".value")
	if err != nil {
		return err
	}
	if e.Kind == ExpectText || e.Kind == ExpectTranscript || e.Kind == ExpectContains {
		e.Text = text
	} else {
		e.Value = text
	}
	return nil
}

func decodeExpectationAt(e *ExpectedBehavior, raw json.RawMessage, location string) (err error) {
	if e.At, err = parseLogical(raw, location+".at"); err != nil {
		return err
	}
	e.Time, e.HasAt = e.At, true
	return nil
}

func decodeExpectationStep(e *ExpectedBehavior, raw json.RawMessage, location string) (err error) {
	if e.StepIndex, err = integer(raw, location+".step"); err != nil {
		return err
	}
	e.Step, e.HasStep = e.StepIndex, true
	return nil
}

func decodeExpectationAfter(e *ExpectedBehavior, raw json.RawMessage, location string) (err error) {
	if e.AfterStep, err = integer(raw, location+".after"); err != nil {
		return err
	}
	e.HasAfter = true
	return nil
}

func decodeExpectationBefore(e *ExpectedBehavior, raw json.RawMessage, location string) (err error) {
	if e.BeforeStep, err = integer(raw, location+".before"); err != nil {
		return err
	}
	e.HasBefore = true
	return nil
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

	// Measurable expectation kinds.
	ExpectAudioEnergy:        {},
	ExpectTranscriptContains: {"text": true},
	ExpectToolCalled:         {"tool_name": true},
	ExpectLatencyWithinTicks: {"at": true},
	ExpectTerminalReason:     {"value": true},
	ExpectTerminalProvenance: {"value": true},
	ExpectOutputState:        {"value": true},
	ExpectFrameCount:         {},
	ExpectBufferDisposition:  {"value": true},
	ExpectMetricsReconcile:   {},
	ExpectResponseCancel:     {"value": true, "at": true},
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
