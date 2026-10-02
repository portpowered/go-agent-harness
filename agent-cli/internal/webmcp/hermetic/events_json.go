package hermetic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

func withLine(err error, line int) error {
	var validation *EventValidationError
	if errors.As(err, &validation) {
		copyOf := *validation
		copyOf.Line = line
		return &copyOf
	}
	return newEventValidationError(line, "line", "%v", err)
}

func decodeJSONObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	fields := make(map[string]json.RawMessage)
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("value must be a JSON object")
	}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("object key must be a string")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = cloneRaw(value)
	}
	end, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := end.(json.Delim); !ok || delimiter != '}' {
		return nil, errors.New("object is not terminated")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values are not allowed")
		}
		return nil, err
	}
	return fields, nil
}

func rejectUnknownFields(fields map[string]json.RawMessage, allowed map[string]struct{}) error {
	for name := range fields {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}

func parseString(raw json.RawMessage) (string, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errors.New("must be a string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func parseBool(raw json.RawMessage) (bool, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, errors.New("must be a boolean")
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	return value, nil
}

func parseUint(raw json.RawMessage) (uint64, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0, errors.New("must be a non-negative integer")
	}
	value, err := strconv.ParseUint(string(trimmed), 10, 64)
	if err != nil {
		return 0, errors.New("must be a non-negative integer")
	}
	return value, nil
}

func scriptArray(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, errors.New("must be a JSON array")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, errors.New("must be a JSON array")
	}
	for index := range values {
		values[index] = cloneRaw(values[index])
	}
	return values, nil
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

func parseScriptString(raw json.RawMessage) (string, error) {
	value, err := parseString(raw)
	if err != nil {
		return "", errors.New("must be a string")
	}
	return value, nil
}

func validateScriptID(value string) error {
	if err := validateOpaqueID(value); err != nil {
		return err
	}
	return nil
}

// jsonObjectFields decodes raw as a JSON object and reports false for every
// other JSON shape, so callers can leave non-object payloads untouched.
func jsonObjectFields(raw []byte) (map[string]json.RawMessage, bool) {
	fields, err := decodeJSONObject(raw)
	return fields, err == nil
}

// MarshalEvents validates stream ordering and emits canonical UTF-8 JSONL.
func MarshalEvents(events []Event) ([]byte, error) {
	if len(events) == 0 {
		return nil, newEventValidationError(0, "stream", "event stream is empty")
	}
	var output bytes.Buffer
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			return nil, newEventValidationError(index+1, "sequence", "want contiguous sequence %d, got %d", index+1, event.Sequence)
		}
		if index > 0 && event.MonotonicMS < events[index-1].MonotonicMS {
			return nil, newEventValidationError(index+1, "monotonic_ms", "decreased from %d to %d", events[index-1].MonotonicMS, event.MonotonicMS)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, withLine(err, index+1)
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}
