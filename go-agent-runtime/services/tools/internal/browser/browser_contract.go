package browser

import public "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"

type ErrorCode = public.ErrorCode
type BrokerToolDefinition = public.BrokerToolDefinition

const (
	ToolResultVersion           = public.ToolResultVersion
	DefaultMaxInputBytes        = public.DefaultMaxInputBytes
	GetContextToolName          = public.GetContextToolName
	ListTabsToolName            = public.ListTabsToolName
	SelectTabToolName           = public.SelectTabToolName
	ListToolsToolName           = public.ListToolsToolName
	InvokeToolName              = public.InvokeToolName
	CancelToolName              = public.CancelToolName
	OpenTabToolName             = public.OpenTabToolName
	NavigateTabToolName         = public.NavigateTabToolName
	ShowPageToolName            = public.ShowPageToolName
	ListCastDevicesToolName     = public.ListCastDevicesToolName
	CastTabToolName             = public.CastTabToolName
	StopCastingToolName         = public.StopCastingToolName
	ErrorWebMCPDisabled         = public.ErrorWebMCPDisabled
	ErrorEndpointNotFound       = public.ErrorEndpointNotFound
	ErrorEndpointUnreachable    = public.ErrorEndpointUnreachable
	ErrorRemoteEndpointDenied   = public.ErrorRemoteEndpointDenied
	ErrorBrowserProtocol        = public.ErrorBrowserProtocol
	ErrorUnsupportedWebMCP      = public.ErrorUnsupportedWebMCP
	ErrorNoEligibleTab          = public.ErrorNoEligibleTab
	ErrorAmbiguousBrowser       = public.ErrorAmbiguousBrowser
	ErrorAmbiguousTab           = public.ErrorAmbiguousTab
	ErrorStaleSelection         = public.ErrorStaleSelection
	ErrorStaleToolRef           = public.ErrorStaleToolRef
	ErrorOriginDenied           = public.ErrorOriginDenied
	ErrorApprovalRequired       = public.ErrorApprovalRequired
	ErrorApprovalDenied         = public.ErrorApprovalDenied
	ErrorInvalidToolInput       = public.ErrorInvalidToolInput
	ErrorResultTooLarge         = public.ErrorResultTooLarge
	ErrorTargetAttachFailed     = public.ErrorTargetAttachFailed
	ErrorTargetDetached         = public.ErrorTargetDetached
	ErrorPageNavigated          = public.ErrorPageNavigated
	ErrorInvocationFailed       = public.ErrorInvocationFailed
	ErrorInvocationCanceled     = public.ErrorInvocationCanceled
	ErrorInvocationTimedOut     = public.ErrorInvocationTimedOut
	ErrorInvocationOrphaned     = public.ErrorInvocationOrphaned
	ErrorBrowserDisconnected    = public.ErrorBrowserDisconnected
	ErrorBrowserProtocolInvalid = public.ErrorBrowserProtocolInvalid
)

// IsKnownErrorCode keeps the implementation package's concise call sites
// while the public contract owns the vocabulary and validation policy.
func IsKnownErrorCode(code ErrorCode) bool { return code.IsKnown() }

// StableToolNames is a copy of the ordered C0 tool-name list.
func StableToolNames() []string {
	return []string{
		GetContextToolName,
		ListTabsToolName,
		SelectTabToolName,
		ListToolsToolName,
		InvokeToolName,
		CancelToolName,
	}
}

// StableBrokerToolDefinitions returns fresh definitions for the six frozen
// C0 broker functions. Returning fresh maps prevents one provider adapter from
// mutating another adapter's definitions.
func StableBrokerToolDefinitions() []BrokerToolDefinition {
	return []BrokerToolDefinition{
		{
			Name:        GetContextToolName,
			Description: "Return the current selected browser page context.",
			Parameters: objectSchema(
				property("refresh", "boolean", "Refresh browser and target metadata before returning.", false, false),
			),
		},
		{
			Name:        ListTabsToolName,
			Description: "List browser tabs available for WebMCP selection. If the user asks to open a website, call webmcp_open_tab directly instead of listing tabs first.",
			Parameters: objectSchema(
				property("browser_id", "string", "Filter tabs to one discovered browser.", false, ""),
				property("origin_contains", "string", "Filter tabs by an origin substring.", false, ""),
				property("eligible_only", "boolean", "Return only targets eligible for WebMCP.", false, true),
				property("include_zero_tool_pages", "boolean", "Include eligible pages that currently expose no tools.", false, false),
			),
		},
		{
			Name:        SelectTabToolName,
			Description: "Select a browser tab for WebMCP operations.",
			Parameters: objectSchema(
				property("browser_id", "string", "Exact discovered browser identifier.", true, nil),
				property("target_id", "string", "Exact target identifier from the selected browser.", true, nil),
				property("activate", "boolean", "Activate the selected page after selection.", false, false),
			),
		},
		{
			Name:        ListToolsToolName,
			Description: "List tools exposed by the selected WebMCP page. Connected page tools are also registered as directly callable session tools under their listed names - prefer calling them directly.",
			Parameters: objectSchema(
				property("refresh", "boolean", "Refresh the selected page catalog before returning.", false, false),
				property("name_contains", "string", "Filter tools by a name substring.", false, ""),
				property("include_schemas", "boolean", "Include complete page input schemas.", false, true),
				property("frame_id", "string", "Filter tools to one frame identifier.", false, ""),
			),
		},
		{
			Name:        InvokeToolName,
			Description: "Invoke one page tool from the current catalog by tool_ref. Prefer calling a connected page tool directly by its listed name; use webmcp_invoke for explicit tool_ref control.",
			Parameters: objectSchema(
				property("tool_ref", "string", "Versioned session-local reference returned by webmcp_list_tools.", true, nil),
				property("input_json", "string", "UTF-8 JSON object containing the page-tool arguments.", true, nil),
				property("reason", "string", "Brief user-facing reason for the action.", true, nil),
			),
		},
		{
			Name:        CancelToolName,
			Description: "Cancel a pending WebMCP invocation.",
			Parameters: objectSchema(
				property("invocation_id", "string", "Invocation identifier returned by the broker.", true, nil),
				property("reason", "string", "Optional user-facing cancellation reason.", false, ""),
			),
		},
	}
}

// StableBrokerToolSchemas returns the six definitions in the same function
// shape used by the existing CLI ToolToSchema helper.
func StableBrokerToolSchemas() []map[string]any {
	return brokerToolSchemas(StableBrokerToolDefinitions())
}

// BrowserToolDefinitions returns the stable broker controls plus opt-in
// browser capabilities. They are deliberately kept out of
// StableBrokerToolDefinitions so disabled sessions and the frozen C0 contract
// remain inert.
func BrowserToolDefinitions(webCast ...bool) []BrokerToolDefinition {
	definitions := StableBrokerToolDefinitions()
	definitions = append(definitions,
		BrokerToolDefinition{
			Name:        OpenTabToolName,
			Description: "Open an absolute website URL in a new browser tab, select it for WebMCP operations, and bring it to the foreground by default. Call this directly whenever the user asks to open a website; it works even when webmcp_list_tabs returned no tabs, and it may be called repeatedly to open additional tabs.",
			Parameters: objectSchema(
				property("browser_id", "string", "Optional exact discovered browser identifier; omit when only one browser is connected.", false, ""),
				property("url", "string", "Absolute http:// or https:// website URL to open.", true, nil),
				property("activate", "boolean", "Bring the new tab to the foreground after opening it.", false, true),
			),
		},
		BrokerToolDefinition{
			Name:        NavigateTabToolName,
			Description: "Navigate the currently selected browser tab to an absolute website URL without opening a new tab. Use this when the user asks to switch, change, or redirect the current tab. The target identity is preserved, so an active cast of that tab continues with the new page.",
			Parameters: objectSchema(
				property("url", "string", "Absolute http:// or https:// website URL to load in the selected tab.", true, nil),
			),
		},
		BrokerToolDefinition{
			Name:        ShowPageToolName,
			Description: "Capture the currently selected browser page without changing browser state.",
			Parameters:  objectSchema(),
		},
	)
	if len(webCast) == 0 || !webCast[0] {
		return definitions
	}
	return append(definitions,
		BrokerToolDefinition{
			Name:        ListCastDevicesToolName,
			Description: "List Google Cast devices visible to the browser hosting the selected WebMCP tab.",
			Parameters:  objectSchema(),
		},
		BrokerToolDefinition{
			Name:        CastTabToolName,
			Description: "Cast the selected page to a device returned by webmcp_list_cast_devices. Use mode=media to hand the active video or audio off to the receiver, or mode=tab to mirror the rendered tab. Only call this after the customer explicitly asks to cast.",
			Parameters: objectSchema(
				property("device_name", "string", "Exact Cast device name returned by webmcp_list_cast_devices.", true, nil),
				enumProperty("mode", "Cast media natively or mirror the complete browser tab.", false, "tab", "media", "tab"),
			),
		},
		BrokerToolDefinition{
			Name:        StopCastingToolName,
			Description: "Stop the active Cast session on a device returned by webmcp_list_cast_devices.",
			Parameters: objectSchema(
				property("device_name", "string", "Exact Cast device name returned by webmcp_list_cast_devices.", true, nil),
			),
		},
	)
}

// BrowserToolSchemas returns fresh provider-facing schemas for the complete
// browser-enabled broker surface.
func BrowserToolSchemas(webCast ...bool) []map[string]any {
	return brokerToolSchemas(BrowserToolDefinitions(webCast...))
}

func brokerToolSchemas(definitions []BrokerToolDefinition) []map[string]any {
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

// BrokerToolDefinitions is a concise alias for callers composing the stable
// broker surface.
func BrokerToolDefinitions() []BrokerToolDefinition {
	return StableBrokerToolDefinitions()
}

type brokerProperty struct {
	name     string
	schema   map[string]any
	required bool
}

func objectSchema(properties ...brokerProperty) map[string]any {
	propertyMap := map[string]any{}
	result := map[string]any{
		"type":                 pageJSONSchemaObjectType,
		"properties":           propertyMap,
		"additionalProperties": false,
	}
	var required []string
	for _, entry := range properties {
		propertyMap[entry.name] = entry.schema
		if entry.required {
			required = append(required, entry.name)
		}
	}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

func property(name, valueType, description string, required bool, defaultValue any) brokerProperty {
	schema := map[string]any{
		"type":        valueType,
		"description": description,
	}
	if !required {
		// Optional fields always carry their frozen C0 default, including an
		// empty string and false.
		schema["default"] = defaultValue
	}
	return brokerProperty{name: name, schema: schema, required: required}
}

func enumProperty(name, description string, required bool, defaultValue string, values ...string) brokerProperty {
	entry := property(name, "string", description, required, defaultValue)
	entry.schema["enum"] = append([]string(nil), values...)
	return entry
}
