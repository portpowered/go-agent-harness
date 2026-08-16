package sessionfixturevalidator

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	shapePayloadTypeStreamMessage = gatewaytesting.SessionPayloadTypeStreamMessage
	shapePayloadTypeWebSocket     = gatewaytesting.SessionPayloadTypeWebSocketMessage
)

type toolObservation struct {
	kind      string
	id        string
	fieldPath string
	recordIdx int
	eventType string
}

// validateSessionFixtureFile preserves the shared hygiene validator and adds
// the command-owned shape checks without changing the shared capture package.
func validateSessionFixtureFile(path string) []gatewaytesting.SessionFixtureValidationError {
	capture, err := gatewaytesting.LoadSessionCapture(path)
	if err != nil {
		return []gatewaytesting.SessionFixtureValidationError{{
			File:      path,
			FieldPath: "$",
			Reason:    err.Error(),
		}}
	}

	errs := gatewaytesting.ValidateSessionCapture(path, capture)
	return append(errs, validateSessionCaptureShapes(path, capture)...)
}

func validateSessionCaptureShapes(file string, capture gatewaytesting.SessionCapture) []gatewaytesting.SessionFixtureValidationError {
	var errs []gatewaytesting.SessionFixtureValidationError
	callOccurrences := make(map[string][]toolObservation)
	resultOccurrences := make(map[string][]toolObservation)
	for recordIndex, record := range capture.Records {
		payload, ok := shapePayload(record)
		if !ok {
			continue
		}

		if audioPath, audioPayload, recognized := recognizedAudioPayload(record, recordIndex, payload); recognized && !hasNonEmptyString(audioPayload) {
			errs = append(errs, gatewaytesting.SessionFixtureValidationError{
				File:      file,
				FieldPath: audioPath,
				Reason:    fmt.Sprintf("recognized audio event %q requires a non-empty audio payload", record.Type),
			})
		}

		if observation, recognized := recognizedToolObservation(record, recordIndex, payload); recognized {
			if strings.TrimSpace(observation.id) == "" {
				errs = append(errs, gatewaytesting.SessionFixtureValidationError{
					File:      file,
					FieldPath: observation.fieldPath,
					Reason:    fmt.Sprintf("recognized %s event %q requires a non-empty persisted call identifier for pairing", observation.kind, observation.eventType),
				})
				continue
			}

			switch observation.kind {
			case "tool call":
				callOccurrences[observation.id] = append(callOccurrences[observation.id], observation)
			case "tool result":
				resultOccurrences[observation.id] = append(resultOccurrences[observation.id], observation)
			}
		}
	}
	for id, occurrences := range callOccurrences {
		callOccurrences[id] = deduplicateToolCallFragments(occurrences)
	}

	unmatched := make([]toolObservation, 0)
	for id, occurrences := range callOccurrences {
		matchingResults := resultOccurrences[id]
		matched := minInt(len(occurrences), len(matchingResults))
		unmatched = append(unmatched, occurrences[matched:]...)
	}
	for id, occurrences := range resultOccurrences {
		matchingCalls := callOccurrences[id]
		matched := minInt(len(occurrences), len(matchingCalls))
		unmatched = append(unmatched, occurrences[matched:]...)
	}
	sort.SliceStable(unmatched, func(i, j int) bool {
		if unmatched[i].recordIdx != unmatched[j].recordIdx {
			return unmatched[i].recordIdx < unmatched[j].recordIdx
		}
		return unmatched[i].kind < unmatched[j].kind
	})
	for _, occurrence := range unmatched {
		var reason string
		if occurrence.kind == "tool call" {
			reason = fmt.Sprintf("tool call %q has no matching tool result with the same exact non-empty call identifier", occurrence.id)
		} else {
			reason = fmt.Sprintf("tool result %q has no matching tool call with the same exact non-empty call identifier", occurrence.id)
		}
		errs = append(errs, gatewaytesting.SessionFixtureValidationError{
			File:      file,
			FieldPath: occurrence.fieldPath,
			Reason:    reason,
		})
	}

	if strings.TrimSpace(capture.Session.FixtureProvenance) == gatewaytesting.SessionFixtureProvenanceProviderRecorded && !hasRecognizedTerminal(capture) {
		errs = append(errs, gatewaytesting.SessionFixtureValidationError{
			File:      file,
			FieldPath: "records[*].type",
			Reason:    "provider-recorded fixture requires a recognized success or error terminal event (response.done, MESSAGE.END, or error)",
		})
	}

	return errs
}

func shapePayload(record gatewaytesting.CapturedSessionEvent) (map[string]any, bool) {
	raw := record.Payload
	if len(raw) == 0 {
		raw = record.Data
	}
	if len(raw) == 0 {
		return map[string]any{}, true
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

func recognizedAudioPayload(record gatewaytesting.CapturedSessionEvent, recordIndex int, payload map[string]any) (string, any, bool) {
	base := fmt.Sprintf("records[%d].payload", recordIndex)
	switch record.PayloadType {
	case shapePayloadTypeStreamMessage:
		if record.Type != "AUDIO.DELTA" {
			return "", nil, false
		}
		value, _ := objectField(payload, "value")
		audio, _ := field(value, "content")
		return base + ".value.content", audio, true
	case shapePayloadTypeWebSocket:
		switch record.Type {
		case "response.output_audio.delta", "response.audio.delta":
			audio, _ := field(payload, "delta")
			return base + ".delta", audio, true
		case "input_audio_buffer.append":
			audio, _ := field(payload, "audio")
			return base + ".audio", audio, true
		default:
			return "", nil, false
		}
	default:
		return "", nil, false
	}
}

func recognizedToolObservation(record gatewaytesting.CapturedSessionEvent, recordIndex int, payload map[string]any) (toolObservation, bool) {
	base := fmt.Sprintf("records[%d].payload", recordIndex)
	switch record.PayloadType {
	case shapePayloadTypeStreamMessage:
		switch record.Type {
		case "TOOLCALL.START", "TOOLCALL.END":
			id, fieldPath := streamToolCallID(payload, base)
			return toolObservation{kind: "tool call", id: id, fieldPath: fieldPath, recordIdx: recordIndex, eventType: record.Type}, true
		case "SYSTEM.FULL_MESSAGE":
			message, ok := streamFullMessage(payload)
			if !ok || !isToolRole(message) {
				return toolObservation{}, false
			}
			id, fieldPath := firstStringField(message, base+".value.message.tool_call_id", "tool_call_id", "ToolCallID")
			return toolObservation{kind: "tool result", id: id, fieldPath: fieldPath, recordIdx: recordIndex, eventType: record.Type}, true
		default:
			return toolObservation{}, false
		}
	case shapePayloadTypeWebSocket:
		switch record.Type {
		case "response.output_item.added":
			item, ok := objectField(payload, "item")
			if !ok || !hasStringValue(item, "type", "function_call") {
				return toolObservation{}, false
			}
			id, fieldPath := firstStringField(item, base+".item.call_id", "call_id", "item_id", "id")
			return toolObservation{kind: "tool call", id: id, fieldPath: fieldPath, recordIdx: recordIndex, eventType: record.Type}, true
		case "response.function_call_arguments.done":
			id, fieldPath := firstStringField(payload, base+".call_id", "call_id", "item.call_id", "item_id")
			return toolObservation{kind: "tool call", id: id, fieldPath: fieldPath, recordIdx: recordIndex, eventType: record.Type}, true
		case "conversation.item.create":
			item, ok := objectField(payload, "item")
			if !ok || !hasStringValue(item, "type", "function_call_output") {
				return toolObservation{}, false
			}
			id, fieldPath := firstStringField(item, base+".item.call_id", "call_id")
			return toolObservation{kind: "tool result", id: id, fieldPath: fieldPath, recordIdx: recordIndex, eventType: record.Type}, true
		default:
			return toolObservation{}, false
		}
	default:
		return toolObservation{}, false
	}
}

func streamToolCallID(payload map[string]any, base string) (string, string) {
	if id, ok := stringField(payload, "tool_call_id"); ok {
		return id, base + ".tool_call_id"
	}
	value, _ := objectField(payload, "value")
	if id, ok := stringField(value, "tool_call_id"); ok {
		return id, base + ".value.tool_call_id"
	}
	return "", base + ".tool_call_id"
}

func streamFullMessage(payload map[string]any) (map[string]any, bool) {
	value, ok := objectField(payload, "value")
	if !ok {
		return nil, false
	}
	if message, ok := objectField(value, "message"); ok {
		return message, true
	}
	return value, true
}

func isToolRole(message map[string]any) bool {
	role, ok := stringField(message, "role")
	return ok && strings.EqualFold(strings.TrimSpace(role), "tool")
}

func hasRecognizedTerminal(capture gatewaytesting.SessionCapture) bool {
	for _, record := range capture.Records {
		switch record.PayloadType {
		case shapePayloadTypeWebSocket:
			if record.Type == "response.done" || record.Type == "error" {
				return true
			}
		case shapePayloadTypeStreamMessage:
			if record.Type == "MESSAGE.END" || record.Type == "ERROR" {
				return true
			}
		}
	}
	return false
}

func hasNonEmptyString(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func objectField(value any, key string) (map[string]any, bool) {
	child, ok := field(value, key)
	if !ok {
		return nil, false
	}
	object, ok := child.(map[string]any)
	return object, ok
}

func field(value any, key string) (any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	normalizedKey := normalizeShapeFieldKey(key)
	for candidate, child := range object {
		if normalizeShapeFieldKey(candidate) == normalizedKey {
			return child, true
		}
	}
	return nil, false
}

func stringField(value any, key string) (string, bool) {
	child, ok := field(value, key)
	if !ok {
		return "", false
	}
	text, ok := child.(string)
	return text, ok
}

func firstStringField(value any, firstPath string, paths ...string) (string, string) {
	for _, path := range append([]string{firstPath}, paths...) {
		if text, ok := stringFieldPath(value, path); ok {
			return text, firstPath
		}
	}
	return "", firstPath
}

func stringFieldPath(value any, path string) (string, bool) {
	current := any(value)
	for _, part := range strings.Split(path, ".") {
		child, ok := field(current, part)
		if !ok {
			return "", false
		}
		current = child
	}
	text, ok := current.(string)
	return text, ok
}

func hasStringValue(value any, key, expected string) bool {
	actual, ok := stringField(value, key)
	return ok && actual == expected
}

func deduplicateToolCallFragments(occurrences []toolObservation) []toolObservation {
	unique := make([]toolObservation, 0, len(occurrences))
	for _, occurrence := range occurrences {
		replaced := false
		for i, prior := range unique {
			if prior.eventType == "TOOLCALL.START" && occurrence.eventType == "TOOLCALL.END" {
				unique[i] = occurrence
				replaced = true
				break
			}
			if prior.eventType == "TOOLCALL.END" && occurrence.eventType == "TOOLCALL.START" {
				replaced = true
				break
			}
			if prior.eventType == "response.output_item.added" && occurrence.eventType == "response.function_call_arguments.done" {
				unique[i] = occurrence
				replaced = true
				break
			}
			if prior.eventType == "response.function_call_arguments.done" && occurrence.eventType == "response.output_item.added" {
				replaced = true
				break
			}
		}
		if !replaced {
			unique = append(unique, occurrence)
		}
	}
	return unique
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func normalizeShapeFieldKey(key string) string {
	return strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
}
