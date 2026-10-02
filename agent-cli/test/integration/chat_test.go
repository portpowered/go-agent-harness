package integration

import (
	"bytes"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

// asChatModel narrows a bubbletea model returned by Update back to the chat model.
func asChatModel(model tea.Model) *services.ChatModel {
	chat, ok := model.(*services.ChatModel)
	if !ok {
		panic(fmt.Sprintf("model is %T, want *services.ChatModel", model))
	}
	return chat
}

// NewTestWriter returns a writer that captures stdout and stderr.
func NewTestWriter() *testWriter {
	return &testWriter{
		stdout: bytes.Buffer{},
		stderr: bytes.Buffer{},
	}
}
