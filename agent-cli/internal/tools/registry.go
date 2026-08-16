package tools

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
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

type RegistryErrorKind string

const (
	RegistryErrorDuplicate RegistryErrorKind = "duplicate"
	RegistryErrorNotFound  RegistryErrorKind = "not_found"
	RegistryErrorEmptyName RegistryErrorKind = "empty_name"
	RegistryErrorNilTool   RegistryErrorKind = "nil_tool"
)

var (
	ErrDuplicateTool = errors.New("tool already registered")
	ErrToolNotFound  = errors.New("tool not found")
	ErrEmptyToolName = errors.New("tool name is empty")
	ErrNilTool       = errors.New("tool is nil")
)

var registryErrorSentinels = map[RegistryErrorKind]error{
	RegistryErrorDuplicate: ErrDuplicateTool,
	RegistryErrorNotFound:  ErrToolNotFound,
	RegistryErrorEmptyName: ErrEmptyToolName,
	RegistryErrorNilTool:   ErrNilTool,
}

type RegistryError struct {
	Kind RegistryErrorKind
	Name string
}

type ToolInvocationError struct{ Err error }

func (e *ToolInvocationError) Error() string { return e.Err.Error() }
func (e *ToolInvocationError) Unwrap() error { return e.Err }

func (e *RegistryError) Error() string {
	switch e.Kind {
	case RegistryErrorDuplicate:
		return fmt.Sprintf("tool %q is already registered", e.Name)
	case RegistryErrorNotFound:
		return fmt.Sprintf("tool %q not found", e.Name)
	case RegistryErrorEmptyName:
		return "tool name must not be empty"
	case RegistryErrorNilTool:
		return "tool must not be nil"
	default:
		return fmt.Sprintf("registry error: %s", e.Kind)
	}
}

func (e *RegistryError) Unwrap() error {
	return registryErrorSentinels[e.Kind]
}

func newRegistryError(kind RegistryErrorKind, name string) *RegistryError {
	return &RegistryError{Kind: kind, Name: name}
}

func isNilTool(tool Tool) bool {
	if tool == nil {
		return true
	}
	v := reflect.ValueOf(tool)
	return v.Kind() == reflect.Pointer && v.IsNil()
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
			_ = registry.Register(NewExecToolWithConfig("", false, cfg))
		} else {
			_ = registry.Register(NewExecTool("", false))
		}
	}
	if enabled("read_file") {
		_ = registry.Register(NewReadFileTool("", false))
	}
	if enabled("write_file") {
		_ = registry.Register(NewWriteFileTool("", false))
	}
	if enabled("edit_file") {
		_ = registry.Register(NewEditFileTool("", false))
	}
	if enabled("append_file") {
		_ = registry.Register(NewAppendFileTool("", false))
	}
	if enabled("list_dir") {
		_ = registry.Register(NewListDirTool("", false))
	}
	if enabled("web_fetch") {
		_ = registry.Register(NewWebFetchTool(0))
	}
	if enabled("web_search") {
		if searchTool := NewWebSearchTool(WebSearchToolOptions{DuckDuckGoEnabled: true}); searchTool != nil {
			_ = registry.Register(searchTool)
		}
	}
	if enabled("show") {
		_ = registry.Register(NewScreenTool())
	}
	if enabled("mouse") {
		_ = registry.Register(NewMouseTool())
	}
	if enabled("load_skill") {
		_ = registry.Register(NewLoadSkillTool())
	}
	if enabled("sleep") {
		_ = registry.Register(NewSleepTool())
	}
	return registry
}

func (r *ToolRegistry) Register(tool Tool) error {
	if isNilTool(tool) {
		return newRegistryError(RegistryErrorNilTool, "")
	}
	name := tool.Name()
	if strings.TrimSpace(name) == "" {
		return newRegistryError(RegistryErrorEmptyName, name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return newRegistryError(RegistryErrorDuplicate, name)
	}
	r.tools[name] = tool
	return nil
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// Lookup returns a registered tool or a typed not-found error.
func (r *ToolRegistry) Lookup(name string) (Tool, error) {
	tool, ok := r.Get(name)
	if !ok {
		return nil, newRegistryError(RegistryErrorNotFound, name)
	}
	return tool, nil
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

	tool, err := r.Lookup(name)
	if err != nil {
		l.Error("Tool not found", zap.String("tool", name))
		return nil, err
	}

	start := time.Now()
	msgs, err := tool.Execute(ctx, args)
	duration := time.Since(start)

	if err != nil {
		l.Error("Tool execution failed", zap.String("tool", name), zap.Duration("duration", duration), zap.Error(err))
		return nil, &ToolInvocationError{Err: err}
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
