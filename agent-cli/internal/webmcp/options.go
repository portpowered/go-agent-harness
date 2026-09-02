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

// DefaultDirectCancellationTimeout bounds the wait for a browser terminal
// event after a direct cancellation command has been dispatched. A missing
// terminal is an uncertain, non-retryable outcome; it must never leave a
// fresh cancel process waiting indefinitely.
const DefaultDirectCancellationTimeout = 5 * time.Second

// DefaultBrokerCloseTimeout bounds each browser session, handle, and worker
// shutdown step. A non-cooperative page or transport is reported as
// unresolved instead of holding the session coordinator forever.
const DefaultBrokerCloseTimeout = 15 * time.Second

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

// OpenTabRequest creates, selects, and optionally foregrounds one absolute
// HTTP(S) page (or about:blank for managed-browser startup recovery).
type OpenTabRequest struct {
	BrowserID BrowserID
	URL       string
	Activate  bool
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

// ToolRefFactory mints an opaque page-tool reference after the broker has
// normalized a descriptor. Production compositions may provide a stable
// implementation so a reference returned by one short-lived CLI command can
// be used by the next command without persisting transport state.
type ToolRefFactory func(ToolDescriptor) (ToolRef, error)

type InvokeRequest struct {
	ToolRef     ToolRef
	Input       json.RawMessage
	Reason      string
	ModelCallID string
	SessionID   string
	ResponseID  string
}

type InvokeResult struct {
	// InvocationID is the broker-owned correlation ID used by model-facing
	// callers and WaitInvocation.
	InvocationID InvocationID
	// BrowserInvocationID is the browser protocol ID returned by the selected
	// target. It is available for direct CLI handoff; model-facing callers
	// should continue to use InvocationID.
	BrowserInvocationID InvocationID
	State               InvocationState
	Output              json.RawMessage
	ErrorCode           string
	ErrorDetails        map[string]any
}

type CancelRequest struct {
	InvocationID InvocationID
	Reason       string
}

// DirectCancelRequest is the exact-selection handoff used by a direct CLI
// process that did not admit the original invocation. InvocationID is the
// browser-issued protocol ID from the invoke dispatch receipt, not a broker
// registry lookup key.
type DirectCancelRequest struct {
	Target       TargetSelector
	InvocationID InvocationID
	Reason       string
}
