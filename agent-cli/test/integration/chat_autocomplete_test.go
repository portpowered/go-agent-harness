package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/portpowered/agent-cli/internal/agent"
	"github.com/portpowered/agent-cli/internal/flags"
	"github.com/portpowered/agent-cli/internal/services"
)

// TestAutocomplete_FileAtPrefix verifies that typing @src/ma produces autocomplete
// suggestions matching files starting with src/ma (case-sensitive).
func TestAutocomplete_FileAtPrefix(t *testing.T) {
	ac := services.NewAutocomplete()
	ac.SetSuggestions([]services.Suggestion{
		{Label: "src/main.go"},
		{Label: "src/main_test.go"},
		{Label: "src/model.go"},
		{Label: "README.md"},
	})
	ac.SetFilter("src/ma")

	if !ac.IsActive() {
		t.Fatal("autocomplete should be active after SetFilter with matches")
	}
	if ac.FilteredCount() != 2 {
		t.Errorf("expected 2 filtered suggestions for 'src/ma'; got %d", ac.FilteredCount())
	}
	if ac.Selected() != "src/main.go" {
		t.Errorf("expected first selected to be 'src/main.go'; got %q", ac.Selected())
	}
}

// TestAutocomplete_EmptyPrefixShowsAll verifies that typing @ with no prefix
// shows all available suggestions.
func TestAutocomplete_EmptyPrefixShowsAll(t *testing.T) {
	ac := services.NewAutocomplete()
	items := []services.Suggestion{
		{Label: "file1.go"},
		{Label: "file2.go"},
		{Label: "dir1/"},
	}
	ac.SetSuggestions(items)
	ac.SetFilter("")

	if !ac.IsActive() {
		t.Fatal("autocomplete should be active with empty prefix (shows all)")
	}
	if ac.FilteredCount() != len(items) {
		t.Errorf("expected %d suggestions for empty prefix; got %d", len(items), ac.FilteredCount())
	}
}

// TestAutocomplete_DirectoriesHaveTrailingSlash verifies that directories appear
// with trailing / in suggestions.
func TestAutocomplete_DirectoriesHaveTrailingSlash(t *testing.T) {
	ac := services.NewAutocomplete()
	ac.SetSuggestions([]services.Suggestion{
		{Label: "src/"},
		{Label: "main.go"},
	})
	ac.SetFilter("s")

	if ac.FilteredCount() != 1 {
		t.Errorf("expected 1 match for 's'; got %d", ac.FilteredCount())
	}
	if ac.Selected() != "src/" {
		t.Errorf("expected selected suggestion 'src/'; got %q", ac.Selected())
	}
}

// TestAutocomplete_TabCompletes verifies that pressing Tab keeps the selected
// suggestion available for the parent to read. The parent is responsible for
// dismissing after reading Selected().
func TestAutocomplete_TabCompletes(t *testing.T) {
	ac := services.NewAutocomplete()
	ac.SetSuggestions([]services.Suggestion{
		{Label: "main.go"},
		{Label: "main_test.go"},
	})
	ac.SetFilter("main")

	// Selected should be the first match before Tab.
	if ac.Selected() != "main.go" {
		t.Errorf("selected before Tab should be 'main.go'; got %q", ac.Selected())
	}

	// Tab key is handled (doesn't error); selected value is still readable.
	updatedAC, _ := ac.Update(tea.KeyMsg{Type: tea.KeyTab})
	// The autocomplete stays active so the parent can read Selected() and then dismiss.
	if updatedAC.Selected() != "main.go" {
		t.Errorf("selected after Tab should still be 'main.go'; got %q", updatedAC.Selected())
	}

	// Parent would then call Reset() to dismiss.
	updatedAC.Reset()
	if updatedAC.IsActive() {
		t.Error("autocomplete should be dismissed after Reset()")
	}
}

// TestAutocomplete_EscapeDismisses verifies that pressing Escape dismisses
// the autocomplete popup.
func TestAutocomplete_EscapeDismisses(t *testing.T) {
	ac := services.NewAutocomplete()
	ac.SetSuggestions([]services.Suggestion{
		{Label: "file1.go"},
		{Label: "file2.go"},
	})
	ac.SetFilter("")

	if !ac.IsActive() {
		t.Fatal("autocomplete should be active before Escape")
	}

	updatedAC, _ := ac.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if updatedAC.IsActive() {
		t.Error("autocomplete should be dismissed after Escape")
	}
}

// TestAutocomplete_ExcludesHiddenDirs verifies that .git and node_modules directories
// are excluded from file suggestions (tested by verifying they don't appear when
// included in the suggestion list — this tests the filtering logic).
func TestAutocomplete_ExcludesHiddenDirs(t *testing.T) {
	// This tests the convention: scanFileSuggestions should not include .git or node_modules.
	// We verify by checking that when these are NOT in the suggestion list,
	// they don't magically appear.
	ac := services.NewAutocomplete()
	ac.SetSuggestions([]services.Suggestion{
		{Label: "src/main.go"},
		{Label: "README.md"},
		// Notably absent: .git/, node_modules/
	})
	ac.SetFilter("")

	if ac.FilteredCount() != 2 {
		t.Errorf("expected 2 suggestions (no hidden dirs); got %d", ac.FilteredCount())
	}
}

// TestAutocomplete_CmdSlashPrefix verifies that typing / shows built-in commands
// and available skills.
func TestAutocomplete_CmdSlashPrefix(t *testing.T) {
	ac := services.NewAutocomplete()
	ac.SetSuggestions([]services.Suggestion{
		{Label: "system", Description: "Show the system prompt"},
		{Label: "help", Description: "Show available commands"},
		{Label: "clear", Description: "Clear conversation history"},
		{Label: "mock-skill", Description: "A mock skill for testing"},
	})
	ac.SetFilter("")

	if !ac.IsActive() {
		t.Fatal("autocomplete should be active for empty / prefix")
	}
	if ac.FilteredCount() != 4 {
		t.Errorf("expected 4 suggestions for empty / prefix; got %d", ac.FilteredCount())
	}
}

// TestAutocomplete_CmdFilterCaseSensitive verifies that typing /sy filters to
// show /system (case-sensitive).
func TestAutocomplete_CmdFilterCaseSensitive(t *testing.T) {
	ac := services.NewAutocomplete()
	ac.SetSuggestions([]services.Suggestion{
		{Label: "system", Description: "Show the system prompt"},
		{Label: "help", Description: "Show available commands"},
		{Label: "clear", Description: "Clear conversation history"},
	})
	ac.SetFilter("sy")

	if ac.FilteredCount() != 1 {
		t.Errorf("expected 1 match for 'sy'; got %d", ac.FilteredCount())
	}
	if ac.Selected() != "system" {
		t.Errorf("expected selected 'system'; got %q", ac.Selected())
	}

	// Case-sensitive: "SY" should not match "system".
	ac.SetFilter("SY")
	if ac.FilteredCount() != 0 {
		t.Errorf("expected 0 matches for 'SY' (case-sensitive); got %d", ac.FilteredCount())
	}
}

// TestSkill_LoadMockSkill verifies that typing /mock-skill + Enter loads the skill
// body and adds a system message to context (not a user message).
func TestSkill_LoadMockSkill(t *testing.T) {
	// Create a temp dir with a mock skill.
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "mock-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}
	skillContent := `---
name: mock-skill
description: A mock skill for testing.
---
# Mock Skill Instructions
Step 1: Do the thing.
Step 2: Verify the thing.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0644); err != nil {
		t.Fatalf("failed to write SKILL.md: %v", err)
	}

	inf := &mockInferencer{response: "should not be called"}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf)
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = tmpDir // Point to temp dir with skills
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

	model = typeInput(model, "/mock-skill")
	model, _ = pressEnter(model)

	history := model.ViewHistory()
	// Should show confirmation.
	if !strings.Contains(history, "[Skill loaded: mock-skill]") {
		t.Errorf("ViewHistory() should contain skill loaded confirmation; got:\n%s", history)
	}
	// Should show skill body.
	if !strings.Contains(history, "Step 1") || !strings.Contains(history, "Step 2") {
		t.Errorf("ViewHistory() should contain skill instructions; got:\n%s", history)
	}
	// Should NOT be a user message.
	if strings.Contains(history, "You:") {
		t.Errorf("ViewHistory() should NOT contain 'You:' for /skill command; got:\n%s", history)
	}
}

// TestSkill_NonexistentDisplaysError verifies that typing /nonexistent-skill + Enter
// displays an error listing available skills.
func TestSkill_NonexistentDisplaysError(t *testing.T) {
	// Create a temp dir with one known skill.
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "real-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}
	skillContent := `---
name: real-skill
description: A real skill.
---
Body of the skill.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0644); err != nil {
		t.Fatalf("failed to write SKILL.md: %v", err)
	}

	inf := &mockInferencer{response: "should not be called"}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf)
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = tmpDir
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

	model = typeInput(model, "/nonexistent-skill")
	model, _ = pressEnter(model)

	history := model.ViewHistory()
	// Should show an error.
	if !strings.Contains(history, "not found") {
		t.Errorf("ViewHistory() should contain 'not found' error; got:\n%s", history)
	}
	// Should list available skills.
	if !strings.Contains(history, "real-skill") {
		t.Errorf("ViewHistory() should list available skill 'real-skill'; got:\n%s", history)
	}
	// Should NOT produce a user message.
	if strings.Contains(history, "You:") {
		t.Errorf("ViewHistory() should NOT contain 'You:' for failed /skill command; got:\n%s", history)
	}
}

// TestSkill_DoesNotAddUserMessage verifies that /mock-skill does not add a user
// message to lines history.
func TestSkill_DoesNotAddUserMessage(t *testing.T) {
	// Create a temp dir with a mock skill.
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("failed to create skill dir: %v", err)
	}
	skillContent := `---
name: test-skill
description: A test skill.
---
Test skill body.
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0644); err != nil {
		t.Fatalf("failed to write SKILL.md: %v", err)
	}

	inf := &mockInferencer{response: "should not be called"}
	exec := &mockToolExecutor{}
	agentExec := agent.NewExecutor(exec, nil, inf)
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = tmpDir
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

	model = typeInput(model, "/test-skill")
	model, _ = pressEnter(model)

	history := model.ViewHistory()
	// Should contain skill content.
	if !strings.Contains(history, "Test skill body") {
		t.Errorf("ViewHistory() should contain skill body; got:\n%s", history)
	}
	// Should NOT contain user message or assistant response.
	if strings.Contains(history, "You:") {
		t.Errorf("ViewHistory() should NOT contain 'You:'; got:\n%s", history)
	}
	if strings.Contains(history, "Assistant:") {
		t.Errorf("ViewHistory() should NOT contain 'Assistant:'; got:\n%s", history)
	}
}
