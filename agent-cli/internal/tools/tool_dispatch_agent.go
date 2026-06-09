package tools

import (
	"context"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// DispatchAgentTool spawns an isolated child agent with its own system prompt and task,
// runs it to completion (ModeAskOnce), and returns the final text response as the tool result.
// Child agents are fully isolated: own conversation, own system prompt, no shared history.
type DispatchAgentTool struct {
	callback   AsyncCallback
	inferencer messages.Inferencer
	registry   *ToolRegistry
}

// Compile-time check that DispatchAgentTool implements AsyncTool.
var _ AsyncTool = (*DispatchAgentTool)(nil)

// NewDispatchAgentTool creates a new DispatchAgentTool.
// inferencer is the LLM provider used by child agents (configured from the parent's config).
// registry is the parent tool registry; child agents draw their tools from it (filtered by the tools parameter).
func NewDispatchAgentTool(inferencer messages.Inferencer, registry *ToolRegistry) *DispatchAgentTool {
	return &DispatchAgentTool{
		inferencer: inferencer,
		registry:   registry,
	}
}

func (t *DispatchAgentTool) Name() string { return "dispatch_agent" }

func (t *DispatchAgentTool) Description() string {
	return "Spawn an isolated child agent with a custom system prompt and task. " +
		"The child agent runs to completion and returns its final text response. " +
		"Child agents are fully isolated with their own conversation history and no shared state with the parent."
}

func (t *DispatchAgentTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"system_prompt": map[string]any{
				"type":        "string",
				"description": "The system prompt for the child agent.",
			},
			"task": map[string]any{
				"type":        "string",
				"description": "The task for the child agent to complete.",
			},
			"tools": map[string]any{
				"type":        "array",
				"description": "Optional list of tool names to enable for the child agent. Defaults to all enabled tools.",
				"items": map[string]any{
					"type": "string",
				},
			},
		},
		"required": []string{"system_prompt", "task"},
	}
}

// SetCallback registers a callback for async completion notification.
func (t *DispatchAgentTool) SetCallback(cb AsyncCallback) {
	t.callback = cb
}

// Execute spawns and runs the child agent synchronously, returning its final text response.
func (t *DispatchAgentTool) Execute(ctx context.Context, args map[string]any) ([]messages.Message, error) {
	systemPrompt, ok := args["system_prompt"].(string)
	if !ok || systemPrompt == "" {
		return ErrorAsToolMessage(fmt.Errorf("system_prompt is required"))
	}
	task, ok := args["task"].(string)
	if !ok || task == "" {
		return ErrorAsToolMessage(fmt.Errorf("task is required"))
	}

	// Parse optional tools list.
	toolNames := parseToolNames(args["tools"])

	// Build an isolated child registry (filtered or full copy of parent).
	childRegistry := t.buildChildRegistry(toolNames)
	childExecutor := NewRegistryExecutor(childRegistry)
	childToolDefs := childRegistry.ToAgentLoopDefs()

	// Build a fully isolated child AgentLoop (ModeAskOnce, fresh conversation).
	loopOpts := []agentloop.Option{
		agentloop.WithInferencer(t.inferencer),
		agentloop.WithToolExecutor(childExecutor),
		agentloop.WithTools(childToolDefs),
		agentloop.WithSystemPrompt(systemPrompt),
	}
	childLoop, err := agentloop.New(loopOpts...)
	if err != nil {
		return ErrorAsToolMessage(fmt.Errorf("failed to create child agent: %w", err))
	}

	// Run the child agent to completion.
	input := agentloop.ExecuteInput{Message: task}
	result, err := childLoop.Execute(ctx, input)
	if err != nil {
		return ErrorAsToolMessage(fmt.Errorf("child agent error: %w", err))
	}

	response := result.Text()
	if response == "" {
		response = "(child agent returned no text response)"
	}
	return []messages.Message{messages.NewTextMessage(messages.RoleTool, response)}, nil
}

// buildChildRegistry returns a ToolRegistry containing only the tools the child agent should access.
// If toolNames is non-empty, only those named tools are included; otherwise all parent tools are copied.
func (t *DispatchAgentTool) buildChildRegistry(toolNames []string) *ToolRegistry {
	t.registry.mu.RLock()
	defer t.registry.mu.RUnlock()

	child := &ToolRegistry{tools: make(map[string]Tool)}
	if len(toolNames) == 0 {
		for name, tool := range t.registry.tools {
			child.tools[name] = tool
		}
	} else {
		for _, name := range toolNames {
			if tool, ok := t.registry.tools[name]; ok {
				child.tools[name] = tool
			}
		}
	}
	return child
}

// parseToolNames extracts a []string from the raw "tools" argument (handles []any from JSON).
func parseToolNames(raw any) []string {
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		names := make([]string, 0, len(v))
		for _, item := range v {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
		return names
	}
	return nil
}
