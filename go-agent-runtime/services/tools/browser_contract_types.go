package tools

import "encoding/json"

// Browser tool contracts are stable values and interfaces. Their concrete
// serializers and policy helpers live below the service's internal boundary;
// hosts obtain them through BrowserContract on the composed Service.
const (
	ToolResultVersion = "webmcp.tool-result.v1"
	// DefaultMaxInputBytes bounds one page-tool input before validation or
	// dispatch. Hosts may choose a tighter request-specific limit.
	DefaultMaxInputBytes = 262144

	GetContextToolName      = "webmcp_get_context"
	ListTabsToolName        = "webmcp_list_tabs"
	SelectTabToolName       = "webmcp_select_tab"
	ListToolsToolName       = "webmcp_list_tools"
	InvokeToolName          = "webmcp_invoke"
	CancelToolName          = "webmcp_cancel"
	OpenTabToolName         = "webmcp_open_tab"
	NavigateTabToolName     = "webmcp_navigate_tab"
	ShowPageToolName        = "show_page"
	ListCastDevicesToolName = "webmcp_list_cast_devices"
	CastTabToolName         = "webmcp_cast_tab"
	StopCastingToolName     = "webmcp_stop_casting"
)

// ErrorCode is the stable model-facing WebMCP failure vocabulary. It is
// intentionally transport-neutral so host protocol adapters and reusable
// tool services emit the same bounded result shape.
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
	ErrorInvalidToolInput       ErrorCode = "invalid_tool_input"
	ErrorResultTooLarge         ErrorCode = "result_too_large"
	ErrorTargetAttachFailed     ErrorCode = "target_attach_failed"
	ErrorTargetDetached         ErrorCode = "target_detached"
	ErrorPageNavigated          ErrorCode = "page_navigated"
	ErrorInvocationFailed       ErrorCode = "invocation_failed"
	ErrorInvocationCanceled     ErrorCode = "invocation_canceled"
	ErrorInvocationTimedOut     ErrorCode = "invocation_timed_out"
	ErrorInvocationOrphaned     ErrorCode = "invocation_orphaned"
	ErrorBrowserDisconnected    ErrorCode = "browser_disconnected"
	ErrorBrowserProtocolInvalid ErrorCode = ErrorBrowserProtocol
)

// IsKnown reports whether the code is admitted by the browser result
// boundary. A method keeps this value policy in the contract package without
// exposing a mutable lookup table or a root-level helper function.
func (code ErrorCode) IsKnown() bool {
	switch code {
	case ErrorWebMCPDisabled,
		ErrorEndpointNotFound,
		ErrorEndpointUnreachable,
		ErrorRemoteEndpointDenied,
		ErrorBrowserProtocol,
		ErrorUnsupportedWebMCP,
		ErrorNoEligibleTab,
		ErrorAmbiguousBrowser,
		ErrorAmbiguousTab,
		ErrorStaleSelection,
		ErrorStaleToolRef,
		ErrorOriginDenied,
		ErrorApprovalRequired,
		ErrorApprovalDenied,
		ErrorInvalidToolInput,
		ErrorResultTooLarge,
		ErrorTargetAttachFailed,
		ErrorTargetDetached,
		ErrorPageNavigated,
		ErrorInvocationFailed,
		ErrorInvocationCanceled,
		ErrorInvocationTimedOut,
		ErrorInvocationOrphaned,
		ErrorBrowserDisconnected:
		return true
	default:
		return false
	}
}

// BrokerToolDefinition is the provider-independent representation of one
// stable broker function. Parameters is a complete object schema, including
// defaults and additionalProperties, rather than the page catalog schema.
type BrokerToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// PageScreenshot is the host-to-tools capture value. Browser adapters may
// return bytes, but the tools service validates their identity, MIME, image
// encoding, and dimensions before anything is projected to a model.
type PageScreenshot struct {
	BrowserID string
	TargetID  string
	MIMEType  string
	Bytes     []byte
	Width     int
	Height    int
}

// ValidatedPageScreenshot is the bounded capture value accepted by a host
// presentation adapter after tools-owned validation. Bytes are copied so a
// broker cannot mutate the model-facing image while it is being encoded.
type ValidatedPageScreenshot struct {
	BrowserID string
	TargetID  string
	MIMEType  string
	Bytes     []byte
	Width     int
	Height    int
}

// BrowserContract provides the pure browser protocol and result policy owned
// by the reusable tools service. Host packages receive this interface from
// their service wire; they do not import the implementation package.
type BrowserContract interface {
	StableToolNames() []string
	StableBrokerToolDefinitions() []BrokerToolDefinition
	StableBrokerToolSchemas() []map[string]any
	BrowserToolDefinitions(webCast ...bool) []BrokerToolDefinition
	BrowserToolSchemas(webCast ...bool) []map[string]any
	BrokerToolDefinitions() []BrokerToolDefinition
	NewClassifiedError(code ErrorCode, message string, details map[string]any) *ClassifiedError
	ResultErrorFor(err error, fallback ErrorCode, details map[string]any) ToolResultError
	DefaultErrorMessage(code ErrorCode) string
	ContextErrorCode(err error) ErrorCode
	NewToolResultSuccess(data any) (ToolResultEnvelope, error)
	NewToolResultFailure(resultError ToolResultError) ToolResultEnvelope
	MarshalToolResult(envelope ToolResultEnvelope) ([]byte, error)
	EncodeToolResult(data any, resultError *ToolResultError) ([]byte, error)
	UnmarshalToolResult(data []byte) (ToolResultEnvelope, error)
	NormalizeBrowserParameterSchema(schema json.RawMessage) (json.RawMessage, string, bool)
	ValidatePageToolInput(input, schema json.RawMessage, maxBytes int) []ToolResultIssue
	ValidatePageScreenshot(PageScreenshot) (ValidatedPageScreenshot, error)
}
