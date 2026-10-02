package participants

import (
	"context"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/participants/internal/sessionstate"
)

// A progress acknowledgement that arrives while a response is speaking is
// dropped, never replayed. Dropping it must still release its pending tool
// boundary, or later inference requests wait on a boundary that never comes.
func TestModelRunner_DroppedAcknowledgementReleasesPendingToolBoundary(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()

	if err := runner.EnqueueSessionInput(ctx, SessionEvent(messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewToolAcknowledgementResponseCreateValue(),
	}), SessionAdmitOrFail); err != nil {
		t.Fatalf("queue acknowledgement: %v", err)
	}
	input := <-runner.ingress.ordered

	state := &sessionRunState{}
	state.Response.Start("resp-speaking")
	runner.forwardQueuedSessionEvent(ctx, session, state, input.event)

	if sent := session.sentMessages(); len(sent) != 0 {
		t.Fatalf("acknowledgement over a live response reached the provider: %#v", sent)
	}
	if len(state.Deferred) != 0 {
		t.Fatalf("dropped acknowledgement was deferred: %#v", state.Deferred)
	}
	if runner.hasPendingSessionToolEvents() {
		t.Fatal("dropped acknowledgement left its tool boundary pending")
	}
}

// A continuation held behind an outstanding acknowledgement replays later, so
// its tool boundary stays pending until the replay forwards it.
func TestModelRunner_HeldContinuationStaysPendingUntilReplayed(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()

	if err := runner.EnqueueSessionInput(ctx, SessionEvent(messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	}), SessionAdmitOrFail); err != nil {
		t.Fatalf("queue continuation: %v", err)
	}
	input := <-runner.ingress.ordered

	state := &sessionRunState{}
	state.Ack.Request()
	runner.forwardQueuedSessionEvent(ctx, session, state, input.event)
	if !runner.hasPendingSessionToolEvents() {
		t.Fatal("held continuation released its tool boundary before it was forwarded")
	}

	state.Ack.Phase = sessionstate.AcknowledgementNone
	runner.flushDeferredSessionEvents(ctx, session, state)
	if runner.hasPendingSessionToolEvents() {
		t.Fatal("replayed continuation left its tool boundary pending")
	}
	if sent := session.sentMessages(); len(sent) != 1 || sent[0].Type != messages.StreamTypeResponseCreate {
		t.Fatalf("replayed continuation sends = %#v, want one RESPONSE.CREATE", sent)
	}
}
