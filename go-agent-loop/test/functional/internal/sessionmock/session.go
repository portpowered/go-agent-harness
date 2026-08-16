package sessionmock

import (
	"context"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Session implements messages.Session for functional tests.
type Session struct {
	recvBuf *messages.TypedBuffer[messages.StreamMessage]
	sendBuf *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
}

func (s *Session) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.sendBuf.Write(ctx, msg)
}

func (s *Session) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recvBuf }
func (s *Session) Done() <-chan struct{}                                  { return s.done }

func (s *Session) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

// Inferencer is a test double for session-mode inference.
type Inferencer struct {
	mu      sync.Mutex
	session *Session
}

// NewInferencer creates an Inferencer ready for testing.
func NewInferencer() *Inferencer { return &Inferencer{} }

func (m *Inferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &Session{
		recvBuf: messages.NewTypedBuffer[messages.StreamMessage](256),
		sendBuf: messages.NewTypedBuffer[messages.StreamMessage](256),
		done:    make(chan struct{}),
	}
	s.recvBuf.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("mock-session", "session"),
	})
	m.session = s
	return s, nil
}

func (m *Inferencer) AddServerEvent(event messages.StreamMessage) {
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	if sess != nil {
		sess.recvBuf.Write(context.Background(), event)
	}
}

func (m *Inferencer) AddServerEventSequence(events []messages.StreamMessage) {
	for _, event := range events {
		m.AddServerEvent(event)
	}
}

func (m *Inferencer) SimulateError(msg string) {
	m.AddServerEvent(messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewErrorValue(msg),
	})
}

func (m *Inferencer) SimulateDisconnect() {
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

func (m *Inferencer) Close() { m.SimulateDisconnect() }

func (m *Inferencer) WaitForSentMessage(msgType messages.StreamMessageType, timeout time.Duration) (messages.StreamMessage, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var sess *Session
	for {
		m.mu.Lock()
		sess = m.session
		m.mu.Unlock()
		if sess != nil {
			break
		}
		select {
		case <-ctx.Done():
			return messages.StreamMessage{}, false
		case <-time.After(5 * time.Millisecond):
		}
	}

	for range 32 {
		msg, ok := sess.sendBuf.ReadBlockingContext(ctx)
		if !ok {
			return messages.StreamMessage{}, false
		}
		if msg.Type == msgType {
			return msg, true
		}
	}
	return messages.StreamMessage{}, false
}
