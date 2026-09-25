package wire

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

const (
	owedToolCallID = "call-owed"
	owedToolName   = "slow_tool"
	// unresolvedRaceIterations repeats each terminal path because the defect
	// was an ordering race between tool dispatch and the live event pump.
	unresolvedRaceIterations = 20
)

// resultRejectingSession refuses tool-result sends with a fixed status while
// accepting every other client send.
type resultRejectingSession struct {
	*scriptedLiveSession
	status messages.SessionSendStatus
}

func (s resultRejectingSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if msg.Type == messages.StreamTypeToolCallEnd && s.status != "" {
		return messages.SessionSendOutcome{Status: s.status, Err: errors.New("provider result queue is full")}
	}
	if !s.Send(ctx, msg) {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: ctx.Err()}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

type sessionInferencer struct{ session messages.Session }

func (i sessionInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

type owedToolExecutor struct {
	started chan struct{}
	once    sync.Once
	block   bool
}

func (e *owedToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.once.Do(func() { close(e.started) })
	if e.block {
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	}
	return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: "result"}, nil
}

func newOwedToolProvider() *scriptedLiveSession {
	var once sync.Once
	return newScriptedLiveSession(func(s *scriptedLiveSession, msg messages.StreamMessage) {
		if msg.Type != messages.StreamTypeTextDelta {
			return
		}
		once.Do(func() {
			s.emit(
				messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
				messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue(owedToolCallID, owedToolName)},
				messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue(owedToolCallID, owedToolName, `{}`)},
				messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			)
		})
	})
}

func newOwedToolRunner(t *testing.T, provider messages.Session) session.LiveRunner {
	t.Helper()
	runner, ok := newScriptedLiveService(sessionInferencer{session: provider}).(session.LiveRunner)
	if !ok {
		t.Fatal("live service does not own complete invocations")
	}
	return runner
}

func owedToolRunOptions(executor *owedToolExecutor) session.LiveRunOptions {
	return session.LiveRunOptions{Request: session.LiveRequest{
		SessionID: "owed-tool", OpeningPrompt: "use the tool",
		Capabilities: &session.LiveCapabilities{Executor: executor, Definitions: []messages.ToolDefinition{{Name: owedToolName}}},
	}}
}

func requireOwedToolUnresolved(t *testing.T, err error) {
	t.Helper()
	var unresolved *session.LiveUnresolvedToolResultsError
	if !errors.As(err, &unresolved) {
		t.Fatalf("run error = %v, want LiveUnresolvedToolResultsError", err)
	}
	if ids := unresolved.UnresolvedCallIDs(); len(ids) != 1 || ids[0] != owedToolCallID {
		t.Fatalf("unresolved call IDs = %v, want [%s]", ids, owedToolCallID)
	}
}

// TestRunLiveCancellationReportsDispatchedToolResultAsUnresolved guards the
// race where caller cancellation landed after the executor started but before
// the event pump observed the provider tool call.
func TestRunLiveCancellationReportsDispatchedToolResultAsUnresolved(t *testing.T) {
	for range unresolvedRaceIterations {
		executor := &owedToolExecutor{started: make(chan struct{}), block: true}
		runner := newOwedToolRunner(t, newOwedToolProvider())
		ctx, cancel := context.WithTimeout(context.Background(), liveRuntimeTestTimeout)
		runErr := make(chan error, 1)
		go func() { runErr <- runner.RunLive(ctx, owedToolRunOptions(executor)) }()
		select {
		case <-executor.started:
		case err := <-runErr:
			cancel()
			t.Fatalf("session ended before tool dispatch: %v", err)
		}
		cancel()
		requireOwedToolUnresolved(t, <-runErr)
	}
}

// TestRunLiveResultSendFailureKeepsUnresolvedToolResult guards the provider
// failure path, which previously returned the send error without the typed
// unresolved-result diagnostic.
func TestRunLiveResultSendFailureKeepsUnresolvedToolResult(t *testing.T) {
	for range unresolvedRaceIterations {
		executor := &owedToolExecutor{started: make(chan struct{})}
		provider := resultRejectingSession{scriptedLiveSession: newOwedToolProvider(), status: messages.SessionSendBufferFull}
		runner := newOwedToolRunner(t, provider)
		ctx, cancel := context.WithTimeout(context.Background(), liveRuntimeTestTimeout)
		err := runner.RunLive(ctx, owedToolRunOptions(executor))
		cancel()
		requireOwedToolUnresolved(t, err)
	}
}
