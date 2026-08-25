package services

import (
	"strings"
	"testing"
)

func TestChatScrollback_RendersOrderedHistory(t *testing.T) {
	harness := newChatTestHarness(t)
	model := harness.model
	model.width = 60
	model.lines = []chatLine{
		{kind: chatLineUser, content: "first user"},
		{kind: chatLineThinking, content: "thinking"},
		{kind: chatLineThinkingBlock, content: "reasoning details"},
		{kind: chatLineTool, content: "lookup"},
		{kind: chatLineToolResult, content: "tool result"},
		{kind: chatLineMedia, content: "[Image returned]"},
		{kind: chatLineSystem, content: "system notice"},
		{kind: chatLineAssistant, content: "assistant answer"},
	}
	history := model.ViewHistory()
	last := -1
	for _, expected := range []string{"You: first user", "thinking", "reasoning details", "lookup", "tool result", "[Image returned]", "system notice", "assistant answer"} {
		index := strings.Index(history, expected)
		if index < 0 {
			t.Fatalf("history missing %q: %s", expected, history)
		}
		if index <= last {
			t.Fatalf("history reordered %q at %d after %d: %s", expected, index, last, history)
		}
		last = index
	}
	if strings.Count(history, "assistant answer") != 1 || strings.Count(history, "first user") != 1 {
		t.Fatalf("history duplicate counts = user %d assistant %d", strings.Count(history, "first user"), strings.Count(history, "assistant answer"))
	}
}

func TestChatScrollback_ViewRendersLiveTurnAndQuitting(t *testing.T) {
	harness := newChatTestHarness(t)
	model := harness.model
	model.width = 32
	model.input.SetValue("typed prompt")
	model.currentTurnLines = []chatLine{
		{kind: chatLineThinking, content: "Thinking..."},
		{kind: chatLineTool, content: "search"},
		{kind: chatLineMedia, content: "[File returned]"},
	}
	model.thinkingActive = true
	model.reasoningPartial = "partial reasoning"
	model.toolTextPartial = "partial tool result"
	model.assistantPartial = "partial assistant"
	model.fileAutocomplete.SetSuggestions([]Suggestion{{Label: "notes.txt"}})
	model.fileAutocomplete.SetFilter("")
	model.cmdAutocomplete.SetSuggestions([]Suggestion{{Label: "help"}})
	model.cmdAutocomplete.SetFilter("")
	view := model.View()
	for _, expected := range []string{"Thinking...", "search", "[File returned]", "partial reasoning", "partial tool result", "partial assistant", "typed prompt", "notes.txt", "help"} {
		if !strings.Contains(view, expected) {
			t.Errorf("live view missing %q: %s", expected, view)
		}
	}
	model.quitting = true
	if model.View() != "" {
		t.Fatalf("quitting View() = %q, want empty", model.View())
	}
}

func TestChatScrollback_PlainLineKindsAndWrapping(t *testing.T) {
	for _, test := range []struct {
		kind    chatLineKind
		content string
		want    string
	}{
		{kind: chatLineUser, content: "u", want: "You: u"},
		{kind: chatLineAssistant, content: "a", want: "Assistant: a"},
		{kind: chatLineThinking, content: "t", want: "t"},
		{kind: chatLineThinkingBlock, content: "b", want: "b"},
		{kind: chatLineTool, content: "call", want: "  [Tool call] call"},
		{kind: chatLineToolResult, content: "result", want: "  [Tool result] result"},
		{kind: chatLineMedia, content: "media", want: "  media"},
		{kind: chatLineSystem, content: "system", want: "system"},
		{kind: chatLineKind("unknown"), content: "fallback", want: "fallback"},
	} {
		if got := getPlainLine(chatLine{kind: test.kind, content: test.content}); got != test.want {
			t.Errorf("getPlainLine(%q) = %q, want %q", test.kind, got, test.want)
		}
	}
	if got := renderChatLineWrapped(chatLine{kind: chatLineAssistant}, 40); !strings.Contains(got, "Assistant:") {
		t.Fatalf("empty assistant rendering = %q", got)
	}
	if got := renderChatLineWrapped(chatLine{kind: chatLineAssistant, content: "**bold**"}, 40); !strings.Contains(got, "bold") {
		t.Fatalf("markdown assistant rendering = %q", got)
	}
	if got := renderChatLineWrapped(chatLine{kind: chatLineToolResult}, 40); !strings.Contains(got, "Tool result") {
		t.Fatalf("empty tool result rendering = %q", got)
	}
	if got := renderChatLineWrapped(chatLine{kind: chatLineToolResult, content: "result"}, 40); !strings.Contains(got, "result") {
		t.Fatalf("tool result rendering = %q", got)
	}
	if got := renderChatLineWrapped(chatLine{kind: chatLineKind("unknown"), content: "fallback"}, 40); got != "fallback\n" {
		t.Fatalf("unknown rendering = %q", got)
	}

	for _, test := range []struct {
		input string
		width int
		want  []string
	}{
		{input: "zero", width: 0, want: []string{"zero"}},
		{input: "one two", width: 6, want: []string{"one", "two"}},
		{input: "one\ntwo", width: 20, want: []string{"one", "two"}},
		{input: "", width: 4, want: []string{""}},
		{input: "abcdef", width: 2, want: []string{"ab", "cd", "ef"}},
	} {
		got := wrapToWidth(test.input, test.width)
		if strings.Join(got, "|") != strings.Join(test.want, "|") {
			t.Errorf("wrapToWidth(%q, %d) = %#v, want %#v", test.input, test.width, got, test.want)
		}
	}
	if got := wrapSingleLine("   ", 4); len(got) != 1 || got[0] != "" {
		t.Fatalf("blank single line = %#v", got)
	}
	if got := renderMarkdown("plain", 0); !strings.Contains(got, "plain") {
		t.Fatalf("default-width markdown = %q", got)
	}
}

func TestChatScrollback_BoundedEvictionContractIsNotPresent(t *testing.T) {
	t.Skip("the current chat split flushes committed lines to terminal scrollback and exposes no bounded buffer capacity or eviction counter")
}
