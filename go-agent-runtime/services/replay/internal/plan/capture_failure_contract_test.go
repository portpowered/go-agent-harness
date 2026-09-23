package plan

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/replay"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestInspectRealtimeCaptureRejectsInvalidProviderEvents(t *testing.T) {
	tests := []struct {
		name    string
		event   gatewaytesting.CapturedSessionEvent
		wantErr string
	}{
		{
			name: "invalid direction",
			event: gatewaytesting.CapturedSessionEvent{
				Direction:   "provider_to_server",
				Type:        "session.updated",
				PayloadType: gatewaytesting.SessionPayloadTypeWebSocketMessage,
				Payload:     json.RawMessage(`{"type":"session.updated"}`),
			},
			wantErr: "expected client_to_server or server_to_client",
		},
		{
			name: "unsupported payload type",
			event: gatewaytesting.CapturedSessionEvent{
				Direction:   gatewaytesting.DirectionServerToClient,
				Type:        "session.updated",
				PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage,
				Payload:     json.RawMessage(`{"type":"session.updated"}`),
			},
			wantErr: `expected "websocket_message"`,
		},
	}
	var service replay.Service = New()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writePlanCapture(t, clientRecord("session.update", `{"type":"session.update","session":{}}`), test.event)
			if _, err := service.InspectCapture(t.Context(), path); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("InspectCapture error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestCaptureReplayRejectsUnsupportedMessagePayload(t *testing.T) {
	event := streamServerRecord(t, 1, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("must not arrive")})
	event.PayloadType = gatewaytesting.SessionPayloadTypeWebSocketMessage
	var service replay.Service = New()
	result, err := service.Replay(t.Context(), writeStreamEventsCapture(t, event))
	if err != nil {
		t.Fatal(err)
	}
	closeReplayTestResource(t, result, "replay")
	delivered := 0
	err = result.Drain(t.Context(), func(messages.StreamMessage) error {
		delivered++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported payload type") || delivered != 0 {
		t.Fatalf("Drain delivered %d messages, error = %v; want unsupported payload rejected before delivery", delivered, err)
	}
}

func TestSessionInferencerPreservesSendDeadlineOutcome(t *testing.T) {
	path := writeStreamReplayCapture(t, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("recorded")})
	var service replay.Service = New()
	inferencer, err := service.NewSessionInferencer(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := inferencer.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	closeReplayTestResource(t, session, "replay session")
	sendCtx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	outcome := messages.SendSessionWithOutcome(sendCtx, session, messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	if outcome.Status != messages.SessionSendTimedOut || !errors.Is(outcome.Err, context.DeadlineExceeded) {
		t.Fatalf("expired replay send outcome = %+v, want timeout with deadline exceeded", outcome)
	}
}
