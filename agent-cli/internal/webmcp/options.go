package webmcp

import "encoding/json"

type DiscoverOptions struct {
	BrowserID        BrowserID
	ExplicitOnly     bool
	AllowProcessScan bool
	AllowRemoteCDP   bool
}

type BrowserSelector struct {
	BrowserID BrowserID
}

type TargetSelector struct {
	BrowserID BrowserID
	TargetID  TargetID
}

// SelectOptions is an optional extension seam for brokers that can activate a
// target as part of selection. The frozen Broker interface remains compatible
// with callers that only need exact selection.
type SelectOptions struct {
	Activate bool
}

type ListToolsOptions struct {
	Refresh        bool
	NameContains   string
	IncludeSchemas bool
	FrameID        FrameID
}

type ToolCatalogSnapshot struct {
	Context    PageContext
	Generation uint64
	Tools      []ToolDescriptor
}

type InvokeRequest struct {
	ToolRef     ToolRef
	Input       json.RawMessage
	Reason      string
	ModelCallID string
	SessionID   string
	ResponseID  string
}

type InvokeResult struct {
	InvocationID InvocationID
	State        InvocationState
	Output       json.RawMessage
	ErrorCode    string
}

type CancelRequest struct {
	InvocationID InvocationID
	Reason       string
}
