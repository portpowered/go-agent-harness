package services

import (
	"context"
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

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
