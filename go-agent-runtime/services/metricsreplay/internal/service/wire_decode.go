package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/metricsreplay"
)

func decodeEvent(record metricsreplay.Record) (wireEvent, error) {
	payload := bytes.TrimSpace(record.Payload)
	if len(payload) == 0 {
		return wireEvent{}, errors.New("payload is empty")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return wireEvent{}, err
	}
	if fields == nil {
		return wireEvent{}, errors.New("payload must be a JSON object")
	}
	typeName, err := requiredString(fields, "type")
	if err != nil {
		return wireEvent{}, err
	}
	if record.Type != "" && record.Type != typeName {
		return wireEvent{}, fmt.Errorf("record type %q does not match payload type %q", record.Type, typeName)
	}
	if record.PayloadType == "" || record.PayloadType == metricsreplay.PayloadTypeWebSocketMessage {
		return wireEvent{typeName: typeName, fields: fields}, nil
	}
	if record.PayloadType != metricsreplay.PayloadTypeStreamMessage {
		return wireEvent{}, fmt.Errorf("unsupported payload type %q", record.PayloadType)
	}
	value, err := requiredObject(fields, "value")
	if err != nil {
		return wireEvent{}, err
	}
	merged := make(map[string]json.RawMessage, len(fields)+len(value))
	for key, raw := range fields {
		merged[key] = raw
	}
	for key, raw := range value {
		merged[key] = raw
	}
	merged["type"] = fields["type"]
	return wireEvent{typeName: typeName, fields: merged}, nil
}

func requiredString(fields map[string]json.RawMessage, key string) (string, error) {
	value, present, err := optionalString(fields, key)
	if err != nil {
		return "", err
	}
	if !present {
		return "", fmt.Errorf("missing %s", key)
	}
	return value, nil
}

func requiredStringForType(fields map[string]json.RawMessage, primary, fallback string) (string, error) {
	if _, ok := fields[primary]; ok {
		return requiredString(fields, primary)
	}
	return requiredString(fields, fallback)
}

func optionalString(fields map[string]json.RawMessage, key string) (string, bool, error) {
	raw, ok := fields[key]
	if !ok {
		return "", false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true, fmt.Errorf("%s must be a string, got null", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, fmt.Errorf("%s must be a string: %w", key, err)
	}
	return value, true, nil
}

func requiredObject(fields map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	raw, ok := fields[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("missing %s object", key)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		if err == nil {
			err = errors.New("must be a JSON object")
		}
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}

func requiredArray(fields map[string]json.RawMessage, key string) ([]json.RawMessage, error) {
	raw, ok := fields[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("missing %s array", key)
	}
	var value []json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		if err == nil {
			err = errors.New("must be a JSON array")
		}
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
}
