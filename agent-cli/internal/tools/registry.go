package tools

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/logger"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"go.uber.org/zap"
)

type ToolRegistry struct {
	tools map[string]Tool
	mu    sync.RWMutex
}

func NewToolRegistry() *ToolRegistry {
	return NewToolRegistryFromConfig(nil)
}

// NewToolRegistryFromConfig creates a registry with only tools that are enabled in config.
// If cfg is nil or tools.list is empty, all tools are enabled. Use tools.list with enabled: false to disable tools.
func NewToolRegistryFromConfig(cfg *config.Config) *ToolRegistry {
	registry := &ToolRegistry{
		tools: make(map[string]Tool),
	}
	toolsCfg := config.ToolsConfig{}
	if cfg != nil {
		toolsCfg = cfg.Tools
	}
	enabled := func(id string) bool { return toolsCfg.ToolEnabled(id) }

	if enabled("exec") {
		if cfg != nil {
			registry.Register(NewExecToolWithConfig("", false, cfg))
		} else {
			registry.Register(NewExecTool("", false))
		}
	}
	if enabled("read_file") {
		registry.Register(NewReadFileTool("", false))
	}
	if enabled("write_file") {
		registry.Register(NewWriteFileTool("", false))
	}
	if enabled("edit_file") {
		registry.Register(NewEditFileTool("", false))
	}
	if enabled("append_file") {
		registry.Register(NewAppendFileTool("", false))
	}
	if enabled("list_dir") {
		registry.Register(NewListDirTool("", false))
	}
	if enabled("web_fetch") {
		registry.Register(NewWebFetchTool(0))
	}
	if enabled("web_search") {
		if searchTool := NewWebSearchTool(WebSearchToolOptions{DuckDuckGoEnabled: true}); searchTool != nil {
			registry.Register(searchTool)
		}
	}
	if enabled("show") {
		registry.Register(NewScreenTool())
	}
	if enabled("mouse") {
		registry.Register(NewMouseTool())
	}
	if enabled("load_skill") {
		registry.Register(NewLoadSkillTool())
	}
	if enabled("sleep") {
		registry.Register(NewSleepTool())
	}
	return registry
}

func (r *ToolRegistry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, args map[string]any) ([]messages.Message, error) {
	return r.ExecuteWithContext(ctx, name, args)
}

// ExecuteWithContext executes a tool with channel/chatID context and optional async callback.
// If the tool implements AsyncTool and a non-nil callback is provided,
// the callback will be set on the tool before execution.
// Returns messages (e.g. text, images) for the agent loop and supports streaming and multimodal content.
func (r *ToolRegistry) ExecuteWithContext(
	ctx context.Context,
	name string,
	args map[string]any,
) ([]messages.Message, error) {
	l := logger.GetRequestLoggerFromContext(ctx)
	l.Info("Tool execution started", zap.String("tool", name), zap.Any("args", args))

	tool, ok := r.Get(name)
	if !ok {
		l.Error("Tool not found", zap.String("tool", name))
		return nil, fmt.Errorf("tool %q not found", name)
	}

	start := time.Now()
	msgs, err := tool.Execute(ctx, args)
	duration := time.Since(start)

	if err != nil {
		l.Error("Tool execution failed", zap.String("tool", name), zap.Duration("duration", duration), zap.Error(err))
		return nil, err
	}
	resultLen := 0
	for _, m := range msgs {
		resultLen += len(m.TextContent())
	}
	l.Info("Tool execution completed", zap.String("tool", name), zap.Duration("duration", duration), zap.Int("message_count", len(msgs)), zap.Int("result_length", resultLen))
	return msgs, nil
}

func (r *ToolRegistry) GetDefinitions() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	definitions := make([]map[string]any, 0, len(r.tools))
	for _, tool := range r.tools {
		definitions = append(definitions, ToolToSchema(tool))
	}
	return definitions
}

// List returns a list of all registered tool names.
func (r *ToolRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// GetSummaries returns human-readable summaries of all registered tools.
// Returns a slice of "name - description" strings.
func (r *ToolRegistry) GetSummaries() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	summaries := make([]string, 0, len(r.tools))
	for _, tool := range r.tools {
		summaries = append(summaries, fmt.Sprintf("- `%s` - %s", tool.Name(), tool.Description()))
	}
	return summaries
}
