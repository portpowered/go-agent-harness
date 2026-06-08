package inference

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-llm-gateway/pkg/models"
)

// mockSession is a simple messages.Session implementation for testing.
type mockSession struct {
	recvBuf *messages.TypedBuffer[messages.StreamMessage]
	sendBuf *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
}

func newMockSession() *mockSession {
	return &mockSession{
		recvBuf: messages.NewTypedBuffer[messages.StreamMessage](64),
		sendBuf: messages.NewTypedBuffer[messages.StreamMessage](64),
		done:    make(chan struct{}),
	}
}

func (s *mockSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.sendBuf.Write(ctx, msg)
}

func (s *mockSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recvBuf
}

func (s *mockSession) Done() <-chan struct{} {
	return s.done
}

func (s *mockSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

// mockSessionGateway implements sessionGateway for testing.
type mockSessionGateway struct {
	mu             sync.Mutex
	capturedConfig models.SessionConfig
	session        messages.Session
	err            error
}

func (m *mockSessionGateway) ConnectSession(_ context.Context, config models.SessionConfig) (messages.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capturedConfig = config
	if m.err != nil {
		return nil, m.err
	}
	return m.session, nil
}

// --- SessionGatewayInferencer tests ---

func TestSessionGatewayInferencer_ConnectSession(t *testing.T) {
	sess := newMockSession()
	gw := &mockSessionGateway{session: sess}

	si := NewSessionGatewayInferencer(gw,
		WithSessionModel("grok-3-mini"),
		WithSessionVoice("Eve"),
		WithSessionInstructions("Be helpful"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := si.ConnectSession(ctx)
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	if gw.capturedConfig.Model != "grok-3-mini" {
		t.Errorf("model: got %q, want %q", gw.capturedConfig.Model, "grok-3-mini")
	}
	if gw.capturedConfig.Voice != "Eve" {
		t.Errorf("voice: got %q, want %q", gw.capturedConfig.Voice, "Eve")
	}
	if gw.capturedConfig.Instructions != "Be helpful" {
		t.Errorf("instructions: got %q, want %q", gw.capturedConfig.Instructions, "Be helpful")
	}
	if session != sess {
		t.Fatal("ConnectSession should return the loop-owned session from the gateway unchanged")
	}
}

func TestSessionGatewayInferencer_ConnectSessionError(t *testing.T) {
	gw := &mockSessionGateway{err: errors.New("connection refused")}
	si := NewSessionGatewayInferencer(gw, WithSessionModel("grok-3-mini"))

	_, err := si.ConnectSession(context.Background())
	if err == nil {
		t.Fatal("expected error from gateway")
	}
}

func TestSessionGatewayInferencer_ReturnedSessionIsUsable(t *testing.T) {
	sess := newMockSession()
	gw := &mockSessionGateway{session: sess}
	si := NewSessionGatewayInferencer(gw, WithSessionModel("grok-3-mini"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := si.ConnectSession(ctx)
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Inject an inbound StreamMessage via the mock's recvBuf.
	msg := messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{0x01, 0x02}),
	}
	sess.recvBuf.Write(ctx, msg)

	// Read it from the returned session's Receive buffer.
	received, ok := session.Receive().ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("Read from session Receive buffer returned false")
	}
	if received.Type != messages.StreamTypeAudioDelta {
		t.Errorf("type: got %q, want %q", received.Type, messages.StreamTypeAudioDelta)
	}
}

func TestSessionGatewayInferencer_SendRouted(t *testing.T) {
	sess := newMockSession()
	gw := &mockSessionGateway{session: sess}
	si := NewSessionGatewayInferencer(gw, WithSessionModel("grok-3-mini"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := si.ConnectSession(ctx)
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	defer func() { _ = session.Close() }()

	// Send a StreamMessage via the session.
	sent := messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Value: messages.NewAudioDeltaValue([]byte{0xAB, 0xCD}),
	}
	if !session.Send(ctx, sent) {
		t.Fatal("Send returned false")
	}

	// The mock session's sendBuf should have the message.
	received, ok := sess.sendBuf.ReadBlockingContext(ctx)
	if !ok {
		t.Fatal("sendBuf read returned false")
	}
	if received.Type != messages.StreamTypeAudioDelta {
		t.Errorf("type: got %q, want %q", received.Type, messages.StreamTypeAudioDelta)
	}
}
