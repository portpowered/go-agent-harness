package testing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestSessionCaptureIntegritySealAndLoad(t *testing.T) {
	capture := protectedTestCapture()
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "valid.session.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	loaded, err := LoadSessionCapture(path)
	if err != nil {
		t.Fatalf("LoadSessionCapture: %v", err)
	}
	if loaded.Version != SessionCaptureVersion {
		t.Fatalf("version = %d, want %d", loaded.Version, SessionCaptureVersion)
	}
	if loaded.Integrity.Algorithm != SessionCaptureIntegrityAlgorithm {
		t.Fatalf("algorithm = %q, want %q", loaded.Integrity.Algorithm, SessionCaptureIntegrityAlgorithm)
	}
	if loaded.Integrity.Coverage != SessionCaptureIntegrityCoverage {
		t.Fatalf("coverage = %q, want %q", loaded.Integrity.Coverage, SessionCaptureIntegrityCoverage)
	}
	digest, err := ComputeSessionCaptureDigest(loaded)
	if err != nil {
		t.Fatalf("ComputeSessionCaptureDigest: %v", err)
	}
	if loaded.Integrity.Digest != digest {
		t.Fatalf("digest = %q, recomputed %q", loaded.Integrity.Digest, digest)
	}
	if loaded.Integrity.Digest != strings.ToLower(loaded.Integrity.Digest) || len(loaded.Integrity.Digest) != 64 {
		t.Fatalf("digest is not lowercase SHA-256 hex: %q", loaded.Integrity.Digest)
	}
}

func TestSessionCaptureIntegrityDetectsReplayRelevantPayloadMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.session.json")
	data := marshalProtectedTestCapture(t)
	mutated := []byte(strings.Replace(string(data), "captured text", "corrupted text", 1))
	if string(mutated) == string(data) {
		t.Fatal("test mutation did not change capture bytes")
	}
	if err := os.WriteFile(path, mutated, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	_, err := LoadSessionCapture(path)
	if err == nil {
		t.Fatal("LoadSessionCapture accepted a corrupted capture")
	}
	var validationErr *SessionCaptureValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want SessionCaptureValidationError", err)
	}
	if validationErr.Classification != SessionCaptureErrorClassIntegrityChecksum {
		t.Fatalf("classification = %q, want %q", validationErr.Classification, SessionCaptureErrorClassIntegrityChecksum)
	}
	if validationErr.Path != path || validationErr.Algorithm != SessionCaptureIntegrityAlgorithm {
		t.Fatalf("validation error = %+v, want path and algorithm", validationErr)
	}
	if validationErr.Expected == "" || validationErr.Actual == "" || validationErr.Expected == validationErr.Actual {
		t.Fatalf("validation error lacks differing bounded digest details: %+v", validationErr)
	}
	if len(validationErr.Expected) > 80 || len(validationErr.Actual) > 80 {
		t.Fatalf("digest details are not bounded: %+v", validationErr)
	}
	if !errors.Is(err, ErrSessionCaptureIntegrity) {
		t.Fatalf("error = %v, want errors.Is(ErrSessionCaptureIntegrity)", err)
	}
}

func TestSessionCaptureIntegrityReportsFirstStructuralDifference(t *testing.T) {
	capture := protectedTestCapture()
	capture.Records[0].Sequence = 2
	capture.Records[1].Sequence = 2
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "structural.session.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	_, err = LoadSessionCapture(path)
	if err == nil {
		t.Fatal("LoadSessionCapture accepted invalid event ordering")
	}
	var validationErr *SessionCaptureValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want SessionCaptureValidationError", err)
	}
	if validationErr.Classification != SessionCaptureErrorClassStructure {
		t.Fatalf("classification = %q, want %q", validationErr.Classification, SessionCaptureErrorClassStructure)
	}
	if validationErr.FieldPath != "/records/1/sequence" || validationErr.RecordSequence != 2 {
		t.Fatalf("validation error = %+v, want first record sequence pointer", validationErr)
	}
	if !errors.Is(err, ErrSessionCaptureStructure) {
		t.Fatalf("error = %v, want errors.Is(ErrSessionCaptureStructure)", err)
	}
}

func TestSessionCaptureIntegrityRejectsUnprotectedInputs(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{name: "legacy envelope", data: `{"version":1,"provider":{},"session":{},"records":[]}`},
		{name: "legacy event array", data: `[{"sequence":1}]`},
		{name: "missing integrity", data: `{"version":2,"provider":{},"session":{},"records":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unprotected.session.json")
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatalf("write capture: %v", err)
			}
			_, err := LoadSessionCapture(path)
			if err == nil {
				t.Fatal("LoadSessionCapture accepted unprotected input")
			}
			if !errors.Is(err, ErrSessionCaptureIntegrityUnavailable) && tc.name != "missing integrity" {
				t.Fatalf("error = %v, want integrity-unavailable classification", err)
			}
			if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "integrity") {
				t.Fatalf("error = %v, want path and integrity classification", err)
			}
		})
	}
}

func TestSessionCaptureIntegrityRejectsTruncatedJSON(t *testing.T) {
	data := marshalProtectedTestCapture(t)
	if len(data) < 2 {
		t.Fatal("protected capture unexpectedly empty")
	}
	path := filepath.Join(t.TempDir(), "truncated.session.json")
	if err := os.WriteFile(path, data[:len(data)-1], 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	_, err := LoadSessionCapture(path)
	if err == nil {
		t.Fatal("LoadSessionCapture accepted truncated JSON")
	}
	var validationErr *SessionCaptureValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want SessionCaptureValidationError", err)
	}
	if validationErr.Classification != SessionCaptureErrorClassStructure || validationErr.FieldPath != "$" {
		t.Fatalf("validation error = %+v, want structural root error", validationErr)
	}
	if !errors.Is(err, ErrSessionCaptureStructure) {
		t.Fatalf("error = %v, want errors.Is(ErrSessionCaptureStructure)", err)
	}
}

func TestSessionCaptureIntegrityRejectsMalformedMetadata(t *testing.T) {
	data := marshalProtectedTestCapture(t)
	mutated := []byte(strings.Replace(string(data), `"algorithm": "sha256"`, `"algorithm": 7`, 1))
	if string(mutated) == string(data) {
		t.Fatal("test mutation did not change integrity metadata")
	}
	path := filepath.Join(t.TempDir(), "malformed-integrity.session.json")
	if err := os.WriteFile(path, mutated, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	_, err := LoadSessionCapture(path)
	if err == nil {
		t.Fatal("LoadSessionCapture accepted malformed integrity metadata")
	}
	var validationErr *SessionCaptureValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want SessionCaptureValidationError", err)
	}
	if validationErr.Classification != SessionCaptureErrorClassIntegrityMetadata || validationErr.FieldPath != "/integrity/algorithm" {
		t.Fatalf("validation error = %+v, want metadata algorithm error", validationErr)
	}
	if !errors.Is(err, ErrSessionCaptureIntegrity) {
		t.Fatalf("error = %v, want errors.Is(ErrSessionCaptureIntegrity)", err)
	}
}

func protectedTestCapture() SessionCapture {
	return SessionCapture{
		Version:  SessionCaptureVersion,
		Provider: SessionProviderMetadata{Name: "test", Model: "test-model"},
		Session: SessionMetadata{
			ID:                "sess-integrity-test",
			StartedAtUTC:      time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
			FixtureProvenance: SessionFixtureProvenanceSynthetic,
		},
		Records: []CapturedSessionEvent{
			{
				Sequence:    1,
				Direction:   DirectionServerToClient,
				TimestampMs: 0,
				Type:        string(messages.StreamTypeTextDelta),
				PayloadType: SessionPayloadTypeStreamMessage,
				Payload:     json.RawMessage(`{"type":"TEXT.DELTA","value":{"type":"delta_text","content":"captured text"}}`),
			},
			{
				Sequence:    2,
				Direction:   DirectionClientToServer,
				TimestampMs: 1,
				Type:        string(messages.StreamTypeResponseCreate),
				PayloadType: SessionPayloadTypeStreamMessage,
				Payload:     json.RawMessage(`{"type":"RESPONSE.CREATE","value":{"type":"response_create"}}`),
			},
		},
	}
}

func marshalProtectedTestCapture(t *testing.T) []byte {
	t.Helper()
	data, err := json.MarshalIndent(protectedTestCapture(), "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	return data
}
