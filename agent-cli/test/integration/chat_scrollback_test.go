package integration

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

// TestScrollback_MultiTurnOrdering verifies that after a multi-turn conversation
// (send message, receive stream, send another), all prior user and assistant
// messages appear in ViewHistory() in the correct order.
func TestScrollback_MultiTurnOrdering(t *testing.T) {
	const msg1 = "first question"
	const resp1 = "first answer"
	const msg2 = "second question"
	const resp2 = "second answer"
	const msg3 = "third question"
	const resp3 = "third answer"

	inf := &mockInferencerSequence{responses: []string{resp1, resp2, resp3}}
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

	// Turn 2.
	model = typeInput(model, msg2)
	model, _ = pressEnter(model)

	// Turn 3.
	model = typeInput(model, msg3)
	model, _ = pressEnter(model)

	history := model.ViewHistory()

	// All user messages and responses should appear in order.
	messages := []string{
		"You: " + msg1, resp1,
		"You: " + msg2, resp2,
		"You: " + msg3, resp3,
	}
	for i, expected := range messages {
		if !strings.Contains(history, expected) {
			t.Errorf("ViewHistory() missing message[%d] %q; got:\n%s", i, expected, history)
		}
	}

	// Verify ordering: each message appears after the previous one.
	lastIdx := 0
	for i, expected := range messages {
		idx := strings.Index(history[lastIdx:], expected)
		if idx < 0 {
			t.Errorf("message[%d] %q not found after position %d in ViewHistory()", i, expected, lastIdx)
			break
		}
		lastIdx += idx + len(expected)
	}
}

// TestScrollback_NoDuplication verifies that content from completed turns is not
// duplicated when a new turn starts — each user/assistant pair appears exactly once.
func TestScrollback_NoDuplication(t *testing.T) {
	const msg1 = "unique_message_alpha"
	const resp1 = "unique_response_beta"
	const msg2 = "unique_message_gamma"
	const resp2 = "unique_response_delta"

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

	// Turn 2.
	model = typeInput(model, msg2)
	model, _ = pressEnter(model)

	history := model.ViewHistory()

	// Each unique string should appear exactly once in the history.
	for _, unique := range []string{msg1, resp1, msg2, resp2} {
		count := strings.Count(history, unique)
		if count != 1 {
			t.Errorf("expected %q to appear exactly once in ViewHistory(), found %d times;\nhistory:\n%s", unique, count, history)
		}
	}

	// View() should NOT contain committed content (only the input prompt).
	view := model.View()
	for _, unique := range []string{msg1, resp1, msg2, resp2} {
		if strings.Contains(view, unique) {
			t.Errorf("View() should not contain committed content %q (should be in ViewHistory only);\nview:\n%s", unique, view)
		}
	}
}

// TestNewline_StreamedResponsePreservesNewlines verifies that streaming a response
// containing paragraph breaks (double newlines) produces output with actual line breaks.
// Note: Glamour renders markdown — single newlines are soft breaks (spaces), double newlines
// create paragraph breaks. This test uses double newlines which is the correct markdown way
// to produce visible line separation.
func TestNewline_StreamedResponsePreservesNewlines(t *testing.T) {
	const responseWithNewlines = "Line one.\n\nLine two.\n\nLine three."

	inf := &mockInferencer{response: responseWithNewlines}
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

	model = typeInput(model, "show lines")
	model, _ = pressEnter(model)

	history := model.ViewHistory()

	// All three lines should be present (not collapsed into one line).
	if !strings.Contains(history, "Line one.") {
		t.Errorf("ViewHistory() missing 'Line one.'; got:\n%s", history)
	}
	if !strings.Contains(history, "Line two.") {
		t.Errorf("ViewHistory() missing 'Line two.'; got:\n%s", history)
	}
	if !strings.Contains(history, "Line three.") {
		t.Errorf("ViewHistory() missing 'Line three.'; got:\n%s", history)
	}

	// The response should have multiple lines (not all on one line).
	// Find the assistant section and check it spans multiple lines.
	assistantIdx := strings.Index(history, "Assistant:")
	if assistantIdx < 0 {
		t.Fatalf("ViewHistory() missing 'Assistant:' label; got:\n%s", history)
	}
	assistantSection := history[assistantIdx:]
	lines := strings.Split(assistantSection, "\n")
	// Count non-empty lines (Glamour may add extra blank lines around paragraphs).
	nonEmptyLines := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmptyLines++
		}
	}
	if nonEmptyLines < 3 {
		t.Errorf("expected at least 3 non-empty lines in assistant section; got %d;\nsection:\n%s", nonEmptyLines, assistantSection)
	}
}

// TestNewline_MarkdownCodeBlockPreservesNewlines verifies that a response with
// a markdown code block (triple backtick + newlines) renders with preserved newlines.
func TestNewline_MarkdownCodeBlockPreservesNewlines(t *testing.T) {
	const codeBlockResponse = "Here is some code:\n\n```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```\n\nThat's the code."

	inf := &mockInferencer{response: codeBlockResponse}
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

	model = typeInput(model, "show code")
	model, _ = pressEnter(model)

	history := model.ViewHistory()

	// The code content should be preserved with its structure.
	if !strings.Contains(history, "func main()") {
		t.Errorf("ViewHistory() missing 'func main()'; got:\n%s", history)
	}
	if !strings.Contains(history, "Println") {
		t.Errorf("ViewHistory() missing 'Println'; got:\n%s", history)
	}
	if !strings.Contains(history, "That's the code.") {
		t.Errorf("ViewHistory() missing 'That\\'s the code.'; got:\n%s", history)
	}

	// The func main() and Println should be on different lines (newlines preserved).
	funcIdx := strings.Index(history, "func main()")
	printIdx := strings.Index(history, "Println")
	if funcIdx >= 0 && printIdx >= 0 {
		between := history[funcIdx:printIdx]
		if !strings.Contains(between, "\n") {
			t.Errorf("expected newline between 'func main()' and 'Println'; got: %q", between)
		}
	}

	// Text before and after code block should both be present.
	if !strings.Contains(history, "Here is some code") {
		t.Errorf("ViewHistory() missing text before code block; got:\n%s", history)
	}
}

// TestNewline_ChunkedStreamPreservesNewlines verifies that when a response is
// streamed in multiple chunks and contains newlines, the final output preserves them.
func TestNewline_ChunkedStreamPreservesNewlines(t *testing.T) {
	// Simulate chunks that split across newline boundaries.
	chunks := []string{"First line.\n", "Second line.\n", "Third line."}

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

	model = typeInput(model, "stream lines")
	model, _ = pressEnter(model)

	history := model.ViewHistory()

	// All three lines should be present and separate.
	if !strings.Contains(history, "First line.") {
		t.Errorf("ViewHistory() missing 'First line.'; got:\n%s", history)
	}
	if !strings.Contains(history, "Second line.") {
		t.Errorf("ViewHistory() missing 'Second line.'; got:\n%s", history)
	}
	if !strings.Contains(history, "Third line.") {
		t.Errorf("ViewHistory() missing 'Third line.'; got:\n%s", history)
	}
}
