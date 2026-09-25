package sessionwrap

import (
	"context"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/mediagate"
	"github.com/stretchr/testify/require"
)

func TestOrderedSessionAutomaticSendAheadOfPendingControlDoesNotDeadlock(t *testing.T) {
	gate := mediagate.New(nil)
	ackID, _, err := gate.RegisterAck()
	require.NoError(t, err)
	automaticStarted := make(chan struct{})
	releaseAutomatic := make(chan struct{})
	controlSent := make(chan struct{})
	provider := &orderingSession{automaticStarted: automaticStarted, releaseAutomatic: releaseAutomatic, controlSent: controlSent}
	ordered := WrapOrderedSession(provider, OrderedSessionOptions{Media: gate})
	automaticDone := make(chan messages.SessionSendOutcome, 1)
	go func() {
		automaticDone <- ordered.SendWithOutcome(context.Background(), messages.StreamMessage{
			Type:  messages.StreamTypeTextDelta,
			Value: messages.NewTextDeltaValue("automatic"),
		})
	}()
	select {
	case <-automaticStarted:
	case <-time.After(time.Second):
		t.Fatal("automatic provider send did not start")
	}
	controlDone := make(chan messages.SessionSendOutcome, 1)
	go func() {
		controlDone <- ordered.SendWithOutcome(context.Background(), messages.StreamMessage{
			Type:            messages.StreamTypeMessageEnd,
			ActorProvidedID: ackID,
			Value:           messages.NewMessageEndValue(messages.TokenUsage{}),
		})
	}()
	select {
	case <-controlSent:
		t.Fatal("control overtook the automatic send")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseAutomatic)
	select {
	case outcome := <-automaticDone:
		require.True(t, outcome.OK())
	case <-time.After(time.Second):
		t.Fatal("automatic provider send did not finish")
	}
	select {
	case outcome := <-controlDone:
		require.True(t, outcome.OK())
	case <-time.After(time.Second):
		t.Fatal("control provider send deadlocked behind automatic send")
	}
	select {
	case <-controlSent:
	case <-time.After(time.Second):
		t.Fatal("control provider send did not run")
	}
}

type orderingSession struct{ automaticStarted, releaseAutomatic, controlSent chan struct{} }

func (s *orderingSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeMessageEnd {
		close(s.controlSent)
		return true
	}
	select {
	case <-s.automaticStarted:
	default:
		close(s.automaticStarted)
	}
	select {
	case <-s.releaseAutomatic:
		return true
	case <-ctx.Done():
		return false
	}
}
func (s *orderingSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return messages.NewTypedBuffer[messages.StreamMessage](1)
}
func (s *orderingSession) Done() <-chan struct{} { return nil }
func (s *orderingSession) Close() error          { return nil }
