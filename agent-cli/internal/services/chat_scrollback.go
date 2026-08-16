// This file owns chat terminal rendering, committed scrollback, markdown styling, and display-width-aware line wrapping.

package services

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// styles for the chat UI (safe when terminal has no color)
var (
	styleUser          = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // bright blue
	styleAssistant     = lipgloss.NewStyle().Foreground(lipgloss.Color("15")) // white
	styleThinking      = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	styleThinkingBlock = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	styleTool          = lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // yellow
	styleToolResult    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green (tool output, distinct from assistant)
	styleMedia         = lipgloss.NewStyle().Foreground(lipgloss.Color("13")) // magenta
	styleSystem        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // dim gray (local-only output)
	stylePrompt        = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// View implements tea.Model. It renders only the current in-progress turn
// (thinking/tool/media lines, partials) and the input line with cursor.
// Committed conversation history is NOT rendered here — it is flushed to
// terminal scrollback via tea.Println so that it persists when scrolling up.
// Returns an empty string when the session is ending.
func (m ChatModel) View() string {
	if m.quitting {
		return ""
	}
	width := m.effectiveWidth()
	var b strings.Builder
	// Only render in-progress turn content (committed history is in scrollback).
	for _, ln := range m.currentTurnLines {
		b.WriteString(renderChatLineWrapped(ln, width))
	}
	if m.thinkingActive && m.reasoningPartial != "" {
		for _, line := range wrapToWidth(m.reasoningPartial, width) {
			b.WriteString(styleThinkingBlock.Render(line))
			b.WriteByte('\n')
		}
	}
	if m.toolTextPartial != "" {
		b.WriteString(styleToolResult.Render("  [Tool result]"))
		b.WriteByte('\n')
		b.WriteString(renderMarkdown(m.toolTextPartial, width))
		b.WriteByte('\n')
	}
	if m.assistantPartial != "" {
		b.WriteString(styleAssistant.Render("Assistant:"))
		b.WriteByte('\n')
		b.WriteString(renderMarkdown(m.assistantPartial, width))
		b.WriteByte('\n')
	}
	// Prompt and input line (Bubbles textinput provides cursor and blink)
	b.WriteByte('\n')
	b.WriteString(m.input.View())
	// Show autocomplete suggestions below the input line.
	if acView := m.fileAutocomplete.View(); acView != "" {
		b.WriteByte('\n')
		b.WriteString(acView)
	}
	if acView := m.cmdAutocomplete.View(); acView != "" {
		b.WriteByte('\n')
		b.WriteString(acView)
	}
	return b.String()
}

// ViewHistory returns the rendered committed conversation lines as a single
// string. This is used by tests to verify committed content that has been
// flushed to scrollback (and is no longer in View()).
func (m ChatModel) ViewHistory() string {
	width := m.effectiveWidth()
	var b strings.Builder
	for _, ln := range m.lines {
		b.WriteString(renderChatLineWrapped(ln, width))
	}
	return b.String()
}

// getPlainLine returns the unstyled single-line representation of a chat line (for wrapping).
func getPlainLine(ln chatLine) string {
	switch ln.kind {
	case chatLineUser:
		return "You: " + ln.content
	case chatLineAssistant:
		return "Assistant: " + ln.content
	case chatLineThinking, chatLineThinkingBlock:
		return ln.content
	case chatLineTool:
		return "  [Tool call] " + ln.content
	case chatLineToolResult:
		return "  [Tool result] " + ln.content
	case chatLineMedia:
		return "  " + ln.content
	case chatLineSystem:
		return ln.content
	default:
		return ln.content
	}
}

// renderMarkdown renders markdown to ANSI-styled terminal output using Glamour
// (same library as Glow). Uses auto style (dark/light) and word wrap. Returns
// the original content on error or when width is invalid.
func renderMarkdown(content string, width int) string {
	if width <= 0 {
		width = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return content
	}
	out, err := r.Render(content)
	if err != nil {
		return content
	}
	// Glamour may add a trailing newline; we handle layout in callers
	return strings.TrimSuffix(out, "\n")
}

// renderChatLineWrapped returns the styled and width-wrapped output for one chat line (with trailing newline).
// Assistant and tool-result content are rendered as markdown (Glamour/Glow-style); other kinds use plain wrapping.
func renderChatLineWrapped(ln chatLine, width int) string {
	switch ln.kind {
	case chatLineAssistant:
		label := styleAssistant.Render("Assistant:") + "\n"
		if ln.content == "" {
			return label
		}
		return label + renderMarkdown(ln.content, width) + "\n"
	case chatLineToolResult:
		label := styleToolResult.Render("  [Tool result]") + "\n"
		if ln.content == "" {
			return label
		}
		return label + renderMarkdown(ln.content, width) + "\n"
	}

	plain := getPlainLine(ln)
	lines := wrapToWidth(plain, width)
	// Indent continuation lines for consistency
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + strings.TrimLeft(lines[i], " ")
	}
	var style *lipgloss.Style
	switch ln.kind {
	case chatLineUser:
		style = &styleUser
	case chatLineThinking, chatLineThinkingBlock:
		style = &styleThinking
	case chatLineTool:
		style = &styleTool
	case chatLineMedia:
		style = &styleMedia
	case chatLineSystem:
		style = &styleSystem
	default:
		return plain + "\n"
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(style.Render(line))
		b.WriteByte('\n')
	}
	return b.String()
}

// wrapToWidth breaks s into lines of at most width display columns (runewidth), breaking at spaces when possible.
// Explicit newlines in the input are preserved: the input is first split on \n, then each segment is wrapped independently.
func wrapToWidth(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	// Split on explicit newlines first to preserve them.
	paragraphs := strings.Split(s, "\n")
	var out []string
	for _, para := range paragraphs {
		wrapped := wrapSingleLine(para, width)
		out = append(out, wrapped...)
	}
	return out
}

// wrapSingleLine wraps a single line (no embedded newlines) to the given width, breaking at spaces when possible.
func wrapSingleLine(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var out []string
	var line string
	for _, w := range words {
		try := line
		if try != "" {
			try += " " + w
		} else {
			try = w
		}
		if runewidth.StringWidth(try) <= width {
			line = try
			continue
		}
		if line != "" {
			out = append(out, line)
			line = ""
		}
		// Word itself may be longer than width; break by runes
		for _, r := range w {
			if runewidth.StringWidth(line)+runewidth.RuneWidth(r) <= width {
				line += string(r)
			} else {
				if line != "" {
					out = append(out, line)
				}
				line = string(r)
			}
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
