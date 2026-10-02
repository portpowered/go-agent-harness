package browser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
)

// NewToolResultSuccess builds a success envelope with one non-null JSON value
// as data.
func NewToolResultSuccess(data any) (public.ToolResultEnvelope, error) {
	raw, err := marshalOneJSONValue(data, false)
	if err != nil {
		return public.ToolResultEnvelope{}, err
	}
	return public.ToolResultEnvelope{
		Version: public.ToolResultVersion,
		OK:      true,
		Data:    raw,
		Error:   nil,
	}, nil
}

// NewToolResultFailure builds a failure envelope with null data and a
// normalized details object.
func NewToolResultFailure(resultError public.ToolResultError) public.ToolResultEnvelope {
	if resultError.Details == nil {
		resultError.Details = map[string]any{}
	}
	return public.ToolResultEnvelope{
		Version: public.ToolResultVersion,
		OK:      false,
		Data:    json.RawMessage("null"),
		Error:   &resultError,
	}
}

// EncodeToolResult is the common success/failure serializer.
func EncodeToolResult(data any, resultError *public.ToolResultError) ([]byte, error) {
	if resultError != nil {
		return MarshalToolResult(NewToolResultFailure(*resultError))
	}
	success, err := NewToolResultSuccess(data)
	if err != nil {
		return nil, err
	}
	return MarshalToolResult(success)
}

// MarshalToolResult validates and emits exactly one compact JSON object.
func MarshalToolResult(envelope public.ToolResultEnvelope) ([]byte, error) {
	if err := envelope.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}

// UnmarshalToolResult strictly decodes a C0 envelope. Unknown members and
// unknown versions are rejected rather than guessed.
func UnmarshalToolResult(data []byte) (public.ToolResultEnvelope, error) {
	var envelope public.ToolResultEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return public.ToolResultEnvelope{}, err
	}
	if err := envelope.Validate(); err != nil {
		return public.ToolResultEnvelope{}, err
	}
	return envelope, nil
}

func marshalOneJSONValue(value any, allowNull bool) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var valueRaw json.RawMessage
	if err := decoder.Decode(&valueRaw); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("more than one JSON value")
		}
		return nil, err
	}
	trimmed := bytes.TrimSpace(valueRaw)
	if !allowNull && bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("JSON null is not allowed here")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, err
	}
	return json.RawMessage(compact.Bytes()), nil
}
