package integration

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const sessionToolLifecycleCallID = "call_lifecycle_slow"

// lifecycleSession is a small provider-facing session double. It emits one
// scheduled assistant response containing a tool call, then reports the
// result accepted only through SendWithOutcome. The local SESSION.CLOSE delta
// is observed through the normal session runner lifecycle.
type lifecycleSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}

	closeOnce          sync.Once
	responseOnce       sync.Once
	resultAcceptedOnce sync.Once

	mu   sync.Mutex
	sent []messages.StreamMessage

	resultAccepted chan struct{}
}

func newLifecycleSession() *lifecycleSession {
	return &lifecycleSession{
		recv:           messages.NewTypedBuffer[messages.StreamMessage](32),
		done:           make(chan struct{}),
		resultAccepted: make(chan struct{}),
	}
}

func (s *lifecycleSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *lifecycleSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if err := ctx.Err(); err != nil {
		return lifecycleSessionContextOutcome(err)
	}
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()

	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		s.responseOnce.Do(func() {
			s.emitProviderToolTurn()
		})
	case messages.StreamTypeToolCallEnd:
		value, ok := msg.Value.(*messages.ToolCallEndValue)
		if ok && value != nil && value.ToolCallID == sessionToolLifecycleCallID {
			s.resultAcceptedOnce.Do(func() { close(s.resultAccepted) })
		}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func lifecycleSessionContextOutcome(err error) messages.SessionSendOutcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

func (s *lifecycleSession) emitProviderToolTurn() {
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue(sessionToolLifecycleCallID, "slow_tool")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue(sessionToolLifecycleCallID, "slow_tool", `{"value":"wait"}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		s.recv.Write(context.Background(), msg)
	}
}

func (s *lifecycleSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }

func (s *lifecycleSession) Done() <-chan struct{} { return s.done }

func (s *lifecycleSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *lifecycleSession) sentSnapshot() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

type lifecycleSessionInferencer struct {
	mu      sync.Mutex
	session *lifecycleSession
	ready   chan struct{}
}

func (i *lifecycleSessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	session := newLifecycleSession()
	session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("lifecycle-session", "test"),
	})
	i.mu.Lock()
	i.session = session
	i.mu.Unlock()
	close(i.ready)
	return session, nil
}

func (i *lifecycleSessionInferencer) connectedSession() *lifecycleSession {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.session
}

type lifecycleToolExecutor struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *lifecycleToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.once.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return messages.ToolCallResponse{Content: "lifecycle-result"}, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

func waitLifecycleSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

// TestScheduledSessionWaitsForAcceptedToolResultAfterResponseDone drives the
// real session composition boundary with one scheduled turn. The provider's
// response.done is observed while the executor is still blocked; close is
// absent until the exact correlated result send is accepted, after which the
// normal close is emitted once.
func TestScheduledSessionWaitsForAcceptedToolResultAfterResponseDone(t *testing.T) {
	inferencer := &lifecycleSessionInferencer{}
	inferencer.ready = make(chan struct{})
	executor := &lifecycleToolExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	providerResponseDone := make(chan struct{})
	var responseDoneOnce sync.Once
	var localSessionCloseOnce sync.Once
	localSessionClose := make(chan struct{})
	var traceMu sync.Mutex
	var trace []messages.StreamMessage

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- services.RunSession(ctx, io.Discard, services.SessionRunOptions{
			RecordPath:        "scheduled-tool-lifecycle.session.json",
			Provider:          "grok",
			Model:             "grok-realtime",
			APIKey:            "test-key",
			SessionInferencer: inferencer,
			ToolExecutor:      executor,
			AudioInputs: []services.ScheduledAudioInput{{
				AfterCompletedTurns: 0,
				PCM:                 []byte{1, 2, 3, 4},
				EndOfTurn:           true,
			}},
			StreamObserver: func(msg messages.StreamMessage) {
				traceMu.Lock()
				trace = append(trace, msg)
				traceMu.Unlock()
				if msg.Type == messages.StreamTypeSessionClose {
					localSessionCloseOnce.Do(func() { close(localSessionClose) })
				}
				if msg.Type == messages.StreamTypeMessageEnd && msg.Role == messages.RoleAssistant {
					responseDoneOnce.Do(func() { close(providerResponseDone) })
				}
			},
		})
	}()

	select {
	case <-inferencer.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("session was not connected")
	}
	sessionReady := inferencer.connectedSession()
	if sessionReady == nil {
		t.Fatal("connected session was not retained")
	}

	waitLifecycleSignal(t, executor.started, "tool executor to start")
	waitLifecycleSignal(t, providerResponseDone, "provider response.done")
	select {
	case <-localSessionClose:
		t.Fatal("session emitted SESSION.CLOSE before the outstanding tool result was accepted")
	default:
	}

	close(executor.release)
	waitLifecycleSignal(t, sessionReady.resultAccepted, "correlated tool result acceptance")
	waitLifecycleSignal(t, localSessionClose, "SESSION.CLOSE after result acceptance")

	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("scheduled session returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled session did not finish after client close")
	}

	var resultCount int
	for _, msg := range sessionReady.sentSnapshot() {
		switch msg.Type {
		case messages.StreamTypeToolCallEnd:
			value, ok := msg.Value.(*messages.ToolCallEndValue)
			if ok && value != nil && value.ToolCallID == sessionToolLifecycleCallID {
				resultCount++
			}
		}
	}
	if resultCount != 1 {
		t.Fatalf("provider sends contained %d correlated tool results, want exactly one", resultCount)
	}
	traceMu.Lock()
	observed := append([]messages.StreamMessage(nil), trace...)
	traceMu.Unlock()
	var closeCount int
	for _, msg := range observed {
		if msg.Type == messages.StreamTypeSessionClose {
			closeCount++
		}
	}
	if closeCount != 1 {
		t.Fatalf("session emitted %d SESSION.CLOSE events, want exactly one", closeCount)
	}
}
