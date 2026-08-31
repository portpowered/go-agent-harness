package testing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// SessionCaptureVersion is the current on-disk session capture schema version.
	SessionCaptureVersion = 2
	// SessionCaptureLegacyVersion identifies the unprotected envelope that was
	// written before capture integrity was introduced. Strict loading rejects it,
	// while the explicit replay compatibility loader accepts it with a warning.
	SessionCaptureLegacyVersion = 1
	// SessionCaptureIntegrityAlgorithm is the digest algorithm used by the
	// protected capture envelope.
	SessionCaptureIntegrityAlgorithm = "sha256"
	// SessionCaptureIntegrityCoverage is the exact set of envelope fields fed to
	// the digest serializer. The integrity object itself is intentionally absent.
	SessionCaptureIntegrityCoverage = "session_capture.v2:json(version,provider,session,records,ends_with_disconnect)"
	// SessionPayloadTypeStreamMessage identifies payloads encoded as messages.StreamMessage JSON.
	SessionPayloadTypeStreamMessage = "stream_message"
	// SessionPayloadTypeWebSocketMessage identifies raw provider WebSocket JSON messages.
	SessionPayloadTypeWebSocketMessage = "websocket_message"
)

var (
	// ErrSessionCaptureIntegrity is the common class for protected-capture
	// validation failures.
	ErrSessionCaptureIntegrity = errors.New("session capture integrity validation failed")
	// ErrSessionCaptureIntegrityUnavailable identifies legacy or otherwise
	// unprotected input that cannot be integrity-verified by strict loading.
	ErrSessionCaptureIntegrityUnavailable = errors.New("session capture integrity unavailable")
	// ErrSessionCaptureStructure identifies a malformed protected capture
	// envelope or event stream.
	ErrSessionCaptureStructure = errors.New("session capture structure invalid")
	// ErrSessionCaptureUnsupportedVersion identifies a schema version that this
	// build does not understand.
	ErrSessionCaptureUnsupportedVersion = errors.New("unsupported session capture schema version")
)

// SessionCaptureReplayLoad describes a capture that is safe to hand to the
// replay transport. Current captures are integrity-verified; legacy captures
// are structurally checked but intentionally carry a reduced guarantee because
// they were written before capture digests existed.
type SessionCaptureReplayLoad struct {
	Capture           SessionCapture
	IntegrityVerified bool
}

// IntegrityWarning returns the one-line operator warning for an unprotected
// replay capture. An empty string means the capture is integrity-verified.
func (l SessionCaptureReplayLoad) IntegrityWarning(path string) string {
	if l.IntegrityVerified {
		return ""
	}
	return fmt.Sprintf(
		"warning: session capture %s uses unprotected schema version %d; integrity was unavailable, so replay continues with reduced guarantees",
		path,
		l.Capture.Version,
	)
}

const (
	// SessionCaptureErrorClassIntegrityUnavailable is reported for legacy or
	// missing integrity metadata that cannot be verified.
	SessionCaptureErrorClassIntegrityUnavailable = "integrity_unavailable"
	// SessionCaptureErrorClassIntegrityMetadata is reported when the protected
	// envelope's integrity object is present but malformed.
	SessionCaptureErrorClassIntegrityMetadata = "integrity_metadata"
	// SessionCaptureErrorClassIntegrityChecksum is reported when the stored
	// digest does not match the protected envelope.
	SessionCaptureErrorClassIntegrityChecksum = "integrity_checksum_mismatch"
	// SessionCaptureErrorClassStructure is reported for deterministic envelope,
	// event, or payload-shape violations.
	SessionCaptureErrorClassStructure = "structure"
	// SessionCaptureErrorClassUnsupportedVersion is reported for an unknown
	// protected-envelope schema version.
	SessionCaptureErrorClassUnsupportedVersion = "unsupported_version"
)

// SessionEventDirection indicates whether an event was sent by the client or received from the server.
type SessionEventDirection string

const (
	// DirectionClientToServer marks events sent from the client to the server.
	DirectionClientToServer SessionEventDirection = "client_to_server"
	// DirectionServerToClient marks events received from the server by the client.
	DirectionServerToClient SessionEventDirection = "server_to_client"
)

// CapturedSessionEvent is a single recorded event from a bidirectional session.
type CapturedSessionEvent struct {
	// Sequence is the logical ordering of this event within the session.
	Sequence int `json:"sequence"`
	// Direction indicates whether the event was sent or received.
	Direction SessionEventDirection `json:"direction"`
	// TimestampMs is the time elapsed since session start, in milliseconds.
	TimestampMs int64 `json:"timestamp_ms"`
	// Type is the session event type (e.g. "session.created", "response.output_text.delta").
	Type string `json:"type"`
	// PayloadType describes how Payload should be decoded.
	PayloadType string `json:"payload_type"`
	// Payload is the serialized event payload.
	Payload json.RawMessage `json:"payload,omitempty"`
	// Data is kept for backwards compatibility with pre-envelope session captures.
	Data json.RawMessage `json:"data,omitempty"`
}

// SessionProviderMetadata describes the provider whose traffic was captured.
type SessionProviderMetadata struct {
	Name  string `json:"name,omitempty"`
	Model string `json:"model,omitempty"`
}

// SessionMetadata describes the captured session without storing credentials.
type SessionMetadata struct {
	ID                string `json:"id,omitempty"`
	StartedAtUTC      string `json:"started_at_utc,omitempty"`
	FixtureProvenance string `json:"fixture_provenance,omitempty"`
}

// SessionCaptureIntegrity contains the versioned digest metadata for a
// protected capture. Digest is lowercase hexadecimal SHA-256 over the exact
// coverage contract named by Coverage.
type SessionCaptureIntegrity struct {
	Algorithm string `json:"algorithm"`
	Coverage  string `json:"coverage"`
	Digest    string `json:"digest"`
}

// SessionCapture is the on-disk envelope for bidirectional session traffic.
type SessionCapture struct {
	Version   int                     `json:"version"`
	Provider  SessionProviderMetadata `json:"provider"`
	Session   SessionMetadata         `json:"session"`
	Records   []CapturedSessionEvent  `json:"records"`
	Integrity SessionCaptureIntegrity `json:"integrity"`

	// EndsWithDisconnect marks captures whose provider connection ended
	// without an explicit server session-close event. Replay connections built
	// from such captures report io.EOF once every record has been consumed so
	// mid-session disconnects replay hermetically instead of hanging.
	EndsWithDisconnect bool `json:"ends_with_disconnect,omitempty"`
}

// SessionCaptureValidationError is a typed, bounded diagnostic for a capture
// that cannot be safely replayed. FieldPath is an RFC 6901-style JSON pointer
// when a deterministic structural location is available. Expected and Actual
// are intentionally short and never contain capture payload excerpts.
type SessionCaptureValidationError struct {
	Path           string
	Classification string
	FieldPath      string
	RecordSequence int
	Algorithm      string
	Expected       string
	Actual         string
	Err            error
}

// Error returns a stable operator-facing validation diagnostic.
func (e *SessionCaptureValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	var b strings.Builder
	b.WriteString("session capture")
	if e.Path != "" {
		b.WriteByte(' ')
		b.WriteString(e.Path)
	}
	if e.Classification != "" {
		b.WriteString(": ")
		b.WriteString(e.Classification)
	}
	if e.FieldPath != "" {
		b.WriteString(" at ")
		b.WriteString(e.FieldPath)
	}
	if e.RecordSequence > 0 {
		fmt.Fprintf(&b, " (record sequence %d)", e.RecordSequence)
	}
	if e.Algorithm != "" {
		b.WriteString(" algorithm=")
		b.WriteString(e.Algorithm)
	}
	if e.Expected != "" || e.Actual != "" {
		b.WriteString(": expected ")
		b.WriteString(boundCaptureDetail(e.Expected))
		b.WriteString(", actual ")
		b.WriteString(boundCaptureDetail(e.Actual))
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap preserves stable errors.Is checks for callers that need to branch on
// integrity versus structural failures.
func (e *SessionCaptureValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// MarshalJSON seals current-version captures automatically. This keeps test
// and fixture construction safe while legacy version-1 values remain
// serializable for explicit migration tooling.
func (c SessionCapture) MarshalJSON() ([]byte, error) {
	version := c.Version
	if version == 0 {
		version = SessionCaptureVersion
	}
	c.Version = version

	var integrity *SessionCaptureIntegrity
	if version == SessionCaptureVersion {
		sealed, err := SealSessionCapture(c)
		if err != nil {
			return nil, err
		}
		c = sealed
		integrity = &c.Integrity
	} else if !isZeroSessionCaptureIntegrity(c.Integrity) {
		integrity = &c.Integrity
	}

	type sessionCaptureJSON struct {
		Version            int                      `json:"version"`
		Provider           SessionProviderMetadata  `json:"provider"`
		Session            SessionMetadata          `json:"session"`
		Records            []CapturedSessionEvent   `json:"records"`
		Integrity          *SessionCaptureIntegrity `json:"integrity,omitempty"`
		EndsWithDisconnect bool                     `json:"ends_with_disconnect,omitempty"`
	}
	return json.Marshal(sessionCaptureJSON{
		Version:            c.Version,
		Provider:           c.Provider,
		Session:            c.Session,
		Records:            c.Records,
		Integrity:          integrity,
		EndsWithDisconnect: c.EndsWithDisconnect,
	})
}

// SealSessionCapture returns a current-version capture with fresh integrity
// metadata. It is explicit so trusted fixture migration and in-memory replay
// callers can protect data without weakening path-based loading.
func SealSessionCapture(capture SessionCapture) (SessionCapture, error) {
	if capture.Version == 0 || capture.Version == SessionCaptureLegacyVersion {
		capture.Version = SessionCaptureVersion
	}
	if capture.Version != SessionCaptureVersion {
		return SessionCapture{}, newSessionCaptureValidationError(
			"",
			SessionCaptureErrorClassUnsupportedVersion,
			"/version",
			0,
			"",
			fmt.Sprintf("%d", SessionCaptureVersion),
			fmt.Sprintf("%d", capture.Version),
			ErrSessionCaptureUnsupportedVersion,
		)
	}
	if capture.Records == nil {
		capture.Records = make([]CapturedSessionEvent, 0)
	}
	digest, err := ComputeSessionCaptureDigest(capture)
	if err != nil {
		return SessionCapture{}, fmt.Errorf("compute session capture digest: %w", err)
	}
	capture.Integrity = SessionCaptureIntegrity{
		Algorithm: SessionCaptureIntegrityAlgorithm,
		Coverage:  SessionCaptureIntegrityCoverage,
		Digest:    digest,
	}
	return capture, nil
}

// ComputeSessionCaptureDigest computes the protected envelope digest without
// including Integrity itself. The serializer is a fixed-order JSON struct;
// event payload bytes remain part of the replay contract.
func ComputeSessionCaptureDigest(capture SessionCapture) (string, error) {
	coverage, err := marshalSessionCaptureCoverage(capture)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(coverage)
	return hex.EncodeToString(digest[:]), nil
}

func marshalSessionCaptureCoverage(capture SessionCapture) ([]byte, error) {
	type sessionCaptureCoverage struct {
		Version            int                     `json:"version"`
		Provider           SessionProviderMetadata `json:"provider"`
		Session            SessionMetadata         `json:"session"`
		Records            []CapturedSessionEvent  `json:"records"`
		EndsWithDisconnect bool                    `json:"ends_with_disconnect,omitempty"`
	}
	return json.Marshal(sessionCaptureCoverage{
		Version:            capture.Version,
		Provider:           capture.Provider,
		Session:            capture.Session,
		Records:            capture.Records,
		EndsWithDisconnect: capture.EndsWithDisconnect,
	})
}

func isZeroSessionCaptureIntegrity(integrity SessionCaptureIntegrity) bool {
	return integrity.Algorithm == "" && integrity.Coverage == "" && integrity.Digest == ""
}

func newSessionCaptureValidationError(path, classification, fieldPath string, sequence int, algorithm, expected, actual string, err error) *SessionCaptureValidationError {
	return &SessionCaptureValidationError{
		Path:           path,
		Classification: classification,
		FieldPath:      fieldPath,
		RecordSequence: sequence,
		Algorithm:      boundCaptureDetail(algorithm),
		Expected:       boundCaptureDetail(expected),
		Actual:         boundCaptureDetail(actual),
		Err:            err,
	}
}

func boundCaptureDetail(detail string) string {
	const maxDetail = 128
	if len(detail) <= maxDetail {
		return detail
	}
	return detail[:maxDetail-14] + "...(truncated)"
}

func captureJSONType(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "missing"
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

func validateSessionCapturePath(path string, data []byte) (SessionCapture, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' && json.Valid(trimmed) {
		return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityUnavailable, "$", 0, "", fmt.Sprintf("protected schema version %d envelope", SessionCaptureVersion), "legacy event array", ErrSessionCaptureIntegrityUnavailable)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, "$", 0, "", "JSON object", "invalid JSON", errors.Join(ErrSessionCaptureStructure, err))
	}
	if fields == nil {
		return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, "$", 0, "", "JSON object", captureJSONType(data), ErrSessionCaptureStructure)
	}

	versionRaw, ok := fields["version"]
	if !ok {
		return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityUnavailable, "/version", 0, "", "protected schema version", "missing", ErrSessionCaptureIntegrityUnavailable)
	}
	var version int
	if err := json.Unmarshal(versionRaw, &version); err != nil {
		return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, "/version", 0, "", "integer", captureJSONType(versionRaw), errors.Join(ErrSessionCaptureStructure, err))
	}
	if version == SessionCaptureLegacyVersion {
		return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityUnavailable, "/version", 0, "", fmt.Sprintf("protected schema version %d", SessionCaptureVersion), fmt.Sprintf("unprotected schema version %d", version), ErrSessionCaptureIntegrityUnavailable)
	}
	if version != SessionCaptureVersion {
		return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassUnsupportedVersion, "/version", 0, "", fmt.Sprintf("%d", SessionCaptureVersion), fmt.Sprintf("%d", version), ErrSessionCaptureUnsupportedVersion)
	}

	for _, field := range []string{"provider", "session", "records"} {
		raw, exists := fields[field]
		if !exists {
			return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, "/"+field, 0, "", "present", "missing", ErrSessionCaptureStructure)
		}
		want := "object"
		if field == "records" {
			want = "array"
		}
		if captureJSONType(raw) != want {
			return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, "/"+field, 0, "", want, captureJSONType(raw), ErrSessionCaptureStructure)
		}
	}

	integrityRaw, ok := fields["integrity"]
	if !ok || captureJSONType(integrityRaw) == "null" {
		return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity", 0, "", "object with algorithm, coverage, and digest", "missing", ErrSessionCaptureIntegrity)
	}
	if captureJSONType(integrityRaw) != "object" {
		return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity", 0, "", "object", captureJSONType(integrityRaw), ErrSessionCaptureIntegrity)
	}

	integrity, err := parseSessionCaptureIntegrity(path, integrityRaw)
	if err != nil {
		return SessionCapture{}, err
	}

	var capture SessionCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, "$", 0, "", "valid capture envelope", "unparseable", errors.Join(ErrSessionCaptureStructure, err))
	}
	capture.Integrity = integrity
	if err := validateSessionCaptureStructure(path, capture); err != nil {
		return SessionCapture{}, err
	}
	actual, err := ComputeSessionCaptureDigest(capture)
	if err != nil {
		return SessionCapture{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, "$", 0, SessionCaptureIntegrityAlgorithm, "serializable protected envelope", "serialization failed", errors.Join(ErrSessionCaptureStructure, err))
	}
	if actual != capture.Integrity.Digest {
		return SessionCapture{}, newSessionCaptureValidationError(
			path,
			SessionCaptureErrorClassIntegrityChecksum,
			"/integrity/digest",
			0,
			capture.Integrity.Algorithm,
			"stored "+capture.Integrity.Digest,
			"computed "+actual,
			ErrSessionCaptureIntegrity,
		)
	}
	return capture, nil
}

func validateSessionCaptureIntegrityMetadata(path string, raw json.RawMessage, integrity SessionCaptureIntegrity) error {
	parsed, err := parseSessionCaptureIntegrity(path, raw)
	if err != nil {
		return err
	}
	if parsed != integrity {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity", 0, "", "decoded integrity metadata", "inconsistent integrity metadata", ErrSessionCaptureIntegrity)
	}
	return nil
}

func parseSessionCaptureIntegrity(path string, raw json.RawMessage) (SessionCaptureIntegrity, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return SessionCaptureIntegrity{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity", 0, "", "object", captureJSONType(raw), ErrSessionCaptureIntegrity)
	}
	for _, field := range []string{"algorithm", "coverage", "digest"} {
		value, ok := fields[field]
		if !ok {
			return SessionCaptureIntegrity{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity/"+field, 0, "", "present", "missing", ErrSessionCaptureIntegrity)
		}
		if captureJSONType(value) != "string" {
			return SessionCaptureIntegrity{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity/"+field, 0, "", "string", captureJSONType(value), ErrSessionCaptureIntegrity)
		}
	}
	var parsed SessionCaptureIntegrity
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return SessionCaptureIntegrity{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity", 0, "", "valid integrity object", "unparseable", errors.Join(ErrSessionCaptureIntegrity, err))
	}
	if parsed.Algorithm != SessionCaptureIntegrityAlgorithm {
		return SessionCaptureIntegrity{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity/algorithm", 0, parsed.Algorithm, SessionCaptureIntegrityAlgorithm, parsed.Algorithm, ErrSessionCaptureIntegrity)
	}
	if parsed.Coverage != SessionCaptureIntegrityCoverage {
		return SessionCaptureIntegrity{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity/coverage", 0, parsed.Algorithm, SessionCaptureIntegrityCoverage, parsed.Coverage, ErrSessionCaptureIntegrity)
	}
	if len(parsed.Digest) != sha256.Size*2 || strings.ToLower(parsed.Digest) != parsed.Digest {
		return SessionCaptureIntegrity{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity/digest", 0, parsed.Algorithm, "64 lowercase hexadecimal characters", parsed.Digest, ErrSessionCaptureIntegrity)
	}
	if _, err := hex.DecodeString(parsed.Digest); err != nil {
		return SessionCaptureIntegrity{}, newSessionCaptureValidationError(path, SessionCaptureErrorClassIntegrityMetadata, "/integrity/digest", 0, parsed.Algorithm, "64 lowercase hexadecimal characters", parsed.Digest, ErrSessionCaptureIntegrity)
	}
	return parsed, nil
}

func validateSessionCaptureStructure(path string, capture SessionCapture) error {
	if capture.Version != SessionCaptureVersion {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, "/version", 0, "", fmt.Sprintf("%d", SessionCaptureVersion), fmt.Sprintf("%d", capture.Version), ErrSessionCaptureStructure)
	}
	return validateSessionCaptureRecords(path, capture)
}

func validateLegacySessionCaptureStructure(path string, capture SessionCapture) error {
	if capture.Version != SessionCaptureLegacyVersion {
		return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, "/version", 0, "", fmt.Sprintf("%d", SessionCaptureLegacyVersion), fmt.Sprintf("%d", capture.Version), ErrSessionCaptureStructure)
	}
	return validateSessionCaptureRecords(path, capture)
}

func validateSessionCaptureRecords(path string, capture SessionCapture) error {
	previousSequence := 0
	for index, record := range capture.Records {
		fieldPrefix := fmt.Sprintf("/records/%d", index)
		if record.Sequence <= 0 {
			return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/sequence", record.Sequence, "", "positive integer", fmt.Sprintf("%d", record.Sequence), ErrSessionCaptureStructure)
		}
		if record.Sequence <= previousSequence {
			return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/sequence", record.Sequence, "", fmt.Sprintf("greater than %d", previousSequence), fmt.Sprintf("%d", record.Sequence), ErrSessionCaptureStructure)
		}
		previousSequence = record.Sequence
		if record.Direction != DirectionClientToServer && record.Direction != DirectionServerToClient {
			return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/direction", record.Sequence, "", "client_to_server or server_to_client", string(record.Direction), ErrSessionCaptureStructure)
		}
		if record.TimestampMs < 0 {
			return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/timestamp_ms", record.Sequence, "", "non-negative integer", fmt.Sprintf("%d", record.TimestampMs), ErrSessionCaptureStructure)
		}
		if strings.TrimSpace(record.Type) == "" {
			return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/type", record.Sequence, "", "non-empty string", "missing", ErrSessionCaptureStructure)
		}
		if record.PayloadType != SessionPayloadTypeStreamMessage && record.PayloadType != SessionPayloadTypeWebSocketMessage {
			return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/payload_type", record.Sequence, "", SessionPayloadTypeStreamMessage+" or "+SessionPayloadTypeWebSocketMessage, record.PayloadType, ErrSessionCaptureStructure)
		}
		payload := eventPayload(record)
		if len(bytes.TrimSpace(payload)) == 0 {
			return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/payload", record.Sequence, "", "non-empty JSON value", "missing", ErrSessionCaptureStructure)
		}
		if !json.Valid(payload) {
			return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/payload", record.Sequence, "", "valid JSON value", "invalid JSON", ErrSessionCaptureStructure)
		}
		if captureJSONType(payload) == "null" {
			return newSessionCaptureValidationError(path, SessionCaptureErrorClassStructure, fieldPrefix+"/payload", record.Sequence, "", "non-null JSON value", "null", ErrSessionCaptureStructure)
		}
	}
	return nil
}
