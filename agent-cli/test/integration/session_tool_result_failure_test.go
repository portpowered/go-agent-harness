package integration

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const unresolvedToolCallID = "call_unresolved_failure"

type unresolvedToolDiagnosticSink struct {
	mu      sync.Mutex
	records []services.SessionDiagnosticRecord
}

func (s *unresolvedToolDiagnosticSink) RecordSessionDiagnostic(record services.SessionDiagnosticRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
}

func (s *unresolvedToolDiagnosticSink) failureRecords() []services.SessionDiagnosticRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []services.SessionDiagnosticRecord
	for _, record := range s.records {
		if record.Event == services.SessionDiagnosticEventFailure {
			records = append(records, record)
		}
	}
	return records
}

func (s *unresolvedToolDiagnosticSink) recordsFor(event string) []services.SessionDiagnosticRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []services.SessionDiagnosticRecord
	for _, record := range s.records {
		if record.Event == event {
			records = append(records, record)
		}
	}
	return records
}

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

func runUnresolvedFailureSession(t *testing.T, session *unresolvedFailureSession, executor *unresolvedFailureToolExecutor, sink *unresolvedToolDiagnosticSink) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return runUnresolvedFailureSessionWithContext(ctx, &sessionRunInputs{
		session:  session,
		executor: executor,
		sink:     sink,
	})
}

type sessionRunInputs struct {
	session  *unresolvedFailureSession
	executor *unresolvedFailureToolExecutor
	sink     *unresolvedToolDiagnosticSink
}

func runUnresolvedFailureSessionWithContext(ctx context.Context, inputs *sessionRunInputs) error {
	var out bytes.Buffer
	return services.RunSession(ctx, &out, services.SessionRunOptions{
		RecordPath:        "unresolved-tool-result.session.json",
		Provider:          "grok",
		Model:             "grok-realtime",
		APIKey:            "test-key",
		SessionInferencer: &fixedUnresolvedFailureInferencer{session: inputs.session},
		ToolExecutor:      inputs.executor,
		Diagnostics:       inputs.sink,
		AudioInputs: []services.ScheduledAudioInput{{
			AfterCompletedTurns: 0,
			PCM:                 []byte{1, 2, 3, 4},
			EndOfTurn:           true,
		}},
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

func assertUnresolvedFailure(t *testing.T, err error, sink *unresolvedToolDiagnosticSink, wantStatus messages.SessionSendStatus) {
	t.Helper()
	if err == nil {
		t.Fatal("RunSession returned nil for a terminal path with an unresolved tool result")
	}
	var unresolved *services.SessionUnresolvedToolResultsError
	if !errors.As(err, &unresolved) {
		t.Fatalf("RunSession error = %v, want SessionUnresolvedToolResultsError", err)
	}
	if !errors.Is(err, services.ErrSessionUnresolvedToolResults) {
		t.Fatalf("RunSession error = %v, want ErrSessionUnresolvedToolResults identity", err)
	}
	if got := unresolved.UnresolvedCallIDs(); len(got) != 1 || got[0] != unresolvedToolCallID {
		t.Fatalf("unresolved IDs = %v, want [%s]", got, unresolvedToolCallID)
	}
	if wantStatus != "" {
		if got := unresolved.SendStatuses[unresolvedToolCallID]; got != wantStatus {
			t.Fatalf("unresolved send status = %q, want %q", got, wantStatus)
		}
	}
	if !strings.Contains(err.Error(), "tool results were not delivered") || !strings.Contains(err.Error(), unresolvedToolCallID) {
		t.Fatalf("human error = %q, want undelivered result and call ID", err)
	}
	failures := sink.failureRecords()
	if len(failures) != 1 {
		t.Fatalf("failure diagnostic count = %d, want exactly one", len(failures))
	}
	if turns := sink.recordsFor(services.SessionDiagnosticEventTurn); len(turns) != 0 {
		t.Fatalf("unresolved terminal path emitted %d completed-turn records: %#v", len(turns), turns)
	}
	fields := failures[0].Fields
	if fields[services.SessionDiagnosticFieldUnresolvedToolResultCount] != "1" {
		t.Fatalf("unresolved count field = %q, want 1", fields[services.SessionDiagnosticFieldUnresolvedToolResultCount])
	}
	if fields[services.SessionDiagnosticFieldUnresolvedToolCallIDs] != unresolvedToolCallID {
		t.Fatalf("unresolved IDs field = %q, want %s", fields[services.SessionDiagnosticFieldUnresolvedToolCallIDs], unresolvedToolCallID)
	}
}

func TestSessionUnresolvedToolResultTerminalPathsFailWithStableDiagnostic(t *testing.T) {
	t.Run("provider close", func(t *testing.T) {
		session := newUnresolvedFailureSession("", nil)
		executor := &unresolvedFailureToolExecutor{started: make(chan struct{}), block: true}
		sink := &unresolvedToolDiagnosticSink{}
		runErr := make(chan error, 1)
		go func() { runErr <- runUnresolvedFailureSession(t, session, executor, sink) }()
		waitLifecycleSignal(t, executor.started, "unresolved tool executor to start")
		session.emitTerminalClose()
		select {
		case err := <-runErr:
			assertUnresolvedFailure(t, err, sink, "")
		case <-time.After(sessionLifecycleSafetyTimeout):
			t.Fatalf("provider close did not terminate the unresolved session within %s", sessionLifecycleSafetyTimeout)
		}
	})

	t.Run("buffer full result send", func(t *testing.T) {
		session := newUnresolvedFailureSession(messages.SessionSendBufferFull, errors.New("provider result queue is full"))
		executor := &unresolvedFailureToolExecutor{started: make(chan struct{})}
		sink := &unresolvedToolDiagnosticSink{}
		runErr := runUnresolvedFailureSession(t, session, executor, sink)
		assertUnresolvedFailure(t, runErr, sink, messages.SessionSendBufferFull)
	})

	t.Run("caller cancellation", func(t *testing.T) {
		session := newUnresolvedFailureSession("", nil)
		executor := &unresolvedFailureToolExecutor{started: make(chan struct{}), block: true}
		sink := &unresolvedToolDiagnosticSink{}
		ctx, cancel := context.WithCancel(context.Background())
		runErr := make(chan error, 1)
		go func() {
			runErr <- runUnresolvedFailureSessionWithContext(ctx, &sessionRunInputs{
				session:  session,
				executor: executor,
				sink:     sink,
			})
		}()
		waitLifecycleSignal(t, executor.started, "unresolved tool executor to start before cancellation")
		cancel()

		select {
		case err := <-runErr:
			assertUnresolvedFailure(t, err, sink, "")
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled unresolved session error = %v, want context.Canceled preserved", err)
			}
		case <-time.After(sessionLifecycleSafetyTimeout):
			t.Fatalf("caller cancellation did not terminate the unresolved session within %s", sessionLifecycleSafetyTimeout)
		}
	})

	t.Run("caller deadline", func(t *testing.T) {
		session := newUnresolvedFailureSession("", nil)
		executor := &unresolvedFailureToolExecutor{started: make(chan struct{}), block: true}
		sink := &unresolvedToolDiagnosticSink{}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		runErr := make(chan error, 1)
		go func() {
			runErr <- runUnresolvedFailureSessionWithContext(ctx, &sessionRunInputs{
				session:  session,
				executor: executor,
				sink:     sink,
			})
		}()
		waitLifecycleSignal(t, executor.started, "unresolved tool executor to start before deadline")

		select {
		case err := <-runErr:
			assertUnresolvedFailure(t, err, sink, "")
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline unresolved session error = %v, want context.DeadlineExceeded preserved", err)
			}
		case <-time.After(sessionLifecycleSafetyTimeout):
			t.Fatalf("caller deadline did not terminate the unresolved session within %s", sessionLifecycleSafetyTimeout)
		}
	})

	t.Run("explicit client close", func(t *testing.T) {
		session := newUnresolvedFailureSession("", nil)
		executor := &unresolvedFailureToolExecutor{started: make(chan struct{}), block: true}
		sink := &unresolvedToolDiagnosticSink{}
		ctx, cancel := context.WithTimeout(context.Background(), sessionLifecycleSafetyTimeout)
		defer cancel()
		runErr := make(chan error, 1)
		go func() {
			runErr <- runUnresolvedFailureSessionWithContext(ctx, &sessionRunInputs{
				session:  session,
				executor: executor,
				sink:     sink,
			})
		}()
		waitLifecycleSignal(t, executor.started, "unresolved tool executor to start before client close")
		if err := session.Close(); err != nil {
			t.Fatalf("close unresolved session: %v", err)
		}

		select {
		case err := <-runErr:
			assertUnresolvedFailure(t, err, sink, "")
		case <-time.After(sessionLifecycleSafetyTimeout):
			t.Fatalf("explicit client close did not terminate the unresolved session within %s", sessionLifecycleSafetyTimeout)
		}
	})
}

var _ messages.SessionInferencer = (*fixedUnresolvedFailureInferencer)(nil)
var _ messages.ToolExecutor = (*unresolvedFailureToolExecutor)(nil)
