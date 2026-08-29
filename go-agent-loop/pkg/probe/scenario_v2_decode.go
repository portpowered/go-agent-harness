package probe

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func decodeScenarioV2Object(data []byte, location string) (scenarioV2Object, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, newScenarioV2Error(location, "malformed JSON: %v", err)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, newScenarioV2Error(location, "must be a JSON object")
	}
	result := make(scenarioV2Object)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, newScenarioV2Error(location, "malformed JSON: %v", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, newScenarioV2Error(location, "object key must be a string")
		}
		if _, exists := result[key]; exists {
			return nil, newScenarioV2Error(location+"."+key, "duplicate field")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, newScenarioV2Error(location+"."+key, "malformed JSON: %v", err)
		}
		result[key] = cloneScenarioV2Raw(raw)
	}
	end, err := decoder.Token()
	if err != nil {
		return nil, newScenarioV2Error(location, "malformed JSON: %v", err)
	}
	if delim, ok := end.(json.Delim); !ok || delim != '}' {
		return nil, newScenarioV2Error(location, "malformed JSON object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, newScenarioV2Error(location, "trailing JSON document")
		}
		return nil, newScenarioV2Error(location, "malformed trailing JSON: %v", err)
	}
	return result, nil
}

func rejectScenarioV2Fields(value scenarioV2Object, allowed map[string]struct{}, location string) error {
	unknown := make([]string, 0)
	for key := range value {
		if _, ok := allowed[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return newScenarioV2Error(location+"."+unknown[0], "unknown field")
}

func scenarioV2VariantFields(fields map[string]struct{}) map[string]struct{} {
	allowed := make(map[string]struct{}, len(fields)+1)
	allowed["type"] = struct{}{}
	for fieldName := range fields {
		allowed[fieldName] = struct{}{}
	}
	return allowed
}

func scenarioV2Array(raw json.RawMessage, location string) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, newScenarioV2Error(location, "must be a JSON array")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil || values == nil {
		if err == nil {
			err = errors.New("must be a JSON array")
		}
		return nil, newScenarioV2Error(location, "%v", err)
	}
	for index := range values {
		values[index] = cloneScenarioV2Raw(values[index])
	}
	return values, nil
}

func requiredScenarioV2String(value scenarioV2Object, location, fieldName string) (string, error) {
	raw, ok := value[fieldName]
	if !ok {
		return "", newScenarioV2Error(location+"."+fieldName, "required field is missing")
	}
	result, err := scenarioV2String(raw, location+"."+fieldName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(result) == "" {
		return "", newScenarioV2Error(location+"."+fieldName, "must not be empty")
	}
	return result, nil
}

func optionalScenarioV2String(value scenarioV2Object, location, fieldName string) (string, error) {
	raw, ok := value[fieldName]
	if !ok {
		return "", nil
	}
	return scenarioV2String(raw, location+"."+fieldName)
}

func scenarioV2String(raw json.RawMessage, location string) (string, error) {
	var result string
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &result) != nil {
		return "", newScenarioV2Error(location, "must be a string")
	}
	if !utf8.ValidString(result) {
		return "", newScenarioV2Error(location, "must be valid UTF-8")
	}
	return result, nil
}

func optionalScenarioV2JSON(value scenarioV2Object, location, fieldName string, objectOnly bool) (json.RawMessage, bool, error) {
	raw, ok := value[fieldName]
	if !ok {
		return nil, false, nil
	}
	result, err := scenarioV2JSON(raw, location+"."+fieldName, objectOnly)
	return result, true, err
}

func scenarioV2JSON(raw json.RawMessage, location string, objectOnly bool) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil, newScenarioV2Error(location, "must be a JSON value")
	}
	if objectOnly {
		var object map[string]json.RawMessage
		if json.Unmarshal(trimmed, &object) != nil || object == nil {
			return nil, newScenarioV2Error(location, "must be a JSON object")
		}
	}
	return cloneScenarioV2Raw(trimmed), nil
}

func optionalScenarioV2Bool(value scenarioV2Object, location, fieldName string) (bool, bool, error) {
	raw, ok := value[fieldName]
	if !ok {
		return false, false, nil
	}
	result, err := scenarioV2Bool(raw, location+"."+fieldName)
	return result, true, err
}

func scenarioV2Bool(raw json.RawMessage, location string) (bool, error) {
	var result bool
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &result) != nil {
		return false, newScenarioV2Error(location, "must be a boolean")
	}
	return result, nil
}

func scenarioV2Int(raw json.RawMessage, location string) (int64, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, newScenarioV2Error(location, "must be an integer")
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, newScenarioV2Error(location, "must be an integer")
	}
	return value, nil
}

func optionalScenarioV2StringArray(value scenarioV2Object, location, fieldName string) ([]string, bool, error) {
	raw, ok := value[fieldName]
	if !ok {
		return nil, false, nil
	}
	result, err := scenarioV2StringArray(raw, location+"."+fieldName)
	return result, true, err
}

func scenarioV2StringArray(raw json.RawMessage, location string) ([]string, error) {
	values, err := scenarioV2Array(raw, location)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index], err = scenarioV2String(value, fmt.Sprintf("%s[%d]", location, index))
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(result[index]) == "" {
			return nil, newScenarioV2Error(fmt.Sprintf("%s[%d]", location, index), "must not be empty")
		}
	}
	return result, nil
}

func parseScenarioV2Step(raw json.RawMessage, index int, lookup CorpusLookup, fixtureRoot string) (ScenarioV2Step, error) {
	location := fmt.Sprintf("scenario.steps[%d]", index)
	value, err := decodeScenarioV2Object(raw, location)
	if err != nil {
		return ScenarioV2Step{}, err
	}
	typeName, err := requiredScenarioV2String(value, location, "type")
	if err != nil {
		return ScenarioV2Step{}, err
	}
	stepType := ScenarioV2StepType(typeName)
	allowed, ok := scenarioV2StepFields[stepType]
	if !ok {
		return ScenarioV2Step{}, newScenarioV2Error(location+".type", "unknown step variant")
	}
	if err := rejectScenarioV2Fields(value, scenarioV2VariantFields(allowed), location); err != nil {
		return ScenarioV2Step{}, err
	}
	step := ScenarioV2Step{Type: stepType}
	for fieldName, destination := range map[string]*string{
		"browser_id": &step.BrowserID, "endpoint_id": &step.EndpointID,
		"target_id": &step.TargetID, "origin_contains": &step.OriginContains,
		"url": &step.URL, "fixture": &step.Fixture, "name_contains": &step.NameContains,
		"frame_id": &step.FrameID, "tool_ref": &step.ToolRef, "input_json": &step.InputJSON,
		"reason": &step.Reason, "invocation_id": &step.InvocationID, "corpus_id": &step.CorpusID,
		"text": &step.Text, "after_event": &step.AfterEvent,
	} {
		if value[fieldName] == nil {
			continue
		}
		parsed, parseErr := scenarioV2String(value[fieldName], location+"."+fieldName)
		if parseErr != nil {
			return ScenarioV2Step{}, parseErr
		}
		*destination = parsed
	}
	var has bool
	if step.EligibleOnly, has, err = optionalScenarioV2Bool(value, location, "eligible_only"); err != nil {
		return ScenarioV2Step{}, err
	}
	step.HasEligibleOnly = has
	if step.IncludeZeroToolPages, has, err = optionalScenarioV2Bool(value, location, "include_zero_tool_pages"); err != nil {
		return ScenarioV2Step{}, err
	}
	step.HasIncludeZeroToolPages = has
	if step.Activate, has, err = optionalScenarioV2Bool(value, location, "activate"); err != nil {
		return ScenarioV2Step{}, err
	}
	step.HasActivate = has
	if step.Refresh, has, err = optionalScenarioV2Bool(value, location, "refresh"); err != nil {
		return ScenarioV2Step{}, err
	}
	step.HasRefresh = has
	if step.IncludeSchemas, has, err = optionalScenarioV2Bool(value, location, "include_schemas"); err != nil {
		return ScenarioV2Step{}, err
	}
	step.HasIncludeSchemas = has
	if rawDuration, exists := value["duration_ms"]; exists {
		step.DurationMS, err = scenarioV2Int(rawDuration, location+".duration_ms")
		if err != nil {
			return ScenarioV2Step{}, err
		}
		if step.DurationMS < 0 {
			return ScenarioV2Step{}, newScenarioV2Error(location+".duration_ms", "must not be negative")
		}
		step.HasDurationMS = true
	}
	if err := validateScenarioV2StepRequiredFields(step, location, lookup); err != nil {
		return ScenarioV2Step{}, err
	}
	if step.Fixture != "" {
		if fixtureRoot == "" {
			return ScenarioV2Step{}, newScenarioV2Error(location+".fixture", "scenario path is required for fixture references")
		}
		step.FixturePath, err = resolveScenarioV2FixturePathFromRoot(fixtureRoot, step.Fixture)
		if err != nil {
			return ScenarioV2Step{}, wrapScenarioV2Error(location+".fixture", err)
		}
	}
	return step, nil
}
