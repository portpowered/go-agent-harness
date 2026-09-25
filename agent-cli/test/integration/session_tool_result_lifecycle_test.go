package integration

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	serviceTools "github.com/portpowered/go-agent-harness/agent-cli/internal/services/tools"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/spf13/cobra"
)

// sessionLifecycleSafetyTimeout bounds each lifecycle wait in the hermetic
// tool-result session tests.
const sessionLifecycleSafetyTimeout = 10 * time.Second

func waitLifecycleSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(sessionLifecycleSafetyTimeout):
		t.Fatalf("timed out waiting for %s after %s", name, sessionLifecycleSafetyTimeout)
	}
}

// liveToolSessionOptions describes one hermetic session run through the
// composed CLI with the application's session-inferencer and tool-service
// ports replaced by test doubles.
type liveToolSessionOptions struct {
	inferencer messages.SessionInferencer
	executor   messages.ToolExecutor
	toolNames  []string
	observer   func(messages.StreamMessage)
	output     io.Writer
	// inputPCM, when present, is admitted as one scheduled customer audio
	// turn (--audio-in-turn, which requires a --record-dir bundle).
	inputPCM []byte
	args     []string
}

// newLiveToolSessionRoot composes the production CLI root with hermetic
// session ports and the invocation arguments for one live tool session.
func newLiveToolSessionRoot(t *testing.T, options liveToolSessionOptions) *cobra.Command {
	t.Helper()
	definitions := make([]messages.ToolDefinition, 0, len(options.toolNames))
	for _, name := range options.toolNames {
		definitions = append(definitions, messages.ToolDefinition{Name: name, Description: "Hermetic " + name + " fixture."})
	}
	capabilities := serviceTools.Factory(func(*config.Config) (serviceTools.Capabilities, error) {
		return serviceTools.Capabilities{Executor: options.executor, Definitions: definitions}, nil
	})
	agentCLI, err := wire.InitializeMockAgentCLIWithPorts(
		wire.NewToolServicePort(capabilities),
		wire.NewPortSwap(wire.PortInferencer, &mockInferencer{response: "unused"}),
		wire.NewPortSwap(wire.PortSessionInferencer, options.inferencer),
	)
	if err != nil {
		t.Fatalf("initialize live tool session CLI: %v", err)
	}
	if options.observer != nil {
		agentCLI.SetSessionStreamObserver(options.observer)
	}
	output := options.output
	if output == nil {
		output = io.Discard
	}
	root := agentCLI.Generate()
	root.SetOut(output)
	root.SetErr(io.Discard)
	args := append([]string{"--config-dir", t.TempDir(), "session"}, options.args...)
	if len(options.inputPCM) > 0 {
		inputPath := filepath.Join(t.TempDir(), "customer-turn.wav")
		writeAsyncCollisionInputWAV(t, inputPath, options.inputPCM)
		args = append(args, "--audio-in-turn", inputPath, "--record-dir", filepath.Join(t.TempDir(), "recording"))
	}
	root.SetArgs(args)
	return root
}

// Live-runtime port of the v4d tool-timeout vertical: the configured
// tools.interactive policy bounds every live tool call through the composed
// CLI. A fast/read call whose executor ignores its context is ended by the
// fast deadline and returned to the provider as a correlated failure, while a
// bounded-long-running call slower than that deadline still completes under
// the long budget, and the session keeps serving to a clean finish.

const (
	interactiveFastCallID   = "call_interactive_fast"
	interactiveFastToolName = "get_weather"
	interactiveLongCallID   = "call_interactive_long"
	interactiveLongToolName = "sleep"
	interactiveLongPayload  = `{"slept":"ok"}`
	interactiveTimeoutText  = "tool execution timed out"

	interactiveFastTimeout = 100 * time.Millisecond
	// interactiveLongWork exceeds the fast deadline but stays well inside the
	// long-running budget, so only the class-specific bound lets it finish.
	interactiveLongWork   = 300 * time.Millisecond
	interactiveRunTimeout = 10 * time.Second
)

// interactiveTimeoutSession requests one fast/read and one long-running call,
// records the provider-visible results, and answers the continuation.
type interactiveTimeoutSession struct {
	recv         *messages.TypedBuffer[messages.StreamMessage]
	done         chan struct{}
	closeOnce    sync.Once
	responseOnce sync.Once
	continueOnce sync.Once
	started      time.Time
	mu           sync.Mutex
	results      map[string]string
	elapsed      map[string]time.Duration
}

func newInteractiveTimeoutSession() *interactiveTimeoutSession {
	return &interactiveTimeoutSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](64), done: make(chan struct{}),
		started: time.Now(), results: map[string]string{}, elapsed: map[string]time.Duration{},
	}
}

func (s *interactiveTimeoutSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if ctx.Err() != nil {
		return false
	}
	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		s.responseOnce.Do(s.emitToolCalls)
	case messages.StreamTypeResponseCreate:
		s.continueOnce.Do(s.emitContinuation)
	case messages.StreamTypeToolCallEnd:
		if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
			s.mu.Lock()
			s.results[value.ToolCallID] = value.Arguments
			s.elapsed[value.ToolCallID] = time.Since(s.started)
			s.mu.Unlock()
		}
	}
	return true
}

func (s *interactiveTimeoutSession) emitToolCalls() {
	s.mu.Lock()
	s.started = time.Now()
	s.mu.Unlock()
	s.write(messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()})
	for _, call := range [][2]string{{interactiveFastCallID, interactiveFastToolName}, {interactiveLongCallID, interactiveLongToolName}} {
		s.write(
			messages.StreamMessage{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ToolCallId: call[0], Value: messages.NewToolCallStartValue(call[0], call[1])},
			messages.StreamMessage{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ToolCallId: call[0], Value: messages.NewToolCallEndValue(call[0], call[1], `{}`)},
		)
	}
	s.write(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}

func (s *interactiveTimeoutSession) emitContinuation() {
	s.write(
		messages.StreamMessage{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		messages.StreamMessage{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("Recovered from the delayed lookup.")},
		messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	)
}

func (s *interactiveTimeoutSession) write(msgs ...messages.StreamMessage) {
	for _, msg := range msgs {
		s.recv.Write(context.Background(), msg)
	}
}

func (s *interactiveTimeoutSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *interactiveTimeoutSession) Done() <-chan struct{} { return s.done }

func (s *interactiveTimeoutSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *interactiveTimeoutSession) result(callID string) (string, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, ok := s.results[callID]
	return content, s.elapsed[callID], ok
}

type interactiveTimeoutInferencer struct{ session *interactiveTimeoutSession }

func (i interactiveTimeoutInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("interactive-timeout", "test")},
		{Type: messages.StreamTypeSessionUpdated, Value: messages.NewSessionUpdatedValue("interactive-timeout")},
	} {
		i.session.recv.Write(ctx, msg)
	}
	return i.session, nil
}

// interactiveTimeoutExecutor hangs the fast/read call without honoring its
// context, so only the session deadline can end it, and makes the
// long-running call outlast the fast deadline.
type interactiveTimeoutExecutor struct{ release chan struct{} }

func (e interactiveTimeoutExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if call.Name == interactiveFastToolName {
		<-e.release
		return messages.ToolCallResponse{}, errors.New("released after the test")
	}
	timer := time.NewTimer(interactiveLongWork)
	defer timer.Stop()
	select {
	case <-timer.C:
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, Content: interactiveLongPayload}, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

func TestSessionInteractiveToolPolicyBoundsLiveToolCalls(t *testing.T) {
	t.Setenv("AGENT_TOOLS__INTERACTIVE__FAST_READ_TIMEOUT", interactiveFastTimeout.String())
	t.Setenv("AGENT_TOOLS__INTERACTIVE__LONG_RUNNING_TIMEOUT", "5s")
	t.Setenv("AGENT_TOOLS__INTERACTIVE__ACKNOWLEDGEMENT_THRESHOLD", "50ms")
	session := newInteractiveTimeoutSession()
	executor := interactiveTimeoutExecutor{release: make(chan struct{})}
	defer close(executor.release)
	root := newLiveToolSessionRoot(t, liveToolSessionOptions{
		inferencer: interactiveTimeoutInferencer{session: session},
		executor:   executor,
		toolNames:  []string{interactiveFastToolName, interactiveLongToolName},
		output:     io.Discard,
		inputPCM:   []byte{1, 2, 3, 4},
		args: []string{
			"--provider", "openai", "--model", "gpt-realtime", "--api-key", "test-key", "--experimental-tools",
			"--record", filepath.Join(t.TempDir(), "interactive-timeout.session.json"),
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), interactiveRunTimeout)
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- root.ExecuteContext(ctx) }()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("session with a timed-out tool must keep serving to a clean finish: %v", err)
		}
	case <-time.After(2 * interactiveRunTimeout):
		// A context-ignoring tool can only be ended by the interactive bound.
		t.Fatalf("session did not finish within %s; the interactive policy did not bound the hanging call", 2*interactiveRunTimeout)
	}
	fast, fastElapsed, ok := session.result(interactiveFastCallID)
	if !ok || !strings.Contains(fast, interactiveTimeoutText) {
		t.Fatalf("fast/read result = %q (delivered=%v), want the correlated %q failure", fast, ok, interactiveTimeoutText)
	}
	if fastElapsed < interactiveFastTimeout || fastElapsed >= interactiveLongWork+interactiveFastTimeout {
		t.Fatalf("fast/read timeout crossed after %s, want the configured %s bound", fastElapsed, interactiveFastTimeout)
	}
	long, longElapsed, ok := session.result(interactiveLongCallID)
	if !ok || long != interactiveLongPayload {
		t.Fatalf("long-running result = %q (delivered=%v), want %q under the long budget", long, ok, interactiveLongPayload)
	}
	if longElapsed < interactiveLongWork {
		t.Fatalf("long-running result crossed after %s, before its %s of work", longElapsed, interactiveLongWork)
	}
}
