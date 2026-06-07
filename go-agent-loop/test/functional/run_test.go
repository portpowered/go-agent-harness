package functional

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// ---------------------------------------------------------------------------
// safeBuffer — thread-safe writer/reader for output collection
// ---------------------------------------------------------------------------

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---------------------------------------------------------------------------
// RunScenario — harness for turn-taking (Run) tests
// ---------------------------------------------------------------------------

// RunScenario wraps a turn-taking AgentLoop with a running context and an
// output buffer. Use Send to inject user messages and WaitForOutput to observe
// responses written to the configured output sink.
type RunScenario struct {
	t        *testing.T
	Loop     agentloop.AgenticLoop
	Inf      *MockInferencer
	Tool     *MockToolExecutor
	Output   *safeBuffer
	ctx      context.Context
	cancel   context.CancelFunc
	runErr   chan error
	stopOnce sync.Once
	stopErr  error
}

// NewRunScenario creates a loop in ModeTurnTaking, registers an output writer,
// starts Run in a background goroutine, and registers a test cleanup that
// cancels the context and waits for Run to exit.
func NewRunScenario(t *testing.T, inf *MockInferencer, tool *MockToolExecutor, opts ...agentloop.Option) *RunScenario {
	t.Helper()

	out := &safeBuffer{}

	allOpts := []agentloop.Option{
		agentloop.WithInferencer(inf),
		agentloop.WithToolExecutor(tool),
		agentloop.WithMode(engine.ModeTurnTaking),
	}
	allOpts = append(allOpts, opts...)

	loop, err := agentloop.New(allOpts...)
	if err != nil {
		t.Fatalf("NewRunScenario: failed to create loop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)

	rs := &RunScenario{
		t:      t,
		Loop:   loop,
		Inf:    inf,
		Tool:   tool,
		Output: out,
		ctx:    ctx,
		cancel: cancel,
		runErr: make(chan error, 1),
	}

	if err := loop.SetOutputs(ctx, []agentloop.Output{{Writer: out, Label: "test"}}); err != nil {
		cancel()
		t.Fatalf("NewRunScenario: SetOutputs: %v", err)
	}

	go func() {
		rs.runErr <- loop.Run(ctx)
	}()

	t.Cleanup(func() { _ = rs.Stop() })

	return rs
}

// Send injects a user message into the running loop.
func (rs *RunScenario) Send(message string) {
	rs.t.Helper()
	msg := messages.NewTextMessage(messages.RoleUser, message)
	if err := rs.Loop.Send(rs.ctx, []messages.Message{msg}); err != nil {
		rs.t.Fatalf("RunScenario.Send(%q): %v", message, err)
	}
}

// WaitForOutput polls until the output buffer contains want or timeout elapses.
// Returns true if the text appeared within the deadline.
func (rs *RunScenario) WaitForOutput(want string, timeout time.Duration) bool {
	rs.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s := rs.Output.String(); len(s) >= len(want) && containsString(s, want) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// Stop cancels the loop context and waits for Run to return. It is idempotent;
// subsequent calls return the same error as the first.
func (rs *RunScenario) Stop() error {
	rs.stopOnce.Do(func() {
		rs.cancel()
		select {
		case rs.stopErr = <-rs.runErr:
		case <-time.After(5 * time.Second):
			rs.t.Error("RunScenario.Stop: Run did not exit within 5s")
		}
	})
	return rs.stopErr
}

// containsString reports whether s contains substr.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRun_RejectsNonTurnTakingMode verifies that Run returns an error
// immediately when the loop was not created with WithMode(engine.ModeTurnTaking).
func TestRun_RejectsNonTurnTakingMode(t *testing.T) {
	inf := new(MockInferencer).AddTextResponse("hello")
	tool := NewMockToolExecutor()

	// Default mode is ModeAskOnce; Run must reject it.
	s := NewScenario(t, inf, tool)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := s.Loop.Run(ctx)
	if err == nil {
		t.Fatal("Run on ModeAskOnce loop: expected error, got nil")
	}
}

// TestRun_ExitsOnContextCancellation verifies that Run returns context.Canceled
// when the context is cancelled while the loop is idle.
func TestRun_ExitsOnContextCancellation(t *testing.T) {
	inf := new(MockInferencer).AddTextResponse("hello")
	tool := NewMockToolExecutor()

	rs := NewRunScenario(t, inf, tool)
	err := rs.Stop()

	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run exit error: got %v, want context.Canceled or context.DeadlineExceeded", err)
	}
}

// TestRun_SingleTurnForwardsResponse verifies the canonical turn-taking flow:
// Send injects a user message; the model responds; Run forwards the response
// text to all registered output writers.
func TestRun_SingleTurnForwardsResponse(t *testing.T) {
	const userMessage = "what is two plus two?"
	const modelResponse = "2+2 = 4"

	inf := new(MockInferencer).AddTextResponse(modelResponse)
	tool := NewMockToolExecutor()

	rs := NewRunScenario(t, inf, tool)
	rs.Send(userMessage)

	if !rs.WaitForOutput(modelResponse, 5*time.Second) {
		t.Errorf("output after first turn: got %q, want %q", rs.Output.String(), modelResponse)
	}

	if inf.CallCount() != 1 {
		t.Errorf("inference calls: got %d, want 1", inf.CallCount())
	}
}

// TestRun_MultiTurnForwardsAllResponses verifies that two sequential Send/response
// cycles both deliver their model responses to the output writer.
func TestRun_MultiTurnForwardsAllResponses(t *testing.T) {
	const msg1, resp1 = "My name is Bob.", "Nice to meet you, Bob!"
	const msg2, resp2 = "What is my name?", "Your name is Bob."

	inf := new(MockInferencer).
		AddTextResponse(resp1).
		AddTextResponse(resp2)
	tool := NewMockToolExecutor()

	rs := NewRunScenario(t, inf, tool)

	rs.Send(msg1)
	if !rs.WaitForOutput(resp1, 5*time.Second) {
		t.Fatalf("turn 1 response not received: got %q", rs.Output.String())
	}

	rs.Send(msg2)
	if !rs.WaitForOutput(resp2, 5*time.Second) {
		t.Fatalf("turn 2 response not received: got %q", rs.Output.String())
	}

	if inf.CallCount() != 2 {
		t.Errorf("inference calls: got %d, want 2", inf.CallCount())
	}
}

// TestRun_ConversationHistoryAccumulates verifies that each turn's messages are
// appended to the conversation history so subsequent inference calls receive
// the full prior context (matching the multi-turn behaviour of Execute).
func TestRun_ConversationHistoryAccumulates(t *testing.T) {
	const msg1, resp1 = "My name is Carol.", "Hello, Carol!"
	const msg2, resp2 = "Repeat my name.", "Your name is Carol."

	inf := new(MockInferencer).
		AddTextResponse(resp1).
		AddTextResponse(resp2)
	tool := NewMockToolExecutor()

	rs := NewRunScenario(t, inf, tool)

	rs.Send(msg1)
	if !rs.WaitForOutput(resp1, 5*time.Second) {
		t.Fatalf("turn 1 response not received")
	}

	rs.Send(msg2)
	if !rs.WaitForOutput(resp2, 5*time.Second) {
		t.Fatalf("turn 2 response not received")
	}

	// Verify turn 2's inference call received turn 1's assistant response.
	if len(inf.CapturedMessages) < 2 {
		t.Fatalf("expected ≥2 captured message slices, got %d", len(inf.CapturedMessages))
	}
	hasTurn1Response := false
	for _, m := range inf.CapturedMessages[1] {
		if m.Role == messages.RoleAssistant && m.TextContent() == resp1 {
			hasTurn1Response = true
			break
		}
	}
	if !hasTurn1Response {
		t.Error("turn 2 inference did not receive turn 1 assistant response in history")
	}

	// Full history after two turns: user1 → assistant1 → user2 → assistant2.
	history := rs.Loop.GetConversationHistory()
	AssertMessages(t, history, []ExpectedMessage{
		{Role: messages.RoleUser, Text: msg1},
		{Role: messages.RoleAssistant, Text: resp1},
		{Role: messages.RoleUser, Text: msg2},
		{Role: messages.RoleAssistant, Text: resp2},
	})
}

// TestRun_WithSystemPrompt verifies that a system prompt configured via
// WithSystemPrompt is present in the conversation history exactly once and
// that the first inference call receives it as the leading message.
func TestRun_WithSystemPrompt(t *testing.T) {
	const systemPrompt = "You are a helpful assistant."
	const userMsg = "Hello"
	const modelResp = "Hi there!"

	inf := new(MockInferencer).AddTextResponse(modelResp)
	tool := NewMockToolExecutor()

	rs := NewRunScenario(t, inf, tool, agentloop.WithSystemPrompt(systemPrompt))

	rs.Send(userMsg)
	if !rs.WaitForOutput(modelResp, 5*time.Second) {
		t.Fatalf("response not received: got %q", rs.Output.String())
	}

	history := rs.Loop.GetConversationHistory()
	systemCount := 0
	for _, m := range history {
		if m.Role == messages.RoleSystem {
			systemCount++
		}
	}
	if systemCount != 1 {
		t.Errorf("system message count in history: got %d, want 1", systemCount)
	}

	// The first inference call must lead with the system prompt.
	if len(inf.CapturedMessages) == 0 {
		t.Fatal("no inference calls captured")
	}
	if len(inf.CapturedMessages[0]) == 0 || inf.CapturedMessages[0][0].Role != messages.RoleSystem {
		t.Error("first message in inference call was not the system prompt")
	}
}

// TestRun_MultipleOutputWriters verifies that when multiple output writers are
// registered via SetOutputs, each receives the model response text.
func TestRun_MultipleOutputWriters(t *testing.T) {
	const userMsg = "ping"
	const modelResp = "pong"

	inf := new(MockInferencer).AddTextResponse(modelResp)
	tool := NewMockToolExecutor()

	out1 := &safeBuffer{}
	out2 := &safeBuffer{}

	allOpts := []agentloop.Option{
		agentloop.WithInferencer(inf),
		agentloop.WithToolExecutor(tool),
		agentloop.WithMode(engine.ModeTurnTaking),
	}
	loop, err := agentloop.New(allOpts...)
	if err != nil {
		t.Fatalf("failed to create loop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	if err := loop.SetOutputs(ctx, []agentloop.Output{
		{Writer: out1, Label: "out1"},
		{Writer: out2, Label: "out2"},
	}); err != nil {
		t.Fatalf("SetOutputs: %v", err)
	}

	runErr := make(chan error, 1)
	go func() { runErr <- loop.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-runErr
	})

	if err := loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, userMsg)}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if containsString(out1.String(), modelResp) && containsString(out2.String(), modelResp) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !containsString(out1.String(), modelResp) {
		t.Errorf("out1: got %q, want %q", out1.String(), modelResp)
	}
	if !containsString(out2.String(), modelResp) {
		t.Errorf("out2: got %q, want %q", out2.String(), modelResp)
	}
}
