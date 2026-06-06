package functional

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// ---------------------------------------------------------------------------
// MockSession
// ---------------------------------------------------------------------------

// MockSession implements messages.Session for testing. It exposes a
// TypedBuffer receive channel that tests write into (simulating server events)
// and a TypedBuffer send channel that captures outbound agent events.
type MockSession struct {
	recvBuf *messages.TypedBuffer[messages.StreamMessage]
	sendBuf *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
}

func (s *MockSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.sendBuf.Write(ctx, msg)
}

func (s *MockSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recvBuf
}

func (s *MockSession) Done() <-chan struct{} {
	return s.done
}

func (s *MockSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

// ---------------------------------------------------------------------------
// MockSessionInferencer
// ---------------------------------------------------------------------------

// MockSessionInferencer is a test double for session-mode inference.
// It implements messages.SessionInferencer: ConnectSession returns a MockSession
// whose receive buffer is pre-seeded with SESSION.OPEN (mirroring what the real
// session inferencer does on session.created). Tests inject server events with
// AddServerEvent; outbound agent events are captured in the session's send buffer.
type MockSessionInferencer struct {
	mu      sync.Mutex
	session *MockSession
}

// NewMockSessionInferencer creates a MockSessionInferencer ready for testing.
func NewMockSessionInferencer() *MockSessionInferencer {
	return &MockSessionInferencer{}
}

// ConnectSession implements messages.SessionInferencer. It creates a new MockSession,
// pre-seeds it with SESSION.OPEN (mirroring the real session.created → SESSION.OPEN
// translation), and stores it for use by AddServerEvent.
func (m *MockSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &MockSession{
		recvBuf: messages.NewTypedBuffer[messages.StreamMessage](256),
		sendBuf: messages.NewTypedBuffer[messages.StreamMessage](256),
		done:    make(chan struct{}),
	}
	// Pre-inject SESSION.OPEN: mirrors what the real session does on session.created.
	s.recvBuf.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("mock-session", "session"),
	})
	m.session = s
	return s, nil
}

// AddServerEvent injects a server event into the session's receive buffer.
// Safe to call from any goroutine after Start() has been called.
func (m *MockSessionInferencer) AddServerEvent(event messages.StreamMessage) {
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	if sess != nil {
		sess.recvBuf.Write(context.Background(), event)
	}
}

// AddServerEventSequence injects multiple events in order.
func (m *MockSessionInferencer) AddServerEventSequence(events []messages.StreamMessage) {
	for _, e := range events {
		m.AddServerEvent(e)
	}
}

// SimulateError injects an ERROR event into the session stream.
func (m *MockSessionInferencer) SimulateError(msg string) {
	m.AddServerEvent(messages.StreamMessage{
		Type:  messages.StreamTypeError,
		Value: messages.NewErrorValue(msg),
	})
}

// SimulateDisconnect closes the session's done channel to signal disconnect.
// This causes runSession to exit (case <-session.Done()).
func (m *MockSessionInferencer) SimulateDisconnect() {
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
}

// Close shuts down the mock, simulating a session disconnect.
func (m *MockSessionInferencer) Close() {
	m.SimulateDisconnect()
}

// WaitForSentMessage blocks until a message of the given type appears in the
// session's outbound send buffer (i.e. a message the agent loop sent TO the
// inference provider via session.Send). Returns the message and true on
// success, or the zero value and false on timeout.
//
// It drains up to 32 messages from the send buffer searching for msgType;
// messages of other types are consumed and not returned. Suitable for tests
// where the expected message is known to arrive early and the buffer is small.
func (m *MockSessionInferencer) WaitForSentMessage(msgType messages.StreamMessageType, timeout time.Duration) (messages.StreamMessage, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Poll until the session is set (ConnectSession may not have been called yet).
	var sess *MockSession
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

	// Drain messages until we find the target type or time out.
	for i := 0; i < 32; i++ {
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

// ---------------------------------------------------------------------------
// SessionScenario
// ---------------------------------------------------------------------------

// SessionScenario manages a session-mode AgenticLoop lifecycle for testing.
// Create with NewSessionScenario, start with Start, inject events, then stop.
type SessionScenario struct {
	t    *testing.T
	Loop *agentloop.AgentLoop
	Inf  *MockSessionInferencer
	Tool *MockToolExecutor

	cancel   context.CancelFunc
	errCh    chan error
	deltasMu sync.Mutex
	deltas   []messages.StreamMessage
}

// NewSessionScenario creates a SessionScenario with the given mock inferencer
// and tool executor. Additional options (e.g. WithTools) can be passed.
func NewSessionScenario(t *testing.T, inf *MockSessionInferencer, tool *MockToolExecutor, opts ...agentloop.Option) *SessionScenario {
	t.Helper()

	allOpts := []agentloop.Option{
		agentloop.WithSessionInferencer(inf),
		agentloop.WithToolExecutor(tool),
		agentloop.WithMode(engine.DuplexSession),
	}
	allOpts = append(allOpts, opts...)

	loop, err := agentloop.New(allOpts...)
	if err != nil {
		t.Fatalf("NewSessionScenario: failed to create loop: %v", err)
	}

	return &SessionScenario{
		t:    t,
		Loop: loop,
		Inf:  inf,
		Tool: tool,
	}
}

// Start begins the session. Call this before sending events.
func (s *SessionScenario) Start() {
	s.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.errCh = make(chan error, 1)

	// Collect deltas from the loop's consumer-facing Deltas() buffer in background.
	go func() {
		for {
			delta, ok := s.Loop.Deltas().ReadBlockingContext(ctx)
			if !ok {
				return
			}
			s.deltasMu.Lock()
			s.deltas = append(s.deltas, delta)
			s.deltasMu.Unlock()
		}
	}()

	// Start the loop.
	go func() {
		s.errCh <- s.Loop.Run(ctx)
	}()

	// Brief wait for engine to initialize.
	time.Sleep(50 * time.Millisecond)
}

// SendControlPlane sends a control plane message to the session (e.g. session_close, stop, ping).
func (s *SessionScenario) SendControlPlane(cpType messages.ControlPlaneMessageType) {
	msg := messages.Message{
		Role: messages.RoleUser,
		ContentParts: []messages.ContentPart{
			messages.ControlPlanePart{ControlPlaneMessageType: cpType},
		},
	}
	if err := s.Loop.Send(context.Background(), []messages.Message{msg}); err != nil {
		s.t.Fatalf("SessionScenario.SendControlPlane: %v", err)
	}
}

// SendAudioInput sends raw PCM audio to the session loop for user audio forwarding
// and barge-in. Panics if the loop is not in session mode.
func (s *SessionScenario) SendAudioInput(pcm []byte) {
	s.t.Helper()
	if err := s.Loop.SendAudioInput(context.Background(), pcm); err != nil {
		s.t.Fatalf("SessionScenario.SendAudioInput: %v", err)
	}
}

// SendText sends a text message to the session.
func (s *SessionScenario) SendText(text string) {
	msg := messages.NewTextMessage(messages.RoleUser, text)
	if err := s.Loop.Send(context.Background(), []messages.Message{msg}); err != nil {
		s.t.Fatalf("SessionScenario.SendText: %v", err)
	}
}

// Stop triggers a graceful session close. It sends session_close, waits
// briefly for the events to propagate, then cancels the context and
// closes the mock inferencer.
func (s *SessionScenario) Stop(timeout time.Duration) error {
	s.SendControlPlane(messages.ControlPlaneMessageTypeSessionClose)

	// Brief wait for the control plane message to be processed by the engine.
	time.Sleep(200 * time.Millisecond)

	// Close the mock session (unblocks runSession if it's blocked on session.Done()).
	s.Inf.Close()

	// Cancel context to stop the engine hot loop.
	s.cancel()

	select {
	case err := <-s.errCh:
		// context.Canceled is expected — the loop was cancelled by us.
		if err == context.Canceled {
			return nil
		}
		return err
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

// Deltas returns a copy of all collected delta events.
func (s *SessionScenario) Deltas() []messages.StreamMessage {
	s.deltasMu.Lock()
	defer s.deltasMu.Unlock()
	out := make([]messages.StreamMessage, len(s.deltas))
	copy(out, s.deltas)
	return out
}

// WaitForEvent blocks until a delta event with the given type appears or times out.
func (s *SessionScenario) WaitForEvent(eventType messages.StreamMessageType, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		s.deltasMu.Lock()
		for _, d := range s.deltas {
			if d.Type == eventType {
				s.deltasMu.Unlock()
				return true
			}
		}
		s.deltasMu.Unlock()

		select {
		case <-deadline:
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// ---------------------------------------------------------------------------
// Session assertion helpers
// ---------------------------------------------------------------------------

// AssertSessionDeltaContains checks that the delta slice contains at least one
// event matching every entry in required (subsequence matching).
func AssertSessionDeltaContains(t *testing.T, deltas []messages.StreamMessage, required []ExpectedDelta) {
	t.Helper()
	AssertDeltaContains(t, deltas, required)
}

// AssertSessionLifecycle verifies that SESSION.OPEN is the first event and
// SESSION.CLOSE + LOOP.END are the final events in correct order.
func AssertSessionLifecycle(t *testing.T, deltas []messages.StreamMessage) {
	t.Helper()
	if len(deltas) < 3 {
		t.Fatalf("expected at least 3 delta events (SESSION.OPEN, SESSION.CLOSE, LOOP.END), got %d", len(deltas))
	}

	if deltas[0].Type != messages.StreamTypeSessionOpen {
		t.Errorf("first event should be SESSION.OPEN, got %q", deltas[0].Type)
	}

	n := len(deltas)
	if deltas[n-1].Type != messages.StreamTypeLoopEnd {
		t.Errorf("last event should be LOOP.END, got %q", deltas[n-1].Type)
	}
	if deltas[n-2].Type != messages.StreamTypeSessionClose {
		t.Errorf("second-to-last event should be SESSION.CLOSE, got %q", deltas[n-2].Type)
	}
}
