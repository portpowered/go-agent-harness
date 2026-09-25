package integration

import (
	"bytes"
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// runInit gives the Bubbles textinput focus by sending FocusInputMsg (same as Init()'s first Cmd).
func runInit(model services.ChatModel) services.ChatModel {
	m, _ := model.Update(services.FocusInputMsg{})
	return asChatModel(m)
}

// typeInput simulates the user typing a string into model one rune at a time,
// matching how bubbletea dispatches KeyRunes/KeySpace events from real keyboard input.
// Bubbles textinput expects KeySpace to include Runes: []rune{' '} to insert a space.
func typeInput(model services.ChatModel, text string) services.ChatModel {
	for _, r := range text {
		if r == ' ' {
			m, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			model = asChatModel(m)
		} else {
			m, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			model = asChatModel(m)
		}
	}
	return model
}

// pressEnter simulates pressing Enter and executes any returned commands
// until the streaming turn is complete (streamDoneMsg or error).
// It handles tea.BatchMsg by expanding batch commands into the work queue.
func pressEnter(model services.ChatModel) (services.ChatModel, string) {
	m, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = asChatModel(m)
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
		model = asChatModel(m)
		if nextCmd != nil {
			cmds = append(cmds, nextCmd)
		}
	}
	return model, ""
}

// TestChatModel_BackspaceEditing verifies that the backspace key removes the
// last character from the input buffer without submitting.
func TestChatModel_BackspaceEditing(t *testing.T) {
	inf := &mockInferencer{response: "ok"}
	exec := &mockToolExecutor{}
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	chatService := newPublicTextSessionService(globalFlags, exec, inf, nil)
	sessionID := newChatSessionID(t, chatService, *cfg)

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(chatService, sessionID, globalFlags, askFlags, ctx, &out, &out)
	model = runInit(model)

	// Type "hello" then backspace twice → "hel"
	model = typeInput(model, "hello")
	m, _ := model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	model = asChatModel(m)
	m, _ = model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	model = asChatModel(m)

	view := model.View()
	if !strings.Contains(view, "hel") {
		t.Errorf("after two backspaces expected view to contain %q; got: %s", "hel", view)
	}
	if strings.Contains(view, "hello") {
		t.Errorf("after two backspaces view should not contain %q; got: %s", "hello", view)
	}
}

// TestChatModel_SessionPersistence verifies that multiple turns in the same
// chat session use the same session ID so that conversation history is maintained.
func TestChatModel_SessionPersistence(t *testing.T) {
	inf := &mockInferencerSequence{responses: []string{"first", "second"}}
	exec := &mockToolExecutor{}
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	chatService := newPublicTextSessionService(globalFlags, exec, inf, nil)
	sessionID, err := chatService.NewSessionID(context.Background(), *cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(chatService, sessionID, globalFlags, askFlags, ctx, &out, &out)
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

// TestChatModel_MarkdownRendering verifies that assistant and tool-result content
// are rendered as markdown (Glamour/Glow-style): labels on their own line, body
// formatted. Plain text and markdown both appear correctly in the view.
func TestChatModel_MarkdownRendering(t *testing.T) {
	const mdResponse = "Here is **bold** and _italic_ text.\n\n- List item one\n- List item two"
	inf := &mockInferencer{response: mdResponse}
	exec := &mockToolExecutor{}
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	chatService := newPublicTextSessionService(globalFlags, exec, inf, nil)
	sessionID, err := chatService.NewSessionID(context.Background(), *cfg)
	if err != nil {
		t.Fatalf("NewChatSessionID: %v", err)
	}

	var out bytes.Buffer
	ctx := context.Background()
	model := services.NewChatModel(chatService, sessionID, globalFlags, askFlags, ctx, &out, &out)
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

// TestSessionService_Open_ReturnsStream verifies that the public session
// service returns an event stream that the caller can drain and assemble into
// the expected text.
func TestExecutor_ExecuteStreamingTurn_ReturnsStream(t *testing.T) {
	const expected = "streamed response"
	inf := &mockInferencer{response: expected}
	exec := &mockToolExecutor{}
	globalFlags := flags.NewGlobalFlags()
	askFlags := flags.NewAskFlags()
	cfg := services.BuildAgentConfigFromFlags(globalFlags, askFlags, nil, "")
	chatService := newPublicTextSessionService(globalFlags, exec, inf, nil)

	ctx := context.Background()
	handle, err := chatService.Open(ctx, *cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	stream, err := handle.Stream(ctx, agentloop.NewExecuteInput("hello"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer closeForTest(t, stream)

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
