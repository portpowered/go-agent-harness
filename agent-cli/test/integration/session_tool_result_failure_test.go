package integration

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/spf13/cobra"
)

const unresolvedToolCallID = "call_unresolved_failure"

// unresolvedFailureSession emits one real provider tool call and then either
// rejects its result send or waits for the test to inject a terminal close.
// It is deliberately a session-level double so the test reaches the shipped
// RunSession composition boundary without credentials or network traffic.
type unresolvedFailureSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}

	resultStatus messages.SessionSendStatus
	resultErr    error

	closeOnce    sync.Once
	responseOnce sync.Once
	mu           sync.Mutex
	sent         []messages.StreamMessage
}

func newUnresolvedFailureSession(resultStatus messages.SessionSendStatus, resultErr error) *unresolvedFailureSession {
	return &unresolvedFailureSession{
		recv:         messages.NewTypedBuffer[messages.StreamMessage](32),
		done:         make(chan struct{}),
		resultStatus: resultStatus,
		resultErr:    resultErr,
	}
}

func (s *unresolvedFailureSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *unresolvedFailureSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if err := ctx.Err(); err != nil {
		return unresolvedFailureContextOutcome(err)
	}
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()

	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		s.responseOnce.Do(func() { s.emitToolTurn() })
	case messages.StreamTypeToolCallEnd:
		if s.resultStatus != "" {
			return messages.SessionSendOutcome{Status: s.resultStatus, Err: s.resultErr}
		}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func unresolvedFailureContextOutcome(err error) messages.SessionSendOutcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

func (s *unresolvedFailureSession) emitToolTurn() {
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue(unresolvedToolCallID, "slow_tool")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue(unresolvedToolCallID, "slow_tool", `{"value":"wait"}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		s.recv.Write(context.Background(), msg)
	}
}

func (s *unresolvedFailureSession) emitTerminalClose() {
	s.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValue("unresolved-failure-session", "client_close"),
	})
}

func (s *unresolvedFailureSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *unresolvedFailureSession) Done() <-chan struct{} { return s.done }

func (s *unresolvedFailureSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

type unresolvedFailureToolExecutor struct {
	started chan struct{}
	once    sync.Once
	block   bool
}

func (e *unresolvedFailureToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.once.Do(func() { close(e.started) })
	if e.block {
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "result"}, nil
}

func runUnresolvedFailureSession(t *testing.T, session *unresolvedFailureSession, executor *unresolvedFailureToolExecutor) error {
	t.Helper()
	root := newUnresolvedFailureSessionRoot(t, session, executor)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return root.ExecuteContext(ctx)
}

func newUnresolvedFailureSessionRoot(t *testing.T, session *unresolvedFailureSession, executor *unresolvedFailureToolExecutor) *cobra.Command {
	t.Helper()
	return newLiveToolSessionRoot(t, liveToolSessionOptions{
		inferencer: &fixedUnresolvedFailureInferencer{session: session},
		executor:   executor,
		toolNames:  []string{"slow_tool"},
		inputPCM:   []byte{1, 2, 3, 4},
		args: []string{
			"--provider", "grok", "--model", "grok-realtime", "--api-key", "test-key",
			"--record", filepath.Join(t.TempDir(), "unresolved-tool-result.session.json"),
		},
	})
}

type fixedUnresolvedFailureInferencer struct {
	session *unresolvedFailureSession
}

func (i *fixedUnresolvedFailureInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("unresolved-failure-session", "test"),
	})
	return i.session, nil
}

func assertUnresolvedFailure(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("session returned nil for a terminal path with an unresolved tool result")
	}
	var unresolved *servicetest.SessionUnresolvedToolResultsError
	if !errors.As(err, &unresolved) {
		t.Fatalf("session error = %v, want SessionUnresolvedToolResultsError", err)
	}
	if !errors.Is(err, servicetest.ErrSessionUnresolvedToolResults) {
		t.Fatalf("session error = %v, want ErrSessionUnresolvedToolResults identity", err)
	}
	if got := unresolved.UnresolvedCallIDs(); len(got) != 1 || got[0] != unresolvedToolCallID {
		t.Fatalf("unresolved IDs = %v, want [%s]", got, unresolvedToolCallID)
	}
	if !strings.Contains(err.Error(), "tool results were not delivered") || !strings.Contains(err.Error(), unresolvedToolCallID) {
		t.Fatalf("human error = %q, want undelivered result and call ID", err)
	}
}

func TestSessionUnresolvedToolResultTerminalPathsFailWithStableDiagnostic(t *testing.T) {
	t.Run("provider close", func(t *testing.T) {
		session := newUnresolvedFailureSession("", nil)
		executor := &unresolvedFailureToolExecutor{started: make(chan struct{}), block: true}
		runErr := make(chan error, 1)
		go func() { runErr <- runUnresolvedFailureSession(t, session, executor) }()
		waitLifecycleSignal(t, executor.started, "unresolved tool executor to start")
		session.emitTerminalClose()
		select {
		case err := <-runErr:
			assertUnresolvedFailure(t, err)
		case <-time.After(sessionLifecycleSafetyTimeout):
			t.Fatalf("provider close did not terminate the unresolved session within %s", sessionLifecycleSafetyTimeout)
		}
	})

	t.Run("buffer full result send", func(t *testing.T) {
		session := newUnresolvedFailureSession(messages.SessionSendBufferFull, errors.New("provider result queue is full"))
		executor := &unresolvedFailureToolExecutor{started: make(chan struct{})}
		runErr := runUnresolvedFailureSession(t, session, executor)
		assertUnresolvedFailure(t, runErr)
	})

	t.Run("caller cancellation", func(t *testing.T) {
		session := newUnresolvedFailureSession("", nil)
		executor := &unresolvedFailureToolExecutor{started: make(chan struct{}), block: true}
		root := newUnresolvedFailureSessionRoot(t, session, executor)
		ctx, cancel := context.WithCancel(context.Background())
		runErr := make(chan error, 1)
		go func() { runErr <- root.ExecuteContext(ctx) }()
		waitLifecycleSignal(t, executor.started, "unresolved tool executor to start before cancellation")
		cancel()

		select {
		case err := <-runErr:
			assertUnresolvedFailure(t, err)
		case <-time.After(sessionLifecycleSafetyTimeout):
			t.Fatalf("caller cancellation did not terminate the unresolved session within %s", sessionLifecycleSafetyTimeout)
		}
	})

	t.Run("caller deadline", func(t *testing.T) {
		session := newUnresolvedFailureSession("", nil)
		executor := &unresolvedFailureToolExecutor{started: make(chan struct{}), block: true}
		root := newUnresolvedFailureSessionRoot(t, session, executor)
		// Coverage instrumentation and a loaded CI runner can take substantially
		// longer than 50ms to compose the session and dispatch the tool call. Give
		// setup enough headroom while still exercising a real caller deadline.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		runErr := make(chan error, 1)
		go func() { runErr <- root.ExecuteContext(ctx) }()
		waitLifecycleSignal(t, executor.started, "unresolved tool executor to start before deadline")

		select {
		case err := <-runErr:
			assertUnresolvedFailure(t, err)
		case <-time.After(sessionLifecycleSafetyTimeout):
			t.Fatalf("caller deadline did not terminate the unresolved session within %s", sessionLifecycleSafetyTimeout)
		}
	})

	t.Run("explicit client close", func(t *testing.T) {
		session := newUnresolvedFailureSession("", nil)
		executor := &unresolvedFailureToolExecutor{started: make(chan struct{}), block: true}
		root := newUnresolvedFailureSessionRoot(t, session, executor)
		ctx, cancel := context.WithTimeout(context.Background(), sessionLifecycleSafetyTimeout)
		defer cancel()
		runErr := make(chan error, 1)
		go func() { runErr <- root.ExecuteContext(ctx) }()
		waitLifecycleSignal(t, executor.started, "unresolved tool executor to start before client close")
		if err := session.Close(); err != nil {
			t.Fatalf("close unresolved session: %v", err)
		}

		select {
		case err := <-runErr:
			assertUnresolvedFailure(t, err)
		case <-time.After(sessionLifecycleSafetyTimeout):
			t.Fatalf("explicit client close did not terminate the unresolved session within %s", sessionLifecycleSafetyTimeout)
		}
	})
}

var _ messages.SessionInferencer = (*fixedUnresolvedFailureInferencer)(nil)
var _ messages.ToolExecutor = (*unresolvedFailureToolExecutor)(nil)
