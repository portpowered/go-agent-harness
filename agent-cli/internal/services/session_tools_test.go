package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/tools"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type sessionToolExecutorFunc func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error)

func (f sessionToolExecutorFunc) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	return f(ctx, call)
}

func TestSessionToolExecutor_PreservesCallAndCorrelatesSuccess(t *testing.T) {
	call := messages.ToolCall{
		ID:        "s14-call-42",
		Name:      "distinctive_session_tool",
		Arguments: `{"city":"São Paulo","units":"metric"}`,
	}
	var got messages.ToolCall
	var mu sync.Mutex
	inner := sessionToolExecutorFunc(func(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		mu.Lock()
		got = call
		mu.Unlock()
		return messages.ToolCallResponse{Content: "s14-result-payload"}, nil
	})

	response, err := newSessionToolExecutor(inner, time.Second).Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if got != call {
		t.Fatalf("executor call = %#v, want %#v", got, call)
	}
	if response.ToolCallID != call.ID || response.Name != call.Name {
		t.Fatalf("response correlation = (%q, %q), want (%q, %q)", response.ToolCallID, response.Name, call.ID, call.Name)
	}
	if response.Content != "s14-result-payload" {
		t.Fatalf("response content = %q, want exact executor payload", response.Content)
	}
}

func TestSessionToolExecutor_ConvertsFailuresToCorrelatedResults(t *testing.T) {
	call := messages.ToolCall{ID: "s4-call-1", Name: "failure_case", Arguments: `{not-json}`}

	cases := []struct {
		name     string
		executor messages.ToolExecutor
		timeout  time.Duration
		contains string
	}{
		{
			name: "unknown tool",
			executor: sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
				return messages.ToolCallResponse{}, errors.New(`tool "missing_tool" not found`)
			}),
			contains: "not found",
		},
		{
			name: "malformed arguments",
			executor: sessionToolExecutorFunc(func(_ context.Context, got messages.ToolCall) (messages.ToolCallResponse, error) {
				if got.Arguments != call.Arguments {
					return messages.ToolCallResponse{}, errors.New("arguments were changed")
				}
				return messages.ToolCallResponse{}, errors.New("failed to parse tool arguments")
			}),
			contains: "parse tool arguments",
		},
		{
			name: "executor panic",
			executor: sessionToolExecutorFunc(func(context.Context, messages.ToolCall) (messages.ToolCallResponse, error) {
				panic("panic detail must stay inside this invocation")
			}),
			contains: "panicked",
		},
		{
			name:    "executor timeout",
			timeout: 10 * time.Millisecond,
			executor: sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
				<-ctx.Done()
				return messages.ToolCallResponse{}, ctx.Err()
			}),
			contains: "timed out",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, err := newSessionToolExecutor(tc.executor, tc.timeout).Execute(context.Background(), call)
			if err != nil {
				t.Fatalf("Execute returned Go error: %v", err)
			}
			if response.ToolCallID != call.ID || response.Name != call.Name {
				t.Fatalf("response correlation = (%q, %q), want (%q, %q)", response.ToolCallID, response.Name, call.ID, call.Name)
			}
			if strings.TrimSpace(response.Content) == "" {
				t.Fatal("failure response content is empty")
			}
			if !strings.Contains(response.Content, tc.contains) {
				t.Fatalf("failure response = %q, want substring %q", response.Content, tc.contains)
			}
			if len(response.ContentParts) != 0 {
				t.Fatalf("failure response unexpectedly contained content parts: %#v", response.ContentParts)
			}
		})
	}
}

// TestSessionToolExecutor_CooperativeWorkerExitsAfterTimeout proves the
// context-cooperative execution contract: a worker that honors its context
// exits promptly once the adapter has returned on timeout, so no invocation
// goroutine outlives the call.
func TestSessionToolExecutor_CooperativeWorkerExitsAfterTimeout(t *testing.T) {
	workerExited := make(chan struct{})
	inner := sessionToolExecutorFunc(func(ctx context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		defer close(workerExited)
		<-ctx.Done()
		return messages.ToolCallResponse{}, ctx.Err()
	})

	response, err := newSessionToolExecutor(inner, 10*time.Millisecond).Execute(context.Background(), messages.ToolCall{ID: "t-call", Name: "t-tool"})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if !strings.Contains(response.Content, "timed out") {
		t.Fatalf("response = %q, want timeout failure", response.Content)
	}

	select {
	case <-workerExited:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout worker did not exit after Execute returned")
	}
}

func TestSessionToolExecutor_UsesDefaultTimeoutForNonPositivePolicy(t *testing.T) {
	inner := sessionToolExecutorFunc(func(_ context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{Content: "ok"}, nil
	})

	response, err := newSessionToolExecutor(inner, 0).Execute(context.Background(), messages.ToolCall{ID: "call", Name: "tool"})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if response.Content != "ok" {
		t.Fatalf("response content = %q, want ok", response.Content)
	}
}

type stubConfigTool struct {
	name    string
	content string
}

func (t *stubConfigTool) Name() string               { return t.name }
func (t *stubConfigTool) Description() string        { return "stub tool for session wiring tests" }
func (t *stubConfigTool) Parameters() map[string]any { return map[string]any{} }
func (t *stubConfigTool) Execute(context.Context, map[string]any) ([]messages.Message, error) {
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, t.content)}, nil
}

func TestResolveSessionToolExecution_ComposesFromConfigRegistry(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.List = []config.ToolEntry{{ID: "sleep", Enabled: true}}

	registry := tools.NewToolRegistryFromConfig(cfg)
	registry.Register(&stubConfigTool{name: "distinctive_session_tool", content: "registry payload"})

	executor := newSessionToolExecutor(tools.NewRegistryExecutor(registry), time.Second)
	defs := registry.ToAgentLoopDefs()
	foundSleep := false
	for _, def := range defs {
		if def.Name == "sleep" {
			foundSleep = true
		}
	}
	if !foundSleep {
		t.Fatalf("registry defs missing sleep, got %#v", defs)
	}

	response, err := executor.Execute(context.Background(), messages.ToolCall{
		ID:        "cfg-call-1",
		Name:      "distinctive_session_tool",
		Arguments: `{}`,
	})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if response.Content != "registry payload" || response.ToolCallID != "cfg-call-1" {
		t.Fatalf("response = %#v, want registry payload correlated to cfg-call-1", response)
	}
}

func TestResolveSessionToolExecution_NoToolsAndInjectedPaths(t *testing.T) {
	injected := sessionToolExecutorFunc(func(_ context.Context, _ messages.ToolCall) (messages.ToolCallResponse, error) {
		return messages.ToolCallResponse{Content: "injected"}, nil
	})
	executor, defs := resolveSessionToolExecution(SessionRunOptions{
		ConfigDir:       t.TempDir(),
		ToolExecutor:    injected,
		ToolDefinitions: []messages.ToolDefinition{{Name: "injected_tool"}},
	})
	if reflect.ValueOf(executor).Pointer() != reflect.ValueOf(injected).Pointer() {
		t.Fatalf("injected executor = %v, want the injected instance unchanged", executor)
	}
	if len(defs) != 1 || defs[0].Name != "injected_tool" {
		t.Fatalf("defs = %#v, want injected_tool definition", defs)
	}

	// A config that disables every tool yields a no-tools configuration.
	noToolsDir := t.TempDir()
	cfgPath := filepath.Join(noToolsDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("tools:\n  list: []\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	executor, defs = resolveSessionToolExecution(SessionRunOptions{ConfigDir: noToolsDir})
	if executor != nil || defs != nil {
		t.Fatalf("no-tools resolution = (%v, %v), want nil/nil", executor, defs)
	}
}

// roundTripSession records everything the loop sends back to the provider.
type roundTripSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}
	once sync.Once

	mu   sync.Mutex
	sent []messages.StreamMessage
}

var _ messages.Session = (*roundTripSession)(nil)

func newRoundTripSession() *roundTripSession {
	return &roundTripSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](64),
		done: make(chan struct{}),
	}
}

func (s *roundTripSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	return true
}

func (s *roundTripSession) SentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *roundTripSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }

func (s *roundTripSession) Done() <-chan struct{} { return s.done }

func (s *roundTripSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

// roundTripInferencer emits an open, a provider tool-call turn, and a
// follow-up assistant turn. The follow-up proves the session kept making
// progress after tool execution instead of terminating.
type roundTripInferencer struct {
	callEvents   func() []messages.StreamMessage
	followUpText string
	toolInvoked  chan struct{}
	runFinished  chan struct{}
	finishOnce   sync.Once

	session *roundTripSession
}

var _ messages.SessionInferencer = (*roundTripInferencer)(nil)

func newRoundTripInferencer(followUp string) *roundTripInferencer {
	return &roundTripInferencer{
		followUpText: followUp,
		toolInvoked:  make(chan struct{}),
		runFinished:  make(chan struct{}),
	}
}

func (i *roundTripInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := newRoundTripSession()
	i.session = session
	go func() {
		// The session deliberately stays open; the caller's MaxDuration ends
		// the run so all provider turns are drained deterministically.
		defer i.finishOnce.Do(func() { close(i.runFinished) })
		session.recv.Write(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeSessionOpen,
			Value: messages.NewSessionOpenValue("roundtrip-session", "session"),
		})
		for _, evt := range i.callEvents() {
			session.recv.Write(ctx, evt)
		}
		<-i.toolInvoked
		time.Sleep(30 * time.Millisecond)
		session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
		session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(i.followUpText)})
		<-i.runFinished
	}()
	return session, nil
}

func (i *roundTripInferencer) signalInvoked() {
	select {
	case <-i.toolInvoked:
	default:
		close(i.toolInvoked)
	}
}

func toolCallEvents(callID, name, args string) func() []messages.StreamMessage {
	return func() []messages.StreamMessage {
		return []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeToolCallStart, ActorProvidedIndex: 0, Value: messages.NewToolCallStartValue(callID, name)},
			{Type: messages.StreamTypeToolCallDelta, ActorProvidedIndex: 0, Value: messages.NewToolCallDeltaValue(args)},
			{Type: messages.StreamTypeToolCallEnd, ActorProvidedIndex: 0, Value: messages.NewToolCallEndValue(callID, name, args)},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
		}
	}
}

func TestRunAgentLoopSession_ExecutesToolCallAndFeedsResultBack(t *testing.T) {
	const (
		callID     = "s14-call-42"
		toolName   = "distinctive_session_tool"
		rawArgs    = "{\"city\":\"S\\u00e3o Paulo\",\"units\":\"metric\"}"
		resultBody = "s14-result-payload"
	)

	var mu sync.Mutex
	var calls []messages.ToolCall
	inferencer := newRoundTripInferencer("follow-up referencing " + resultBody)
	defer inferencer.finishOnce.Do(func() { close(inferencer.runFinished) })
	inferencer.callEvents = toolCallEvents(callID, toolName, rawArgs)
	executor := sessionToolExecutorFunc(func(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		inferencer.signalInvoked()
		return messages.ToolCallResponse{Content: resultBody}, nil
	})

	var out lockedBuffer
	err := runAgentLoopSession(context.Background(), &out, inferencer, sessionLoopOptions{
		MaxDuration:     2 * time.Second,
		WaitForClose:    true,
		ToolExecutor:    newSessionToolExecutor(executor, time.Second),
		ToolDefinitions: []messages.ToolDefinition{{Name: toolName}},
	})
	if err != nil {
		t.Fatalf("runAgentLoopSession: %v\noutput:\n%s", err, out.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("executor invocations = %d, want exactly 1 (calls=%#v)\noutput:\n%s", len(calls), calls, out.String())
	}
	got := calls[0]
	if got.ID != callID || got.Name != toolName || got.Arguments != rawArgs {
		t.Fatalf("call = %#v, want id=%q name=%q args=%q", got, callID, toolName, rawArgs)
	}

	output := out.String()
	if !strings.Contains(output, resultBody) {
		t.Fatalf("output missing correlated tool result:\n%s", output)
	}
	if !strings.Contains(output, inferencer.followUpText) {
		t.Fatalf("session did not continue to a later model turn:\n%s", output)
	}
	// In duplex mode the executed result feeds the loop's conversation history
	// (ordering.UpdateWorldHistory) rather than being re-sent over session.Send;
	// the emitted RoleTool delta plus the follow-up model turn prove the round
	// trip through the harness.
}

func TestRunAgentLoopSession_FailingToolKeepsSessionAlive(t *testing.T) {
	const (
		callID   = "s4-call-9"
		toolName = "broken_session_tool"
	)

	var mu sync.Mutex
	count := 0
	inferencer := newRoundTripInferencer("recovered after failure")
	defer inferencer.finishOnce.Do(func() { close(inferencer.runFinished) })
	inferencer.callEvents = toolCallEvents(callID, toolName, "{}")
	executor := sessionToolExecutorFunc(func(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
		mu.Lock()
		count++
		current := count
		mu.Unlock()
		inferencer.signalInvoked()
		if current == 1 {
			return messages.ToolCallResponse{}, errors.New("disk on fire")
		}
		return messages.ToolCallResponse{}, errors.New("unexpected second invocation")
	})

	var out lockedBuffer
	err := runAgentLoopSession(context.Background(), &out, inferencer, sessionLoopOptions{
		MaxDuration:     2 * time.Second,
		WaitForClose:    true,
		ToolExecutor:    newSessionToolExecutor(executor, time.Second),
		ToolDefinitions: []messages.ToolDefinition{{Name: toolName}},
	})
	if err != nil {
		t.Fatalf("failing tool must not terminate the session, got: %v\noutput:\n%s", err, out.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Fatalf("executor invocations = %d, want exactly 1\noutput:\n%s", count, out.String())
	}
	output := out.String()
	if !strings.Contains(output, "disk on fire") {
		t.Fatalf("provider-visible failure missing for call %q:\n%s", callID, output)
	}
	if !strings.Contains(output, "recovered after failure") {
		t.Fatalf("session did not continue to a later model turn after failure:\n%s", output)
	}
	// The failure surfaced as a correlated RoleTool text delta (see output)
	// instead of an ERROR delta, which is what keeps the session alive.
}
