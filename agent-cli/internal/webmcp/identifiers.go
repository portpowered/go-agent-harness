// Package webmcp contains the provider-neutral contract for the CLI-owned
// WebMCP broker. Browser protocol implementations belong in subpackages and
// must not leak their transport types through this package.
package webmcp

// Typed identifiers keep browser, target, frame, invocation, and page-tool
// identities distinct even though they are represented as strings on the
// wire. A page tool's display name is not a global identity.
type BrowserID string
type EndpointID string
type TargetID string
type FrameID string
type InvocationID string
type ToolRef string
type ToolName string

// Stable model-facing broker tool names.
const (
	ToolGetContext ToolName = "webmcp_get_context"
	ToolListTabs   ToolName = "webmcp_list_tabs"
	ToolSelectTab  ToolName = "webmcp_select_tab"
	ToolListTools  ToolName = "webmcp_list_tools"
	ToolInvoke     ToolName = "webmcp_invoke"
	ToolCancel     ToolName = "webmcp_cancel"
)

// Version identifiers frozen by the C0 contract.
const (
	ToolResultVersion    = "webmcp.tool-result.v1"
	BrowserEventsVersion = "webmcp.browser-events.v1"
	BrowserScriptVersion = "webmcp.browser-script.v1"
	ProbeScenarioVersion = "probe.scenario.v2"
)
