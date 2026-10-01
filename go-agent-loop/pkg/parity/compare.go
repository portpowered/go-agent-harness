package parity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Difference identifies one semantic mismatch between two projections.
// Expected and Actual are compact JSON values, including "null" for a
// missing collection member.
type Difference struct {
	Path     string
	Expected string
	Actual   string
}

// Compare returns every difference between expected and actual. Projection
// fields are compared through their JSON representation so the public field
// names, byte encoding, optional fields, and collection order are preserved.
// The returned findings are ordered by JSON object path and collection index.
//
// A projection that cannot be encoded is reported as a single root
// difference naming the encoding failure instead of being compared.
func Compare(expected, actual Projection) []Difference {
	expectedValue, expectedErr := projectionJSONValue(expected)
	actualValue, actualErr := projectionJSONValue(actual)
	if expectedErr != nil || actualErr != nil {
		return []Difference{{Path: "$", Expected: encodingOutcome(expectedErr), Actual: encodingOutcome(actualErr)}}
	}
	differences := make([]Difference, 0)
	compareJSONValue("", expectedValue, actualValue, &differences)
	return differences
}

// encodingOutcome renders a projection encoding result for a root
// difference.
func encodingOutcome(err error) string {
	if err == nil {
		return "encodable projection"
	}
	return "unencodable projection: " + err.Error()
}

// projectionJSONValue decodes the JSON form of projection into generic JSON
// values, keeping numbers exact.
func projectionJSONValue(projection Projection) (any, error) {
	encoded, err := json.Marshal(projection)
	if err != nil {
		return nil, fmt.Errorf("parity: marshal projection: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parity: decode projection: %w", err)
	}
	return value, nil
}

func compareJSONValue(path string, expected, actual any, differences *[]Difference) {
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualValue, ok := actual.(map[string]any)
		if !ok {
			appendDifference(differences, path, expected, actual)
			return
		}
		compareJSONObject(path, expectedValue, actualValue, differences)
	case []any:
		actualValue, ok := actual.([]any)
		if !ok {
			appendDifference(differences, path, expected, actual)
			return
		}
		compareJSONArray(path, expectedValue, actualValue, differences)
	default:
		// Scalars decoded from JSON (string, json.Number, bool, nil) are
		// comparable, so equality matches their encoded form.
		if expected != actual {
			appendDifference(differences, path, expected, actual)
		}
	}
}

// compareJSONObject compares the union of both objects' members in sorted key
// order; a member missing on one side compares as JSON null.
func compareJSONObject(path string, expectedValue, actualValue map[string]any, differences *[]Difference) {
	keys := make([]string, 0, len(expectedValue)+len(actualValue))
	for key := range expectedValue {
		keys = append(keys, key)
	}
	for key := range actualValue {
		if _, exists := expectedValue[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	for _, key := range keys {
		childPath := key
		if path != "" {
			childPath = path + "." + key
		}
		compareJSONValue(childPath, expectedValue[key], actualValue[key], differences)
	}
}

// compareJSONArray compares elements by index up to the longer length; a
// missing element compares as JSON null.
func compareJSONArray(path string, expectedValue, actualValue []any, differences *[]Difference) {
	length := max(len(actualValue), len(expectedValue))
	for index := range length {
		var expectedMember, actualMember any
		if index < len(expectedValue) {
			expectedMember = expectedValue[index]
		}
		if index < len(actualValue) {
			actualMember = actualValue[index]
		}
		compareJSONValue(fmt.Sprintf("%s[%d]", path, index), expectedMember, actualMember, differences)
	}
}

func appendDifference(differences *[]Difference, path string, expected, actual any) {
	if path == "" {
		path = "$"
	}
	*differences = append(*differences, Difference{
		Path:     path,
		Expected: renderJSONValue(expected),
		Actual:   renderJSONValue(actual),
	})
}

// renderJSONValue renders a decoded JSON value compactly. Values decoded by
// projectionJSONValue always encode; any other value renders its failure.
func renderJSONValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("unencodable %T: %v", value, err)
	}
	return string(encoded)
}
