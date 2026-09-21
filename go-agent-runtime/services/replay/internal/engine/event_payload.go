package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"

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
	if !jsonPayloadEqual(payload, actualPayload) {
		return gateway.NewReplayPayloadDivergenceError("stream message", "<recorded>", "<sent>")
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
