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
		if pointer, ok := firstJSONDifference(expectedValue, actualValue, ""); ok {
			location := "JSON pointer " + strconv.QuoteToASCII(pointer)[1:len(strconv.QuoteToASCII(pointer))-1]
			return gateway.NewReplayPayloadDivergenceError(location, "<recorded>", "<sent>")
		}
	}
	return gateway.NewReplayPayloadDivergenceError(fallback, "<recorded>", "<sent>")
}

func firstJSONDifference(expected, actual any, pointer string) (string, bool) {
	if expectedMap, ok := expected.(map[string]any); ok {
		actualMap, actualOK := actual.(map[string]any)
		if !actualOK {
			return pointer, true
		}
		keys := make(map[string]struct{}, len(expectedMap)+len(actualMap))
		for key := range expectedMap {
			keys[key] = struct{}{}
		}
		for key := range actualMap {
			keys[key] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		for _, key := range ordered {
			want, wantOK := expectedMap[key]
			got, gotOK := actualMap[key]
			child := appendJSONPointer(pointer, key)
			if !wantOK || !gotOK {
				return child, true
			}
			if found, differs := firstJSONDifference(want, got, child); differs {
				return found, true
			}
		}
		return "", false
	}
	if expectedSlice, ok := expected.([]any); ok {
		actualSlice, actualOK := actual.([]any)
		if !actualOK {
			return pointer, true
		}
		common := min(len(expectedSlice), len(actualSlice))
		for index := 0; index < common; index++ {
			if found, differs := firstJSONDifference(expectedSlice[index], actualSlice[index], appendJSONPointer(pointer, strconv.Itoa(index))); differs {
				return found, true
			}
		}
		if len(expectedSlice) != len(actualSlice) {
			return appendJSONPointer(pointer, strconv.Itoa(common)), true
		}
		return "", false
	}
	if reflect.DeepEqual(expected, actual) {
		return "", false
	}
	return pointer, true
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
