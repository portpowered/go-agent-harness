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
func Compare(expected, actual Projection) []Difference {
	expectedValue := projectionJSONValue(expected)
	actualValue := projectionJSONValue(actual)
	differences := make([]Difference, 0)
	compareJSONValue("", expectedValue, actualValue, &differences)
	return differences
}

func projectionJSONValue(projection Projection) any {
	encoded, err := json.Marshal(projection)
	if err != nil {
		// Projection contains only encoding/json-supported fields. Keep this
		// invariant explicit if that public type changes in the future.
		panic(fmt.Sprintf("parity: marshal projection: %v", err))
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		// The value was produced by encoding/json immediately above, so this is
		// an invariant failure rather than an input validation case.
		panic(fmt.Sprintf("parity: decode projection: %v", err))
	}
	return value
}

func compareJSONValue(path string, expected, actual any, differences *[]Difference) {
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualValue, ok := actual.(map[string]any)
		if !ok {
			appendDifference(differences, path, expected, actual)
			return
		}

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
			expectedMember, expectedPresent := expectedValue[key]
			actualMember, actualPresent := actualValue[key]
			if !expectedPresent {
				expectedMember = nil
			}
			if !actualPresent {
				actualMember = nil
			}
			compareJSONValue(childPath, expectedMember, actualMember, differences)
		}

	case []any:
		actualValue, ok := actual.([]any)
		if !ok {
			appendDifference(differences, path, expected, actual)
			return
		}

		length := len(expectedValue)
		if len(actualValue) > length {
			length = len(actualValue)
		}
		for index := 0; index < length; index++ {
			var expectedMember, actualMember any
			if index < len(expectedValue) {
				expectedMember = expectedValue[index]
			}
			if index < len(actualValue) {
				actualMember = actualValue[index]
			}
			compareJSONValue(fmt.Sprintf("%s[%d]", path, index), expectedMember, actualMember, differences)
		}

	default:
		if !sameJSONValue(expected, actual) {
			appendDifference(differences, path, expected, actual)
		}
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

func sameJSONValue(expected, actual any) bool {
	return bytes.Equal(mustMarshalJSONValue(expected), mustMarshalJSONValue(actual))
}

func renderJSONValue(value any) string {
	return string(mustMarshalJSONValue(value))
}

func mustMarshalJSONValue(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("parity: marshal comparison value: %v", err))
	}
	return encoded
}
