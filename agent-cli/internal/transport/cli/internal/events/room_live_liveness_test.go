package events

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeRooms "github.com/portpowered/go-agent-harness/go-agent-runtime/services/rooms"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

// TestRoomUsesLiveLivenessForPeerFilteredEventsAndEvidence drives the public
// room service with the real continuous-session service. It covers the old
// room contract at its durable boundary: the provider empty-response fault is
// broadcast to a peer-filtered stream before the source terminal event, and
// the same classification survives in the finalized manifest and timeline.
func TestRoomUsesLiveLivenessForPeerFilteredEventsAndEvidence(t *testing.T) {
	runRoomLiveLivenessCase(t, false)
}

// TestRoomUsesLiveTimeoutForPeerFilteredEventsAndEvidence drives the same
// public room boundary through the provider-progress watchdog. The provider
// opens a response but emits no output; advancing the injected scheduler must
// project and persist the timeout before teardown.
func TestRoomUsesLiveTimeoutForPeerFilteredEventsAndEvidence(t *testing.T) {
	runRoomLiveLivenessCase(t, true)
}

func runRoomLiveLivenessCase(t *testing.T, timeoutCase bool) {
	fixture := newRoomLiveLivenessFixture(t, timeoutCase)
	fixture.startRoom()
	if timeoutCase {
		fixture.advanceTimeout(t)
	}
	fixture.waitForLiveness(t)
	fixture.assertPeerFault(t)
	fixture.assertStream(t, fixture.readAllStream(t))
	fixture.assertResult(t)
	fixture.assertEvidence(t)
}

type roomLiveResult struct {
	value runtimeRooms.RoomResult
	err   error
}

type releasePeerOnLiveness struct {
	delegate  runtimeRooms.EventSink
	release   func()
	observed  chan struct{}
	once      sync.Once
	started   chan struct{}
	startOnce sync.Once
}

func (s *releasePeerOnLiveness) Publish(ctx context.Context, participantID string, event session.LiveEvent) error {
	err := s.delegate.Publish(ctx, participantID, event)
	if participantID == roomLiveSilentParticipant && event.Kind == string(messages.StreamTypeMessageStart) && s.started != nil {
		s.startOnce.Do(func() { close(s.started) })
	}
	if participantID == roomLiveSilentParticipant && event.Liveness != nil {
		s.once.Do(func() {
			s.release()
			close(s.observed)
		})
	}
	return err
}

type roomLiveInferencer struct{ session *roomLiveSession }

func (i roomLiveInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type roomLiveSession struct {
	receive  *messages.TypedBuffer[messages.StreamMessage]
	media    *sharedaudio.SessionMedia
	done     chan struct{}
	once     sync.Once
	closeErr error
}

func newRoomLiveSession(events ...messages.StreamMessage) *roomLiveSession {
	session := &roomLiveSession{
		receive: messages.NewTypedBuffer[messages.StreamMessage](32),
		media:   sharedaudio.NewSessionMediaAtRate(func(context.Context, sharedaudio.PCMFrame) error { return nil }, 24000),
		done:    make(chan struct{}),
	}
	for _, event := range events {
		if !session.receive.Write(context.Background(), event) {
			panic("room live fixture event buffer is full")
		}
	}
	return session
}

func (s *roomLiveSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	return ctx.Err() == nil
}
func (s *roomLiveSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *roomLiveSession) Done() <-chan struct{}                                  { return s.done }
func (s *roomLiveSession) RTCMedia() sharedaudio.MediaEndpoints {
	if s == nil || s.media == nil {
		return sharedaudio.MediaEndpoints{}
	}
	return s.media.Endpoints()
}
func (s *roomLiveSession) Close() error {
	s.once.Do(func() {
		close(s.done)
		if s.media != nil {
			s.closeErr = s.media.Close()
		}
	})
	return s.closeErr
}

func roomLiveMessage(kind messages.StreamMessageType, value messages.StreamMessageValue) messages.StreamMessage {
	return messages.StreamMessage{Type: kind, Value: value, Role: messages.RoleAssistant}
}

func roomLiveMessageWithResponse(kind messages.StreamMessageType, responseID string, value messages.StreamMessageValue) messages.StreamMessage {
	return messages.StreamMessage{Type: kind, ResponseID: responseID, Value: value, Role: messages.RoleAssistant}
}

func readRoomJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return value
}

func newTestEventServer(t *testing.T, broker *Broker) *httptest.Server {
	t.Helper()
	return httptest.NewServer(broker)
}

var _ messages.SessionInferencer = roomLiveInferencer{}

var _ runtimeRooms.EventSink = (*releasePeerOnLiveness)(nil)
