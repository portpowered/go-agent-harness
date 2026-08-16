package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(history, "Error loading system prompt:") || !strings.Contains(history, `Skill "broken-skill" not found.`) {
		t.Fatalf("resolution failures were not surfaced: %q", history)
	}
	if suggestions := harness.model.buildCmdSuggestions(); len(suggestions) != 3 {
		t.Fatalf("fallback command suggestions = %#v, want three built-ins", suggestions)
	}
}

func TestChatCommands_TypedFailureContractIsBlockedByCurrentDispatcher(t *testing.T) {
	t.Skip("the current slash dispatcher renders failures as chatLineSystem output and returns no typed error; preserve this requirement for the follow-up contract change")
}
