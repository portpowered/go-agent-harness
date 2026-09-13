package externalconsumer

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	roomswire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms/wire"
)

type session struct {
	mu         sync.Mutex
	receive    *messages.TypedBuffer[messages.StreamMessage]
	done       chan struct{}
	doneOnce   sync.Once
	closeCalls int
	sends      []messages.StreamMessage
}

func newSession() *session {
	return &session{receive: messages.NewTypedBuffer[messages.StreamMessage](4), done: make(chan struct{})}
}

func (s *session) Send(ctx context.Context, message messages.StreamMessage) bool {
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	s.sends = append(s.sends, message)
	s.mu.Unlock()
	return true
}
func (s *session) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *session) Done() <-chan struct{} { return s.done }
func (s *session) Close() error {
	s.mu.Lock()
	s.closeCalls++
	s.mu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
	return nil
}
func (s *session) RequestResponse(context.Context) messages.SessionSendOutcome {
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}
func (s *session) SupportsResponseRequests() bool { return true }
func (s *session) SendMessage(context.Context, messages.Message) bool { return true }
func (s *session) SendMessageWithoutResponse(context.Context, messages.Message) bool { return true }
func (s *session) SupportsCompleteMessages() bool { return true }
func (s *session) SupportsCompleteMessagesWithoutResponse() bool { return true }
func (s *session) TerminalError() error { return nil }

type inferencer struct {
	session messages.Session
	err     error
}

func (i inferencer) ConnectSession(context.Context) (messages.Session, error) { return i.session, i.err }

func TestExternalConsumerOwnsParticipantLifecycle(t *testing.T) {
	admissionClosed := make(chan struct{})
	participant := roomswire.NewParticipantLifecycle(rooms.ParticipantLifecycleOptions{AdmissionClosed: admissionClosed})
	underlying := newSession()
	tracked := roomswire.NewTrackedSession(underlying, participant, admissionClosed)
	participant.SetOwnedSession(tracked)
	participant.MarkConnected(nil)
	participant.MarkSessionCreated()
	participant.MarkDeviceReady()
	participant.Observe(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
	participant.Observe(messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ToolCallId: "call", Value: messages.NewToolCallStartValue("call", "lookup")})
	close(admissionClosed)
	participant.MarkCoordinatorStopping(true, rooms.RoomTerminationMaxTurnsReached)
	if !tracked.SessionAdmissionAllowsCompleteMessage(messages.Message{ToolCallID: "call"}) {
		t.Fatal("public lifecycle rejected a pending complete tool result")
	}
	if !tracked.SendMessage(context.Background(), messages.Message{ToolCallID: "call"}) {
		t.Fatal("public lifecycle rejected an admitted tool result")
	}
	participant.MarkBoundCancellation()
	participant.CancelActiveResponse()
	if participant.ObserveTerminal(rooms.SessionTerminalObservation{TerminalReason: string(messages.TerminalReasonProviderAuthoredCompletion), OutputState: string(messages.TerminalOutputComplete)}) {
		t.Fatal("late terminal crossed the cancellation boundary")
	}
	if got := participant.TerminalObservationSnapshot(); got.Classification != rooms.RoomBoundCancelledClassification || got.TerminationDisposition != "cancelled_after_grace" {
		t.Fatalf("terminal snapshot = %+v", got)
	}
	if err := participant.CloseOwnedSession(); err != nil {
		t.Fatal(err)
	}
	if err := participant.CloseOwnedSession(); err != nil {
		t.Fatal(err)
	}
	underlying.mu.Lock()
	closeCalls := underlying.closeCalls
	underlying.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("underlying close calls = %d, want one", closeCalls)
	}
}

func TestExternalConsumerConnectionTrackerPreservesTypedFailure(t *testing.T) {
	connectErr := errors.New("connect failed")
	underlying := newSession()
	tracker := roomswire.NewConnectionTracker(inferencer{session: underlying, err: connectErr}, nil, nil)
	if _, err := tracker.ConnectSession(context.Background()); !errors.Is(err, connectErr) {
		t.Fatalf("connection error = %v", err)
	}
	if _, ready := tracker.Outcome(); !ready {
		t.Fatal("public connection tracker did not publish failure")
	}
	underlying.mu.Lock()
	closeCalls := underlying.closeCalls
	underlying.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("error session close calls = %d, want one", closeCalls)
	}
}
