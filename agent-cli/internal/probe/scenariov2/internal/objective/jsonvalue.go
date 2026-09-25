package objective

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	jsonPathRoot   = "$"
	jsonPathPrefix = "$."
)

// SemanticJSONEqual compares two JSON documents by value, preserving numeric
// precision and ignoring object key order.
func SemanticJSONEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil {
		return false
	}
	return semanticValueEqual(leftValue, rightValue)
}

func semanticValueEqual(left, right any) bool {
	switch leftValue := left.(type) {
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		return ok && semanticObjectEqual(leftValue, rightValue)
	case []any:
		rightValue, ok := right.([]any)
		return ok && semanticArrayEqual(leftValue, rightValue)
	case json.Number:
		rightValue, ok := right.(json.Number)
		return ok && leftValue.String() == rightValue.String()
	default:
		return left == right
	}
}

func semanticObjectEqual(left, right map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, ok := right[key]
		if !ok || !semanticValueEqual(value, other) {
			return false
		}
	}
	return true
}

func semanticArrayEqual(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !semanticValueEqual(left[index], right[index]) {
			return false
		}
	}
	return true
}

// JSONPathValue resolves the dotted object-field subset of JSONPath ("$" or
// "$.a.b") against raw and returns the selected value re-encoded.
func JSONPathValue(raw json.RawMessage, path string) (json.RawMessage, error) {
	if path == jsonPathRoot {
		return append(json.RawMessage(nil), raw...), nil
	}
	if !strings.HasPrefix(path, jsonPathPrefix) {
		return nil, fmt.Errorf("unsupported JSONPath %q", path)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	for _, segment := range strings.Split(path[len(jsonPathPrefix):], ".") {
		next, err := jsonPathField(value, segment)
		if err != nil {
			return nil, err
		}
		value = next
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func jsonPathField(value any, segment string) (any, error) {
	if segment == "" {
		return nil, errors.New("JSONPath contains an empty segment")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSONPath segment %q is not an object field", segment)
	}
	field, ok := object[segment]
	if !ok {
		return nil, fmt.Errorf("JSONPath field %q is absent", segment)
	}
	return field, nil
}
