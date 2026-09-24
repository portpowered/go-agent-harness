package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestCaptureInspectionVerifiesIntegrityAndReportsLegacyGuarantees(t *testing.T) {
	service := NewService()
	path := filepath.Join(t.TempDir(), "session.capture.json")
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime"},
		Session:  gatewaytesting.SessionMetadata{ID: "session-1"},
		Records:  []gatewaytesting.CapturedSessionEvent{},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeCapture := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	writeCapture(data)
	inspection, err := service.InspectCapture(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Provider != "openai" || inspection.Model != "gpt-realtime" || inspection.IntegrityWarning != "" {
		t.Fatalf("protected capture inspection = %#v", inspection)
	}

	tampered := bytes.Replace(data, []byte(`"model":"gpt-realtime"`), []byte(`"model":"tampered-model"`), 1)
	if bytes.Equal(tampered, data) {
		t.Fatal("test mutation did not change the capture")
	}
	writeCapture(tampered)
	if _, err := service.InspectCapture(context.Background(), path); !errors.Is(err, gatewaytesting.ErrSessionCaptureIntegrity) {
		t.Fatalf("tampered capture inspection = %v, want integrity failure", err)
	}

	messagePayload, err := gatewaytesting.MarshalStreamMessage(messages.StreamMessage{
		Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("legacy text"),
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal([]gatewaytesting.CapturedSessionEvent{{
		Sequence: 1, Direction: gatewaytesting.DirectionServerToClient,
		Type: string(messages.StreamTypeTextDelta), PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage,
		Payload: messagePayload,
	}})
	if err != nil {
		t.Fatal(err)
	}
	writeCapture(legacy)
	inspection, err = service.InspectCapture(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.IntegrityWarning == "" || !strings.Contains(inspection.IntegrityWarning, "integrity was unavailable") || inspection.Kind != replay.CaptureKindTurn {
		t.Fatalf("legacy capture inspection = %#v, want turn capture with reduced-integrity warning", inspection)
	}
}

func TestCaptureInspectionRejectsMalformedProtectedCaptures(t *testing.T) {
	payload, err := gatewaytesting.MarshalStreamMessage(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	if err != nil {
		t.Fatal(err)
	}
	baseCapture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime"},
		Session:  gatewaytesting.SessionMetadata{ID: "session-1"},
		Records: []gatewaytesting.CapturedSessionEvent{{
			Sequence: 1, Direction: gatewaytesting.DirectionClientToServer,
			Type: string(messages.StreamTypeResponseCreate), PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage,
			Payload: payload,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	base, err := json.Marshal(baseCapture)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func([]byte) ([]byte, error)
		cause  error
	}{
		{name: "missing version", mutate: deleteCaptureEnvelopeField("version"), cause: gatewaytesting.ErrSessionCaptureIntegrityUnavailable},
		{name: "unsupported version", mutate: setCaptureEnvelopeField("version", 99), cause: gatewaytesting.ErrSessionCaptureUnsupportedVersion},
		{name: "missing provider metadata", mutate: deleteCaptureProvider, cause: gatewaytesting.ErrSessionCaptureStructure},
		{name: "non-object provider metadata", mutate: setCaptureEnvelopeField("provider", nil), cause: gatewaytesting.ErrSessionCaptureStructure},
		{name: "invalid sequence", mutate: updateCaptureRecord(func(capture *gatewaytesting.SessionCapture) { capture.Records[0].Sequence = 0 }), cause: gatewaytesting.ErrSessionCaptureStructure},
		{name: "invalid direction", mutate: updateCaptureRecord(func(capture *gatewaytesting.SessionCapture) { capture.Records[0].Direction = "unknown" }), cause: gatewaytesting.ErrSessionCaptureStructure},
		{name: "negative timestamp", mutate: updateCaptureRecord(func(capture *gatewaytesting.SessionCapture) { capture.Records[0].TimestampMs = -1 }), cause: gatewaytesting.ErrSessionCaptureStructure},
		{name: "unsupported payload type", mutate: updateCaptureRecord(func(capture *gatewaytesting.SessionCapture) { capture.Records[0].PayloadType = "unknown" }), cause: gatewaytesting.ErrSessionCaptureStructure},
	}

	path := filepath.Join(t.TempDir(), "malformed.capture.json")
	service := NewService()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.mutate(base)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := service.InspectCapture(context.Background(), path); !errors.Is(err, tc.cause) {
				t.Fatalf("malformed capture error = %v, want %v", err, tc.cause)
			}
		})
	}
}

type captureMutation func([]byte) ([]byte, error)

func deleteCaptureEnvelopeField(field string) captureMutation {
	return func(data []byte) ([]byte, error) {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, err
		}
		delete(envelope, field)
		return json.Marshal(envelope)
	}
}

func setCaptureEnvelopeField(field string, value any) captureMutation {
	return func(data []byte) ([]byte, error) {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		envelope[field] = encoded
		return json.Marshal(envelope)
	}
}

func deleteCaptureProvider(data []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	delete(envelope, "provider")
	return json.Marshal(envelope)
}

func updateCaptureRecord(edit func(*gatewaytesting.SessionCapture)) captureMutation {
	return func(data []byte) ([]byte, error) {
		var capture gatewaytesting.SessionCapture
		if err := json.Unmarshal(data, &capture); err != nil {
			return nil, err
		}
		edit(&capture)
		return json.Marshal(capture)
	}
}
