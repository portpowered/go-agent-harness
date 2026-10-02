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

	// sentMu guards sent, the per-type tally of messages the provider
	// accepted. It is kept apart from sendBuf so observing what the provider
	// received never consumes the queue that WaitForSentMessage reads.
	sentMu sync.Mutex
	sent   map[messages.StreamMessageType]int
	// sentSignal is closed and cleared by the next Send so WaitForSentCount
	// blocks on a signal instead of polling.
	sentSignal chan struct{}
}

func (s *Session) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if !s.sendBuf.Write(ctx, msg) {
		return false
	}
	s.sentMu.Lock()
	s.sent[msg.Type]++
	if s.sentSignal != nil {
		close(s.sentSignal)
		s.sentSignal = nil
	}
	s.sentMu.Unlock()
	return true
}

func (s *Session) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recvBuf }
func (s *Session) Done() <-chan struct{}                                  { return s.done }

func (s *Session) Close() error {
	s.closeDone()
	return nil
}

func (s *Session) closeDone() { s.once.Do(func() { close(s.done) }) }

// Inferencer is a test double for session-mode inference.
type Inferencer struct {
	mu      sync.Mutex
	session *Session
	// connected is closed by the first ConnectSession.
	connected chan struct{}
}

// NewInferencer creates an Inferencer ready for testing.
func NewInferencer() *Inferencer { return &Inferencer{connected: make(chan struct{})} }

func (m *Inferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &Session{
		recvBuf: messages.NewTypedBuffer[messages.StreamMessage](sessionBufferCapacity),
		sendBuf: messages.NewTypedBuffer[messages.StreamMessage](sessionBufferCapacity),
		done:    make(chan struct{}),
		sent:    make(map[messages.StreamMessageType]int),
	}
	s.recvBuf.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("mock-session", "session"),
	})
	if m.session == nil && m.connected != nil {
		close(m.connected)
	}
	m.session = s
	return s, nil
}

func (m *Inferencer) AddServerEvent(ctx context.Context, event messages.StreamMessage) {
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	if sess != nil {
		sess.recvBuf.Write(ctx, event)
	}
}

func (m *Inferencer) AddServerEventSequence(ctx context.Context, events []messages.StreamMessage) {
	for _, event := range events {
		m.AddServerEvent(ctx, event)
	}
}

// SentCount reports how many messages of msgType the current provider session
// has received from the runner. It does not consume the send queue, so a
// scripted provider can gate its reply on the request having arrived, as a
// real provider would, while WaitForSentMessage callers still see every frame.
func (m *Inferencer) SentCount(msgType messages.StreamMessageType) int {
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	if sess == nil {
		return 0
	}
	sess.sentMu.Lock()
	defer sess.sentMu.Unlock()
	return sess.sent[msgType]
}

// WaitForSentCount blocks until the current provider session has received at
// least n messages of msgType, ctx ends, or timeout elapses, and reports
// whether the count was reached. Like SentCount it never consumes the send
// queue. timeout is a failure bound only: the wait wakes on each Send.
func (m *Inferencer) WaitForSentCount(ctx context.Context, msgType messages.StreamMessageType, n int, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-m.connected:
	case <-ctx.Done():
		return false
	}
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	for {
		sess.sentMu.Lock()
		count := sess.sent[msgType]
		if sess.sentSignal == nil {
			sess.sentSignal = make(chan struct{})
		}
		signal := sess.sentSignal
		sess.sentMu.Unlock()
		if count >= n {
			return true
		}
		select {
		case <-signal:
		case <-ctx.Done():
			return false
		}
	}
}

func (m *Inferencer) SimulateError(ctx context.Context, msg string) {
	m.AddServerEvent(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewErrorValue(msg),
	})
}

func (m *Inferencer) SimulateDisconnect() {
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	if sess != nil {
		sess.closeDone()
	}
}

func (m *Inferencer) Close() { m.SimulateDisconnect() }

func (m *Inferencer) WaitForSentMessage(ctx context.Context, msgType messages.StreamMessageType, timeout time.Duration) (messages.StreamMessage, bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-m.connected:
	case <-ctx.Done():
		return messages.StreamMessage{}, false
	}
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()

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

// sessionBufferCapacity bounds each mock session direction.
const sessionBufferCapacity = 256
