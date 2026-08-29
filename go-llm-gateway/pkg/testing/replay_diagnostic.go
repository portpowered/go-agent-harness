package testing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
)

const (
	// replayExcerptLimit is the maximum length of each escaped diagnostic
	// excerpt, including any truncation markers.
	replayExcerptLimit     = 96
	replayTruncationMarker = "...(truncated)"
)

type replayJSONDifference struct {
	pointer  string
	expected any
	actual   any
}

type replayMissingJSONValue struct{}

func replayEventDescription(sequence int, eventType string) string {
	return fmt.Sprintf("event type %q at sequence %d", eventType, sequence)
}

func compareReplayPayloads(expected, actual []byte) error {
	if bytes.Equal(expected, actual) {
		return nil
	}

	var expectedValue any
	var actualValue any
	expectedErr := json.Unmarshal(expected, &expectedValue)
	actualErr := json.Unmarshal(actual, &actualValue)
	if expectedErr == nil && actualErr == nil {
		difference := firstReplayJSONDifference(expectedValue, actualValue, "")
		if difference == nil {
			return nil
		}

		expectedBytes := replayJSONValueBytes(difference.expected)
		actualBytes := replayJSONValueBytes(difference.actual)
		return gateway.NewReplayPayloadDivergenceError(
			replayJSONPointerLocation(difference.pointer),
			boundedReplayJSONExcerpt(expectedBytes, firstReplayByteDifference(expectedBytes, actualBytes)),
			boundedReplayJSONExcerpt(actualBytes, firstReplayByteDifference(expectedBytes, actualBytes)),
		)
	}

	offset := firstReplayByteDifference(expected, actual)
	return gateway.NewReplayPayloadDivergenceError(
		fmt.Sprintf("byte offset %d", offset),
		boundedReplayExcerpt(expected, offset),
		boundedReplayExcerpt(actual, offset),
	)
}

func firstReplayJSONDifference(expected, actual any, pointer string) *replayJSONDifference {
	if expected == nil || actual == nil {
		if expected == nil && actual == nil {
			return nil
		}
		return &replayJSONDifference{pointer: pointer, expected: expected, actual: actual}
	}

	switch expectedValue := expected.(type) {
	case map[string]any:
		actualValue, ok := actual.(map[string]any)
		if !ok {
			return &replayJSONDifference{pointer: pointer, expected: expected, actual: actual}
		}

		keys := make(map[string]struct{}, len(expectedValue)+len(actualValue))
		for key := range expectedValue {
			keys[key] = struct{}{}
		}
		for key := range actualValue {
			keys[key] = struct{}{}
		}
		orderedKeys := make([]string, 0, len(keys))
		for key := range keys {
			orderedKeys = append(orderedKeys, key)
		}
		sort.Strings(orderedKeys)

		for _, key := range orderedKeys {
			expectedChild, expectedOK := expectedValue[key]
			actualChild, actualOK := actualValue[key]
			childPointer := appendReplayJSONPointer(pointer, key)
			if !expectedOK {
				return &replayJSONDifference{
					pointer:  childPointer,
					expected: replayMissingJSONValue{},
					actual:   actualChild,
				}
			}
			if !actualOK {
				return &replayJSONDifference{
					pointer:  childPointer,
					expected: expectedChild,
					actual:   replayMissingJSONValue{},
				}
			}
			if difference := firstReplayJSONDifference(expectedChild, actualChild, childPointer); difference != nil {
				return difference
			}
		}
		return nil

	case []any:
		actualValue, ok := actual.([]any)
		if !ok {
			return &replayJSONDifference{pointer: pointer, expected: expected, actual: actual}
		}

		commonLength := len(expectedValue)
		if len(actualValue) < commonLength {
			commonLength = len(actualValue)
		}
		for index := 0; index < commonLength; index++ {
			childPointer := appendReplayJSONPointer(pointer, strconv.Itoa(index))
			if difference := firstReplayJSONDifference(expectedValue[index], actualValue[index], childPointer); difference != nil {
				return difference
			}
		}
		if len(expectedValue) != len(actualValue) {
			childPointer := appendReplayJSONPointer(pointer, strconv.Itoa(commonLength))
			if len(expectedValue) < len(actualValue) {
				return &replayJSONDifference{
					pointer:  childPointer,
					expected: replayMissingJSONValue{},
					actual:   actualValue[commonLength],
				}
			}
			return &replayJSONDifference{
				pointer:  childPointer,
				expected: expectedValue[commonLength],
				actual:   replayMissingJSONValue{},
			}
		}
		return nil

	default:
		if reflect.DeepEqual(expected, actual) {
			return nil
		}
		return &replayJSONDifference{pointer: pointer, expected: expected, actual: actual}
	}
}

func appendReplayJSONPointer(pointer, token string) string {
	var builder strings.Builder
	builder.Grow(len(pointer) + len(token) + 1)
	builder.WriteString(pointer)
	builder.WriteByte('/')
	for _, char := range token {
		switch char {
		case '~':
			builder.WriteString("~0")
		case '/':
			builder.WriteString("~1")
		default:
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func replayJSONPointerLocation(pointer string) string {
	if pointer == "" {
		return `JSON pointer ""`
	}
	quoted := strconv.QuoteToASCII(pointer)
	return "JSON pointer " + quoted[1:len(quoted)-1]
}

func replayJSONValueBytes(value any) []byte {
	if _, ok := value.(replayMissingJSONValue); ok {
		return []byte("<missing>")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return []byte("<unencodable>")
	}
	return data
}

func firstReplayByteDifference(expected, actual []byte) int {
	commonLength := len(expected)
	if len(actual) < commonLength {
		commonLength = len(actual)
	}
	for index := 0; index < commonLength; index++ {
		if expected[index] != actual[index] {
			return index
		}
	}
	return commonLength
}

func boundedReplayExcerpt(data []byte, offset int) string {
	return boundedReplayExcerptWithMode(data, offset, true)
}

func boundedReplayJSONExcerpt(data []byte, offset int) string {
	return boundedReplayExcerptWithMode(data, offset, false)
}

func boundedReplayExcerptWithMode(data []byte, offset int, quote bool) string {
	if len(data) == 0 {
		if quote {
			return `""`
		}
		return ""
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(data) {
		offset = len(data)
	}

	const contextBytes = 24
	start := offset - contextBytes
	if start < 0 {
		start = 0
	}
	end := offset + contextBytes
	if end > len(data) {
		end = len(data)
	}
	if start == end {
		start = offset - 1
		if start < 0 {
			start = 0
		}
		end = offset + 1
		if end > len(data) {
			end = len(data)
		}
	}

	for {
		prefix := ""
		if start > 0 {
			prefix = replayTruncationMarker
		}
		suffix := ""
		if end < len(data) {
			suffix = replayTruncationMarker
		}
		excerpt := string(data[start:end])
		if quote {
			excerpt = strconv.QuoteToASCII(excerpt)
		}
		candidate := prefix + excerpt + suffix
		if len(candidate) <= replayExcerptLimit {
			return candidate
		}
		if end-start <= 1 {
			// QuoteToASCII is bounded for a single byte, so this is only a
			// defensive fallback for a future change to the excerpt format.
			return replayTruncationMarker
		}

		// Keep the first differing byte in the window while removing context
		// from the side furthest from the comparison offset.
		if end-offset > offset-start {
			end--
		} else if start < offset {
			start++
		} else {
			end--
		}
	}
}
