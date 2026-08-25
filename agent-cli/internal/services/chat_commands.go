// This file owns local slash-command handling, skill loading, system/help/clear behavior, and command autocomplete.

package services

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/skills"
)

// ChatCommand is a typed chat slash command. Name is the token typed after the
// leading '/', Summary is the /help description, Hidden excludes the command
// from rendered /help output (it still dispatches), and Handler runs it.
type ChatCommand struct {
	Name    string
	Summary string
	Hidden  bool
	Handler func(m ChatModel) (tea.Model, tea.Cmd)
}

// chatCommands is the ordered registry of built-in chat commands; dispatch
// resolves tokens in slice order, so precedence follows this ordering.
// Populated in init because the help handler renders from this registry,
// which would otherwise form an initialization cycle.
var chatCommands []ChatCommand

func init() {
	chatCommands = []ChatCommand{
		{Name: "system", Summary: "Show the system prompt for this session", Handler: ChatModel.handleSystemCommand},
		{Name: "help", Summary: "Show this help message", Handler: ChatModel.handleHelpCommand},
		{Name: "clear", Summary: "Clear conversation history and start fresh", Handler: ChatModel.handleClearCommand},
	}
}

// lookupChatCommand resolves a trimmed post-'/'-prefix token by case-sensitive
// exact match against the ordered registry.
func lookupChatCommand(token string) (ChatCommand, bool) {
	for _, cmd := range chatCommands {
		if cmd.Name == token {
			return cmd, true
		}
	}
	return ChatCommand{}, false
}

// handleSlashCommand dispatches slash commands (/system, /help, /clear) through
// the chatCommands registry; unmatched names fall through to skill lookup.
// It returns the updated model and any tea.Cmd to execute.
func (m ChatModel) handleSlashCommand(input string) (tea.Model, tea.Cmd) {
	token := strings.TrimSpace(strings.TrimPrefix(input, "/"))
	if cmd, ok := lookupChatCommand(token); ok {
		return cmd.Handler(m)
	}
	// Try loading as a skill name.
	return m.handleSkillCommand(token)
}

// handleSystemCommand displays the resolved system prompt.
func (m ChatModel) handleSystemCommand() (tea.Model, tea.Cmd) {
	prompt, err := m.resolveSystemPrompt()
	if err != nil {
		errLine := chatLine{kind: chatLineSystem, content: "Error loading system prompt: " + err.Error()}
		m.lines = append(m.lines, errLine)
		rendered := strings.TrimSuffix(renderChatLineWrapped(errLine, m.effectiveWidth()), "\n")
		return m, tea.Println(rendered)
	}
	if prompt == "" {
		prompt = "(no system prompt configured)"
	}
	sysLine := chatLine{kind: chatLineSystem, content: prompt}
	m.lines = append(m.lines, sysLine)
	rendered := strings.TrimSuffix(renderChatLineWrapped(sysLine, m.effectiveWidth()), "\n")
	return m, tea.Println(rendered)
}

// chatHelpSyntaxLines are the non-command /help lines: the skill fallback
// syntax and the @file/@dir/ attachment syntax.
const chatHelpSyntaxLines = `/skill   — Load a skill's instructions (e.g. /my-skill)
@file    — Attach a file to your message (e.g. @path/to/file.txt)
@dir/    — Attach a directory listing (e.g. @src/)`

// renderChatHelp assembles the /help output from the visible (non-hidden)
// entries of chatCommands plus the static skill/attachment syntax lines,
// padding each command token so the em-dash separators align.
func renderChatHelp() string {
	var b strings.Builder
	for _, cmd := range chatCommands {
		if cmd.Hidden {
			continue
		}
		fmt.Fprintf(&b, "%-9s— %s\n", "/"+cmd.Name, cmd.Summary)
	}
	b.WriteString(chatHelpSyntaxLines)
	return b.String()
}

// handleHelpCommand displays available commands and syntax.
func (m ChatModel) handleHelpCommand() (tea.Model, tea.Cmd) {
	helpLine := chatLine{kind: chatLineSystem, content: renderChatHelp()}
	m.lines = append(m.lines, helpLine)
	rendered := strings.TrimSuffix(renderChatLineWrapped(helpLine, m.effectiveWidth()), "\n")
	return m, tea.Println(rendered)
}

// handleClearCommand resets conversation history, generates a new session ID, and clears the terminal.
func (m ChatModel) handleClearCommand() (tea.Model, tea.Cmd) {
	// Generate a new session ID so cleared history doesn't pollute the old session.
	cfg := BuildAgentConfigFromFlags(m.globalFlags, m.askFlags, nil, m.sessionID)
	newID, err := m.executor.NewChatSessionID(cfg)
	if err != nil {
		errLine := chatLine{kind: chatLineSystem, content: "Error creating new session: " + err.Error()}
		m.lines = append(m.lines, errLine)
		rendered := strings.TrimSuffix(renderChatLineWrapped(errLine, m.effectiveWidth()), "\n")
		return m, tea.Println(rendered)
	}

	// Reset conversation state.
	m.sessionID = newID
	m.lines = nil
	m.currentTurnLines = nil
	m.assistantPartial = ""
	m.toolTextPartial = ""
	m.reasoningPartial = ""
	m.thinkingActive = false
	m.stream = nil
	m.runData = nil

	// Show confirmation after clearing the screen.
	confirmLine := chatLine{kind: chatLineSystem, content: "Conversation cleared. Starting fresh."}
	m.lines = append(m.lines, confirmLine)
	rendered := strings.TrimSuffix(renderChatLineWrapped(confirmLine, m.effectiveWidth()), "\n")
	return m, tea.Sequence(tea.ClearScreen, tea.Println(rendered))
}

// handleSkillCommand loads a skill's instructions by name and injects them as a system message.
// If the skill is not found, it displays an error listing available skills.
func (m ChatModel) handleSkillCommand(skillName string) (tea.Model, tea.Cmd) {
	loader, err := m.newSkillsLoader()
	if err != nil {
		errLine := chatLine{kind: chatLineSystem, content: "Error loading skills: " + err.Error()}
		m.lines = append(m.lines, errLine)
		rendered := strings.TrimSuffix(renderChatLineWrapped(errLine, m.effectiveWidth()), "\n")
		return m, tea.Println(rendered)
	}

	body, err := loader.LoadSkill(skillName)
	if err != nil {
		// Skill not found — list available skills.
		available := m.listSkillNames(loader)
		msg := fmt.Sprintf("Skill %q not found.", skillName)
		if available != "" {
			msg += " Available: " + available
		}
		errLine := chatLine{kind: chatLineSystem, content: msg}
		m.lines = append(m.lines, errLine)
		rendered := strings.TrimSuffix(renderChatLineWrapped(errLine, m.effectiveWidth()), "\n")
		return m, tea.Println(rendered)
	}

	// Show confirmation and the skill body.
	confirmLine := chatLine{kind: chatLineSystem, content: "[Skill loaded: " + skillName + "]"}
	bodyLine := chatLine{kind: chatLineSystem, content: body}
	m.lines = append(m.lines, confirmLine, bodyLine)
	width := m.effectiveWidth()
	confirmRendered := strings.TrimSuffix(renderChatLineWrapped(confirmLine, width), "\n")
	bodyRendered := strings.TrimSuffix(renderChatLineWrapped(bodyLine, width), "\n")
	return m, tea.Batch(tea.Println(confirmRendered), tea.Println(bodyRendered))
}

// newSkillsLoader creates a skills.Loader using the same workspace and config directories as the executor.
func (m ChatModel) newSkillsLoader() (*skills.Loader, error) {
	cfg := BuildAgentConfigFromFlags(m.globalFlags, m.askFlags, nil, m.sessionID)
	storage, err := m.executor.GetSessionStorage(cfg)
	if err != nil {
		return nil, err
	}
	return skills.NewLoader(storage.WorkspaceDir(), cfg.ConfigDir), nil
}

// listSkillNames returns a comma-separated list of available skill names, or empty string if none.
func (m ChatModel) listSkillNames(loader *skills.Loader) string {
	list, err := loader.List()
	if err != nil || len(list) == 0 {
		return ""
	}
	names := make([]string, len(list))
	for i, s := range list {
		names[i] = s.Meta.Name
	}
	return strings.Join(names, ", ")
}

// resolveSystemPrompt loads the system prompt using the same logic as runAgent/BuildLoop.
func (m ChatModel) resolveSystemPrompt() (string, error) {
	cfg := BuildAgentConfigFromFlags(m.globalFlags, m.askFlags, nil, m.sessionID)
	storage, err := m.executor.GetSessionStorage(cfg)
	if err != nil {
		return "", err
	}
	return m.executor.LoadSystemPrompt(cfg, storage.WorkspaceDir(), nil)
}

// activeAutocomplete returns a pointer to whichever autocomplete is currently active,
// or nil if none is active. Only one autocomplete can be active at a time.
func (m *ChatModel) activeAutocomplete() *Autocomplete {
	if m.fileAutocomplete.IsActive() {
		return &m.fileAutocomplete
	}
	if m.cmdAutocomplete.IsActive() {
		return &m.cmdAutocomplete
	}
	return nil
}

// updateCmdAutocomplete checks the current input for a / prefix and activates
// command/skill autocomplete suggestions if appropriate.
func (m *ChatModel) updateCmdAutocomplete() {
	val := m.input.Value()
	prefix := extractCmdPrefix(val)
	if prefix == "" && val != "/" {
		if m.cmdAutocomplete.IsActive() {
			m.cmdAutocomplete.Reset()
		}
		return
	}
	// Lazy-load command suggestions on first activation.
	if m.cmdSuggestions == nil {
		m.cmdSuggestions = m.buildCmdSuggestions()
		m.cmdAutocomplete.SetSuggestions(m.cmdSuggestions)
	}
	m.cmdAutocomplete.SetFilter(prefix)
}

// buildCmdSuggestions returns a merged list of built-in commands and available skills.
func (m *ChatModel) buildCmdSuggestions() []Suggestion {
	// Built-in commands.
	suggestions := []Suggestion{
		{Label: "system", Description: "Show the system prompt"},
		{Label: "help", Description: "Show available commands"},
		{Label: "clear", Description: "Clear conversation history"},
	}
	// Append skills from the loader.
	loader, err := m.newSkillsLoader()
	if err != nil {
		return suggestions
	}
	list, err := loader.List()
	if err != nil {
		return suggestions
	}
	for _, s := range list {
		suggestions = append(suggestions, Suggestion{Label: s.Meta.Name, Description: s.Meta.Description})
	}
	return suggestions
}

// completeCmdSuggestion replaces the /prefix in the input with the completed command.
func (m *ChatModel) completeCmdSuggestion(selected string) {
	newVal := "/" + selected + " "
	m.input.SetValue(newVal)
	m.input.SetCursor(len(newVal))
}

// extractCmdPrefix returns the prefix typed after / when the input starts with /,
// or empty string if not in a / context. For example:
//
//	"/sys"    → "sys"
//	"/help"   → "help"
//	"/"       → ""
//	"hello"   → ""
//	"/foo bar" → "" (space means no longer in prefix)
func extractCmdPrefix(input string) string {
	if !strings.HasPrefix(input, "/") {
		return ""
	}
	after := input[1:]
	if strings.Contains(after, " ") {
		return "" // cursor moved past the command token
	}
	return after
}
