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
