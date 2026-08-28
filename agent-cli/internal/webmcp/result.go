package webmcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ToolResultIssue is a stable, value-free validation issue. Paths use JSON
// Pointer syntax and deliberately never carry the offending value.
type ToolResultIssue struct {
	Path string `json:"path"`
	Code string `json:"code"`
}

// ToolResultError is the exact error object carried by a C0 result envelope.
// Details is always encoded as a JSON object, even when it is empty.
type ToolResultError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
}

// ToolResultEnvelope is the sole broker result wire shape. Data and Error are
// deliberately not optional: successful results contain data and failures
// contain an error, with the other field set to JSON null.
type ToolResultEnvelope struct {
	Version string           `json:"version"`
	OK      bool             `json:"ok"`
	Data    json.RawMessage  `json:"data"`
	Error   *ToolResultError `json:"error"`
}

// ResultEnvelope and ResultError are compatibility aliases for callers that
// use the shorter contract terminology.
type ResultEnvelope = ToolResultEnvelope
type ResultError = ToolResultError
type ResultIssue = ToolResultIssue

// NewToolResultSuccess builds a validated success envelope. The operation
// data must be one JSON value and must not be JSON null; page-owned null is
// represented inside an operation data object such as data.output.
func NewToolResultSuccess(data any) (ToolResultEnvelope, error) {
	raw, err := marshalOneJSONValue(data, false)
	if err != nil {
		return ToolResultEnvelope{}, err
	}
	return ToolResultEnvelope{
		Version: ToolResultVersion,
		OK:      true,
		Data:    raw,
		Error:   nil,
	}, nil
}

// NewToolResultFailure builds a failure envelope and normalizes a nil details
// map to an empty object so the error shape stays exact.
func NewToolResultFailure(resultError ToolResultError) ToolResultEnvelope {
	if resultError.Details == nil {
		resultError.Details = map[string]any{}
	}
	return ToolResultEnvelope{
		Version: ToolResultVersion,
		OK:      false,
		Data:    json.RawMessage("null"),
		Error:   &resultError,
	}
}

// MarshalToolResult returns one compact JSON object and rejects malformed
// envelopes before they cross the provider-facing textual boundary.
func MarshalToolResult(envelope ToolResultEnvelope) ([]byte, error) {
	if err := envelope.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}

// EncodeToolResult is a convenience for operation adapters.
func EncodeToolResult(data any, resultError *ToolResultError) ([]byte, error) {
	if resultError != nil {
		return MarshalToolResult(NewToolResultFailure(*resultError))
	}
	envelope, err := NewToolResultSuccess(data)
	if err != nil {
		return nil, err
	}
	return MarshalToolResult(envelope)
}

// UnmarshalToolResult decodes and validates the exact C0 envelope. Unknown
// versions and unknown top-level fields are rejected rather than guessed.
func UnmarshalToolResult(data []byte) (ToolResultEnvelope, error) {
	var envelope ToolResultEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ToolResultEnvelope{}, err
	}
	if err := envelope.Validate(); err != nil {
		return ToolResultEnvelope{}, err
	}
	return envelope, nil
}

// Validate checks the envelope invariants that are independent of an
// operation's data schema.
func (e ToolResultEnvelope) Validate() error {
	if e.Version != ToolResultVersion {
		return fmt.Errorf("webmcp result version %q is unsupported", e.Version)
	}
	if len(e.Data) == 0 || !json.Valid(e.Data) {
		return fmt.Errorf("webmcp result data is not one JSON value")
	}
	if e.Error != nil && e.Error.Details == nil {
		return fmt.Errorf("webmcp result error details must be an object")
	}
	if e.OK {
		if bytes.Equal(bytes.TrimSpace(e.Data), []byte("null")) {
			return fmt.Errorf("successful webmcp result data must not be null")
		}
		if e.Error != nil {
			return fmt.Errorf("successful webmcp result must have a null error")
		}
		return nil
	}
	if !bytes.Equal(bytes.TrimSpace(e.Data), []byte("null")) {
		return fmt.Errorf("failed webmcp result must have null data")
	}
	if e.Error == nil {
		return fmt.Errorf("failed webmcp result must have an error")
	}
	if e.Error.Code == "" || e.Error.Message == "" {
		return fmt.Errorf("webmcp result error code and message are required")
	}
	if !IsKnownErrorCode(ErrorCode(e.Error.Code)) {
		return fmt.Errorf("webmcp result error code %q is unsupported", e.Error.Code)
	}
	return nil
}

// UnmarshalJSON rejects unknown envelope fields and preserves data as raw JSON
// so object, array, numeric, and null page values are not converted through
// interface{}.
func (e *ToolResultEnvelope) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("cannot decode webmcp result into nil receiver")
	}
	fields, err := decodeObjectFields(data)
	if err != nil {
		return err
	}
	for field := range fields {
		switch field {
		case "version", "ok", "data", "error":
		default:
			return fmt.Errorf("unknown webmcp result field %q", field)
		}
	}
	for _, field := range []string{"version", "ok", "data", "error"} {
		if _, ok := fields[field]; !ok {
			return fmt.Errorf("webmcp result field %q is required", field)
		}
	}
	if err := json.Unmarshal(fields["version"], &e.Version); err != nil {
		return err
	}
	if err := json.Unmarshal(fields["ok"], &e.OK); err != nil {
		return err
	}
	e.Data = append(e.Data[:0], fields["data"]...)
	if bytes.Equal(bytes.TrimSpace(fields["error"]), []byte("null")) {
		e.Error = nil
	} else {
		var resultError ToolResultError
		if err := unmarshalToolResultError(fields["error"], &resultError); err != nil {
			return err
		}
		e.Error = &resultError
	}
	return nil
}

func unmarshalToolResultError(data []byte, resultError *ToolResultError) error {
	fields, err := decodeObjectFields(data)
	if err != nil {
		return err
	}
	for field := range fields {
		switch field {
		case "code", "message", "retryable", "details":
		default:
			return fmt.Errorf("unknown webmcp result error field %q", field)
		}
	}
	for _, field := range []string{"code", "message", "retryable", "details"} {
		if _, ok := fields[field]; !ok {
			return fmt.Errorf("webmcp result error field %q is required", field)
		}
	}
	if err := json.Unmarshal(fields["code"], &resultError.Code); err != nil {
		return err
	}
	if err := json.Unmarshal(fields["message"], &resultError.Message); err != nil {
		return err
	}
	if err := json.Unmarshal(fields["retryable"], &resultError.Retryable); err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(fields["details"]), []byte("null")) {
		return fmt.Errorf("webmcp result error details must be an object")
	}
	if err := json.Unmarshal(fields["details"], &resultError.Details); err != nil {
		return err
	}
	if resultError.Details == nil {
		return fmt.Errorf("webmcp result error details must be an object")
	}
	return nil
}

func decodeObjectFields(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("expected JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("expected JSON object property name")
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("duplicate JSON object field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = append(json.RawMessage(nil), value...)
	}
	closing, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if closing != json.Delim('}') {
		return nil, fmt.Errorf("expected end of JSON object")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("more than one JSON value")
		}
		return nil, err
	}
	return fields, nil
}

func marshalOneJSONValue(value any, allowNull bool) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	compact, err := compactOneJSONValue(raw, allowNull)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(compact), nil
}

// compactOneJSONValue validates that raw contains exactly one JSON value.
func compactOneJSONValue(raw []byte, allowNull bool) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("more than one JSON value")
		}
		return nil, err
	}
	trimmed := bytes.TrimSpace(value)
	if !allowNull && bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("JSON null is not allowed here")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}
