package webmcp

// The broker tools are intentionally stable even though the page catalog is
// dynamic. Keep this list ordered: it is the order presented to providers and
// the order used by contract/golden tests.
const (
	GetContextToolName = "webmcp_get_context"
	ListTabsToolName   = "webmcp_list_tabs"
	SelectTabToolName  = "webmcp_select_tab"
	ListToolsToolName  = "webmcp_list_tools"
	InvokeToolName     = "webmcp_invoke"
	CancelToolName     = "webmcp_cancel"
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
			Description: "List browser tabs available for WebMCP selection.",
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
			Description: "List tools exposed by the selected WebMCP page.",
			Parameters: objectSchema(
				property("refresh", "boolean", "Refresh the selected page catalog before returning.", false, false),
				property("name_contains", "string", "Filter tools by a name substring.", false, ""),
				property("include_schemas", "boolean", "Include complete page input schemas.", false, true),
				property("frame_id", "string", "Filter tools to one frame identifier.", false, ""),
			),
		},
		{
			Name:        InvokeToolName,
			Description: "Invoke one page tool from the current catalog.",
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
	definitions := StableBrokerToolDefinitions()
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
