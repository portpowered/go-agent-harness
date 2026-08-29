package tools

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"sort"
)

var laneBNormalizedIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type laneBToolSpec struct{ definition ToolDefinition }

func (s *LaneBToolSet) spec(name string) (laneBToolSpec, bool) {
	if s == nil {
		return laneBToolSpec{}, false
	}
	for _, definition := range s.definitions {
		if definition.Name == name {
			return laneBToolSpec{definition: definition}, true
		}
	}
	return laneBToolSpec{}, false
}

func laneBDecodeArguments(raw []byte, spec laneBToolSpec) (map[string]any, []ToolResultIssue) {
	object, issues := laneBDecodeJSONObject(raw)
	if len(issues) > 0 && object == nil {
		return nil, issues
	}
	properties, _ := spec.definition.Parameters["properties"].(map[string]any)
	propertyNames := schemaOrder(spec.definition.Name)
	allowed := make(map[string]map[string]any, len(properties))
	for name, value := range properties {
		property, _ := value.(map[string]any)
		allowed[name] = property
	}
	unknown := make([]string, 0)
	for name := range object {
		if _, ok := allowed[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "unknown_property"})
	}
	result := make(map[string]any, len(properties))
	for _, name := range propertyNames {
		property, exists := allowed[name]
		if !exists {
			continue
		}
		rawValue, present := object[name]
		if !present {
			if requiredPropertyName(spec.definition.Parameters, name) {
				issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "required"})
				continue
			}
			result[name] = property["default"]
			continue
		}
		if bytes.Equal(bytes.TrimSpace(rawValue), []byte("null")) {
			issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "invalid_type"})
			continue
		}
		valueType, _ := property["type"].(string)
		switch valueType {
		case "string":
			var value string
			if err := json.Unmarshal(rawValue, &value); err != nil {
				issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "invalid_type"})
				continue
			}
			result[name] = value
		case "boolean":
			var value bool
			if err := json.Unmarshal(rawValue, &value); err != nil {
				issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "invalid_type"})
				continue
			}
			result[name] = value
		default:
			issues = append(issues, ToolResultIssue{Path: laneBPointerPath(name), Code: "unsupported_type"})
		}
	}
	return result, issues
}

func laneBDecodeJSONObject(raw []byte) (map[string]json.RawMessage, []ToolResultIssue) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, []ToolResultIssue{{Path: "/", Code: "object_required"}}
	}
	object := make(map[string]json.RawMessage)
	var issues []ToolResultIssue
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, []ToolResultIssue{{Path: laneBPointerPath(key), Code: "invalid_json"}}
		}
		if _, exists := object[key]; exists {
			issues = append(issues, ToolResultIssue{Path: laneBPointerPath(key), Code: "duplicate_property"})
			continue
		}
		object[key] = append(json.RawMessage(nil), value...)
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, []ToolResultIssue{{Path: "/", Code: "multiple_json_values"}}
		}
		return nil, []ToolResultIssue{{Path: "/", Code: "invalid_json"}}
	}
	return object, issues
}

func requiredPropertyName(schema map[string]any, name string) bool {
	required, _ := schema["required"].([]string)
	for _, value := range required {
		if value == name {
			return true
		}
	}
	return false
}

func validateDecodedArguments(name string, values map[string]any) []ToolResultIssue {
	var issues []ToolResultIssue
	checkID := func(field string) {
		value, present := values[field]
		if !present {
			return
		}
		text, isString := value.(string)
		if !isString {
			return
		}
		if !laneBNormalizedIDPattern.MatchString(text) {
			issues = append(issues, ToolResultIssue{Path: laneBPointerPath(field), Code: "invalid_identifier"})
		}
	}
	switch name {
	case SelectTabToolName:
		checkID("browser_id")
		checkID("target_id")
	case ListTabsToolName:
		if value, present := values["browser_id"].(string); present && value != "" && !laneBNormalizedIDPattern.MatchString(value) {
			issues = append(issues, ToolResultIssue{Path: laneBPointerPath("browser_id"), Code: "invalid_identifier"})
		}
	}
	return issues
}
