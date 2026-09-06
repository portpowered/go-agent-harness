package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/display"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/internal/filesystem"
)

// RegistryExecutor adapts a ToolRegistry to the subsystems.ToolExecutor interface
// expected by the agent loop.
type RegistryExecutor struct {
	registry *ToolRegistry
}

var _ filesystem.SessionImagePreparerBinder = (*RegistryExecutor)(nil)

type ToolArgumentError struct {
	Err error
}

func (e *ToolArgumentError) Error() string {
	return fmt.Sprintf("failed to parse tool arguments: %v", e.Err)
}

func (e *ToolArgumentError) Unwrap() error { return e.Err }

// NewRegistryExecutor creates a ToolExecutor backed by the given ToolRegistry.
func NewRegistryExecutor(registry *ToolRegistry) *RegistryExecutor {
	return &RegistryExecutor{registry: registry}
}

// WithSessionImagePreparer returns a registry executor isolated to one
// session's provider-aware image preparation callback. The original executor
// remains safe for other sessions and one-shot calls.
func (re *RegistryExecutor) WithSessionImagePreparer(preparer filesystem.ImagePartPreparer) messages.ToolExecutor {
	if re == nil {
		return nil
	}
	return &RegistryExecutor{registry: re.registry.cloneWithSessionImagePreparer(preparer)}
}

// WithFilesystemPolicy returns a registry executor snapshot whose
// customer-facing filesystem tools use policy. Non-filesystem tools remain
// shared immutable values, while dispatch_agent receives a policy-scoped child
// registry as well.
func (re *RegistryExecutor) WithFilesystemPolicy(policy *filesystem.FilesystemPolicy) messages.ToolExecutor {
	if re == nil {
		return nil
	}
	return &RegistryExecutor{registry: re.registry.cloneWithFilesystemPolicy(policy)}
}

// ApplyFilesystemPolicy scopes a registry-backed executor when possible. A
// caller-owned non-registry executor is returned unchanged for compatibility;
// production CLI composition supplies RegistryExecutor values.
func ApplyFilesystemPolicy(executor messages.ToolExecutor, policy *filesystem.FilesystemPolicy) messages.ToolExecutor {
	if executor == nil || policy == nil {
		return executor
	}
	if binder, ok := executor.(interface {
		WithFilesystemPolicy(*filesystem.FilesystemPolicy) messages.ToolExecutor
	}); ok {
		return binder.WithFilesystemPolicy(policy)
	}
	return executor
}

func (re *RegistryExecutor) screenRecordingPermissionRechecker() (display.ScreenRecordingPermissionRechecker, bool) {
	if re == nil || re.registry == nil {
		return nil, false
	}
	for _, name := range []string{display.HostDisplayToolID, display.ScreenToolID} {
		tool, ok := re.registry.Get(name)
		if !ok || isNilTool(tool) {
			continue
		}
		rechecker, ok := tool.(display.ScreenRecordingPermissionRechecker)
		if ok {
			return rechecker, true
		}
	}
	return nil, false
}

func (re *RegistryExecutor) ScreenRecordingPermissionRecheckSupported() bool {
	rechecker, ok := re.screenRecordingPermissionRechecker()
	return ok && rechecker.ScreenRecordingPermissionRecheckSupported()
}

func (re *RegistryExecutor) RecheckScreenRecordingPermission(ctx context.Context) (display.DisplayPermission, error) {
	rechecker, ok := re.screenRecordingPermissionRechecker()
	if !ok {
		return display.DisplayPermission{
			State:  display.DisplayPermissionUnavailable,
			Reason: "screen recording permission re-check is unavailable",
		}, nil
	}
	return rechecker.RecheckScreenRecordingPermission(ctx)
}

// Execute implements subsystems.ToolExecutor.
func (re *RegistryExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	var args map[string]any
	if call.Arguments != "" {
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			return messages.ToolCallResponse{}, &ToolArgumentError{Err: err}
		}
	}

	msgs, err := re.registry.ExecuteWithContext(ctx, call.Name, args)
	if err != nil {
		return messages.ToolCallResponse{}, err
	}
	response := messagesToToolCallResponse(call.ID, msgs)
	response.Name = call.Name
	return response, nil
}

// messagesToToolCallResponse merges tool result messages into a single ToolCallResponse
// (one per tool call), preserving text and ContentParts for streaming and multimodal content.
func messagesToToolCallResponse(toolCallID string, msgs []messages.Message) messages.ToolCallResponse {
	var content strings.Builder
	var contentParts []messages.ContentPart
	for _, m := range msgs {
		for _, part := range m.ContentParts {
			if textPart, ok := part.(messages.TextPart); ok {
				content.WriteString(textPart.Text)
			}
			contentParts = append(contentParts, part)
		}
	}
	resp := messages.ToolCallResponse{ToolCallID: toolCallID}
	if len(contentParts) > 0 {
		resp.ContentParts = contentParts
		resp.Content = content.String() // plain-text fallback
	} else {
		resp.Content = content.String()
	}
	return resp
}

// ToAgentLoopDefs converts the registry's tools to subsystems.ToolDefinition format
// for passing to the agent loop.
func (r *ToolRegistry) ToAgentLoopDefs() []messages.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := make([]messages.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, messages.ToolDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  convertParameters(tool.Parameters()),
		})
	}
	return messages.CanonicalToolDefinitions(defs)
}

// convertParameters converts a JSON Schema parameters map to a flat list of ToolParameter.
func convertParameters(schema map[string]any) []messages.ToolParameter {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil
	}

	requiredSet := requiredParameterSet(schema["required"])

	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)

	params := make([]messages.ToolParameter, 0, len(props))
	for _, name := range names {
		parameter, ok := parameterFromSchema(name, props[name], requiredSet[name])
		if !ok {
			continue
		}
		params = append(params, parameter)
	}
	return params
}

func requiredParameterSet(raw any) map[string]bool {
	set := make(map[string]bool)
	switch required := raw.(type) {
	case []string:
		for _, name := range required {
			set[name] = true
		}
	case []any:
		for _, value := range required {
			if name, ok := value.(string); ok {
				set[name] = true
			}
		}
	}
	return set
}

func parameterFromSchema(name string, raw any, required bool) (messages.ToolParameter, bool) {
	prop, ok := raw.(map[string]any)
	if !ok {
		return messages.ToolParameter{}, false
	}
	paramType, ok := prop["type"].(string)
	if !ok {
		paramType = ""
	}
	description, ok := prop["description"].(string)
	if !ok {
		description = ""
	}
	return messages.ToolParameter{
		Name:        name,
		Type:        paramType,
		Description: description,
		Required:    required,
	}, true
}
