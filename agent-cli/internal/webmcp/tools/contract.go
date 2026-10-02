package tools

import (
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// JSON Schema type names used when normalizing and validating tool schemas.
const (
	schemaTypeString  = "string"
	schemaTypeBoolean = "boolean"
	schemaTypeObject  = "object"
)

const (
	// redactedValue replaces page-controlled metadata and error text that
	// must not reach the model.
	redactedValue = "redacted"
	// noPageSelectedMessage is the model-facing text for calls made before a
	// page is selected.
	noPageSelectedMessage = "no page is selected"
)

// Browser result contracts and the frozen error vocabulary are owned by the
// reusable tools service. This package keeps the Lane B definition projection
// and aliases the shared values for the CLI-hosted discovery adapter.
type ErrorCode = runtimeTools.ErrorCode
type ToolResultIssue = runtimeTools.ToolResultIssue
type ToolResultError = runtimeTools.ToolResultError
type ToolResultEnvelope = runtimeTools.ToolResultEnvelope

const (
	ToolResultVersion = runtimeTools.ToolResultVersion

	GetContextToolName = runtimeTools.GetContextToolName
	ListTabsToolName   = runtimeTools.ListTabsToolName
	SelectTabToolName  = runtimeTools.SelectTabToolName

	ErrorWebMCPDisabled       = runtimeTools.ErrorWebMCPDisabled
	ErrorEndpointNotFound     = runtimeTools.ErrorEndpointNotFound
	ErrorEndpointUnreachable  = runtimeTools.ErrorEndpointUnreachable
	ErrorRemoteEndpointDenied = runtimeTools.ErrorRemoteEndpointDenied
	ErrorBrowserProtocol      = runtimeTools.ErrorBrowserProtocol
	ErrorUnsupportedWebMCP    = runtimeTools.ErrorUnsupportedWebMCP
	ErrorNoEligibleTab        = runtimeTools.ErrorNoEligibleTab
	ErrorAmbiguousBrowser     = runtimeTools.ErrorAmbiguousBrowser
	ErrorAmbiguousTab         = runtimeTools.ErrorAmbiguousTab
	ErrorStaleSelection       = runtimeTools.ErrorStaleSelection
	ErrorStaleToolRef         = runtimeTools.ErrorStaleToolRef
	ErrorOriginDenied         = runtimeTools.ErrorOriginDenied
	ErrorApprovalRequired     = runtimeTools.ErrorApprovalRequired
	ErrorApprovalDenied       = runtimeTools.ErrorApprovalDenied
	ErrorResultTooLarge       = runtimeTools.ErrorResultTooLarge
	ErrorTargetAttachFailed   = runtimeTools.ErrorTargetAttachFailed
	ErrorTargetDetached       = runtimeTools.ErrorTargetDetached
	ErrorPageNavigated        = runtimeTools.ErrorPageNavigated
	ErrorInvocationFailed     = runtimeTools.ErrorInvocationFailed
	ErrorInvocationCanceled   = runtimeTools.ErrorInvocationCanceled
	ErrorInvocationTimedOut   = runtimeTools.ErrorInvocationTimedOut
	ErrorInvocationOrphaned   = runtimeTools.ErrorInvocationOrphaned
	ErrorBrowserDisconnected  = runtimeTools.ErrorBrowserDisconnected
	ErrorInvalidToolInput     = runtimeTools.ErrorInvalidToolInput
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

func objectSchema() map[string]any {
	result := map[string]any{
		"type":                 schemaTypeObject,
		"properties":           map[string]any{},
		"additionalProperties": false,
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

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
