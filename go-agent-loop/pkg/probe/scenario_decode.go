package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

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
