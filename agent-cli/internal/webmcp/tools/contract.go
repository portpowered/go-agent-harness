package tools

import (
	"encoding/json"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

const (
	// ToolResultVersion is the only textual result envelope version emitted by
	// Lane B tools.
	ToolResultVersion = "webmcp.tool-result.v1"

	GetContextToolName = "webmcp_get_context"
	ListTabsToolName   = "webmcp_list_tabs"
	SelectTabToolName  = "webmcp_select_tab"
)

// ErrorCode is the stable model-facing result vocabulary used by the tool
// adapter. Discovery errors use discovery.Code; this string type keeps the
// textual boundary independent of that implementation detail.
type ErrorCode string

const (
	ErrorWebMCPDisabled         ErrorCode = "webmcp_disabled"
	ErrorEndpointNotFound       ErrorCode = "endpoint_not_found"
	ErrorEndpointUnreachable    ErrorCode = "endpoint_unreachable"
	ErrorRemoteEndpointDenied   ErrorCode = "remote_endpoint_denied"
	ErrorBrowserProtocol        ErrorCode = "browser_protocol_invalid"
	ErrorUnsupportedWebMCP      ErrorCode = "unsupported_webmcp"
	ErrorNoEligibleTab          ErrorCode = "no_eligible_tab"
	ErrorAmbiguousBrowser       ErrorCode = "ambiguous_browser"
	ErrorAmbiguousTab           ErrorCode = "ambiguous_tab"
	ErrorStaleSelection         ErrorCode = "stale_selection"
	ErrorStaleToolRef           ErrorCode = "stale_tool_ref"
	ErrorOriginDenied           ErrorCode = "origin_denied"
	ErrorApprovalRequired       ErrorCode = "approval_required"
	ErrorApprovalDenied         ErrorCode = "approval_denied"
	ErrorResultTooLarge         ErrorCode = "result_too_large"
	ErrorTargetAttachFailed     ErrorCode = "target_attach_failed"
	ErrorTargetDetached         ErrorCode = "target_detached"
	ErrorPageNavigated          ErrorCode = "page_navigated"
	ErrorInvocationFailed       ErrorCode = "invocation_failed"
	ErrorInvocationCanceled     ErrorCode = "invocation_canceled"
	ErrorInvocationTimedOut     ErrorCode = "invocation_timed_out"
	ErrorInvocationOrphaned     ErrorCode = "invocation_orphaned"
	ErrorBrowserDisconnected    ErrorCode = "browser_disconnected"
	ErrorInvalidToolInput       ErrorCode = "invalid_tool_input"
	ErrorBrowserProtocolInvalid ErrorCode = ErrorBrowserProtocol
)

// Code aliases make the result vocabulary convenient for callers that use
// Code-prefixed constants alongside discovery.Code.
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

var knownErrorCodes = map[ErrorCode]struct{}{
	ErrorWebMCPDisabled:       {},
	ErrorEndpointNotFound:     {},
	ErrorEndpointUnreachable:  {},
	ErrorRemoteEndpointDenied: {},
	ErrorBrowserProtocol:      {},
	ErrorUnsupportedWebMCP:    {},
	ErrorNoEligibleTab:        {},
	ErrorAmbiguousBrowser:     {},
	ErrorAmbiguousTab:         {},
	ErrorStaleSelection:       {},
	ErrorStaleToolRef:         {},
	ErrorOriginDenied:         {},
	ErrorApprovalRequired:     {},
	ErrorApprovalDenied:       {},
	ErrorResultTooLarge:       {},
	ErrorTargetAttachFailed:   {},
	ErrorTargetDetached:       {},
	ErrorPageNavigated:        {},
	ErrorInvocationFailed:     {},
	ErrorInvocationCanceled:   {},
	ErrorInvocationTimedOut:   {},
	ErrorInvocationOrphaned:   {},
	ErrorBrowserDisconnected:  {},
	ErrorInvalidToolInput:     {},
}

// IsKnownErrorCode reports whether code can cross the result boundary.
func IsKnownErrorCode(code ErrorCode) bool {
	_, ok := knownErrorCodes[code]
	return ok
}

// ToolDefinition is the provider-neutral complete definition for one of the
// three Lane B tools. Parameters retains the closed JSON object schema used by
// CLI tools; Definitions additionally provides the flattened loop view.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// StableToolDefinitions returns fresh definitions in the frozen order.
func StableToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        GetContextToolName,
			Description: "Return the current selected browser page context.",
			Parameters: objectSchema(
				optionalProperty("refresh", "boolean", "Refresh browser and target metadata before returning.", false),
			),
		},
		{
			Name:        ListTabsToolName,
			Description: "List browser tabs available for WebMCP selection.",
			Parameters: objectSchema(
				optionalProperty("browser_id", "string", "Filter tabs to one discovered browser.", ""),
				optionalProperty("origin_contains", "string", "Filter tabs by an origin substring.", ""),
				optionalProperty("eligible_only", "boolean", "Return only targets eligible for WebMCP.", true),
				optionalProperty("include_zero_tool_pages", "boolean", "Include eligible pages that currently expose no tools.", false),
			),
		},
		{
			Name:        SelectTabToolName,
			Description: "Select a browser tab for WebMCP operations.",
			Parameters: objectSchema(
				requiredProperty("browser_id", "string", "Exact discovered browser identifier."),
				requiredProperty("target_id", "string", "Exact target identifier from the selected browser."),
				optionalProperty("activate", "boolean", "Activate the selected page after selection.", false),
			),
		},
	}
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

type schemaProperty struct {
	name     string
	schema   map[string]any
	required bool
}

func objectSchema(properties ...schemaProperty) map[string]any {
	result := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
	propertyMap := result["properties"].(map[string]any)
	var required []string
	for _, property := range properties {
		propertyMap[property.name] = property.schema
		if property.required {
			required = append(required, property.name)
		}
	}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

func optionalProperty(name, valueType, description string, defaultValue any) schemaProperty {
	return schemaProperty{
		name: name,
		schema: map[string]any{
			"type":        valueType,
			"description": description,
			"default":     defaultValue,
		},
	}
}

func requiredProperty(name, valueType, description string) schemaProperty {
	return schemaProperty{
		name: name,
		schema: map[string]any{
			"type":        valueType,
			"description": description,
		},
		required: true,
	}
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
