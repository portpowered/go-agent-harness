// This file owns the interactive chat REPL state and orchestration, including Bubble Tea updates, streaming turns, and ChatService lifecycle.

package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
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

// streamReadyMsg is sent when the streaming turn has started and the event stream is ready.
type streamReadyMsg struct {
	stream agentloop.Stream
	handle session.SessionHandle
	err    error
}

// streamEventMsg is sent for each event while draining the stream (partial content).
type streamEventMsg struct {
	evt messages.StreamMessage
}

// streamDoneMsg is sent when the stream is exhausted; runData is used to save session and flush.
type streamDoneMsg struct {
	handle session.SessionHandle
}

// FocusInputMsg is sent by Init() so that Update can call input.Focus() on the
// actual model. Exported for tests that drive the model without running Init()'s Cmd.
type FocusInputMsg struct{}

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
	service     session.Service
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
	handle           session.SessionHandle
	assistantPartial string     // accumulated text (TEXT.DELTA from assistant only) for the current turn
	toolTextPartial  string     // accumulated text (TEXT.DELTA from tool only) until TEXT.END
	reasoningPartial string     // accumulated reasoning (REASONING.DELTA) for the current turn
	thinkingActive   bool       // true between REASONING.START and REASONING.END
	currentTurnLines []chatLine // thinking/tool/media/tool_result lines for the current turn (committed on streamDone)
}

// NewChatModel constructs a ChatModel ready for use.
func NewChatModel(service session.Service, sessionID string, globalFlags *flags.GlobalFlags, askFlags *flags.AskFlags, ctx context.Context, out, errOut io.Writer) ChatModel {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.PromptStyle = stylePrompt
	ti.Placeholder = "Type a message..."
	ti.Width = 78
	return ChatModel{
		service:     service,
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
		m.handle = msg.handle
		m.assistantPartial = ""
		m.toolTextPartial = ""
		m.reasoningPartial = ""
		m.thinkingActive = false
		m.currentTurnLines = nil
		return m, consumeOneStreamEvent(msg.stream, msg.handle)

	case streamEventMsg:
		if msg.evt.Type == messages.StreamTypeError {
			if v, ok := msg.evt.Value.(*messages.ErrorValue); ok {
				_, _ = fmt.Fprintf(m.errOut, "Error: %s\n", v.Message)
			}
		} else {
			m.applyStreamEvent(msg.evt)
		}
		return m, consumeOneStreamEvent(m.stream, m.handle)

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
		cleanupErr := finalizeChatHandle(m.handle, m.askFlags.RecordCapturePath)
		reportChatHandleError(&m, "finalizing session", cleanupErr)
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
		m.handle = nil
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
func consumeOneStreamEvent(stream agentloop.Stream, handle session.SessionHandle) tea.Cmd {
	return func() tea.Msg {
		if stream.HasNext() {
			return streamEventMsg{evt: stream.Response()}
		}
		_ = stream.Close()
		return streamDoneMsg{handle: handle}
	}
}

// effectiveWidth returns the terminal width to use for wrapping (default 80 if not set).
func (m ChatModel) effectiveWidth() int {
	if m.width > 0 {
		return m.width
	}
	return 80
}

// runAgentWithInput starts a streaming turn: builds the loop, runs ExecuteStreamingTurn,
// and returns a tea.Cmd that emits streamReadyMsg with the event stream. The
// model then drains the stream via consumeOneStreamEvent and renders partials in View.
func (m ChatModel) runAgentWithInput(execInput agentloop.ExecuteInput) tea.Cmd {
	return func() tea.Msg {
		cfg := BuildAgentConfigFromFlags(m.globalFlags, m.askFlags, nil, m.sessionID)
		if m.service == nil {
			return streamReadyMsg{err: fmt.Errorf("session service is not configured")}
		}
		handle, err := m.service.Open(m.ctx, *cfg)
		if err != nil {
			return streamReadyMsg{err: err}
		}
		stream, err := handle.Stream(m.ctx, execInput)
		if err != nil {
			return streamReadyMsg{err: errors.Join(err, closeChatHandle(handle))}
		}
		return streamReadyMsg{stream: stream, handle: handle}
	}
}

// ChatService runs interactive text chat sessions backed by a bubbletea TUI.
type ChatService struct {
	service     session.Service
	globalFlags *flags.GlobalFlags
	askFlags    *flags.AskFlags
}

// NewChatService creates a ChatService backed by the given agent executor and flags.
func NewChatService(service session.Service, globalFlags *flags.GlobalFlags, askFlags *flags.AskFlags) *ChatService {
	return &ChatService{service: service, globalFlags: globalFlags, askFlags: askFlags}
}

// Run starts the interactive chat loop.
//
// It prints the session banner, then hands control to a bubbletea program
// that reads keystrokes from in and writes the rendered UI and agent responses
// to out. The program exits when the user types "exit", "quit", or presses
// Ctrl+C, or when in reaches EOF.
func (s *ChatService) Run(ctx context.Context, in io.Reader, out, errOut io.Writer) error {
	cfg := BuildAgentConfigFromFlags(s.globalFlags, s.askFlags, nil, "")
	if s.service == nil {
		return fmt.Errorf("session service is not configured")
	}
	sessionID, err := s.service.NewSessionID(ctx, *cfg)
	if err != nil {
		return fmt.Errorf("create chat session: %w", err)
	}

	if _, err := fmt.Fprintln(out, "Port OS Agent Chat (type 'exit' or 'quit' to end)"); err != nil {
		return fmt.Errorf("write chat banner: %w", err)
	}
	if _, err := fmt.Fprintln(out, "---"); err != nil {
		return fmt.Errorf("write chat banner separator: %w", err)
	}

	model := NewChatModel(s.service, sessionID, s.globalFlags, s.askFlags, ctx, out, errOut)
	p := tea.NewProgram(model,
		tea.WithInput(in),
		tea.WithOutput(out),
	)
	_, err = p.Run()
	return err
}
