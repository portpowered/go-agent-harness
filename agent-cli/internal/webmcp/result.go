package webmcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var (
	// ErrUnknownEnvelopeVersion identifies a result that cannot safely be
	// decoded as the frozen v1 envelope.
	ErrUnknownEnvelopeVersion = errors.New("unknown WebMCP tool-result envelope version")
	// ErrInvalidToolResultEnvelope identifies a malformed or contradictory
	// success/error envelope.
	ErrInvalidToolResultEnvelope = errors.New("invalid WebMCP tool-result envelope")
)

// ToolResultEnvelope is the exact textual JSON object placed in
// ToolCallResponse.Content. Data and error are mutually exclusive according to
// the ok value; page output remains a JSON value inside data.
type ToolResultEnvelope struct {
	Version string          `json:"version"`
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data"`
	Error   *BrokerError    `json:"error"`
}

// ResultEnvelope is a short compatibility name for the same wire shape.
type ResultEnvelope = ToolResultEnvelope

// NewToolSuccessEnvelope creates a v1 success envelope from one non-null JSON
// data value. The bytes are copied so callers may reuse their input buffer.
func NewToolSuccessEnvelope(data json.RawMessage) (ToolResultEnvelope, error) {
	if err := validateNonNullJSON(data); err != nil {
		return ToolResultEnvelope{}, fmt.Errorf("%w: success data: %v", ErrInvalidToolResultEnvelope, err)
	}
	return ToolResultEnvelope{
		Version: ToolResultVersion,
		OK:      true,
		Data:    cloneJSON(data),
	}, nil
}

// NewToolErrorEnvelope creates a v1 failed envelope. Error validation is
// repeated by MarshalJSON so literals and decoded values obey the same rules.
func NewToolErrorEnvelope(err BrokerError) ToolResultEnvelope {
	if len(bytes.TrimSpace(err.Details)) == 0 {
		err.Details = json.RawMessage(`{}`)
	}
	err.Details = cloneJSON(err.Details)
	return ToolResultEnvelope{
		Version: ToolResultVersion,
		Error:   &err,
	}
}

// MarshalJSON enforces the four-member v1 contract and prevents a producer
// from emitting a contradictory ok/data/error combination.
func (e ToolResultEnvelope) MarshalJSON() ([]byte, error) {
	version := e.Version
	if version == "" {
		version = ToolResultVersion
	}
	if version != ToolResultVersion {
		return nil, fmt.Errorf("%w: %q", ErrUnknownEnvelopeVersion, version)
	}
	data := bytes.TrimSpace(e.Data)
	if !e.OK && len(data) == 0 {
		data = []byte("null")
	}
	if e.OK {
		if err := validateNonNullJSON(data); err != nil {
			return nil, fmt.Errorf("%w: success data: %v", ErrInvalidToolResultEnvelope, err)
		}
		if e.Error != nil {
			return nil, fmt.Errorf("%w: successful envelope has an error", ErrInvalidToolResultEnvelope)
		}
	} else {
		if len(data) == 0 || !bytes.Equal(data, []byte("null")) {
			return nil, fmt.Errorf("%w: failed envelope data must be null", ErrInvalidToolResultEnvelope)
		}
		if e.Error == nil {
			return nil, fmt.Errorf("%w: failed envelope requires an error", ErrInvalidToolResultEnvelope)
		}
		data = []byte("null")
	}

	type wireEnvelope struct {
		Version string          `json:"version"`
		OK      bool            `json:"ok"`
		Data    json.RawMessage `json:"data"`
		Error   *BrokerError    `json:"error"`
	}
	return json.Marshal(wireEnvelope{
		Version: version,
		OK:      e.OK,
		Data:    cloneJSON(data),
		Error:   e.Error,
	})
}

// UnmarshalJSON accepts only the known v1 envelope and rejects unknown
// top-level fields before any broker operation can consume the value.
func (e *ToolResultEnvelope) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("cannot decode WebMCP envelope into nil receiver")
	}
	type wireEnvelope struct {
		Version *string         `json:"version"`
		OK      *bool           `json:"ok"`
		Data    json.RawMessage `json:"data"`
		Error   json.RawMessage `json:"error"`
	}
	var wire wireEnvelope
	if err := decodeStrictJSON(data, &wire); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidToolResultEnvelope, err)
	}
	if wire.Version == nil || wire.OK == nil || len(bytes.TrimSpace(wire.Data)) == 0 || len(bytes.TrimSpace(wire.Error)) == 0 {
		return fmt.Errorf("%w: version, ok, data, and error are required", ErrInvalidToolResultEnvelope)
	}
	if *wire.Version != ToolResultVersion {
		return fmt.Errorf("%w: %q", ErrUnknownEnvelopeVersion, *wire.Version)
	}

	dataValue := bytes.TrimSpace(wire.Data)
	errorValue := bytes.TrimSpace(wire.Error)
	var brokerError *BrokerError
	if !bytes.Equal(errorValue, []byte("null")) {
		var decoded BrokerError
		if err := json.Unmarshal(errorValue, &decoded); err != nil {
			return fmt.Errorf("%w: error: %v", ErrInvalidToolResultEnvelope, err)
		}
		brokerError = &decoded
	}

	if *wire.OK {
		if err := validateNonNullJSON(dataValue); err != nil {
			return fmt.Errorf("%w: success data: %v", ErrInvalidToolResultEnvelope, err)
		}
		if brokerError != nil {
			return fmt.Errorf("%w: successful envelope has an error", ErrInvalidToolResultEnvelope)
		}
	} else {
		if !bytes.Equal(dataValue, []byte("null")) {
			return fmt.Errorf("%w: failed envelope data must be null", ErrInvalidToolResultEnvelope)
		}
		if brokerError == nil {
			return fmt.Errorf("%w: failed envelope requires an error", ErrInvalidToolResultEnvelope)
		}
	}

	*e = ToolResultEnvelope{
		Version: *wire.Version,
		OK:      *wire.OK,
		Data:    cloneJSON(wire.Data),
		Error:   brokerError,
	}
	return nil
}

func validateNonNullJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return fmt.Errorf("data must be one valid JSON value")
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("data must not be null")
	}
	return nil
}

func cloneJSON(data []byte) json.RawMessage {
	if data == nil {
		return nil
	}
	return append(json.RawMessage(nil), data...)
}

func decodeStrictJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
