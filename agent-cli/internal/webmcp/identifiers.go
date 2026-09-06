package webmcp

import runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"

// These named identifiers keep browser and page identities distinct. A page
// tool name is not a globally unique identifier and must never be used in
// place of one of these values.
type BrowserID string
type EndpointID string
type TargetID string
type FrameID string
type InvocationID string
type ToolRef string

const (
	ToolRefVersion       = "webmcp.tool-ref.v1"
	ToolRefPrefix        = ToolRefVersion + ":"
	ToolResultVersion    = runtimeTools.ToolResultVersion
	BrowserScriptVersion = "webmcp.browser-script.v1"
	BrowserEventsVersion = "webmcp.browser-events.v1"
)
