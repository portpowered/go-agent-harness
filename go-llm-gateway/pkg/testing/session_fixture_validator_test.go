package testing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
)

func TestValidateSessionCapture_AcceptsValidSyntheticFixture(t *testing.T) {
	capture := validFixtureCapture(SessionFixtureProvenanceSynthetic, CapturedSessionEvent{
		Sequence:    1,
		Direction:   DirectionServerToClient,
		Type:        string(messages.StreamTypeTextDelta),
		PayloadType: SessionPayloadTypeStreamMessage,
		Payload:     json.RawMessage(`{"type":"TEXT.DELTA","value":{"type":"delta_text","content":"hello"}}`),
	})

	errs := ValidateSessionCapture("valid.session.json", capture)
	if len(errs) != 0 {
		t.Fatalf("ValidateSessionCapture returned %d errors, want 0: %v", len(errs), errs)
	}
}

func TestValidateSessionCaptureFile_AcceptsValidFixturePath(t *testing.T) {
	capture := validFixtureCapture(SessionFixtureProvenanceProviderRecorded, CapturedSessionEvent{
		Sequence:    1,
		Direction:   DirectionServerToClient,
		Type:        string(models.SessionEventSessionCreated),
		PayloadType: SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.created","session_id":"sess_sanitized"}`),
	})
	path := writeValidatorCapture(t, capture)

	errs := ValidateSessionCaptureFile(path)
	if len(errs) != 0 {
		t.Fatalf("ValidateSessionCaptureFile returned %d errors, want 0: %v", len(errs), errs)
	}
}

func TestValidateSessionCapture_FailsWhenFixtureProvenanceMissing(t *testing.T) {
	capture := validFixtureCapture("", CapturedSessionEvent{})

	errs := ValidateSessionCapture("missing.session.json", capture)
	requireValidationError(t, errs, "missing.session.json", "session.fixture_provenance", "must be present")
}

func TestValidateSessionCapture_FailsWhenFixtureProvenanceBlank(t *testing.T) {
	capture := validFixtureCapture("   ", CapturedSessionEvent{})

	errs := ValidateSessionCapture("blank.session.json", capture)
	requireValidationError(t, errs, "blank.session.json", "session.fixture_provenance", "must be present")
}

func TestValidateSessionCapture_FailsSyntheticRawAudioField(t *testing.T) {
	capture := validFixtureCapture(SessionFixtureProvenanceSynthetic, CapturedSessionEvent{
		Sequence:    1,
		Direction:   DirectionClientToServer,
		Type:        string(messages.StreamTypeAudioDelta),
		PayloadType: SessionPayloadTypeStreamMessage,
		Payload:     json.RawMessage(`{"type":"AUDIO.DELTA","value":{"type":"delta_audio","input_audio":"base64-audio"}}`),
	})

	errs := ValidateSessionCapture("audio.session.json", capture)
	requireValidationError(t, errs, "audio.session.json", "records[0].payload.value.input_audio", "raw audio")
}

func TestValidateSessionCapture_FailsCredentialLikeField(t *testing.T) {
	capture := validFixtureCapture(SessionFixtureProvenanceProviderRecorded, CapturedSessionEvent{
		Sequence:    1,
		Direction:   DirectionClientToServer,
		Type:        string(messages.StreamTypeSessionUpdate),
		PayloadType: SessionPayloadTypeStreamMessage,
		Payload:     json.RawMessage(`{"type":"SESSION.UPDATE","value":{"type":"session_update","authorization":"Bearer secret"}}`),
	})

	errs := ValidateSessionCapture("credential.session.json", capture)
	requireValidationError(t, errs, "credential.session.json", "records[0].payload.value.authorization", "credential-like")
}

func TestValidateSessionCapture_FailsCredentialLikeValue(t *testing.T) {
	capture := validFixtureCapture(SessionFixtureProvenanceProviderRecorded, CapturedSessionEvent{
		Sequence:    1,
		Direction:   DirectionClientToServer,
		Type:        "session.update",
		PayloadType: SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"session.update","session":{"instructions":"Bearer sk-live-secret"}}`),
	})

	errs := ValidateSessionCapture("credential-value.session.json", capture)
	requireValidationError(t, errs, "credential-value.session.json", "records[0].payload.session.instructions", "credential-like")
}

func TestValidateSessionCapture_FailsProviderRecordedRawAudioField(t *testing.T) {
	capture := validFixtureCapture(SessionFixtureProvenanceProviderRecorded, CapturedSessionEvent{
		Sequence:    1,
		Direction:   DirectionClientToServer,
		Type:        "input_audio_buffer.append",
		PayloadType: SessionPayloadTypeWebSocketMessage,
		Payload:     json.RawMessage(`{"type":"input_audio_buffer.append","audio":"unsanitized_audio_chunk"}`),
	})

	errs := ValidateSessionCapture("provider-audio.session.json", capture)
	requireValidationError(t, errs, "provider-audio.session.json", "records[0].payload.audio", "raw audio")
}

func TestValidateSessionCapture_FailsProviderWireEventEncodedAsStreamMessage(t *testing.T) {
	capture := validFixtureCapture(SessionFixtureProvenanceProviderRecorded, CapturedSessionEvent{
		Sequence:    1,
		Direction:   DirectionServerToClient,
		Type:        string(models.SessionEventResponseTextDelta),
		PayloadType: SessionPayloadTypeStreamMessage,
		Payload:     json.RawMessage(`{"type":"response.text.delta","delta":"hello"}`),
	})

	errs := ValidateSessionCapture("wire.session.json", capture)
	requireValidationError(t, errs, "wire.session.json", "records[0].payload_type", SessionPayloadTypeWebSocketMessage)
}

func validFixtureCapture(provenance string, records ...CapturedSessionEvent) SessionCapture {
	return SessionCapture{
		Version:  SessionCaptureVersion,
		Provider: SessionProviderMetadata{Name: "grok", Model: "grok-realtime"},
		Session: SessionMetadata{
			ID:                "sess_sanitized",
			StartedAtUTC:      time.Now().UTC().Format(time.RFC3339Nano),
			FixtureProvenance: provenance,
		},
		Records: records,
	}
}

func writeValidatorCapture(t *testing.T, capture SessionCapture) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "capture.session.json")
	data, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		t.Fatalf("marshal capture: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write capture: %v", err)
	}
	return path
}

func requireValidationError(t *testing.T, errs []SessionFixtureValidationError, file, fieldPath, reason string) {
	t.Helper()

	for _, err := range errs {
		if err.File == file && err.FieldPath == fieldPath && strings.Contains(err.Reason, reason) {
			return
		}
	}
	t.Fatalf("missing validation error for file=%q field=%q reason containing %q; got %#v", file, fieldPath, reason, errs)
}
