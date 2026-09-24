package wire

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers"
	gatewaytesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func writeSessionContractCapture(t *testing.T, outbound, inbound messages.StreamMessage) string {
	t.Helper()
	outboundPayload, err := gatewaytesting.MarshalStreamMessage(outbound)
	if err != nil {
		t.Fatal(err)
	}
	inboundPayload, err := gatewaytesting.MarshalStreamMessage(inbound)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime"},
		Session:  gatewaytesting.SessionMetadata{ID: "session-contract"},
		Records: []gatewaytesting.CapturedSessionEvent{
			{Sequence: 1, Direction: gatewaytesting.DirectionClientToServer, Type: string(outbound.Type), PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage, Payload: outboundPayload},
			{Sequence: 2, Direction: gatewaytesting.DirectionServerToClient, Type: string(inbound.Type), PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage, Payload: inboundPayload},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.capture.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type orderedSessionContractEvent struct {
	direction gatewaytesting.SessionEventDirection
	message   messages.StreamMessage
}

func writeOrderedSessionContractCapture(t *testing.T, events ...orderedSessionContractEvent) string {
	t.Helper()
	records := make([]gatewaytesting.CapturedSessionEvent, 0, len(events))
	for index, event := range events {
		payload, err := gatewaytesting.MarshalStreamMessage(event.message)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, gatewaytesting.CapturedSessionEvent{
			Sequence: index + 1, Direction: event.direction, TimestampMs: int64(index),
			Type: string(event.message.Type), PayloadType: gatewaytesting.SessionPayloadTypeStreamMessage, Payload: payload,
		})
	}
	capture, err := gatewaytesting.SealSessionCapture(gatewaytesting.SessionCapture{
		Provider: gatewaytesting.SessionProviderMetadata{Name: "openai", Model: "gpt-realtime"},
		Session:  gatewaytesting.SessionMetadata{ID: "ordered-session-contract"},
		Records:  records,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(capture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ordered-session.capture.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openSessionContract(t *testing.T, path string) messages.Session {
	t.Helper()
	inferencer, err := NewService().NewSessionInferencer(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	session, err := inferencer.ConnectSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestSessionInferencerMatchesOutboundBeforeDeliveringRecordedResponse(t *testing.T) {
	expected := messages.StreamMessage{Type: messages.StreamTypeResponseCreate}
	response := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("recorded response")}
	path := writeSessionContractCapture(t, expected, response)
	session := openSessionContract(t, path)
	if !session.Send(t.Context(), expected) {
		t.Fatal("matching outbound message was rejected")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	got, err := session.Receive().ReadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != response.Type || !reflect.DeepEqual(got.Value, response.Value) {
		t.Fatalf("recorded response = %#v, want %#v", got, response)
	}
	select {
	case <-session.Done():
	case <-ctx.Done():
		t.Fatal("replay did not finish after delivering the response")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionInferencerPreservesAlternatingEventOrder(t *testing.T) {
	firstRequest := messages.StreamMessage{Type: messages.StreamTypeResponseCreate}
	firstResponse := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("first")}
	secondRequest := messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Value: &messages.ResponseCancelValue{}}
	secondResponse := messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1, 2})}
	path := writeOrderedSessionContractCapture(t,
		orderedSessionContractEvent{direction: gatewaytesting.DirectionClientToServer, message: firstRequest},
		orderedSessionContractEvent{direction: gatewaytesting.DirectionServerToClient, message: firstResponse},
		orderedSessionContractEvent{direction: gatewaytesting.DirectionClientToServer, message: secondRequest},
		orderedSessionContractEvent{direction: gatewaytesting.DirectionServerToClient, message: secondResponse},
	)
	session := openSessionContract(t, path)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for index, exchange := range []struct {
		outbound messages.StreamMessage
		inbound  messages.StreamMessage
	}{{firstRequest, firstResponse}, {secondRequest, secondResponse}} {
		if !session.Send(ctx, exchange.outbound) {
			t.Fatalf("outbound event %d was rejected", index+1)
		}
		got, err := session.Receive().ReadContext(ctx)
		if err != nil {
			t.Fatalf("read response %d: %v", index+1, err)
		}
		if !reflect.DeepEqual(got, exchange.inbound) {
			t.Fatalf("response %d = %#v, want %#v", index+1, got, exchange.inbound)
		}
	}
	select {
	case <-session.Done():
	case <-ctx.Done():
		t.Fatal("ordered replay did not complete")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionInferencerReportsDivergenceAndIncompleteClose(t *testing.T) {
	expected := messages.StreamMessage{Type: messages.StreamTypeResponseCreate}
	response := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("recorded response")}
	path := writeSessionContractCapture(t, expected, response)

	t.Run("divergent outbound", func(t *testing.T) {
		session := openSessionContract(t, path)
		sender, ok := session.(messages.SessionSendOutcomeSender)
		if !ok {
			t.Fatalf("replay session %T does not expose send outcomes", session)
		}
		outcome := sender.SendWithOutcome(t.Context(), messages.StreamMessage{Type: messages.StreamTypeSessionClose})
		if outcome.OK() {
			t.Fatal("divergent outbound message was accepted")
		}
		if !errors.Is(outcome.Err, providers.ErrReplayMismatch) {
			t.Fatalf("divergent send error = %v, want replay mismatch", outcome.Err)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("close before required outbound", func(t *testing.T) {
		session := openSessionContract(t, path)
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-session.Done():
		case <-time.After(time.Second):
			t.Fatal("closing an incomplete replay did not terminate the session")
		}
	})

	t.Run("cancelled caller send", func(t *testing.T) {
		session := openSessionContract(t, path)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		sender, ok := session.(messages.SessionSendOutcomeSender)
		if !ok {
			t.Fatalf("replay session %T does not expose send outcomes", session)
		}
		outcome := sender.SendWithOutcome(ctx, expected)
		if outcome.Status != messages.SessionSendCancelled || !errors.Is(outcome.Err, context.Canceled) {
			t.Fatalf("cancelled send outcome = %#v", outcome)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
