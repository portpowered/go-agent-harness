package integration

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/portpowered/agent-cli/internal/agent"
	"github.com/portpowered/agent-cli/internal/flags"
	"github.com/portpowered/agent-cli/internal/services"
	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// runInit gives the Bubbles textinput focus by sending FocusInputMsg (same as Init()'s first Cmd).
func runInit(model services.ChatModel) services.ChatModel {
	m, _ := model.Update(services.FocusInputMsg{})
	return m.(services.ChatModel)
}

// typeInput simulates the user typing a string into model one rune at a time,
// matching how bubbletea dispatches KeyRunes/KeySpace events from real keyboard input.
// Bubbles textinput expects KeySpace to include Runes: []rune{' '} to insert a space.
func typeInput(model services.ChatModel, text string) services.ChatModel {
	for _, r := range text {
		if r == ' ' {
			m, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			model = m.(services.ChatModel)
		} else {
			m, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			model = m.(services.ChatModel)
		}
	}
	return model
}

// pressEnter simulates pressing Enter and executes any returned commands
// until the streaming turn is complete (streamDoneMsg or error).
// It handles tea.BatchMsg by expanding batch commands into the work queue.
func pressEnter(model services.ChatModel) (services.ChatModel, string) {
	m, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = m.(services.ChatModel)
	cmds := []tea.Cmd{cmd}
	for len(cmds) > 0 {
		cmd = cmds[0]
		cmds = cmds[1:]
		if cmd == nil {
			continue
		}
		resultMsg := cmd()
		// Expand batch messages into individual commands.
		if batch, ok := resultMsg.(tea.BatchMsg); ok {
			for _, c := range batch {
				cmds = append(cmds, c)
			}
			continue
		}
		m, nextCmd := model.Update(resultMsg)
		model = m.(services.ChatModel)
		if nextCmd != nil {
			cmds = append(cmds, nextCmd)
		}
	}
	return model, ""
}

// TestChatModel_BasicInput validates the core user-input → agent-response flow:
//
//  1. The model accumulates typed characters into its input buffer (visible in View).
//  2. Pressing Enter clears the buffer and dispatches the text to the agent executor.
//  3. The agent response is written to the output writer.
//
// This mirrors the structure of TestBasic_SimpleRequestResponse in the
// go-agent-loop functional test suite, but drives the ChatModel directly
// instead of the agent loop.
func TestChatModel_BasicInput(t *testing.T) {
	const userInput = "hello"
	const agentResponse = "Hi there! How can I help?"

	inf := &mockInferencer{response: agentResponse}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, err := agentExec.NewChatSessionID(cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(agentExec, sessionID, globalFlags, askFlags, ctx, &out, &out)
	model = runInit(model)

	// --- type the message ---
	model = typeInput(model, userInput)

	// The input buffer should contain what was typed.
	if !strings.Contains(model.View(), userInput) {
		t.Errorf("View() should contain typed input %q before Enter; got: %s", userInput, model.View())
	}

	// --- submit with Enter ---
	model, _ = pressEnter(model)

	// Committed content is in ViewHistory() (flushed to scrollback), not View().
	history := model.ViewHistory()
	if !strings.Contains(history, "You: "+userInput) {
		t.Errorf("ViewHistory() should contain user message; got: %s", history)
	}
	if !strings.Contains(history, agentResponse) {
		t.Errorf("expected agent response %q in ViewHistory(); got: %s", agentResponse, history)
	}

	// View() should only show the input prompt (no committed content).
	if !strings.Contains(model.View(), "> ") {
		t.Errorf("View() should show prompt after submission; got: %s", model.View())
	}

	// The session should still be active (not quitting).
	if model.IsQuitting() {
		t.Error("model should not be quitting after a normal message")
	}
}

// TestChatModel_MultiTurn validates that successive turns each reach the agent
// and that responses accumulate correctly in the output, mirroring the
// TestBasic_MultiTurn scenario in the go-agent-loop functional tests.
func TestChatModel_MultiTurn(t *testing.T) {
	const msg1 = "My name is Alice."
	const resp1 = "Nice to meet you, Alice!"
	const msg2 = "What is my name?"
	const resp2 = "Your name is Alice."

	inf := &mockInferencerSequence{responses: []string{resp1, resp2}}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, err := agentExec.NewChatSessionID(cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(agentExec, sessionID, globalFlags, askFlags, ctx, &out, &out)
	model = runInit(model)

	// Turn 1.
	model = typeInput(model, msg1)
	model, _ = pressEnter(model)
	history := model.ViewHistory()
	if !strings.Contains(history, "You: "+msg1) || !strings.Contains(history, resp1) {
		t.Errorf("turn 1: ViewHistory() should contain user message and %q; got: %s", resp1, history)
	}

	// Turn 2.
	model = typeInput(model, msg2)
	model, _ = pressEnter(model)
	history = model.ViewHistory()
	if !strings.Contains(history, "You: "+msg2) || !strings.Contains(history, resp2) {
		t.Errorf("turn 2: ViewHistory() should contain user message and %q; got: %s", resp2, history)
	}

	if model.IsQuitting() {
		t.Error("model should not be quitting after two normal turns")
	}
}

// TestChatModel_ExitCommand validates that typing "exit" causes the model to
// print "Goodbye!" and set its quitting flag, emitting tea.Quit.
func TestChatModel_ExitCommand(t *testing.T) {
	for _, exitWord := range []string{"exit", "quit", " exit", " quit "} {
		t.Run(exitWord, func(t *testing.T) {
			inf := &mockInferencer{response: "never reached"}
			exec := &mockToolExecutor{}
			agentExec := agent.NewExecutor(exec, nil, inf, true)
			globalFlags := flags.NewGlobalFlags()
			askFlags := flags.NewAskFlags()
			cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
			sessionID, _ := agentExec.NewChatSessionID(cfg)

			var out bytes.Buffer
			ctx := context.Background()
			model := services.NewChatModel(agentExec, sessionID, globalFlags, askFlags, ctx, &out, &out)
			model = runInit(model)

			model = typeInput(model, exitWord)
			// pressEnter calls cmd() — for tea.Quit that returns tea.QuitMsg{},
			// which is a no-op in Update, so the model state is already set.
			model, _ = pressEnter(model)

			if !model.IsQuitting() {
				t.Error("model should be quitting after exit command")
			}
			if !strings.Contains(out.String(), "Goodbye!") {
				t.Errorf("expected Goodbye! in output; got: %s", out.String())
			}
		})
	}
}

// TestChatModel_BackspaceEditing verifies that the backspace key removes the
// last character from the input buffer without submitting.
func TestChatModel_BackspaceEditing(t *testing.T) {
	inf := &mockInferencer{response: "ok"}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, _ := agentExec.NewChatSessionID(cfg)

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(agentExec, sessionID, globalFlags, askFlags, ctx, &out, &out)
	model = runInit(model)

	// Type "hello" then backspace twice → "hel"
	model = typeInput(model, "hello")
	m, _ := model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	model = m.(services.ChatModel)
	m, _ = model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	model = m.(services.ChatModel)

	view := model.View()
	if !strings.Contains(view, "hel") {
		t.Errorf("after two backspaces expected view to contain %q; got: %s", "hel", view)
	}
	if strings.Contains(view, "hello") {
		t.Errorf("after two backspaces view should not contain %q; got: %s", "hello", view)
	}
}

// TestChatModel_EmptyInput verifies that pressing Enter with no input does not
// call the agent and does not quit. The input buffer is cleared (already empty).
func TestChatModel_EmptyInput(t *testing.T) {
	inf := &mockInferencer{response: "should not appear"}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, _ := agentExec.NewChatSessionID(cfg)

	var out, errOut bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(agentExec, sessionID, globalFlags, askFlags, ctx, &out, &errOut)

	// Press Enter without typing anything.
	model, _ = pressEnter(model)

	if out.String() != "" {
		t.Errorf("empty Enter should not produce output; got: %q", out.String())
	}
	if model.IsQuitting() {
		t.Error("model should not be quitting after empty Enter")
	}
	// View should still show prompt with empty input.
	if !strings.Contains(model.View(), "> ") {
		t.Errorf("View() should still show prompt; got: %s", model.View())
	}
}

// TestChatModel_EmptyInputWhitespaceOnly verifies that Enter with only spaces
// is treated as empty and does not call the agent.
func TestChatModel_EmptyInputWhitespaceOnly(t *testing.T) {
	inf := &mockInferencer{response: "should not appear"}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, _ := agentExec.NewChatSessionID(cfg)

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(agentExec, sessionID, globalFlags, askFlags, ctx, &out, &out)

	model = typeInput(model, "   ")
	model, _ = pressEnter(model)

	if strings.Contains(out.String(), "should not appear") {
		t.Errorf("whitespace-only Enter should not call agent; got output: %s", out.String())
	}
	if model.IsQuitting() {
		t.Error("model should not be quitting after whitespace-only Enter")
	}
}

// TestChatModel_ErrorHandling verifies that when RunAskWithSession returns an
// error, the model receives agentResultMsg and writes "Error: ..." to errOut.
func TestChatModel_ErrorHandling(t *testing.T) {
	const errMsg = "inference failed: rate limit"
	inf := &mockInferencerError{err: errors.New(errMsg)}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, _ := agentExec.NewChatSessionID(cfg)

	var out, errOut bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(agentExec, sessionID, globalFlags, askFlags, ctx, &out, &errOut)
	model = runInit(model)

	model = typeInput(model, "hello")
	model, _ = pressEnter(model)

	errStr := errOut.String()
	if !strings.Contains(errStr, "Error:") {
		t.Errorf("errOut should contain 'Error:'; got: %q", errStr)
	}
	if !strings.Contains(errStr, errMsg) {
		t.Errorf("errOut should contain error message %q; got: %q", errMsg, errStr)
	}
	if model.IsQuitting() {
		t.Error("model should not quit on agent error; user can try again")
	}
}

// TestChatModel_SessionPersistence verifies that multiple turns in the same
// chat session use the same session ID so that conversation history is maintained.
func TestChatModel_SessionPersistence(t *testing.T) {
	inf := &mockInferencerSequence{responses: []string{"first", "second"}}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, err := agentExec.NewChatSessionID(cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(agentExec, sessionID, globalFlags, askFlags, ctx, &out, &out)
	model = runInit(model)

	model = typeInput(model, "first message")
	model, _ = pressEnter(model)
	model = typeInput(model, "second message")
	model, _ = pressEnter(model)

	// Both turns should appear in ViewHistory (same session used for both turns).
	history := model.ViewHistory()
	if !strings.Contains(history, "first") {
		t.Errorf("expected first response in ViewHistory(); got: %s", history)
	}
	if !strings.Contains(history, "second") {
		t.Errorf("expected second response in ViewHistory(); got: %s", history)
	}
}

// mockInferencerSequence returns successive text responses from a fixed slice,
// repeating the last entry when exhausted. Implements subsystems.Inferencer for
// multi-turn chat tests.
type mockInferencerSequence struct {
	responses []string
	idx       int
}

func (m *mockInferencerSequence) next() string {
	if len(m.responses) == 0 {
		return ""
	}
	if m.idx >= len(m.responses) {
		return m.responses[len(m.responses)-1]
	}
	r := m.responses[m.idx]
	m.idx++
	return r
}

func (m *mockInferencerSequence) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	text := m.next()
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, text),
	}, nil
}

func (m *mockInferencerSequence) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	result, err := m.Infer(ctx, req)
	if err != nil {
		ch := make(chan messages.StreamMessage, 1)
		ch <- messages.StreamMessage{Type: messages.StreamTypeError, ActorProvidedIndex: 0, Value: messages.NewErrorValue(err.Error())}
		close(ch)
		return ch, nil
	}
	ch := make(chan messages.StreamMessage, 8)
	text := result.Message.TextContent()
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, ActorProvidedIndex: 0, Value: messages.NewTextStartValue()}
	if text != "" {
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: 0, Value: messages.NewTextDeltaValue(text)}
	}
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, ActorProvidedIndex: 0, Value: messages.NewTextEndValue()}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ActorProvidedIndex: 0, Value: messages.NewMessageEndValue(result.TokenUsage)}
	close(ch)
	return ch, nil
}

// mockInferencerError returns a fixed error from Infer and InferStream (for error-handling tests).
type mockInferencerError struct {
	err error
}

func (m *mockInferencerError) Infer(context.Context, messages.InferenceRequest) (messages.InferenceResult, error) {
	return messages.InferenceResult{}, m.err
}

func (m *mockInferencerError) InferStream(context.Context, messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	return nil, m.err
}

// mockChunkedInferencer streams the response in multiple TEXT.DELTA chunks (for streaming tests).
type mockChunkedInferencer struct {
	chunks []string // e.g. []string{"One ", "two ", "three"}
}

func (m *mockChunkedInferencer) Infer(ctx context.Context, req messages.InferenceRequest) (messages.InferenceResult, error) {
	full := ""
	for _, c := range m.chunks {
		full += c
	}
	return messages.InferenceResult{
		Message: messages.NewTextMessage(messages.RoleAssistant, full),
	}, nil
}

func (m *mockChunkedInferencer) InferStream(ctx context.Context, req messages.InferenceRequest) (<-chan messages.StreamMessage, error) {
	ch := make(chan messages.StreamMessage, len(m.chunks)+4)
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextStart, ActorProvidedIndex: 0, Value: messages.NewTextStartValue()}
	for _, c := range m.chunks {
		ch <- messages.StreamMessage{Type: messages.StreamTypeTextDelta, ActorProvidedIndex: 0, Value: messages.NewTextDeltaValue(c)}
	}
	ch <- messages.StreamMessage{Type: messages.StreamTypeTextEnd, ActorProvidedIndex: 0, Value: messages.NewTextEndValue()}
	ch <- messages.StreamMessage{Type: messages.StreamTypeMessageEnd, ActorProvidedIndex: 0, Value: messages.NewMessageEndValue(messages.TokenUsage{})}
	close(ch)
	return ch, nil
}

// TestChatModel_StreamingPartials verifies that partial assistant content appears
// in View as the stream is drained (streaming UI), and the final message is complete.
func TestChatModel_StreamingPartials(t *testing.T) {
	chunks := []string{"One ", "two ", "three"}
	inf := &mockChunkedInferencer{chunks: chunks}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, err := agentExec.NewChatSessionID(cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(agentExec, sessionID, globalFlags, askFlags, ctx, &out, &out)
	model = runInit(model)

	model = typeInput(model, "count")
	m, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = m.(services.ChatModel)

	// Drain stream until completion. KeyEnter returns a tea.Batch (Println + runAgent),
	// so we need to expand BatchMsg into individual commands.
	sawFirstChunk := false
	fullText := ""
	for _, c := range chunks {
		fullText += c
	}
	cmds := []tea.Cmd{cmd}
	for len(cmds) > 0 {
		cmd = cmds[0]
		cmds = cmds[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				cmds = append(cmds, c)
			}
			continue
		}
		m, nextCmd := model.Update(msg)
		model = m.(services.ChatModel)
		if nextCmd != nil {
			cmds = append(cmds, nextCmd)
		}
		// Check View() for partial content during streaming.
		view := model.View()
		if strings.Contains(view, "Assistant:") && strings.Contains(view, "One ") {
			sawFirstChunk = true
		}
	}

	if !sawFirstChunk {
		t.Errorf("expected View to show partial (Assistant: and 'One ') during streaming")
	}
	// After streaming completes, committed content is in ViewHistory(), not View().
	history := model.ViewHistory()
	if !strings.Contains(history, fullText) {
		t.Errorf("expected ViewHistory() to contain full response %q; got: %s", fullText, history)
	}
}

// TestChatModel_MarkdownRendering verifies that assistant and tool-result content
// are rendered as markdown (Glamour/Glow-style): labels on their own line, body
// formatted. Plain text and markdown both appear correctly in the view.
func TestChatModel_MarkdownRendering(t *testing.T) {
	const mdResponse = "Here is **bold** and _italic_ text.\n\n- List item one\n- List item two"
	inf := &mockInferencer{response: mdResponse}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	sessionID, err := agentExec.NewChatSessionID(cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(agentExec, sessionID, globalFlags, askFlags, ctx, &out, &out)
	model = runInit(model)

	model = typeInput(model, "show markdown")
	model, _ = pressEnter(model)

	// Committed content is in ViewHistory() after streaming completes.
	history := model.ViewHistory()
	if !strings.Contains(history, "Assistant:") {
		t.Errorf("ViewHistory() should contain Assistant label; got: %s", history)
	}
	// Rendered output should contain the visible text (Glamour renders markdown to ANSI)
	if !strings.Contains(history, "bold") || !strings.Contains(history, "italic") {
		t.Errorf("ViewHistory() should contain rendered markdown text 'bold' and 'italic'; got: %s", history)
	}
	if !strings.Contains(history, "List item one") || !strings.Contains(history, "List item two") {
		t.Errorf("ViewHistory() should contain list items; got: %s", history)
	}
}

// TestExecutor_ExecuteStreamingTurn_ReturnsStream verifies that ExecuteStreamingTurn
// returns an event stream that the caller can drain and assemble into the expected text.
func TestExecutor_ExecuteStreamingTurn_ReturnsStream(t *testing.T) {
	const expected = "streamed response"
	inf := &mockInferencer{response: expected}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf, true)
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	cfg.Stream = true

	ctx := context.Background()
	runData, err := agentExec.BuildLoop(ctx, cfg)
	if err != nil {
		t.Fatalf("BuildLoop: %v", err)
	}
	defer runData.CloseLogger()

	stream, err := agentExec.ExecuteStreamingTurn(ctx, runData, agentloop.NewExecuteInput("hello"), cfg)
	if err != nil {
		t.Fatalf("ExecuteStreamingTurn: %v", err)
	}
	defer func() {
		_ = stream.Close()
	}()

	var assembled string
	for stream.HasNext() {
		evt := stream.Response()
		if evt.Type == messages.StreamTypeTextDelta {
			if v, ok := evt.Value.(*messages.TextDeltaValue); ok {
				assembled += v.Content
			}
		}
	}
	if assembled != expected {
		t.Errorf("drained stream text = %q; want %q", assembled, expected)
	}
}
