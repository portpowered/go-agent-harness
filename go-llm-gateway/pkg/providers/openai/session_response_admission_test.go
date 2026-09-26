package openai

// This file holds unit tests of the realtime response-admission state that
// drive the observe/admit seams directly, without a running read loop.
import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
)

func TestRealtimeSession_ResponseDoneRequiresMatchingIdentity(t *testing.T) {
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-current"}}`),
	})
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"status":"completed"}}`),
	})
	active := realtimeResponseActive(session)
	if !active {
		t.Fatal("response.done without an id released the active response")
	}
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-current","conversation":"none"}}`),
	})
	active = realtimeResponseActive(session)
	if !active {
		t.Fatal("out-of-band response.done released the active response")
	}
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"resp-current","status":"completed"}}`),
	})
	active = realtimeResponseActive(session)
	if active {
		t.Fatal("matching response.done did not release the active response")
	}

	session.observeResponseCreated(models.SessionEvent{Type: models.SessionEventResponseCreated})
	session.observeResponseDone(models.SessionEvent{
		Type: models.SessionEventResponseDone,
		Data: []byte(`{"response":{"id":"unexpected"}}`),
	})
	active = realtimeResponseActive(session)
	if !active {
		t.Fatal("response.done id released response with unknown local identity")
	}
}

func TestRealtimeSession_ToolResultBufferFullDoesNotAdmitResult(t *testing.T) {
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session.observeResponseCreated(models.SessionEvent{
		Type: models.SessionEventResponseCreated,
		Data: []byte(`{"response":{"id":"resp-tool"}}`),
	})
	session.responseMu.Lock()
	session.pendingResponseIntents = make([]responseIntent, maxPendingResponseIntents)
	session.responseMu.Unlock()
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-full", "tool", "result"),
	}); outcome.Status != messages.SessionSendBufferFull {
		t.Fatalf("tool result admission = %#v, want buffer full", outcome)
	}
	session.responseMu.Lock()
	admitted := session.toolResultAdmitted
	session.responseMu.Unlock()
	if admitted {
		t.Fatal("buffer-full tool result was marked admitted")
	}
}

func TestRealtimeSession_IgnoresStaleResponseDoneForCurrentResponse(t *testing.T) {
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	session.observeResponseCreated(models.SessionEvent{Type: models.SessionEventResponseCreated, Data: []byte(`{"response":{"id":"resp-old"}}`)})
	session.observeResponseDone(models.SessionEvent{Type: models.SessionEventResponseDone, Data: []byte(`{"response":{"id":"resp-old"}}`)})
	if outcome := session.sendEvents(ctx, []models.SessionEvent{models.NewResponseCreateEvent()}); !outcome.OK() {
		t.Fatalf("current response admission: %#v", outcome)
	}
	session.observeResponseCreated(models.SessionEvent{Type: models.SessionEventResponseCreated, Data: []byte(`{"response":{"id":"resp-current"}}`)})
	session.observeResponseDone(models.SessionEvent{Type: models.SessionEventResponseDone, Data: []byte(`{"response":{"id":"resp-old"}}`)})
	active := realtimeResponseActive(session)
	if !active {
		t.Fatal("stale response.done released the current response admission")
	}
	session.observeResponseDone(models.SessionEvent{Type: models.SessionEventResponseDone, Data: []byte(`{"response":{"id":"resp-current"}}`)})
	active = realtimeResponseActive(session)
	if active {
		t.Fatal("current response.done did not release response admission")
	}
}

func TestRealtimeSession_ResponseAdmissionExcludesOutOfBandCreate(t *testing.T) {
	oob := models.SessionEvent{
		Type: models.SessionEventResponseCreate,
		Data: []byte(`{"response":{"conversation":"none"}}`),
	}
	if realtimeEventNeedsResponseAdmission(oob) {
		t.Fatal("out-of-band response.create was incorrectly serialized with default conversation")
	}
	defaultConversation := models.SessionEvent{Type: models.SessionEventResponseCreate}
	if !realtimeEventNeedsResponseAdmission(defaultConversation) {
		t.Fatal("default response.create was not admitted")
	}
}

func TestRealtimeSession_CloneSessionEventsCopiesRawData(t *testing.T) {
	original := []models.SessionEvent{{Type: models.SessionEventResponseCreate, Data: []byte(`{"response":{"conversation":"none"}}`)}}
	cloned := cloneSessionEvents(original)
	if len(cloned) != 1 || &cloned[0].Data[0] == &original[0].Data[0] {
		t.Fatal("clone retained raw event backing storage")
	}
	original[0].Data[0] = 'x'
	if cloned[0].Data[0] == 'x' {
		t.Fatal("mutating source event changed queued intent")
	}
}

func TestRealtimeSession_ResponseIntentOverflowIsExplicit(t *testing.T) {
	session := newRealtimeSession(newMockWebSocketConn(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session.observeResponseCreated(models.SessionEvent{Type: models.SessionEventResponseCreated})
	for i := 0; i < maxPendingResponseIntents; i++ {
		if outcome := session.RequestResponse(ctx); !outcome.OK() {
			t.Fatalf("pending response intent %d: %#v", i, outcome)
		}
	}
	if outcome := session.RequestResponse(ctx); outcome.Status != messages.SessionSendBufferFull {
		t.Fatalf("overflow response intent = %#v, want buffer_full", outcome)
	}
}

// realtimeResponseActive reads the provider response slot under its lock.
func realtimeResponseActive(session *realtimeSession) bool {
	session.responseMu.Lock()
	defer session.responseMu.Unlock()
	return session.responseActive
}

// Reviewer repro: the user ends a turn while a function-call response is
// active, then the tool result and its continuation arrive. The adapter sends
// one response.create and owns which request it answers, so the continuation
// purpose travels on that request as response metadata and comes back on
// response.created. The opened response -- not a runner-side FIFO that cannot
// see held, dropped or retried creates -- identifies the continuation, and the
// following ordinary user turn is not mistaken for it.
func TestRealtimeSession_ContinuationPurposeRoundTripsThroughResponseMetadata(t *testing.T) {
	conn := newMockWebSocketConn()
	session, ctx := startMockRealtimeSession(t, conn)

	conn.addServerEvent("response.created", map[string]any{"response": map[string]any{"id": "resp-call"}})
	readRealtimeMessage(t, session, ctx, "function-call response.created")
	conn.addServerEvent("response.output_item.added", map[string]any{"response_id": "resp-call",
		"item": map[string]any{"type": "function_call", "id": "item-call", "call_id": "call-1", "name": "lookup"}})
	readRealtimeMessage(t, session, ctx, "function call item")
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); !outcome.OK() {
		t.Fatalf("user end of turn: %+v", outcome)
	}
	conn.addServerEvent("response.done", map[string]any{"response": map[string]any{"id": "resp-call", "status": "completed"}})
	readRealtimeMessage(t, session, ctx, "function-call response.done")
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-1", "lookup", `{"ok":true}`)}); !outcome.OK() {
		t.Fatalf("tool result: %+v", outcome)
	}
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeResponseCreate,
		Value: messages.NewToolContinuationResponseCreateValue()}); !outcome.OK() {
		t.Fatalf("continuation: %+v", outcome)
	}

	creates := waitForResponseCreates(t, conn, 1)
	if len(creates) != 1 || realtimeResponsePurpose(creates[0]) != string(messages.ResponsePurposeToolContinuation) {
		t.Fatalf("response.create frames = %s, want exactly one carrying the continuation purpose", creates)
	}
	conn.addServerEvent("response.created", map[string]any{"response": map[string]any{"id": "resp-continuation",
		"metadata": map[string]any{realtimeResponsePurposeKey: string(messages.ResponsePurposeToolContinuation)}}})
	if got := readRealtimeMessage(t, session, ctx, "continuation response.created"); got.ResponsePurpose != messages.ResponsePurposeToolContinuation {
		t.Fatalf("continuation start = %#v, want the tool-continuation purpose", got)
	}
	conn.addServerEvent("response.done", map[string]any{"response": map[string]any{"id": "resp-continuation", "status": "completed"}})
	readRealtimeMessage(t, session, ctx, "continuation response.done")

	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); !outcome.OK() {
		t.Fatalf("next user end of turn: %+v", outcome)
	}
	creates = waitForResponseCreates(t, conn, 2)
	if purpose := realtimeResponsePurpose(creates[1]); purpose != "" {
		t.Fatalf("user-turn response.create carries purpose %q, want none", purpose)
	}
	conn.addServerEvent("response.created", map[string]any{"response": map[string]any{"id": "resp-user"}})
	if got := readRealtimeMessage(t, session, ctx, "user-turn response.created"); got.ResponsePurpose != "" {
		t.Fatalf("user-turn start = %#v, want no purpose", got)
	}
}

func waitForResponseCreates(t *testing.T, conn *mockWebSocketConn, want int) [][]byte {
	t.Helper()
	timer := time.NewTimer(realtimeTestSafetyTimeout)
	defer timer.Stop()
	for {
		var creates [][]byte
		for _, payload := range conn.getClientMessages() {
			if firstStringField(payload, "type") == wireResponseCreate {
				creates = append(creates, payload)
			}
		}
		if len(creates) >= want {
			return creates
		}
		select {
		case <-conn.clientWriteCh:
		case <-timer.C:
			t.Fatalf("timed out waiting for %d response.create frames, got %d", want, len(creates))
		}
	}
}

func realtimeResponsePurpose(payload []byte) string {
	return firstStringField(payload, "response.metadata."+realtimeResponsePurposeKey)
}

// A tool result and its continuation that are queued behind an active
// response are not work of that response: the user interrupting it must not
// drop them, or the tool obligation is never resolved (and the runner waits
// for a continuation that never opens).
func TestRealtimeSession_CancelKeepsQueuedToolResultAndContinuation(t *testing.T) {
	conn := newMockWebSocketConn()
	session, ctx := startMockRealtimeSession(t, conn)
	conn.addServerEvent("response.created", map[string]any{"response": map[string]any{"id": "resp-vad"}})
	readRealtimeMessage(t, session, ctx, "server-VAD response.created")
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-1", "lookup", `{"ok":true}`)}); !outcome.OK() {
		t.Fatalf("tool result: %+v", outcome)
	}
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeResponseCreate,
		Value: messages.NewToolContinuationResponseCreateValue()}); !outcome.OK() {
		t.Fatalf("continuation: %+v", outcome)
	}
	if outcome := session.SendWithOutcome(ctx, messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue()}); !outcome.OK() {
		t.Fatalf("cancel: %+v", outcome)
	}
	conn.addServerEvent("response.done", map[string]any{"response": map[string]any{"id": "resp-vad", "status": "cancelled"}})
	readRealtimeMessage(t, session, ctx, "cancelled response.done")
	creates := waitForResponseCreates(t, conn, 1)
	if realtimeResponsePurpose(creates[0]) != string(messages.ResponsePurposeToolContinuation) {
		t.Fatalf("response.create after the cancel = %s, want the tool continuation", creates[0])
	}
	var sawResult bool
	for _, payload := range conn.getClientMessages() {
		sawResult = sawResult || firstStringField(payload, "item.type") == realtimeFunctionCallOutputType
	}
	if !sawResult {
		t.Fatal("the queued tool result was dropped by the cancel")
	}
}
