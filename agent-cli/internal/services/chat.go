package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/portpowered/agent-cli/internal/agent"
	"github.com/portpowered/agent-cli/internal/flags"
	"github.com/portpowered/agent-cli/internal/skills"
	"github.com/portpowered/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-loop/pkg/messages"
)

// chatLineKind identifies how to style a line in the conversation.
type chatLineKind string

const (
	chatLineUser          chatLineKind = "user"
	chatLineAssistant     chatLineKind = "assistant"
	chatLineThinking      chatLineKind = "thinking"
	chatLineThinkingBlock chatLineKind = "thinking_block"
	chatLineTool          chatLineKind = "tool"
	chatLineToolResult    chatLineKind = "tool_result" // text content returned by a tool
	chatLineMedia         chatLineKind = "media"
	chatLineSystem        chatLineKind = "system" // local-only output (e.g. /system, /help)
)

type chatLine struct {
	kind    chatLineKind
	content string
}

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

// streamReadyMsg is sent when the streaming turn has started and the event stream is ready.
type streamReadyMsg struct {
	stream  agentloop.Stream
	runData *agent.RunData
	err     error
}

// streamEventMsg is sent for each event while draining the stream (partial content).
type streamEventMsg struct {
	evt messages.StreamMessage
}

// streamDoneMsg is sent when the stream is exhausted; runData is used to save session and flush.
type streamDoneMsg struct {
	runData *agent.RunData
}

// FocusInputMsg is sent by Init() so that Update can call input.Focus() on the
// actual model. Exported for tests that drive the model without running Init()'s Cmd.
type FocusInputMsg struct{}

// excludedDirs contains directory names to exclude from file autocomplete suggestions.
var excludedDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"__pycache__":  true,
}

// ChatModel is a bubbletea Model for interactive text chat sessions.
//
// It accumulates keystrokes into an input buffer (with cursor position),
// submits the buffer to the agent executor on Enter, and streams the
// assistant response incrementally. It shows thinking start/stop, tool use,
// and media placeholders (image/audio/video/file). Exit commands ("exit" /
// "quit") print "Goodbye!" and stop the program.
//
// The model can be driven either by a tea.Program (production) or by calling
// Update directly (unit tests).
type ChatModel struct {
	executor    *agent.Executor
	sessionID   string
	globalFlags *flags.GlobalFlags
	askFlags    *flags.AskFlags
	ctx         context.Context
	out         io.Writer
	errOut      io.Writer
	lines       []chatLine       // conversation history (user, assistant, thinking, tool, media)
	input       *textinput.Model // Bubbles textinput (cursor, blink, editing); pointer so focus persists across value copies
	quitting    bool
	width       int // terminal width in columns (for wrapping); 0 = use default
	height      int // terminal height in rows (reserved for future use)

	// Autocomplete state for @file and /command suggestions.
	fileAutocomplete Autocomplete
	fileSuggestions  []Suggestion // cached file suggestions (populated on first @)
	cmdAutocomplete  Autocomplete
	cmdSuggestions   []Suggestion // cached command/skill suggestions (populated on first /)

	// Streaming state: set when a turn is in progress; cleared on streamDoneMsg.
	stream           agentloop.Stream
	runData          *agent.RunData
	assistantPartial string     // accumulated text (TEXT.DELTA from assistant only) for the current turn
	toolTextPartial  string     // accumulated text (TEXT.DELTA from tool only) until TEXT.END
	reasoningPartial string     // accumulated reasoning (REASONING.DELTA) for the current turn
	thinkingActive   bool       // true between REASONING.START and REASONING.END
	currentTurnLines []chatLine // thinking/tool/media/tool_result lines for the current turn (committed on streamDone)
}

// NewChatModel constructs a ChatModel ready for use.
func NewChatModel(executor *agent.Executor, sessionID string, globalFlags *flags.GlobalFlags, askFlags *flags.AskFlags, ctx context.Context, out, errOut io.Writer) ChatModel {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.PromptStyle = stylePrompt
	ti.Placeholder = "Type a message..."
	ti.Width = 78
	return ChatModel{
		executor:    executor,
		sessionID:   sessionID,
		globalFlags: globalFlags,
		askFlags:    askFlags,
		ctx:         ctx,
		out:         out,
		errOut:      errOut,
		input:       &ti,
	}
}

// IsQuitting reports whether the model has received an exit signal.
// Useful in tests to verify the model responded correctly to "exit"/"quit".
func (m ChatModel) IsQuitting() bool { return m.quitting }

// InputFocused reports whether the Bubbles textinput has focus (for tests).
func (m ChatModel) InputFocused() bool { return m.input.Focused() }

// SessionID returns the current session ID (for tests).
func (m ChatModel) SessionID() string { return m.sessionID }

// Init implements tea.Model. Send FocusInputMsg so Update can focus the input on the real model, then start blink.
func (m ChatModel) Init() tea.Cmd {
	return tea.Sequence(func() tea.Msg { return FocusInputMsg{} }, textinput.Blink)
}

// Update implements tea.Model. It handles keyboard events and agent results.
func (m ChatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// When any autocomplete is active, intercept navigation keys.
		if ac := m.activeAutocomplete(); ac != nil && ac.IsActive() {
			switch msg.Type {
			case tea.KeyUp, tea.KeyDown:
				*ac, _ = ac.Update(msg)
				return m, nil
			case tea.KeyTab:
				selected := ac.Selected()
				if selected != "" {
					if ac == &m.fileAutocomplete {
						m.completeAtSuggestion(selected)
					} else {
						m.completeCmdSuggestion(selected)
					}
				}
				ac.Reset()
				return m, nil
			case tea.KeyEsc:
				ac.Reset()
				return m, nil
			}
		}

		switch msg.Type {
		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyEnter:
			m.fileAutocomplete.Reset()
			m.cmdAutocomplete.Reset()
			rawInput := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")
			if rawInput == "" {
				return m, nil
			}
			if rawInput == "exit" || rawInput == "quit" {
				_, _ = fmt.Fprintln(m.out, "Goodbye!")
				m.quitting = true
				return m, tea.Quit
			}
			// Slash commands: dispatch locally without sending to the LLM.
			if strings.HasPrefix(rawInput, "/") {
				return m.handleSlashCommand(rawInput)
			}
			// Parse @file references before sending to the LLM.
			cleanedText, contentParts, refErr := parseAtReferences(rawInput)
			if refErr != "" {
				errLine := chatLine{kind: chatLineSystem, content: refErr}
				m.lines = append(m.lines, errLine)
				rendered := strings.TrimSuffix(renderChatLineWrapped(errLine, m.effectiveWidth()), "\n")
				return m, tea.Println(rendered)
			}
			m.lines = append(m.lines, chatLine{kind: chatLineUser, content: rawInput})
			// Print user message to scrollback so it persists when scrolling up,
			// then start the agent turn. View() does not render committed lines.
			userRendered := strings.TrimSuffix(
				renderChatLineWrapped(chatLine{kind: chatLineUser, content: rawInput}, m.effectiveWidth()), "\n")
			execInput := agentloop.NewExecuteInput(cleanedText)
			execInput.ContentParts = contentParts
			return m, tea.Batch(tea.Println(userRendered), m.runAgentWithInput(execInput))
		}
		// Delegate all other keys to Bubbles textinput (cursor, backspace, runes, etc.)
		var cmd tea.Cmd
		*m.input, cmd = m.input.Update(msg)
		// After updating input, check for @ or / prefix to activate autocomplete.
		m.updateFileAutocomplete()
		m.updateCmdAutocomplete()
		return m, cmd

	case FocusInputMsg:
		// Focus the input on the actual model (Init cannot do this because it has value receiver).
		cmd := m.input.Focus()
		return m, cmd

	case streamReadyMsg:
		if msg.err != nil {
			_, _ = fmt.Fprintf(m.errOut, "Error: %v\n", msg.err)
			return m, nil
		}
		m.stream = msg.stream
		m.runData = msg.runData
		m.assistantPartial = ""
		m.toolTextPartial = ""
		m.reasoningPartial = ""
		m.thinkingActive = false
		m.currentTurnLines = nil
		return m, consumeOneStreamEvent(msg.stream, msg.runData)

	case streamEventMsg:
		if msg.evt.Type == messages.StreamTypeError {
			if v, ok := msg.evt.Value.(*messages.ErrorValue); ok {
				_, _ = fmt.Fprintf(m.errOut, "Error: %s\n", v.Message)
			}
		} else {
			m.applyStreamEvent(msg.evt)
		}
		return m, consumeOneStreamEvent(m.stream, m.runData)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.width < 20 {
			m.width = 20
		}
		w := m.width - 2 // leave room for "> "
		if w > 0 {
			m.input.Width = w
		}
		return m, nil

	case streamDoneMsg:
		if m.runData != nil {
			_ = m.executor.SaveSession(m.runData)
			_ = m.executor.FlushRecorder(m.runData, m.askFlags.RecordCapturePath)
			m.runData.CloseLogger()
		}
		// Flush any remaining tool text (in case TEXT.END was not received), then commit current turn
		if m.toolTextPartial != "" {
			m.currentTurnLines = append(m.currentTurnLines, chatLine{kind: chatLineToolResult, content: m.toolTextPartial})
		}

		// Collect lines to commit and flush to scrollback.
		width := m.effectiveWidth()
		var cmds []tea.Cmd
		for _, ln := range m.currentTurnLines {
			m.lines = append(m.lines, ln)
			rendered := strings.TrimSuffix(renderChatLineWrapped(ln, width), "\n")
			cmds = append(cmds, tea.Println(rendered))
		}
		if m.assistantPartial != "" {
			assistantLine := chatLine{kind: chatLineAssistant, content: m.assistantPartial}
			m.lines = append(m.lines, assistantLine)
			rendered := strings.TrimSuffix(renderChatLineWrapped(assistantLine, width), "\n")
			cmds = append(cmds, tea.Println(rendered))
		}

		// Clear streaming state.
		m.stream = nil
		m.runData = nil
		m.assistantPartial = ""
		m.toolTextPartial = ""
		m.reasoningPartial = ""
		m.thinkingActive = false
		m.currentTurnLines = nil

		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
		return m, nil
	}

	// Pass through to Bubbles textinput (e.g. cursor blink messages from Init).
	var cmd tea.Cmd
	*m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// isFromTool returns true when the stream event is from a tool (tool result), not the assistant.
func isFromTool(evt messages.StreamMessage) bool {
	return evt.Role == messages.RoleTool || evt.ToolCallId != ""
}

// applyStreamEvent updates model state for one stream event (thinking, tool, media, text).
// It routes text by actor: assistant text goes to assistantPartial, tool text to toolTextPartial and then to a tool_result line.
func (m *ChatModel) applyStreamEvent(evt messages.StreamMessage) {
	switch evt.Type {
	case messages.StreamTypeReasoningStart:
		m.thinkingActive = true
		m.currentTurnLines = append(m.currentTurnLines, chatLine{kind: chatLineThinking, content: "Thinking..."})
	case messages.StreamTypeReasoningDelta:
		if v, ok := evt.Value.(*messages.ReasoningDeltaValue); ok {
			m.reasoningPartial += v.Content
		}
	case messages.StreamTypeReasoningEnd:
		if m.reasoningPartial != "" {
			m.currentTurnLines = append(m.currentTurnLines, chatLine{kind: chatLineThinkingBlock, content: m.reasoningPartial})
		}
		m.currentTurnLines = append(m.currentTurnLines, chatLine{kind: chatLineThinking, content: "Done thinking."})
		m.reasoningPartial = ""
		m.thinkingActive = false
	case messages.StreamTypeToolCallStart:
		if v, ok := evt.Value.(*messages.ToolCallStartValue); ok {
			m.currentTurnLines = append(m.currentTurnLines, chatLine{kind: chatLineTool, content: v.Name})
		}
	case messages.StreamTypeImageStart:
		if evt.Role == messages.RoleTool || evt.ToolCallId != "" {
			m.currentTurnLines = append(m.currentTurnLines, chatLine{kind: chatLineMedia, content: "[Image returned]"})
		}
	case messages.StreamTypeAudioStart:
		if evt.Role == messages.RoleTool || evt.ToolCallId != "" {
			m.currentTurnLines = append(m.currentTurnLines, chatLine{kind: chatLineMedia, content: "[Audio returned]"})
		}
	case messages.StreamTypeVideoStart:
		if evt.Role == messages.RoleTool || evt.ToolCallId != "" {
			m.currentTurnLines = append(m.currentTurnLines, chatLine{kind: chatLineMedia, content: "[Video returned]"})
		}
	case messages.StreamTypeFileStart:
		if evt.Role == messages.RoleTool || evt.ToolCallId != "" {
			label := "[File returned]"
			if v, ok := evt.Value.(*messages.FileStartValue); ok && v.Name != "" {
				label = "[File returned: " + v.Name + "]"
			}
			m.currentTurnLines = append(m.currentTurnLines, chatLine{kind: chatLineMedia, content: label})
		}
	case messages.StreamTypeTextStart:
		if isFromTool(evt) {
			m.toolTextPartial = ""
		}
	case messages.StreamTypeTextDelta:
		if v, ok := evt.Value.(*messages.TextDeltaValue); ok {
			if isFromTool(evt) {
				m.toolTextPartial += v.Content
			} else {
				m.assistantPartial += v.Content
			}
		}
	case messages.StreamTypeTextEnd:
		if isFromTool(evt) && m.toolTextPartial != "" {
			m.currentTurnLines = append(m.currentTurnLines, chatLine{kind: chatLineToolResult, content: m.toolTextPartial})
			m.toolTextPartial = ""
		}
	}
}

// consumeOneStreamEvent returns a Cmd that reads one event from the stream and
// sends either streamEventMsg or streamDoneMsg.
func consumeOneStreamEvent(stream agentloop.Stream, runData *agent.RunData) tea.Cmd {
	return func() tea.Msg {
		if stream.HasNext() {
			return streamEventMsg{evt: stream.Response()}
		}
		_ = stream.Close()
		return streamDoneMsg{runData: runData}
	}
}

// effectiveWidth returns the terminal width to use for wrapping (default 80 if not set).
func (m ChatModel) effectiveWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 80
}

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

// runAgentWithInput starts a streaming turn: builds the loop, runs ExecuteStreamingTurn,
// and returns a tea.Cmd that emits streamReadyMsg with the event stream. The
// model then drains the stream via consumeOneStreamEvent and renders partials in View.
func (m ChatModel) runAgentWithInput(execInput agentloop.ExecuteInput) tea.Cmd {
	return func() tea.Msg {
		cfg := BuildAgentConfigFromFlags(m.globalFlags, m.askFlags, nil, m.sessionID)
		cfg.Stream = true
		storage, err := m.executor.GetSessionStorage(cfg)
		if err != nil {
			return streamReadyMsg{err: err}
		}
		initialHistory, err := storage.Load(m.sessionID)
		if err != nil {
			return streamReadyMsg{err: err}
		}
		if initialHistory == nil {
			initialHistory = []messages.Message{}
		}
		cfgWithHistory := *cfg
		cfgWithHistory.SessionID = m.sessionID
		cfgWithHistory.InitialHistory = initialHistory
		if err := cfgWithHistory.Validate(); err != nil {
			return streamReadyMsg{err: err}
		}
		runData, err := m.executor.BuildLoop(m.ctx, &cfgWithHistory)
		if err != nil {
			return streamReadyMsg{err: err}
		}
		stream, err := m.executor.ExecuteStreamingTurn(m.ctx, runData, execInput, &cfgWithHistory)
		if err != nil {
			runData.CloseLogger()
			return streamReadyMsg{err: err}
		}
		return streamReadyMsg{stream: stream, runData: runData}
	}
}

// handleSlashCommand dispatches slash commands (/system, /help, /clear) locally.
// It returns the updated model and any tea.Cmd to execute.
func (m ChatModel) handleSlashCommand(input string) (tea.Model, tea.Cmd) {
	cmd := strings.TrimPrefix(input, "/")
	cmd = strings.TrimSpace(cmd)
	switch cmd {
	case "system":
		return m.handleSystemCommand()
	case "help":
		return m.handleHelpCommand()
	case "clear":
		return m.handleClearCommand()
	default:
		// Try loading as a skill name.
		return m.handleSkillCommand(cmd)
	}
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

const helpText = `/system  — Show the system prompt for this session
/help    — Show this help message
/clear   — Clear conversation history and start fresh
/skill   — Load a skill's instructions (e.g. /my-skill)
@file    — Attach a file to your message (e.g. @path/to/file.txt)
@dir/    — Attach a directory listing (e.g. @src/)`

// handleHelpCommand displays available commands and syntax.
func (m ChatModel) handleHelpCommand() (tea.Model, tea.Cmd) {
	helpLine := chatLine{kind: chatLineSystem, content: helpText}
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

// updateFileAutocomplete checks the current input for an @ prefix and activates
// file autocomplete suggestions if appropriate.
func (m *ChatModel) updateFileAutocomplete() {
	prefix := extractAtPrefix(m.input.Value())
	if prefix == "" && !strings.HasSuffix(m.input.Value(), "@") {
		// No @ context — deactivate.
		if m.fileAutocomplete.IsActive() {
			m.fileAutocomplete.Reset()
		}
		return
	}
	// Lazy-load file suggestions on first activation.
	if m.fileSuggestions == nil {
		workDir, err := os.Getwd()
		if err != nil {
			return
		}
		m.fileSuggestions = scanFileSuggestions(workDir)
		m.fileAutocomplete.SetSuggestions(m.fileSuggestions)
	}
	m.fileAutocomplete.SetFilter(prefix)
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

// completeAtSuggestion replaces the @prefix in the input with the completed suggestion.
func (m *ChatModel) completeAtSuggestion(selected string) {
	val := m.input.Value()
	// Find the last @ token start position.
	idx := -1
	for i := len(val) - 1; i >= 0; i-- {
		if val[i] == '@' {
			if i == 0 || val[i-1] == ' ' {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return
	}
	// Replace from @ to end with @selected + trailing space.
	newVal := val[:idx] + "@" + selected + " "
	m.input.SetValue(newVal)
	m.input.SetCursor(len(newVal))
}

// parseAtReferences extracts @path tokens from the input, reads each referenced file,
// and returns the cleaned prompt text (with @tokens removed), content parts for the LLM,
// and an error message string (empty on success). If any referenced file does not exist
// or cannot be read, an error is returned and no content parts are produced.
func parseAtReferences(input string) (cleanedText string, parts []messages.ContentPart, errMsg string) {
	words := strings.Fields(input)
	if len(words) == 0 {
		return input, nil, ""
	}

	workDir, err := os.Getwd()
	if err != nil {
		return input, nil, ""
	}

	var textWords []string
	var errors []string
	for _, word := range words {
		if !strings.HasPrefix(word, "@") || len(word) == 1 {
			textWords = append(textWords, word)
			continue
		}
		refPath := word[1:]
		absPath := refPath
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(workDir, absPath)
		}
		info, statErr := os.Stat(absPath)
		if statErr != nil {
			errors = append(errors, "File not found: "+refPath)
			continue
		}
		if info.IsDir() {
			entries, dirErr := os.ReadDir(absPath)
			if dirErr != nil {
				errors = append(errors, "Cannot read directory: "+refPath)
				continue
			}
			var listing []string
			for _, entry := range entries {
				name := entry.Name()
				if entry.IsDir() {
					name += "/"
				}
				listing = append(listing, name)
			}
			content := fmt.Sprintf("[Directory: %s]\n%s", refPath, strings.Join(listing, "\n"))
			parts = append(parts, messages.TextPart{Text: content})
			continue
		}
		// Check if image by file extension before reading.
		if isImageExtension(absPath) {
			data, readErr := os.ReadFile(absPath)
			if readErr != nil {
				errors = append(errors, "Cannot read file: "+refPath)
				continue
			}
			parts = append(parts, messages.ImagePart{
				Bytes:     data,
				MediaType: imageMediaType(absPath),
			})
			continue
		}
		data, readErr := os.ReadFile(absPath)
		if readErr != nil {
			errors = append(errors, "Cannot read file: "+refPath)
			continue
		}
		mediaType := http.DetectContentType(data)
		if strings.HasPrefix(mediaType, "text/") || mediaType == "application/octet-stream" {
			// Include text files as TextPart with filename context.
			content := fmt.Sprintf("[File: %s]\n%s", filepath.Base(absPath), string(data))
			parts = append(parts, messages.TextPart{Text: content})
		} else {
			// Unknown binary file — include as text reference.
			content := fmt.Sprintf("[Binary file: %s (%d bytes)]", filepath.Base(absPath), len(data))
			parts = append(parts, messages.TextPart{Text: content})
		}
	}

	if len(errors) > 0 {
		return "", nil, strings.Join(errors, "\n")
	}

	return strings.Join(textWords, " "), parts, ""
}

// imageExtensions maps file extensions to MIME media types for image files.
var imageExtensions = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
}

// isImageExtension returns true if the file path has a recognized image extension.
func isImageExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	_, ok := imageExtensions[ext]
	return ok
}

// imageMediaType returns the MIME type for a recognized image extension.
func imageMediaType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if mt, ok := imageExtensions[ext]; ok {
		return mt
	}
	return "application/octet-stream"
}

// scanFileSuggestions walks the working directory and returns Suggestion entries
// for all files and directories, excluding hidden/ignored directories. Directories
// have a trailing "/" in their label. Paths are relative to workDir.
func scanFileSuggestions(workDir string) []Suggestion {
	var suggestions []Suggestion
	_ = filepath.WalkDir(workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, relErr := filepath.Rel(workDir, path)
		if relErr != nil || rel == "." {
			return nil
		}
		// Use forward slashes for consistent display.
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if excludedDirs[d.Name()] {
				return filepath.SkipDir
			}
			suggestions = append(suggestions, Suggestion{Label: rel + "/"})
			return nil
		}
		suggestions = append(suggestions, Suggestion{Label: rel})
		return nil
	})
	return suggestions
}

// extractAtPrefix returns the prefix typed after the last @ token in the input,
// or empty string if the cursor is not in an @ context. For example:
//
//	"@src/ma"      → "src/ma"
//	"hello @sr"    → "sr"
//	"hello world"  → ""
//	"@"            → ""
func extractAtPrefix(input string) string {
	// Find the last @ that starts a token (preceded by space or at position 0).
	idx := -1
	for i := len(input) - 1; i >= 0; i-- {
		if input[i] == '@' {
			if i == 0 || input[i-1] == ' ' {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return ""
	}
	// The prefix is everything after @ until end of input (no space after @).
	after := input[idx+1:]
	if strings.Contains(after, " ") {
		return "" // cursor moved past the @ token
	}
	return after
}

// ChatService runs interactive text chat sessions backed by a bubbletea TUI.
type ChatService struct {
	executor    *agent.Executor
	globalFlags *flags.GlobalFlags
	askFlags    *flags.AskFlags
}

// NewChatService creates a ChatService backed by the given agent executor and flags.
func NewChatService(executor *agent.Executor, globalFlags *flags.GlobalFlags, askFlags *flags.AskFlags) *ChatService {
	return &ChatService{executor: executor, globalFlags: globalFlags, askFlags: askFlags}
}

// Run starts the interactive chat loop.
//
// It prints the session banner, then hands control to a bubbletea program
// that reads keystrokes from in and writes the rendered UI and agent responses
// to out. The program exits when the user types "exit", "quit", or presses
// Ctrl+C, or when in reaches EOF.
func (s *ChatService) Run(ctx context.Context, in io.Reader, out, errOut io.Writer) error {
	cfg := BuildAgentConfigFromFlags(s.globalFlags, s.askFlags, nil, "")
	sessionID, err := s.executor.NewChatSessionID(cfg)
	if err != nil {
		return fmt.Errorf("create chat session: %w", err)
	}

	_, _ = fmt.Fprintln(out, "Port OS Agent Chat (type 'exit' or 'quit' to end)")
	_, _ = fmt.Fprintln(out, "---")

	model := NewChatModel(s.executor, sessionID, s.globalFlags, s.askFlags, ctx, out, errOut)
	p := tea.NewProgram(model,
		tea.WithInput(in),
		tea.WithOutput(out),
	)
	_, err = p.Run()
	return err
}
