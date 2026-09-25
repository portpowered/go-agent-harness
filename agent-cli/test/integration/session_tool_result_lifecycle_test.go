package integration

import (
	"context"
	"errors"
	"fmt"
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
	// errOutput receives the command's operator diagnostics (stderr).
	errOutput io.Writer
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
	errOutput := options.errOutput
	if errOutput == nil {
		errOutput = io.Discard
	}
	root.SetErr(errOutput)
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
// the long budget, and the session keeps serving to a clean finish. The
// long-running call outlives the acknowledgement threshold, so the provider
// is asked for exactly one spoken acknowledgement, and the timed-out call's
// original error reaches the operator on stderr.

const (
	interactiveFastCallID   = "call_interactive_fast"
	interactiveFastToolName = "get_weather"
	interactiveLongCallID   = "call_interactive_long"
	interactiveLongToolName = "sleep"
	interactiveLongPayload  = `{"slept":"ok"}`
	interactiveTimeoutText  = "tool execution timed out"
	interactiveAckText      = "One moment while I check."

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
	acks         int
	ackElapsed   time.Duration
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
		if value, ok := msg.Value.(*messages.ResponseCreateValue); ok && value.IsToolAcknowledgement() {
			s.emitAcknowledgement(ctx)
			break
		}
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

// emitAcknowledgement answers an acknowledgement request with an untagged
// response; the session loop attributes it to the outstanding request.
func (s *interactiveTimeoutSession) emitAcknowledgement(ctx context.Context) {
	s.mu.Lock()
	s.acks++
	s.ackElapsed = time.Since(s.started)
	s.mu.Unlock()
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: "response-ack", Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: "response-ack", Value: messages.NewTextDeltaValue(interactiveAckText)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: "response-ack", Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		s.recv.Write(ctx, msg)
	}
}

func (s *interactiveTimeoutSession) acknowledgements() (int, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acks, s.ackElapsed
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
	var stderr lockedBuffer
	root := newLiveToolSessionRoot(t, liveToolSessionOptions{
		inferencer: interactiveTimeoutInferencer{session: session},
		executor:   executor,
		toolNames:  []string{interactiveFastToolName, interactiveLongToolName},
		output:     io.Discard,
		errOutput:  &stderr,
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
	acks, ackElapsed := session.acknowledgements()
	if acks != 1 || ackElapsed >= longElapsed {
		t.Fatalf("acknowledgements = %d at %s, want exactly one before the long-running result at %s", acks, ackElapsed, longElapsed)
	}
	diagnostic := fmt.Sprintf("tool diagnostic: tool=%q call_id=%q", interactiveFastToolName, interactiveFastCallID)
	if got := stderr.String(); !strings.Contains(got, diagnostic) || !strings.Contains(got, interactiveTimeoutText) || strings.Contains(got, interactiveLongCallID) {
		t.Fatalf("stderr = %q, want only the timed-out call's operator diagnostic %q", got, diagnostic)
	}
}

// lockedBuffer is a goroutine-safe command error writer.
type lockedBuffer struct {
	mu     sync.Mutex
	buffer strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

// followOnToolObserver holds the second assistant response.done until the
// test releases it and counts client closes.
type followOnToolObserver struct {
	secondAssistantObserved, releaseSecondAssistant, clientCloseObserved chan struct{}

	mu                    sync.Mutex
	session               *sessionToolBargeInSession
	assistantResponses    int
	closes                int
	secondOnce, closeOnce sync.Once
}

func (o *followOnToolObserver) setSession(session *sessionToolBargeInSession) {
	o.mu.Lock()
	o.session = session
	o.mu.Unlock()
}

func (o *followOnToolObserver) clientCloses() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closes
}

func (o *followOnToolObserver) observe(msg messages.StreamMessage) {
	if msg.Type == messages.StreamTypeSessionClose {
		o.mu.Lock()
		session := o.session
		o.closes++
		o.mu.Unlock()
		if session != nil {
			session.recordLifecycle("client_close")
		}
		o.closeOnce.Do(func() { close(o.clientCloseObserved) })
		return
	}
	if msg.Type != messages.StreamTypeMessageEnd || msg.Role != messages.RoleAssistant {
		return
	}
	o.mu.Lock()
	o.assistantResponses++
	response := o.assistantResponses
	o.mu.Unlock()
	if response == 2 {
		o.secondOnce.Do(func() { close(o.secondAssistantObserved) })
		<-o.releaseSecondAssistant
	}
}

// newActiveScheduledToolObserver drives the active scheduled session from its
// stream: it releases the pending barge-in continuation on the ready session
// update, marks the final grounded response, and records the client close.
func newActiveScheduledToolObserver(inferencer *sessionToolBargeInInferencer) func(messages.StreamMessage) {
	var finalResponseTextObserved bool
	return func(msg messages.StreamMessage) {
		session := inferencer.connectedSession()
		switch {
		case msg.Type == messages.StreamTypeSessionUpdated:
			value, ok := msg.Value.(*messages.SessionUpdatedValue)
			if ok && value != nil && value.SessionID == sessionToolBargeInContinuationReadyID && session != nil {
				session.emitPendingBargeInContinuation()
			}
		case msg.Role == messages.RoleAssistant && msg.Type == messages.StreamTypeTextDelta:
			value, ok := msg.Value.(*messages.TextDeltaValue)
			if ok && value != nil && msg.ResponseID == sessionToolBargeInFinalResponseID && value.Content == sessionToolBargeInFinalResponseText {
				finalResponseTextObserved = true
			}
		case msg.Role == messages.RoleAssistant && msg.Type == messages.StreamTypeMessageEnd:
			if msg.ResponseID == sessionToolBargeInFinalResponseID && finalResponseTextObserved && session != nil {
				session.markFinalResponseObserved()
			}
		case msg.Type == messages.StreamTypeSessionClose:
			if session != nil {
				session.recordLifecycle("client_close")
			}
		}
	}
}

// countSessionToolBargeInSent counts the correlated tool results and response
// cancellations the client sent to the provider.
func countSessionToolBargeInSent(session *sessionToolBargeInSession) (results, cancels int) {
	for _, msg := range session.sentSnapshot() {
		switch msg.Type {
		case messages.StreamTypeToolCallEnd:
			value, ok := msg.Value.(*messages.ToolCallEndValue)
			if ok && value != nil && value.ToolCallID == sessionToolBargeInCallID {
				results++
			}
		case messages.StreamTypeResponseCancel:
			cancels++
		}
	}
	return results, cancels
}
