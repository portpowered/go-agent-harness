package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestSyntheticCaptureDocumentIsAdmittedByPublicProbeContract(t *testing.T) {
	service := NewService()
	var document bytes.Buffer
	if err := service.WriteCaptureDocument(context.Background(), replay.CaptureDocumentRequest{SyntheticSessionID: "synthetic-session"}, &document); err != nil {
		t.Fatal(err)
	}
	observation, err := service.InspectProbeDocument(context.Background(), "synthetic.json", document.Bytes())
	if err != nil {
		t.Fatalf("admit exported synthetic capture: %v", err)
	}
	if observation.Provider != "probe" || observation.Model != "fixture" || observation.FixtureProvenance != gatewaytesting.SessionFixtureProvenanceSynthetic {
		t.Fatalf("synthetic probe observation = %#v", observation)
	}
}

func TestProbeDocumentRejectsCredentialBearingCaptureWithoutEchoingSecret(t *testing.T) {
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime"},
		Session:  gatewaytesting.SessionMetadata{FixtureProvenance: gatewaytesting.SessionFixtureProvenanceSynthetic},
		Records: []gatewaytesting.CapturedSessionEvent{{
			Sequence:    1,
			Direction:   gatewaytesting.DirectionClientToServer,
			Type:        "session.update",
			PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
			Payload:     json.RawMessage(`{"type":"session.update","authorization":"Bearer private-value"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewService().InspectProbeDocument(context.Background(), "credential-bearing.json", document)
	if err == nil || !strings.Contains(err.Error(), "credential-like data") {
		t.Fatalf("credential-bearing probe source error = %v", err)
	}
	if strings.Contains(err.Error(), "private-value") {
		t.Fatalf("probe source error exposed credential content: %v", err)
	}
}
