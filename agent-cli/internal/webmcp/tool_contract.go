package webmcp

// The broker tools are intentionally stable even though the page catalog is
// dynamic. Keep this list ordered: it is the order presented to providers and
// the order used by contract/golden tests.
const (
	GetContextToolName      = "webmcp_get_context"
	ListTabsToolName        = "webmcp_list_tabs"
	SelectTabToolName       = "webmcp_select_tab"
	ListToolsToolName       = "webmcp_list_tools"
	InvokeToolName          = "webmcp_invoke"
	CancelToolName          = "webmcp_cancel"
	OpenTabToolName         = "webmcp_open_tab"
	ShowPageToolName        = "show_page"
	ListCastDevicesToolName = "webmcp_list_cast_devices"
	CastTabToolName         = "webmcp_cast_tab"
	StopCastingToolName     = "webmcp_stop_casting"
)

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

// BrokerToolDefinition is the provider-independent representation of one
// stable broker function. Parameters is a complete object schema, including
// defaults and additionalProperties, rather than the page catalog schema.
type BrokerToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
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
			Description: "Start casting the exact selected WebMCP tab to a device returned by webmcp_list_cast_devices. Only call this after the customer explicitly asks to cast.",
			Parameters: objectSchema(
				property("device_name", "string", "Exact Cast device name returned by webmcp_list_cast_devices.", true, nil),
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
	result := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
	propertyMap := result["properties"].(map[string]any)
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
