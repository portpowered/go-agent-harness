package integration

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/agent"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

// TestSlashCommand_SystemShowsPrompt verifies that typing /system + Enter produces
// output containing the system prompt text (or the "(no system prompt configured)" fallback).
func TestSlashCommand_SystemShowsPrompt(t *testing.T) {
	inf := &mockInferencer{response: "should not be called"}
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

	model = typeInput(model, "/system")
	model, _ = pressEnter(model)

	history := model.ViewHistory()
	// The system prompt should be displayed (either actual prompt or fallback).
	// With no config, it should show the fallback message.
	if !strings.Contains(history, "system prompt") && !strings.Contains(history, "You are") {
		t.Errorf("ViewHistory() should contain system prompt or fallback; got:\n%s", history)
	}
}

// TestSlashCommand_SystemDoesNotAddUserMessage verifies that /system does not add
// a user message to the lines history — it is a local-only command.
func TestSlashCommand_SystemDoesNotAddUserMessage(t *testing.T) {
	inf := &mockInferencer{response: "should not be called"}
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

	model = typeInput(model, "/system")
	model, _ = pressEnter(model)

	history := model.ViewHistory()
	// /system should not produce a "You:" user message.
	if strings.Contains(history, "You:") {
		t.Errorf("ViewHistory() should NOT contain 'You:' for /system command; got:\n%s", history)
	}
	// /system should not produce an "Assistant:" response.
	if strings.Contains(history, "Assistant:") {
		t.Errorf("ViewHistory() should NOT contain 'Assistant:' for /system command; got:\n%s", history)
	}
}

// TestSlashCommand_HelpShowsCommands verifies that typing /help + Enter produces
// output listing all slash commands and @file syntax.
func TestSlashCommand_HelpShowsCommands(t *testing.T) {
	inf := &mockInferencer{response: "should not be called"}
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

	model = typeInput(model, "/help")
	model, _ = pressEnter(model)

	history := model.ViewHistory()
	// Should list all built-in commands.
	for _, expected := range []string{"/system", "/help", "/clear", "/skill", "@file", "@dir"} {
		if !strings.Contains(history, expected) {
			t.Errorf("ViewHistory() should mention %q in help output; got:\n%s", expected, history)
		}
	}
}

// TestSlashCommand_HelpDoesNotAddUserMessage verifies that /help does not add
// a user message to lines history.
func TestSlashCommand_HelpDoesNotAddUserMessage(t *testing.T) {
	inf := &mockInferencer{response: "should not be called"}
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

	model = typeInput(model, "/help")
	model, _ = pressEnter(model)

	history := model.ViewHistory()
	if strings.Contains(history, "You:") {
		t.Errorf("ViewHistory() should NOT contain 'You:' for /help command; got:\n%s", history)
	}
}

// TestSlashCommand_ClearResetsHistory verifies that after a multi-turn conversation,
// typing /clear + Enter empties the lines history.
func TestSlashCommand_ClearResetsHistory(t *testing.T) {
	inf := &mockInferencerSequence{responses: []string{"first response", "second response"}}
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

	// Have a conversation first.
	model = typeInput(model, "hello")
	model, _ = pressEnter(model)
	model = typeInput(model, "world")
	model, _ = pressEnter(model)

	// Verify conversation exists.
	historyBefore := model.ViewHistory()
	if !strings.Contains(historyBefore, "first response") {
		t.Fatalf("expected conversation history before clear; got:\n%s", historyBefore)
	}

	// Clear.
	model = typeInput(model, "/clear")
	model, _ = pressEnter(model)

	historyAfter := model.ViewHistory()
	// Previous conversation should be gone.
	if strings.Contains(historyAfter, "first response") {
		t.Errorf("ViewHistory() should NOT contain old conversation after /clear; got:\n%s", historyAfter)
	}
	if strings.Contains(historyAfter, "second response") {
		t.Errorf("ViewHistory() should NOT contain old conversation after /clear; got:\n%s", historyAfter)
	}
	// Should contain the confirmation message.
	if !strings.Contains(historyAfter, "Conversation cleared") {
		t.Errorf("ViewHistory() should contain 'Conversation cleared' after /clear; got:\n%s", historyAfter)
	}
}

// TestSlashCommand_ClearNewSessionID verifies that after /clear, a new session ID
// is assigned (different from the original). Uses a short sleep to avoid timestamp
// collision since session IDs are nanosecond-based.
func TestSlashCommand_ClearNewSessionID(t *testing.T) {
	inf := &mockInferencer{response: "ok"}
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

	originalID := model.SessionID()

	// Brief pause to ensure nanosecond-based session ID differs.
	time.Sleep(time.Millisecond)

	model = typeInput(model, "/clear")
	model, _ = pressEnter(model)

	newID := model.SessionID()
	if newID == originalID {
		t.Errorf("session ID should change after /clear; got same ID: %s", newID)
	}
	if newID == "" {
		t.Error("new session ID should not be empty after /clear")
	}
}

// TestSlashCommand_ClearModelStillFunctional verifies that after /clear, the model
// is still functional and can accept new input and produce responses.
func TestSlashCommand_ClearModelStillFunctional(t *testing.T) {
	inf := &mockInferencerSequence{responses: []string{"before clear", "after clear response"}}
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

	// Send a message before clear.
	model = typeInput(model, "hello before")
	model, _ = pressEnter(model)

	// Clear.
	model = typeInput(model, "/clear")
	model, _ = pressEnter(model)

	// Send a new message after clear.
	model = typeInput(model, "hello after")
	model, _ = pressEnter(model)

	history := model.ViewHistory()
	// Should contain the new response.
	if !strings.Contains(history, "after clear response") {
		t.Errorf("ViewHistory() should contain response after /clear; got:\n%s", history)
	}
	// Should NOT contain the old response.
	if strings.Contains(history, "before clear") {
		t.Errorf("ViewHistory() should NOT contain pre-clear content; got:\n%s", history)
	}
	// Model should not be quitting.
	if model.IsQuitting() {
		t.Error("model should not be quitting after /clear + new input")
	}
}
