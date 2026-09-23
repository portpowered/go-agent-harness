package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay/internal/capture"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const excerptContextBytes = 24

func cloneCaptureEvents(events []gatewaytesting.CapturedSessionEvent) []gatewaytesting.CapturedSessionEvent {
	owned := make([]gatewaytesting.CapturedSessionEvent, len(events))
	copy(owned, events)
	for index := range owned {
		owned[index].Payload = append(json.RawMessage(nil), owned[index].Payload...)
		owned[index].Data = append(json.RawMessage(nil), owned[index].Data...)
	}
	return owned
}

func sendContextOutcome(ctx context.Context) messages.SessionSendOutcome {
	err := ctx.Err()
	if err == context.DeadlineExceeded {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

func replayMismatch(expected, actual string, cause error) error {
	return errors.Join(gateway.NewReplayMismatchError(expected, actual, cause), providers.ErrReplayMismatch)
}

func replayIncomplete(expected, actual string, cause error) error {
	return errors.Join(gateway.NewReplayIncompleteError(expected, actual, cause), providers.ErrReplayIncomplete)
}

func eventDescription(sequence int, eventType string) string {
	return fmt.Sprintf("event type %q at sequence %d", eventType, sequence)
}

func outboundEventDescription(event gatewaytesting.CapturedSessionEvent) string {
	return fmt.Sprintf("outbound event %s at sequence %d", event.Type, event.Sequence)
}

func eventPayload(event gatewaytesting.CapturedSessionEvent) []byte {
	if len(event.Payload) > 0 {
		return event.Payload
	}
	return event.Data
}

func decodeStreamEvent(event gatewaytesting.CapturedSessionEvent) (messages.StreamMessage, error) {
	payload := eventPayload(event)
	if len(payload) == 0 {
		return messages.StreamMessage{}, errors.New("missing payload")
	}
	if event.PayloadType != "" && event.PayloadType != gatewaytesting.SessionPayloadTypeStreamMessage {
		return messages.StreamMessage{}, fmt.Errorf("unsupported payload type: %s", event.PayloadType)
	}
	return capture.UnmarshalStreamMessage(payload)
}

func compareStreamEvent(expected gatewaytesting.CapturedSessionEvent, actual messages.StreamMessage) error {
	payload := eventPayload(expected)
	if len(payload) == 0 {
		return fmt.Errorf("expected outbound event %s is missing payload", expected.Type)
	}
	if expected.PayloadType != "" && expected.PayloadType != gatewaytesting.SessionPayloadTypeStreamMessage {
		return fmt.Errorf("expected outbound event %s has unsupported payload type %s", expected.Type, expected.PayloadType)
	}
	actualPayload, err := capture.MarshalStreamMessage(actual)
	if err != nil {
		return fmt.Errorf("marshal outbound event %s: %w", actual.Type, err)
	}
	if err := compareJSONPayloads(payload, actualPayload, "stream message"); err != nil {
		return err
	}
	if expected.Type != "" && expected.Type != string(actual.Type) {
		return fmt.Errorf("expected event type %q, got %q", expected.Type, actual.Type)
	}
	return nil
}

func jsonPayloadEqual(expected, actual []byte) bool {
	decode := func(data []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			if err == nil {
				return nil, errors.New("multiple JSON values")
			}
			return nil, err
		}
		return value, nil
	}
	expectedValue, expectedErr := decode(expected)
	actualValue, actualErr := decode(actual)
	return expectedErr == nil && actualErr == nil && reflect.DeepEqual(expectedValue, actualValue)
}

func compareJSONPayloads(expected, actual []byte, fallback string) error {
	if jsonPayloadEqual(expected, actual) {
		return nil
	}
	var expectedValue, actualValue any
	if json.Unmarshal(expected, &expectedValue) == nil && json.Unmarshal(actual, &actualValue) == nil {
		if difference := firstJSONDifference(expectedValue, actualValue, ""); difference != nil {
			expectedBytes := jsonDifferenceBytes(difference.expected)
			actualBytes := jsonDifferenceBytes(difference.actual)
			offset := firstByteDifference(expectedBytes, actualBytes)
			return gateway.NewReplayPayloadDivergenceError(
				jsonPointerLocation(difference.pointer),
				boundedJSONExcerpt(expectedBytes, offset), boundedJSONExcerpt(actualBytes, offset),
			)
		}
	}
	offset := firstByteDifference(expected, actual)
	return gateway.NewReplayPayloadDivergenceError(fallback, boundedRawExcerpt(expected, offset), boundedRawExcerpt(actual, offset))
}

type jsonDifference struct {
	pointer  string
	expected any
	actual   any
}

type missingJSONValue struct{}

func firstJSONDifference(expected, actual any, pointer string) *jsonDifference {
	if expected == nil || actual == nil {
		if expected == nil && actual == nil {
			return nil
		}
		return &jsonDifference{pointer: pointer, expected: expected, actual: actual}
	}
	if expectedMap, ok := expected.(map[string]any); ok {
		return firstJSONMapDifference(expectedMap, actual, pointer)
	}
	if expectedSlice, ok := expected.([]any); ok {
		return firstJSONSliceDifference(expectedSlice, actual, pointer)
	}
	if reflect.DeepEqual(expected, actual) {
		return nil
	}
	return &jsonDifference{pointer: pointer, expected: expected, actual: actual}
}

func firstJSONMapDifference(expected map[string]any, actual any, pointer string) *jsonDifference {
	actualMap, ok := actual.(map[string]any)
	if !ok {
		return &jsonDifference{pointer: pointer, expected: expected, actual: actual}
	}
	for _, key := range sortedJSONMapKeys(expected, actualMap) {
		want, wantOK := expected[key]
		got, gotOK := actualMap[key]
		child := appendJSONPointer(pointer, key)
		if !wantOK {
			return &jsonDifference{pointer: child, expected: missingJSONValue{}, actual: got}
		}
		if !gotOK {
			return &jsonDifference{pointer: child, expected: want, actual: missingJSONValue{}}
		}
		if difference := firstJSONDifference(want, got, child); difference != nil {
			return difference
		}
	}
	return nil
}

func sortedJSONMapKeys(expected, actual map[string]any) []string {
	keys := make(map[string]struct{}, len(expected)+len(actual))
	for key := range expected {
		keys[key] = struct{}{}
	}
	for key := range actual {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	return ordered
}

func firstJSONSliceDifference(expected []any, actual any, pointer string) *jsonDifference {
	actualSlice, ok := actual.([]any)
	if !ok {
		return &jsonDifference{pointer: pointer, expected: expected, actual: actual}
	}
	common := min(len(expected), len(actualSlice))
	for index := 0; index < common; index++ {
		child := appendJSONPointer(pointer, strconv.Itoa(index))
		if difference := firstJSONDifference(expected[index], actualSlice[index], child); difference != nil {
			return difference
		}
	}
	if len(expected) == len(actualSlice) {
		return nil
	}
	child := appendJSONPointer(pointer, strconv.Itoa(common))
	if len(expected) < len(actualSlice) {
		return &jsonDifference{pointer: child, expected: missingJSONValue{}, actual: actualSlice[common]}
	}
	return &jsonDifference{pointer: child, expected: expected[common], actual: missingJSONValue{}}
}

func jsonPointerLocation(pointer string) string {
	if pointer == "" {
		return `JSON pointer ""`
	}
	quoted := strconv.QuoteToASCII(pointer)
	return "JSON pointer " + quoted[1:len(quoted)-1]
}

func jsonDifferenceBytes(value any) []byte {
	if _, missing := value.(missingJSONValue); missing {
		return []byte("<missing>")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return []byte("<unencodable>")
	}
	return data
}

func firstByteDifference(expected, actual []byte) int {
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

func boundedJSONExcerpt(data []byte, offset int) string {
	return boundedExcerpt(data, offset, false)
}

func boundedRawExcerpt(data []byte, offset int) string {
	return boundedExcerpt(data, offset, true)
}

func boundedExcerpt(data []byte, offset int, quote bool) string {
	const limit = 96
	const marker = "...(truncated)"
	if len(data) == 0 {
		return emptyExcerpt(quote)
	}
	offset = min(max(offset, 0), len(data))
	start, end := excerptWindow(len(data), offset)
	for {
		if candidate := formatExcerpt(data, start, end, quote, marker); len(candidate) <= limit {
			return candidate
		}
		if end-start <= 1 {
			return marker
		}
		start, end = shrinkExcerptWindow(start, end, offset)
	}
}

func emptyExcerpt(quote bool) string {
	if quote {
		return `""`
	}
	return ""
}

func excerptWindow(length, offset int) (int, int) {
	start, end := max(offset-excerptContextBytes, 0), min(offset+excerptContextBytes, length)
	if start == end {
		start = max(offset-1, 0)
		end = min(offset+1, length)
	}
	return start, end
}

func formatExcerpt(data []byte, start, end int, quote bool, marker string) string {
	prefix, suffix := "", ""
	if start > 0 {
		prefix = marker
	}
	if end < len(data) {
		suffix = marker
	}
	excerpt := string(data[start:end])
	if quote {
		excerpt = strconv.QuoteToASCII(excerpt)
	}
	return prefix + excerpt + suffix
}

func shrinkExcerptWindow(start, end, offset int) (int, int) {
	if end-offset > offset-start {
		return start, end - 1
	}
	if start < offset {
		return start + 1, end
	}
	return start, end - 1
}

func appendJSONPointer(pointer, token string) string {
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
