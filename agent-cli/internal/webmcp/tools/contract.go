package tools

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// Browser result contracts and the frozen error vocabulary are owned by the
// reusable tools service. This package keeps the Lane B definition projection
// and aliases the shared values for the CLI-hosted discovery adapter.
type ErrorCode = runtimeTools.ErrorCode
type ToolResultIssue = runtimeTools.ToolResultIssue
type ToolResultError = runtimeTools.ToolResultError
type ToolResultEnvelope = runtimeTools.ToolResultEnvelope

type ResultEnvelope = runtimeTools.ResultEnvelope
type ResultError = runtimeTools.ResultError
type ResultIssue = runtimeTools.ResultIssue

const (
	ToolResultVersion = runtimeTools.ToolResultVersion

	GetContextToolName = runtimeTools.GetContextToolName
	ListTabsToolName   = runtimeTools.ListTabsToolName
	SelectTabToolName  = runtimeTools.SelectTabToolName

	ErrorWebMCPDisabled         = runtimeTools.ErrorWebMCPDisabled
	ErrorEndpointNotFound       = runtimeTools.ErrorEndpointNotFound
	ErrorEndpointUnreachable    = runtimeTools.ErrorEndpointUnreachable
	ErrorRemoteEndpointDenied   = runtimeTools.ErrorRemoteEndpointDenied
	ErrorBrowserProtocol        = runtimeTools.ErrorBrowserProtocol
	ErrorUnsupportedWebMCP      = runtimeTools.ErrorUnsupportedWebMCP
	ErrorNoEligibleTab          = runtimeTools.ErrorNoEligibleTab
	ErrorAmbiguousBrowser       = runtimeTools.ErrorAmbiguousBrowser
	ErrorAmbiguousTab           = runtimeTools.ErrorAmbiguousTab
	ErrorStaleSelection         = runtimeTools.ErrorStaleSelection
	ErrorStaleToolRef           = runtimeTools.ErrorStaleToolRef
	ErrorOriginDenied           = runtimeTools.ErrorOriginDenied
	ErrorApprovalRequired       = runtimeTools.ErrorApprovalRequired
	ErrorApprovalDenied         = runtimeTools.ErrorApprovalDenied
	ErrorResultTooLarge         = runtimeTools.ErrorResultTooLarge
	ErrorTargetAttachFailed     = runtimeTools.ErrorTargetAttachFailed
	ErrorTargetDetached         = runtimeTools.ErrorTargetDetached
	ErrorPageNavigated          = runtimeTools.ErrorPageNavigated
	ErrorInvocationFailed       = runtimeTools.ErrorInvocationFailed
	ErrorInvocationCanceled     = runtimeTools.ErrorInvocationCanceled
	ErrorInvocationTimedOut     = runtimeTools.ErrorInvocationTimedOut
	ErrorInvocationOrphaned     = runtimeTools.ErrorInvocationOrphaned
	ErrorBrowserDisconnected    = runtimeTools.ErrorBrowserDisconnected
	ErrorInvalidToolInput       = runtimeTools.ErrorInvalidToolInput
	ErrorBrowserProtocolInvalid = runtimeTools.ErrorBrowserProtocolInvalid
)

const (
	CodeWebMCPDisabled         = ErrorWebMCPDisabled
	CodeEndpointNotFound       = ErrorEndpointNotFound
	CodeEndpointUnreachable    = ErrorEndpointUnreachable
	CodeRemoteEndpointDenied   = ErrorRemoteEndpointDenied
	CodeBrowserProtocol        = ErrorBrowserProtocol
	CodeBrowserProtocolInvalid = ErrorBrowserProtocol
	CodeUnsupportedWebMCP      = ErrorUnsupportedWebMCP
	CodeNoEligibleTab          = ErrorNoEligibleTab
	CodeAmbiguousBrowser       = ErrorAmbiguousBrowser
	CodeAmbiguousTab           = ErrorAmbiguousTab
	CodeStaleSelection         = ErrorStaleSelection
	CodeStaleToolRef           = ErrorStaleToolRef
	CodeOriginDenied           = ErrorOriginDenied
	CodeApprovalRequired       = ErrorApprovalRequired
	CodeApprovalDenied         = ErrorApprovalDenied
	CodeResultTooLarge         = ErrorResultTooLarge
	CodeTargetAttachFailed     = ErrorTargetAttachFailed
	CodeTargetDetached         = ErrorTargetDetached
	CodePageNavigated          = ErrorPageNavigated
	CodeInvocationFailed       = ErrorInvocationFailed
	CodeInvocationCanceled     = ErrorInvocationCanceled
	CodeInvocationTimedOut     = ErrorInvocationTimedOut
	CodeInvocationOrphaned     = ErrorInvocationOrphaned
	CodeBrowserDisconnected    = ErrorBrowserDisconnected
	CodeInvalidToolInput       = ErrorInvalidToolInput
)

func IsKnownErrorCode(code ErrorCode) bool { return code.IsKnown() }

func NewToolResultSuccess(data any) (ToolResultEnvelope, error) {
	return runtimeToolsWire.NewService().BrowserContract().NewToolResultSuccess(data)
}

func NewToolResultFailure(resultError ToolResultError) ToolResultEnvelope {
	return runtimeToolsWire.NewService().BrowserContract().NewToolResultFailure(resultError)
}

func MarshalToolResult(envelope ToolResultEnvelope) ([]byte, error) {
	return runtimeToolsWire.NewService().BrowserContract().MarshalToolResult(envelope)
}

func EncodeToolResult(data any, resultError *ToolResultError) ([]byte, error) {
	return runtimeToolsWire.NewService().BrowserContract().EncodeToolResult(data, resultError)
}

func UnmarshalToolResult(data []byte) (ToolResultEnvelope, error) {
	return runtimeToolsWire.NewService().BrowserContract().UnmarshalToolResult(data)
}

// ToolDefinition is the provider-neutral complete definition for one of the
// three Lane B tools. Parameters retains the closed JSON object schema used by
// CLI tools; Definitions additionally provides the flattened loop view.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// StableToolDefinitions projects the reusable browser contract into the
// legacy Lane B value shape. The runtime owns names, descriptions, defaults,
// and closed schemas; Lane B keeps only its three discovery tools and its
// host-side executor.
func StableToolDefinitions() []ToolDefinition {
	definitions := runtimeToolsWire.NewService().BrowserContract().StableBrokerToolDefinitions()
	result := make([]ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Name != GetContextToolName && definition.Name != ListTabsToolName && definition.Name != SelectTabToolName {
			continue
		}
		parameters := laneBCloneMap(definition.Parameters)
		// JSON round-tripping is used by the Lane B compatibility helper, but
		// this contract historically exposed required as []string. Preserve
		// that typed value so the validator can distinguish required fields.
		if required, ok := parameters["required"].([]any); ok {
			values := make([]string, 0, len(required))
			for _, value := range required {
				if name, ok := value.(string); ok {
					values = append(values, name)
				}
			}
			parameters["required"] = values
		}
		result = append(result, ToolDefinition{
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  parameters,
		})
	}
	return result
}

// ToolDefinitions is a descriptive alias for StableToolDefinitions.
func ToolDefinitions() []ToolDefinition { return StableToolDefinitions() }

// Definitions is a concise alias for StableToolDefinitions.
func Definitions() []ToolDefinition { return StableToolDefinitions() }

// StableToolSchemas returns complete function definitions for the existing
// CLI registry boundary. Every parameters object is closed.
func StableToolSchemas() []map[string]any {
	definitions := StableToolDefinitions()
	result := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        definition.Name,
				"description": definition.Description,
				"parameters":  definition.Parameters,
			},
		})
	}
	return result
}

// ToolSchemas is a descriptive alias for StableToolSchemas.
func ToolSchemas() []map[string]any { return StableToolSchemas() }

// BrokerToolSchemas is a compatibility alias for StableToolSchemas.
func BrokerToolSchemas() []map[string]any { return StableToolSchemas() }

func objectSchema() map[string]any {
	result := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
	return result
}

// DefinitionSchemas returns fresh complete schemas. It is the model/provider
// definition view; no dynamic page schema is projected into these values.
func DefinitionSchemas() []map[string]any { return StableToolSchemas() }

// AgentLoopDefinitions returns the flat representation accepted by the
// existing go-agent-loop ToolDefinition contract.
func AgentLoopDefinitions() []messages.ToolDefinition {
	definitions := StableToolDefinitions()
	result := make([]messages.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		properties, _ := definition.Parameters["properties"].(map[string]any)
		ordered := schemaOrder(definition.Name)
		required := requiredNames(definition.Parameters)
		for name := range properties {
			if !containsString(ordered, name) {
				ordered = append(ordered, name)
			}
		}
		parameters := make([]messages.ToolParameter, 0, len(ordered))
		for _, name := range ordered {
			property, _ := properties[name].(map[string]any)
			valueType, _ := property["type"].(string)
			description, _ := property["description"].(string)
			parameters = append(parameters, messages.ToolParameter{
				Name:        name,
				Type:        valueType,
				Description: description,
				Required:    required[name],
			})
		}
		result = append(result, messages.ToolDefinition{
			Name:             definition.Name,
			Description:      definition.Description,
			Parameters:       parameters,
			ParametersClosed: true,
		})
	}
	return result
}

func schemaOrder(name string) []string {
	switch name {
	case GetContextToolName:
		return []string{"refresh"}
	case ListTabsToolName:
		return []string{"browser_id", "origin_contains", "eligible_only", "include_zero_tool_pages"}
	case SelectTabToolName:
		return []string{"browser_id", "target_id", "activate"}
	default:
		return nil
	}
}

func requiredNames(schema map[string]any) map[string]bool {
	result := make(map[string]bool)
	values, _ := schema["required"].([]string)
	for _, value := range values {
		result[value] = true
	}
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// MarshalSchema is a small convenience for callers that snapshot the
// provider-facing definitions.
func MarshalSchema() ([]byte, error) { return json.Marshal(StableToolSchemas()) }
