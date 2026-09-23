package capture

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const (
	captureJSONObject = "object"
	captureJSONNull   = "null"
)

type ReplayLoad struct {
	Capture           gatewaytesting.SessionCapture
	IntegrityVerified bool
}

func (l ReplayLoad) IntegrityWarning(path string) string {
	if l.IntegrityVerified {
		return ""
	}
	return fmt.Sprintf(
		"warning: session capture %s uses unprotected schema version %d; integrity was unavailable, so replay continues with reduced guarantees",
		path,
		l.Capture.Version,
	)
}

func LoadReplayCapture(ctx context.Context, path string) (ReplayLoad, error) {
	if err := replayContextError(ctx); err != nil {
		return ReplayLoad{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReplayLoad{}, fmt.Errorf("read session capture file: %w", err)
	}
	loaded, err := DecodeReplayCapture(path, data)
	if err != nil {
		return ReplayLoad{}, err
	}
	if err := replayContextError(ctx); err != nil {
		return ReplayLoad{}, err
	}
	return loaded, nil
}

func replayContextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("replay capture admission requires a context")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return nil
}

func DecodeReplayCapture(path string, data []byte) (ReplayLoad, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' && json.Valid(trimmed) {
		return decodeLegacyReplayCapture(path, trimmed)
	}
	return decodeReplayCaptureEnvelope(path, data)
}

func decodeLegacyReplayCapture(path string, data []byte) (ReplayLoad, error) {
	var records []gatewaytesting.CapturedSessionEvent
	if err := json.Unmarshal(data, &records); err != nil {
		return ReplayLoad{}, captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, "$", 0, "JSON event array", "invalid JSON", errors.Join(gatewaytesting.ErrSessionCaptureStructure, err))
	}
	capture := gatewaytesting.SessionCapture{Version: gatewaytesting.SessionCaptureLegacyVersion, Records: records}
	if err := validateReplayCaptureRecords(path, capture); err != nil {
		return ReplayLoad{}, err
	}
	return ReplayLoad{Capture: capture}, nil
}

func decodeReplayCaptureEnvelope(path string, data []byte) (ReplayLoad, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return ReplayLoad{}, captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, "$", 0, "JSON object", "invalid JSON", errors.Join(gatewaytesting.ErrSessionCaptureStructure, err))
	}
	if fields == nil {
		return ReplayLoad{}, captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, "$", 0, "JSON object", captureJSONType(data), gatewaytesting.ErrSessionCaptureStructure)
	}
	versionRaw, ok := fields["version"]
	if !ok {
		return ReplayLoad{}, captureValidationError(path, gatewaytesting.SessionCaptureErrorClassIntegrityUnavailable, "/version", 0, "protected schema version", "missing", gatewaytesting.ErrSessionCaptureIntegrityUnavailable)
	}
	var version int
	if err := json.Unmarshal(versionRaw, &version); err != nil {
		return ReplayLoad{}, captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, "/version", 0, "integer", captureJSONType(versionRaw), errors.Join(gatewaytesting.ErrSessionCaptureStructure, err))
	}
	var capture gatewaytesting.SessionCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		return ReplayLoad{}, captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, "$", 0, "valid capture envelope", "unparseable", errors.Join(gatewaytesting.ErrSessionCaptureStructure, err))
	}
	if version == gatewaytesting.SessionCaptureLegacyVersion {
		if err := validateReplayCaptureRecords(path, capture); err != nil {
			return ReplayLoad{}, err
		}
		return ReplayLoad{Capture: capture}, nil
	}
	if version != gatewaytesting.SessionCaptureVersion {
		return ReplayLoad{}, captureValidationError(path, gatewaytesting.SessionCaptureErrorClassUnsupportedVersion, "/version", 0, "supported protected version", fmt.Sprintf("%d", version), gatewaytesting.ErrSessionCaptureUnsupportedVersion)
	}
	if err := validateProtectedCaptureFields(path, fields, capture); err != nil {
		return ReplayLoad{}, err
	}
	if err := validateReplayCaptureRecords(path, capture); err != nil {
		return ReplayLoad{}, err
	}
	if err := validateReplayCaptureDigest(path, capture); err != nil {
		return ReplayLoad{}, err
	}
	return ReplayLoad{Capture: capture, IntegrityVerified: true}, nil
}

func validateReplayCaptureDigest(path string, capture gatewaytesting.SessionCapture) error {
	actual, err := gatewaytesting.ComputeSessionCaptureDigest(capture)
	if err != nil {
		return &gatewaytesting.SessionCaptureValidationError{
			Path:           path,
			Classification: gatewaytesting.SessionCaptureErrorClassStructure,
			FieldPath:      "$",
			Algorithm:      gatewaytesting.SessionCaptureIntegrityAlgorithm,
			Expected:       "serializable protected envelope",
			Actual:         "serialization failed",
			Err:            errors.Join(gatewaytesting.ErrSessionCaptureStructure, err),
		}
	}
	if actual == capture.Integrity.Digest {
		return nil
	}
	return &gatewaytesting.SessionCaptureValidationError{
		Path:           path,
		Classification: gatewaytesting.SessionCaptureErrorClassIntegrityChecksum,
		FieldPath:      "/integrity/digest",
		Algorithm:      capture.Integrity.Algorithm,
		Expected:       "stored " + capture.Integrity.Digest,
		Actual:         "computed " + actual,
		Err:            gatewaytesting.ErrSessionCaptureIntegrity,
	}
}

func validateProtectedCaptureFields(path string, fields map[string]json.RawMessage, capture gatewaytesting.SessionCapture) error {
	if err := validateCaptureEnvelopeFields(path, fields); err != nil {
		return err
	}
	return validateCaptureIntegrityFields(path, fields, capture)
}

func validateCaptureEnvelopeFields(path string, fields map[string]json.RawMessage) error {
	for _, field := range []string{"provider", "session", "records"} {
		raw, ok := fields[field]
		if !ok {
			return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, "/"+field, 0, "present", "missing", gatewaytesting.ErrSessionCaptureStructure)
		}
		want := captureJSONObject
		if field == "records" {
			want = "array"
		}
		if captureJSONType(raw) != want {
			return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, "/"+field, 0, want, captureJSONType(raw), gatewaytesting.ErrSessionCaptureStructure)
		}
	}
	return nil
}

func validateCaptureIntegrityFields(path string, fields map[string]json.RawMessage, capture gatewaytesting.SessionCapture) error {
	raw, ok := fields["integrity"]
	if !ok || captureJSONType(raw) == "null" {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassIntegrityMetadata, "/integrity", 0, "object with algorithm, coverage, and digest", "missing", gatewaytesting.ErrSessionCaptureIntegrity)
	}
	if captureJSONType(raw) != captureJSONObject {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassIntegrityMetadata, "/integrity", 0, captureJSONObject, captureJSONType(raw), gatewaytesting.ErrSessionCaptureIntegrity)
	}
	var integrityFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &integrityFields); err != nil || integrityFields == nil {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassIntegrityMetadata, "/integrity", 0, captureJSONObject, captureJSONType(raw), gatewaytesting.ErrSessionCaptureIntegrity)
	}
	for _, field := range []string{"algorithm", "coverage", "digest"} {
		value, ok := integrityFields[field]
		if !ok || captureJSONType(value) != "string" {
			return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassIntegrityMetadata, "/integrity/"+field, 0, "string field", captureJSONType(value), gatewaytesting.ErrSessionCaptureIntegrity)
		}
	}
	if capture.Integrity.Algorithm != gatewaytesting.SessionCaptureIntegrityAlgorithm {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassIntegrityMetadata, "/integrity/algorithm", 0, gatewaytesting.SessionCaptureIntegrityAlgorithm, capture.Integrity.Algorithm, gatewaytesting.ErrSessionCaptureIntegrity)
	}
	if capture.Integrity.Coverage != gatewaytesting.SessionCaptureIntegrityCoverage {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassIntegrityMetadata, "/integrity/coverage", 0, gatewaytesting.SessionCaptureIntegrityCoverage, capture.Integrity.Coverage, gatewaytesting.ErrSessionCaptureIntegrity)
	}
	if len(capture.Integrity.Digest) != 64 || strings.ToLower(capture.Integrity.Digest) != capture.Integrity.Digest {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassIntegrityMetadata, "/integrity/digest", 0, "64 lowercase hexadecimal characters", capture.Integrity.Digest, gatewaytesting.ErrSessionCaptureIntegrity)
	}
	if _, err := hex.DecodeString(capture.Integrity.Digest); err != nil {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassIntegrityMetadata, "/integrity/digest", 0, "64 lowercase hexadecimal characters", capture.Integrity.Digest, gatewaytesting.ErrSessionCaptureIntegrity)
	}
	return nil
}

func validateReplayCaptureRecords(path string, capture gatewaytesting.SessionCapture) error {
	previousSequence := 0
	for index, record := range capture.Records {
		if err := validateReplayCaptureRecord(path, index, previousSequence, record); err != nil {
			return err
		}
		previousSequence = record.Sequence
	}
	return nil
}

func validateReplayCaptureRecord(path string, index, previousSequence int, record gatewaytesting.CapturedSessionEvent) error {
	field := fmt.Sprintf("/records/%d", index)
	if record.Sequence <= 0 || record.Sequence <= previousSequence {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, field+"/sequence", record.Sequence, "positive, increasing sequence", fmt.Sprintf("%d", record.Sequence), gatewaytesting.ErrSessionCaptureStructure)
	}
	if record.Direction != gatewaytesting.DirectionClientToServer && record.Direction != gatewaytesting.DirectionServerToClient {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, field+"/direction", record.Sequence, "client_to_server or server_to_client", string(record.Direction), gatewaytesting.ErrSessionCaptureStructure)
	}
	if record.TimestampMs < 0 {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, field+"/timestamp_ms", record.Sequence, "non-negative integer", fmt.Sprintf("%d", record.TimestampMs), gatewaytesting.ErrSessionCaptureStructure)
	}
	if strings.TrimSpace(record.Type) == "" {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, field+"/type", record.Sequence, "non-empty string", "missing", gatewaytesting.ErrSessionCaptureStructure)
	}
	if record.PayloadType != gatewaytesting.SessionPayloadTypeStreamMessage && record.PayloadType != gatewaytesting.SessionPayloadTypeWebSocketMessage {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, field+"/payload_type", record.Sequence, gatewaytesting.SessionPayloadTypeStreamMessage+" or "+gatewaytesting.SessionPayloadTypeWebSocketMessage, record.PayloadType, gatewaytesting.ErrSessionCaptureStructure)
	}
	payload := record.Payload
	if len(payload) == 0 {
		payload = record.Data
	}
	if len(bytes.TrimSpace(payload)) == 0 || !json.Valid(payload) || captureJSONType(payload) == captureJSONNull {
		return captureValidationError(path, gatewaytesting.SessionCaptureErrorClassStructure, field+"/payload", record.Sequence, "non-null JSON value", "missing or invalid", gatewaytesting.ErrSessionCaptureStructure)
	}
	return nil
}

func captureValidationError(path, classification, field string, sequence int, expected, actual string, err error) *gatewaytesting.SessionCaptureValidationError {
	return &gatewaytesting.SessionCaptureValidationError{
		Path:           path,
		Classification: classification,
		FieldPath:      field,
		RecordSequence: sequence,
		Expected:       expected,
		Actual:         actual,
		Err:            err,
	}
}

func captureJSONType(raw []byte) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "missing"
	}
	switch trimmed[0] {
	case '{':
		return captureJSONObject
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return captureJSONNull
	default:
		return "number"
	}
}
