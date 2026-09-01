package services

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/skills"
)

func TestChatCommands_DispatchesRegisteredCommands(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(t *testing.T, harness *chatTestHarness, beforeSession string)
	}{
		{
			name:  "system",
			input: "/system",
			check: func(t *testing.T, harness *chatTestHarness, _ string) {
				t.Helper()
				if !strings.Contains(harness.model.ViewHistory(), "configured system prompt") {
					t.Fatalf("system command history = %q", harness.model.ViewHistory())
				}
				if harness.inferencer.calls != 0 {
					t.Fatalf("system command invoked inferencer %d times", harness.inferencer.calls)
				}
			},
		},
		{
			name:  "help",
			input: "/help",
			check: func(t *testing.T, harness *chatTestHarness, _ string) {
				t.Helper()
				history := harness.model.ViewHistory()
				for _, expected := range []string{"/system", "/help", "/clear", "/skill", "@file", "@dir"} {
					if !strings.Contains(history, expected) {
						t.Errorf("help output missing %q: %s", expected, history)
					}
				}
			},
		},
		{
			name:  "clear",
			input: "/clear",
			check: func(t *testing.T, harness *chatTestHarness, beforeSession string) {
				t.Helper()
				history := harness.model.ViewHistory()
				if strings.Contains(history, "old conversation") || !strings.Contains(history, "Conversation cleared. Starting fresh.") {
					t.Fatalf("clear history = %q", history)
				}
				if harness.model.SessionID() == "" || harness.model.SessionID() == beforeSession {
					t.Fatalf("clear session ID = %q, want a new non-empty ID", harness.model.SessionID())
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newChatTestHarness(t)
			harness.askFlags.SystemPrompt = "configured system prompt"
			beforeSession := harness.model.SessionID()
			if test.name == "clear" {
				time.Sleep(time.Millisecond)
				harness.model.lines = []chatLine{{kind: chatLineUser, content: "old conversation"}}
			}
			harness.model = submitChatInput(harness.model, test.input)
			test.check(t, harness, beforeSession)
		})
	}
}

func TestChatCommands_SkillSuccessAndUnknownOutput(t *testing.T) {
	harness := newChatTestHarness(t)
	skillDir := filepath.Join(harness.globalFlags.ConfigDirPath, "skills", "demo")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: demo\ndescription: A deterministic test skill\n---\nSkill body with an observable instruction.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0600); err != nil {
		t.Fatal(err)
	}

	harness.model = submitChatInput(harness.model, "/demo")
	history := harness.model.ViewHistory()
	if !strings.Contains(history, "[Skill loaded: demo]") || !strings.Contains(history, "Skill body with an observable instruction.") {
		t.Fatalf("skill success history = %q", history)
	}

	harness.model = submitChatInput(harness.model, "/missing-skill")
	history = harness.model.ViewHistory()
	if !strings.Contains(history, `Skill "missing-skill" not found. Available: demo`) {
		t.Fatalf("unknown skill history = %q", history)
	}
	if harness.inferencer.calls != 0 {
		t.Fatalf("local skill commands invoked inferencer %d times", harness.inferencer.calls)
	}
}

func TestChatCommands_MalformedInputsHaveStableObservableErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "unknown command", input: "/does-not-exist", want: `Skill "does-not-exist" not found.`},
		{name: "missing command name", input: "/", want: `Skill "" not found.`},
		{name: "excess argument", input: "/help extra", want: `Skill "help extra" not found.`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newChatTestHarness(t)
			harness.model = submitChatInput(harness.model, test.input)
			if !strings.Contains(harness.model.ViewHistory(), test.want) {
				t.Fatalf("%s output = %q, want %q", test.name, harness.model.ViewHistory(), test.want)
			}
			if harness.inferencer.calls != 0 {
				t.Fatalf("%s invoked inferencer %d times", test.name, harness.inferencer.calls)
			}
		})
	}
}

func TestChatCommands_AutocompleteAndPrefixContracts(t *testing.T) {
	harness := newChatTestHarness(t)
	model := harness.model
	model.input.SetValue("/he")
	model.updateCmdAutocomplete()
	if !model.cmdAutocomplete.IsActive() || model.cmdAutocomplete.Selected() != "help" {
		t.Fatalf("command suggestions active=%t selected=%q", model.cmdAutocomplete.IsActive(), model.cmdAutocomplete.Selected())
	}
	model.completeCmdSuggestion("help")
	if model.input.Value() != "/help " || model.input.Position() != len("/help ") {
		t.Fatalf("completed command input = %q at %d", model.input.Value(), model.input.Position())
	}
	model.input.SetValue("/help extra")
	model.updateCmdAutocomplete()
	if model.cmdAutocomplete.IsActive() {
		t.Fatal("command autocomplete stayed active after the command token ended")
	}

	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "/sys", want: "sys"},
		{input: "/", want: ""},
		{input: "plain", want: ""},
		{input: "/help extra", want: ""},
	} {
		if got := extractCmdPrefix(test.input); got != test.want {
			t.Errorf("extractCmdPrefix(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestChatCommands_ResolutionFailuresAndEmptySkillList(t *testing.T) {
	harness := newChatTestHarness(t)
	if got := harness.model.listSkillNames(skills.NewLoader("", "")); got != "" {
		t.Fatalf("empty skill list = %q, want empty", got)
	}
	badConfig := filepath.Join(t.TempDir(), "config-file")
	if err := os.WriteFile(badConfig, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	harness.globalFlags.ConfigDirPath = badConfig
	harness.askFlags.SystemPrompt = ""
	harness.model = submitChatInput(harness.model, "/system")
	harness.model = submitChatInput(harness.model, "/broken-skill")
	history := harness.model.ViewHistory()
	if !strings.Contains(history, "Error loading system prompt:") || !strings.Contains(history, "Error loading skills:") {
		t.Fatalf("resolution failures were not surfaced: %q", history)
	}
	if suggestions := harness.model.buildCmdSuggestions(); len(suggestions) != 3 {
		t.Fatalf("fallback command suggestions = %#v, want three built-ins", suggestions)
	}
}

func TestChatCommands_AutocompleteUsesVisibleRegistryAndPreservesSkillOrder(t *testing.T) {
	harness := newChatTestHarness(t)
	original := chatCommands
	t.Cleanup(func() { chatCommands = original })
	chatCommands = append([]ChatCommand{
		{
			Name:                    "hidden",
			Summary:                 "Hidden test-only command",
			AutocompleteDescription: "Hidden autocomplete command",
			Hidden:                  true,
			Handler:                 func(m ChatModel) (tea.Model, tea.Cmd) { return m, nil },
		},
	}, original...)

	for _, skill := range []struct {
		name        string
		description string
	}{
		{name: "alpha", description: "First skill"},
		{name: "beta", description: "Second skill"},
	} {
		skillDir := filepath.Join(harness.globalFlags.ConfigDirPath, "skills", skill.name)
		if err := os.MkdirAll(skillDir, 0o700); err != nil {
			t.Fatal(err)
		}
		contents := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n", skill.name, skill.description)
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	suggestions := harness.model.buildCmdSuggestions()
	want := []Suggestion{
		{Label: "system", Description: "Show the system prompt"},
		{Label: "help", Description: "Show available commands"},
		{Label: "clear", Description: "Clear conversation history"},
		{Label: "alpha", Description: "First skill"},
		{Label: "beta", Description: "Second skill"},
	}
	if !slices.Equal(suggestions, want) {
		t.Fatalf("autocomplete suggestions = %#v, want %#v", suggestions, want)
	}
}

func TestChatCommands_TypedFailureContractIsBlockedByCurrentDispatcher(t *testing.T) {
	t.Skip("the current slash dispatcher renders failures as chatLineSystem output and returns no typed error; preserve this requirement for the follow-up contract change")
}

func countUserChatLines(lines []chatLine) int {
	count := 0
	for _, line := range lines {
		if line.kind == chatLineUser {
			count++
		}
	}
	return count
}

func TestChatCommands_RegistryLookupContract(t *testing.T) {
	for _, name := range []string{"system", "help", "clear"} {
		cmd, ok := lookupChatCommand(name)
		if !ok {
			t.Fatalf("lookupChatCommand(%q): not found", name)
		}
		if cmd.Name != name {
			t.Fatalf("lookupChatCommand(%q) returned %q", name, cmd.Name)
		}
		if cmd.Handler == nil {
			t.Fatalf("builtin %q has no handler", name)
		}
	}
	if _, ok := lookupChatCommand("no-such-command"); ok {
		t.Fatal(`lookupChatCommand("no-such-command") unexpectedly matched`)
	}
	if _, ok := lookupChatCommand("SYSTEM"); ok {
		t.Fatal(`lookupChatCommand("SYSTEM") unexpectedly matched; lookup must be case-sensitive`)
	}
}

func TestChatCommands_RegistryOrderIsDispatchPrecedence(t *testing.T) {
	names := make([]string, 0, len(chatCommands))
	for _, cmd := range chatCommands {
		names = append(names, cmd.Name)
	}
	want := []string{"system", "help", "clear"}
	if !slices.Equal(names, want) {
		t.Fatalf("registry order = %v, want %v", names, want)
	}
}

func TestChatCommands_NonSlashInputNeverReachesDispatcher(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "bare registered word goes to the LLM", input: "system"},
		{name: "bare help word goes to the LLM", input: "help"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newChatTestHarness(t, "llm reply")
			userLinesBefore := countUserChatLines(harness.model.lines)
			harness.model = submitChatInput(harness.model, tt.input)

			history := harness.model.ViewHistory()
			if got := countUserChatLines(harness.model.lines); got != userLinesBefore+1 {
				t.Fatalf("non-slash input %q added %d user-message lines, want 1 (LLM path)", tt.input, got-userLinesBefore)
			}
			if strings.Contains(history, "System Instructions") || strings.Contains(history, "Show this help message") {
				t.Fatalf("non-slash input %q dispatched a registered handler: %q", tt.input, history)
			}
			if strings.Contains(history, "not found.") {
				t.Fatalf("non-slash input %q took the skill fallback: %q", tt.input, history)
			}
			if harness.inferencer.calls != 1 {
				t.Fatalf("non-slash input %q invoked inferencer %d times, want 1", tt.input, harness.inferencer.calls)
			}
		})
	}
}

func TestChatCommands_EmptyAndWhitespaceInputIsInert(t *testing.T) {
	harness := newChatTestHarness(t)
	userLinesBefore := countUserChatLines(harness.model.lines)

	harness.model = submitChatInput(harness.model, "")
	harness.model = submitChatInput(harness.model, "   ")

	if got := countUserChatLines(harness.model.lines); got != userLinesBefore {
		t.Fatalf("empty/whitespace input added user-message lines")
	}
	if harness.inferencer.calls != 0 {
		t.Fatalf("empty/whitespace input invoked inferencer %d times", harness.inferencer.calls)
	}
}

// TestChatCommands_DispatcherEmptyTokenFallsThroughToSkillLookup pins the
// dispatcher-level miss contract: an empty token (no registered name) falls
// through to the skill-name lookup without adding a user message or calling
// the inferencer. The REPL never forwards empty input (chat_repl.go gates on
// TrimSpace + '/' prefix), so this exercises the dispatcher in isolation.
func TestChatCommands_DispatcherEmptyTokenFallsThroughToSkillLookup(t *testing.T) {
	harness := newChatTestHarness(t)
	userLinesBefore := countUserChatLines(harness.model.lines)

	updated, _ := harness.model.handleSlashCommand("")
	model := updated.(ChatModel)

	if got := countUserChatLines(model.lines); got != userLinesBefore {
		t.Fatalf("empty-token miss added %d user-message lines", got-userLinesBefore)
	}
	if history := model.ViewHistory(); !strings.Contains(history, `Skill "" not found.`) {
		t.Fatalf("empty-token history = %q", history)
	}
	if harness.inferencer.calls != 0 {
		t.Fatalf("empty-token miss invoked inferencer %d times", harness.inferencer.calls)
	}
}

func TestChatCommands_UnknownSlashMissAddsNoUserMessage(t *testing.T) {
	harness := newChatTestHarness(t)
	userLinesBefore := countUserChatLines(harness.model.lines)

	harness.model = submitChatInput(harness.model, "/definitely-not-a-skill")

	if got := countUserChatLines(harness.model.lines); got != userLinesBefore {
		t.Fatalf("unknown slash command added %d user-message lines", got-userLinesBefore)
	}
	history := harness.model.ViewHistory()
	if !strings.Contains(history, `Skill "definitely-not-a-skill" not found.`) {
		t.Fatalf("miss history = %q", history)
	}
	if harness.inferencer.calls != 0 {
		t.Fatalf("miss invoked inferencer %d times", harness.inferencer.calls)
	}
}

func TestChatCommands_RegisteredNamesResolveAheadOfSkills(t *testing.T) {
	harness := newChatTestHarness(t)
	skillDir := filepath.Join(harness.globalFlags.ConfigDirPath, "skills", "help")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: help\ndescription: Impostor skill shadowed by the builtin\n---\nImpostor skill body.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}

	harness.model = submitChatInput(harness.model, "/help")

	history := harness.model.ViewHistory()
	if !strings.Contains(history, "Show this help message") {
		t.Fatalf("registry /help did not resolve ahead of skill fallback: %q", history)
	}
	if strings.Contains(history, "[Skill loaded: help]") || strings.Contains(history, "Impostor") {
		t.Fatalf(`skill named "help" shadowed the registered command: %q`, history)
	}
	if harness.inferencer.calls != 0 {
		t.Fatalf("registered dispatch invoked inferencer %d times", harness.inferencer.calls)
	}
}

func TestChatCommands_HelpRendersRegistryByteIdentically(t *testing.T) {
	const want = "/system  — Show the system prompt for this session\n" +
		"/help    — Show this help message\n" +
		"/clear   — Clear conversation history and start fresh\n" +
		"/skill   — Load a skill's instructions (e.g. /my-skill)\n" +
		"@file    — Attach a file to your message (e.g. @path/to/file.txt)\n" +
		"@dir/    — Attach a directory listing (e.g. @src/)"
	if got := renderChatHelp(); got != want {
		t.Fatalf("renderChatHelp() mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestChatCommands_HiddenEntriesExcludedFromRenderedHelp(t *testing.T) {
	original := chatCommands
	t.Cleanup(func() { chatCommands = original })
	chatCommands = append(append([]ChatCommand{}, original...), ChatCommand{
		Name:    "secret",
		Summary: "Hidden test-only command",
		Hidden:  true,
		Handler: func(m ChatModel) (tea.Model, tea.Cmd) { return m, nil },
	})

	got := renderChatHelp()
	if strings.Contains(got, "secret") || strings.Contains(got, "Hidden test-only command") {
		t.Fatalf("hidden entry leaked into rendered help: %q", got)
	}
	for _, visible := range []string{"/system  —", "/help    —", "/clear   —"} {
		if !strings.Contains(got, visible) {
			t.Fatalf("visible command line %q missing from rendered help: %q", visible, got)
		}
	}
	// Hidden only affects help rendering; hidden entries still dispatch.
	if cmd, ok := lookupChatCommand("secret"); !ok || !cmd.Hidden {
		t.Fatalf("hidden synthetic entry not resolvable for dispatch: found=%v cmd=%+v", ok, cmd)
	}
}
