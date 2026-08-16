package sessions

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/test/functional/internal/sessionmock"
)

type MockSession = sessionmock.Session
type MockSessionInferencer = sessionmock.Inferencer

var NewMockSessionInferencer = sessionmock.NewInferencer

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
