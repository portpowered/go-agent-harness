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

// ContextualTool is an optional interface that tools can implement
// to receive the current message context (channel, chatID)
type ContextualTool interface {
	Tool
	SetContext(channel, chatID string)
}

// AsyncCallback is a function type that async tools use to notify completion.
// When an async tool finishes its work, it calls this callback with the result messages or error.
//
// The ctx parameter allows the callback to be canceled if the agent is shutting down.
//
// Example usage in an async tool:
//
//	func (t *MyAsyncTool) Execute(ctx context.Context, args map[string]interface{}) ([]messages.Message, error) {
//	    go func() {
//	        msgs, err := doAsyncWork()
//	        if t.callback != nil {
//	            t.callback(ctx, msgs, err)
//	        }
//	    }()
//	    return []messages.Message{messages.NewTextMessage(messages.RoleTool, "Async task started")}, nil
//	}
type AsyncCallback func(ctx context.Context, messages []messages.Message, err error)

// AsyncTool is an optional interface that tools can implement to support
// asynchronous execution with completion callbacks.
//
// Async tools return immediately with an AsyncResult, then notify completion
// via the callback set by SetCallback.
//
// This is useful for:
// - Long-running operations that shouldn't block the agent loop
// - Subagent spawns that complete independently
// - Background tasks that need to report results later
//
// Example:
//
//	type SpawnTool struct {
//	    callback AsyncCallback
//	}
//
//	func (t *SpawnTool) SetCallback(cb AsyncCallback) {
//	    t.callback = cb
//	}
//
//	func (t *SpawnTool) Execute(ctx context.Context, args map[string]interface{}) ([]messages.Message, error) {
//	    go t.runSubagent(ctx, args)
//	    return []messages.Message{messages.NewTextMessage(messages.RoleTool, "Subagent spawned, will report back")}, nil
//	}
type AsyncTool interface {
	Tool
	// SetCallback registers a callback function to be invoked when the async operation completes.
	// The callback will be called from a goroutine and should handle thread-safety if needed.
	SetCallback(cb AsyncCallback)
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
