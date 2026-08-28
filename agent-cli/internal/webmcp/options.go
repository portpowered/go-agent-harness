package webmcp

import (
	"encoding/json"
	"time"
)

// DefaultMaxInputBytes is the C0 bound for the UTF-8 input_json payload sent
// to a page tool before validation or dispatch.
const DefaultMaxInputBytes = 262144

// DefaultMaxResultBytes is the C0 bound for the compact textual result
// envelope produced for a completed page invocation.
const DefaultMaxResultBytes = 262144

// DefaultInvocationTimeout bounds every admitted invocation. The session
// coordinator may apply a tighter context deadline for one call.
const DefaultInvocationTimeout = 30 * time.Second

const (
	CancelOnInterruptNever    = "never"
	CancelOnInterruptReadOnly = "read-only"
	CancelOnInterruptAlways   = "always"
)

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
	ErrorDetails map[string]any
}

type CancelRequest struct {
	InvocationID InvocationID
	Reason       string
}
