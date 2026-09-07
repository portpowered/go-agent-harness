package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	scheduledContinuationCallOne         = "scheduled-call-one"
	scheduledContinuationCallTwo         = "scheduled-call-two"
	scheduledContinuationToolResponseOne = "scheduled-tool-response-one"
	scheduledContinuationToolResponseTwo = "scheduled-tool-response-two"
	scheduledContinuationResponseOne     = "scheduled-continuation-one"
	scheduledContinuationResponseTwo     = "scheduled-continuation-two"
	scheduledContinuationResponseThree   = "scheduled-response-three"
)

// scheduledContinuationSession is a provider-shaped session double for the
// shipped CLI. Each input response requests one tool for the first two inputs;
// the corresponding RESPONSE.CREATE emits that call's terminal assistant
// continuation. The third input is plain assistant output.
type scheduledContinuationSession struct {
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}

	secondContinuationObserved <-chan struct{}

	closeOnce sync.Once

	mu                           sync.Mutex
	sent                         []messages.StreamMessage
	inputTurns                   int
	responseCreates              int
	toolResults                  map[string]int
	thirdInputBeforeContinuation bool
	pendingContinuations         []string
}

func newScheduledContinuationSession(secondContinuationObserved <-chan struct{}) *scheduledContinuationSession {
	return &scheduledContinuationSession{
		recv:                       messages.NewTypedBuffer[messages.StreamMessage](128),
		done:                       make(chan struct{}),
		secondContinuationObserved: secondContinuationObserved,
		toolResults:                make(map[string]int),
	}
}

func (s *scheduledContinuationSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *scheduledContinuationSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if err := ctx.Err(); err != nil {
		return scheduledContinuationContextOutcome(err)
	}

	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()

	switch msg.Type {
	case messages.StreamTypeMessageEnd:
		s.mu.Lock()
		s.inputTurns++
		inputTurn := s.inputTurns
		if inputTurn == 3 && !scheduledContinuationSignalObserved(s.secondContinuationObserved) {
			s.thirdInputBeforeContinuation = true
		}
		s.mu.Unlock()
		s.emitInputResponse(inputTurn)
	case messages.StreamTypeToolCallEnd:
		value, ok := msg.Value.(*messages.ToolCallEndValue)
		if !ok || value == nil {
			return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: errors.New("scheduled continuation result has no typed value")}
		}
		s.mu.Lock()
		s.toolResults[value.ToolCallID]++
		s.pendingContinuations = append(s.pendingContinuations, value.ToolCallID)
		s.mu.Unlock()
	case messages.StreamTypeResponseCreate:
		s.mu.Lock()
		s.responseCreates++
		var callID string
		if len(s.pendingContinuations) > 0 {
			callID = s.pendingContinuations[0]
			s.pendingContinuations = s.pendingContinuations[1:]
		}
		s.mu.Unlock()
		if callID != "" {
			s.emitContinuation(callID)
		}
	case messages.StreamTypeSessionClose:
		s.closeOnce.Do(func() { close(s.done) })
	}

	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func scheduledContinuationContextOutcome(err error) messages.SessionSendOutcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
}

func scheduledContinuationSignalObserved(signal <-chan struct{}) bool {
	if signal == nil {
		return false
	}
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

func (s *scheduledContinuationSession) emitInputResponse(inputTurn int) {
	switch inputTurn {
	case 1:
		s.emitToolResponse(scheduledContinuationToolResponseOne, scheduledContinuationCallOne)
	case 2:
		s.emitToolResponse(scheduledContinuationToolResponseTwo, scheduledContinuationCallTwo)
	case 3:
		s.emitPlainResponse(scheduledContinuationResponseThree, "third scheduled response")
	}
}

func (s *scheduledContinuationSession) emitToolResponse(responseID, callID string) {
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: callID, Value: messages.NewToolCallStartValue(callID, "scheduled_tool")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, ResponseID: responseID, ToolCallId: callID, Value: messages.NewToolCallEndValue(callID, "scheduled_tool", `{"call_id":"`+callID+`"}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		s.recv.Write(context.Background(), msg)
	}
}

func (s *scheduledContinuationSession) emitContinuation(callID string) {
	responseID := scheduledContinuationResponseOne
	text := "first scheduled continuation"
	if callID == scheduledContinuationCallTwo {
		responseID = scheduledContinuationResponseTwo
		text = "second scheduled continuation"
	}
	s.emitPlainResponse(responseID, text)
}

func (s *scheduledContinuationSession) emitPlainResponse(responseID, text string) {
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextDeltaValue(text)},
		{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewTextEndValue()},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, ResponseID: responseID, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		s.recv.Write(context.Background(), msg)
	}
}

func (s *scheduledContinuationSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *scheduledContinuationSession) Done() <-chan struct{} { return s.done }

func (s *scheduledContinuationSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *scheduledContinuationSession) snapshot() (sent []messages.StreamMessage, inputTurns, responseCreates int, toolResults map[string]int, thirdInputBeforeContinuation bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sent = append([]messages.StreamMessage(nil), s.sent...)
	toolResults = make(map[string]int, len(s.toolResults))
	for callID, count := range s.toolResults {
		toolResults[callID] = count
	}
	return sent, s.inputTurns, s.responseCreates, toolResults, s.thirdInputBeforeContinuation
}

type scheduledContinuationInferencer struct {
	ready   chan struct{}
	session *scheduledContinuationSession
}

func newScheduledContinuationInferencer(secondContinuationObserved <-chan struct{}) *scheduledContinuationInferencer {
	return &scheduledContinuationInferencer{
		ready:   make(chan struct{}),
		session: newScheduledContinuationSession(secondContinuationObserved),
	}
}

func (i *scheduledContinuationInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("scheduled-continuation-session", "test"),
	})
	i.session.recv.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("scheduled-continuation-session"),
	})
	close(i.ready)
	return i.session, nil
}

type scheduledContinuationExecutor struct {
	mu    sync.Mutex
	calls []messages.ToolCall
}

func (e *scheduledContinuationExecutor) Execute(_ context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
	return messages.ToolCallResponse{
		ToolCallID: call.ID,
		Name:       call.Name,
		Content:    "result for " + call.ID,
	}, nil
}

func (e *scheduledContinuationExecutor) callsSnapshot() []messages.ToolCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]messages.ToolCall(nil), e.calls...)
}

// TestSessionCommand_CreditsConsecutiveScheduledToolContinuations drives the
// shipped agent session command through three ordered audio inputs. The first
// two provider responses request distinct tools; each accepted result must
// retain its originating scheduled lifecycle through the terminal assistant
// continuation before the next audio input is admitted.
func TestSessionCommand_CreditsConsecutiveScheduledToolContinuations(t *testing.T) {
	secondContinuationObserved := make(chan struct{})
	inferencer := newScheduledContinuationInferencer(secondContinuationObserved)
	executor := &scheduledContinuationExecutor{}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		executor,
		&mockInferencer{response: "stateless inferencer should not be called"},
		inferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	var observedMu sync.Mutex
	var observed []messages.StreamMessage
	var continuationOneObserved = make(chan struct{})
	var continuationOneOnce sync.Once
	var continuationTwoOnce sync.Once
	agentCLI.SetSessionStreamObserver(func(msg messages.StreamMessage) {
		observedMu.Lock()
		observed = append(observed, msg)
		observedMu.Unlock()
		if msg.Type != messages.StreamTypeMessageEnd || msg.Role != messages.RoleAssistant {
			return
		}
		switch msg.ResponseID {
		case scheduledContinuationResponseOne:
			continuationOneOnce.Do(func() { close(continuationOneObserved) })
		case scheduledContinuationResponseTwo:
			continuationTwoOnce.Do(func() { close(secondContinuationObserved) })
		}
	})

	recordDir := filepath.Join(t.TempDir(), "scheduled-continuation-recording")
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime-2.1-mini",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn1.wav"),
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn2.wav"),
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn1.wav"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	runErr := rootCmd.ExecuteContext(ctx)
	if runErr != nil {
		assertExpectedSemanticLiveRunResult(t, runErr)
	}

	select {
	case <-continuationOneObserved:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for first scheduled continuation: %v", ctx.Err())
	}
	select {
	case <-secondContinuationObserved:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for second scheduled continuation: %v", ctx.Err())
	}

	sent, inputTurns, responseCreates, results, thirdInputBeforeContinuation := inferencer.session.snapshot()
	if inputTurns != 3 {
		t.Fatalf("provider observed %d scheduled input turns, want 3; sent=%v", inputTurns, sent)
	}
	if responseCreates != 2 {
		t.Fatalf("provider observed %d continuation requests, want 2; sent=%v", responseCreates, sent)
	}
	if thirdInputBeforeContinuation {
		t.Fatal("third scheduled input crossed the provider boundary before the second continuation terminal")
	}
	for _, callID := range []string{scheduledContinuationCallOne, scheduledContinuationCallTwo} {
		if results[callID] != 1 {
			t.Fatalf("provider received %d results for %q, want exactly one; sent=%v", results[callID], callID, sent)
		}
	}

	calls := executor.callsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("executor observed %d tool calls, want 2: %v", len(calls), calls)
	}
	if calls[0].ID != scheduledContinuationCallOne || calls[1].ID != scheduledContinuationCallTwo {
		t.Fatalf("executor call order = %q, %q; want %q, %q", calls[0].ID, calls[1].ID, scheduledContinuationCallOne, scheduledContinuationCallTwo)
	}

	observedMu.Lock()
	defer observedMu.Unlock()
	terminalResponses := make([]string, 0, 3)
	for _, msg := range observed {
		if msg.Type == messages.StreamTypeMessageEnd && msg.Role == messages.RoleAssistant &&
			(msg.ResponseID == scheduledContinuationResponseOne ||
				msg.ResponseID == scheduledContinuationResponseTwo ||
				msg.ResponseID == scheduledContinuationResponseThree) {
			terminalResponses = append(terminalResponses, fmt.Sprintf("%s:%s", msg.ResponseID, msg.Role))
		}
	}
	wantTerminals := []string{
		scheduledContinuationResponseOne + ":assistant",
		scheduledContinuationResponseTwo + ":assistant",
		scheduledContinuationResponseThree + ":assistant",
	}
	if len(terminalResponses) != len(wantTerminals) {
		t.Fatalf("assistant terminal responses = %v, want %v", terminalResponses, wantTerminals)
	}
	for index := range wantTerminals {
		if terminalResponses[index] != wantTerminals[index] {
			t.Fatalf("assistant terminal response %d = %q, want %q", index, terminalResponses[index], wantTerminals[index])
		}
	}
}

var _ messages.Session = (*scheduledContinuationSession)(nil)
var _ messages.SessionInferencer = (*scheduledContinuationInferencer)(nil)
var _ messages.ToolExecutor = (*scheduledContinuationExecutor)(nil)

// assertExpectedSemanticLiveRunResult preserves the semantic assertions for
// injected provider doubles while acknowledging their deliberate recording
// boundary: without a raw provider recorder, audio evidence is a partial bundle
// whose missing provider artifact is the expected result.
func assertExpectedSemanticLiveRunResult(t testing.TB, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if errors.Is(err, os.ErrNotExist) && strings.Contains(err.Error(), "finalize provider evidence") {
		return
	}
	t.Fatalf("semantic live command returned an unrelated error: %v", err)
}
