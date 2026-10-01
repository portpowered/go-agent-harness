package tools

import (
	"context"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// Tool is the interface that all tools must implement.
// Execute returns messages (e.g. text, images, audio) for the agent loop; use RoleTool for tool result content.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	// When it returns error, we presume that we should terminate the loop entirely,
	// however, if its a failure that the model can handle such as a file not found, just return the error as a message.
	Execute(ctx context.Context, args map[string]any) ([]messages.Message, error)
}

// ErrorAsToolMessage returns the error as a single tool text message and nil error,
// so the agent loop continues and the model can see and handle the failure (e.g. file not found, permission denied).
func ErrorAsToolMessage(err error) ([]messages.Message, error) {
	if err == nil {
		return nil, nil
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, err.Error())}, nil
}

func ToolToSchema(tool Tool) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        tool.Name(),
			"description": tool.Description(),
			"parameters":  tool.Parameters(),
		},
	}
}
