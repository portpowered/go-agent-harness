package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	core "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/filesystem"
)

type ToolRegistry struct {
	tools            map[string]core.Tool
	diagnosticWriter io.Writer
	mu               sync.RWMutex
}

func registryDiagnosticWriter(registry *ToolRegistry) io.Writer {
	if registry == nil || registry.diagnosticWriter == nil {
		return io.Discard
	}
	return registry.diagnosticWriter
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
	ErrToolNotFound  = public.ErrToolNotFound
	ErrEmptyToolName = errors.New("tool name is empty")
	ErrNilTool       = errors.New("tool is nil")
)

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
	switch e.Kind {
	case RegistryErrorDuplicate:
		return ErrDuplicateTool
	case RegistryErrorNotFound:
		return ErrToolNotFound
	case RegistryErrorEmptyName:
		return ErrEmptyToolName
	case RegistryErrorNilTool:
		return ErrNilTool
	default:
		return nil
	}
}

func newRegistryError(kind RegistryErrorKind, name string) *RegistryError {
	return &RegistryError{Kind: kind, Name: name}
}

func isNilTool(tool core.Tool) bool {
	if tool == nil {
		return true
	}
	v := reflect.ValueOf(tool)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// cloneWithSessionImagePreparer returns a registry snapshot whose read_image
// tool is bound to one session. core.Tool instances for every other name are
// shared read-only values; the registry map itself is always copied so a
// session cannot overwrite another session's image preparer.
func (r *ToolRegistry) cloneWithSessionImagePreparer(preparer filesystem.ImagePartPreparer) *ToolRegistry {
	clone := &ToolRegistry{tools: make(map[string]core.Tool), diagnosticWriter: registryDiagnosticWriter(r)}
	if r == nil {
		return clone
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, tool := range r.tools {
		if name == filesystem.ReadImageToolID {
			if readImage, ok := tool.(*filesystem.ReadImageTool); ok {
				tool = readImage.WithSessionImagePreparer(preparer)
			}
		}
		clone.tools[name] = tool
	}
	return clone
}

// cloneWithFilesystemPolicy returns a registry snapshot with every known
// customer-facing filesystem tool rebuilt against the same policy. The
// original registry remains untouched for callers that own a separate scope.
func (r *ToolRegistry) cloneWithFilesystemPolicy(policy *filesystem.FilesystemPolicy) *ToolRegistry {
	clone := &ToolRegistry{tools: make(map[string]core.Tool), diagnosticWriter: registryDiagnosticWriter(r)}
	if r == nil {
		return clone
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, tool := range r.tools {
		switch name {
		case "read_file":
			tool = filesystem.NewReadFileToolWithPolicy(policy)
		case filesystem.ReadImageToolID:
			if readImage, ok := tool.(*filesystem.ReadImageTool); ok {
				tool = filesystem.NewReadImageToolWithPolicy(policy, readImage.SessionImagePreparer())
			} else {
				tool = filesystem.NewReadImageToolWithPolicy(policy)
			}
		case "write_file":
			tool = filesystem.NewWriteFileToolWithPolicy(policy)
		case "edit_file":
			tool = filesystem.NewEditFileToolWithPolicy(policy)
		case "append_file":
			tool = filesystem.NewAppendFileToolWithPolicy(policy)
		case "list_dir":
			tool = filesystem.NewListDirToolWithPolicy(policy)
		}
		clone.tools[name] = tool
	}

	if dispatch, ok := clone.tools[dispatchAgentToolID].(*DispatchAgentTool); ok {
		dispatch.registry = clone.cloneWithDispatchRegistry()
	}
	return clone
}

// WithFilesystemPolicy returns a policy-scoped registry snapshot. The source
// registry and its tool instances remain untouched.
func (r *ToolRegistry) WithFilesystemPolicy(policy *filesystem.FilesystemPolicy) *ToolRegistry {
	return r.cloneWithFilesystemPolicy(policy)
}

// cloneWithDispatchRegistry gives nested dispatch calls the same filesystem
// policy without recursively rebuilding the dispatch tool itself.
func (r *ToolRegistry) cloneWithDispatchRegistry() *ToolRegistry {
	clone := &ToolRegistry{tools: make(map[string]core.Tool), diagnosticWriter: registryDiagnosticWriter(r)}
	if r == nil {
		return clone
	}
	for name, tool := range r.tools {
		if name == dispatchAgentToolID {
			continue
		}
		clone.tools[name] = tool
	}
	return clone
}

func (r *ToolRegistry) Register(tool core.Tool) error {
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

func (r *ToolRegistry) Get(name string) (core.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// Lookup returns a registered tool or a typed not-found error.
func (r *ToolRegistry) Lookup(name string) (core.Tool, error) {
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
	r.writeDiagnostic("tool %q execution started\n", name)

	tool, err := r.Lookup(name)
	if err != nil {
		r.writeDiagnostic("tool %q not found\n", name)
		return nil, err
	}

	start := time.Now()
	msgs, err := tool.Execute(ctx, args)
	duration := time.Since(start)

	if err != nil {
		r.writeDiagnostic("tool %q execution failed after %s: %v\n", name, duration, err)
		return nil, &ToolInvocationError{Err: err}
	}
	resultLen := 0
	for _, m := range msgs {
		resultLen += len(m.TextContent())
	}
	r.writeDiagnostic("tool %q execution completed in %s (%d messages, %d result bytes)\n", name, duration, len(msgs), resultLen)
	return msgs, nil
}

// writeDiagnostic sends best-effort operator diagnostics to the writer bound
// to this registry. Diagnostics never change a tool result or surface a
// request's ambient logger through context.
func (r *ToolRegistry) writeDiagnostic(format string, args ...any) {
	if r == nil || r.diagnosticWriter == nil {
		return
	}
	if _, err := fmt.Fprintf(r.diagnosticWriter, format, args...); err != nil {
		return
	}
}

func (r *ToolRegistry) GetDefinitions() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	definitions := make([]map[string]any, 0, len(r.tools))
	for _, name := range sortedRegistryToolNames(r.tools) {
		definitions = append(definitions, core.ToolToSchema(r.tools[name]))
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
	sort.Strings(names)
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
	for _, name := range sortedRegistryToolNames(r.tools) {
		tool := r.tools[name]
		summaries = append(summaries, fmt.Sprintf("- `%s` - %s", tool.Name(), tool.Description()))
	}
	return summaries
}

func sortedRegistryToolNames(registry map[string]core.Tool) []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
