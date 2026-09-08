package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ToolResultIssue is a value-free input validation issue. Paths use JSON
// Pointer syntax so invalid user values never need to be echoed.
type ToolResultIssue struct {
	Path string `json:"path"`
	Code string `json:"code"`
}

// ToolResultError is the exact four-field failure object in the C0 envelope.
type ToolResultError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
}

// ToolResultEnvelope is the sole textual result shape emitted by the tools
// service. Data and Error are explicit so success and failure cannot be
// confused by omitted JSON members.
type ToolResultEnvelope struct {
	Version string           `json:"version"`
	OK      bool             `json:"ok"`
	Data    json.RawMessage  `json:"data"`
	Error   *ToolResultError `json:"error"`
}

// ResultEnvelope, ResultError, and ResultIssue are concise compatibility
// aliases for callers using the generic result terminology.
type ResultEnvelope = ToolResultEnvelope
type ResultError = ToolResultError
type ResultIssue = ToolResultIssue

// Validate checks envelope shape and the stable failure code vocabulary.
func (e ToolResultEnvelope) Validate() error {
	if e.Version != ToolResultVersion {
		return fmt.Errorf("unsupported webmcp result version %q", e.Version)
	}
	if len(bytes.TrimSpace(e.Data)) == 0 || !json.Valid(e.Data) {
		return fmt.Errorf("webmcp result data must be one JSON value")
	}
	if e.OK {
		return e.validateSuccess()
	}
	return e.validateFailure()
}

func (e ToolResultEnvelope) validateSuccess() error {
	if bytes.Equal(bytes.TrimSpace(e.Data), []byte("null")) {
		return fmt.Errorf("successful webmcp result data must not be null")
	}
	if e.Error != nil {
		return fmt.Errorf("successful webmcp result must have null error")
	}
	return nil
}

func (e ToolResultEnvelope) validateFailure() error {
	if !bytes.Equal(bytes.TrimSpace(e.Data), []byte("null")) {
		return fmt.Errorf("failed webmcp result must have null data")
	}
	if e.Error == nil {
		return fmt.Errorf("failed webmcp result must have an error")
	}
	if e.Error.Code == "" || e.Error.Message == "" {
		return fmt.Errorf("webmcp result error code and message are required")
	}
	if !ErrorCode(e.Error.Code).IsKnown() {
		return fmt.Errorf("unsupported webmcp result error code %q", e.Error.Code)
	}
	if e.Error.Details == nil {
		return fmt.Errorf("webmcp result error details must be an object")
	}
	return nil
}

// UnmarshalJSON rejects unknown or duplicate top-level members.
func (e *ToolResultEnvelope) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("cannot decode webmcp result into nil receiver")
	}
	fields, err := decodeObjectFields(data)
	if err != nil {
		return err
	}
	if err := validateEnvelopeFields(fields); err != nil {
		return err
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
		return nil
	}
	var resultError ToolResultError
	if err := unmarshalToolResultError(fields["error"], &resultError); err != nil {
		return err
	}
	e.Error = &resultError
	return nil
}

func validateEnvelopeFields(fields map[string]json.RawMessage) error {
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
	if isJSONNull(fields["version"]) || isJSONNull(fields["ok"]) {
		return fmt.Errorf("webmcp result version and ok must not be null")
	}
	return nil
}

func unmarshalToolResultError(data []byte, resultError *ToolResultError) error {
	fields, err := decodeObjectFields(data)
	if err != nil {
		return err
	}
	if err := validateResultErrorFields(fields); err != nil {
		return err
	}
	return decodeResultErrorFields(fields, resultError)
}

func validateResultErrorFields(fields map[string]json.RawMessage) error {
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
	if isJSONNull(fields["code"]) || isJSONNull(fields["message"]) || isJSONNull(fields["retryable"]) || isJSONNull(fields["details"]) {
		return fmt.Errorf("webmcp result error fields must not be null")
	}
	return nil
}

func decodeResultErrorFields(fields map[string]json.RawMessage, resultError *ToolResultError) error {
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

func isJSONNull(data []byte) bool {
	return bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

func decodeObjectFields(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := expectObjectStart(decoder); err != nil {
		return nil, err
	}
	fields, err := decodeObjectEntries(decoder)
	if err != nil {
		return nil, err
	}
	if err := expectObjectEnd(decoder); err != nil {
		return nil, err
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return nil, err
	}
	return fields, nil
}

func expectObjectStart(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return fmt.Errorf("expected JSON object")
	}
	return nil
}

func decodeObjectEntries(decoder *json.Decoder) (map[string]json.RawMessage, error) {
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
	return fields, nil
}

func expectObjectEnd(decoder *json.Decoder) error {
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	if closing != json.Delim('}') {
		return fmt.Errorf("expected end of JSON object")
	}
	return nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("more than one JSON value")
		}
		return err
	}
	return nil
}
