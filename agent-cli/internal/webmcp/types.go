package webmcp

import (
	"encoding/json"
	"time"
)

// DiscoverySource identifies the ordered source that produced a browser
// candidate. The value is diagnostic metadata and never contains credentials.
type DiscoverySource string

const (
	DiscoverySourceExplicitCDP       DiscoverySource = "explicit_cdp_url"
	DiscoverySourceExplicitWebSocket DiscoverySource = "explicit_ws_endpoint"
	DiscoverySourceUserDataDir       DiscoverySource = "user_data_dir"
	DiscoverySourceConfigured        DiscoverySource = "configured_endpoint"
	DiscoverySourceProcessScan       DiscoverySource = "process_scan"
	DiscoverySourceAutoConnect       DiscoverySource = "approved_auto_connect"
)

// Diagnostic is bounded, provider-neutral diagnostic metadata. Details must
// already be redacted before they cross this package boundary.
type Diagnostic struct {
	Code    string          `json:"code"`
	Message string          `json:"message,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`
}

// BrowserVersion contains the safe subset of the DevTools version response
// needed by the neutral broker and fixture layers.
type BrowserVersion struct {
	Browser              string `json:"Browser,omitempty"`
	ProtocolVersion      string `json:"Protocol-Version,omitempty"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl,omitempty"`
}

// BrowserCandidate is a discovered browser endpoint. Endpoint and profile
// values are composition inputs; implementations must redact them in
// diagnostics and recordings according to the C0 policy.
type BrowserCandidate struct {
	ID           BrowserID       `json:"id"`
	Source       DiscoverySource `json:"source"`
	Product      string          `json:"product,omitempty"`
	Protocol     string          `json:"protocol,omitempty"`
	HTTPURL      string          `json:"http_url,omitempty"`
	BrowserWSURL string          `json:"browser_ws_url,omitempty"`
	UserDataDir  string          `json:"user_data_dir,omitempty"`
	PID          int             `json:"pid,omitempty"`
	Loopback     bool            `json:"loopback"`
	Explicit     bool            `json:"explicit"`
	HarnessOwned bool            `json:"harness_owned"`
	Diagnostics  []Diagnostic    `json:"diagnostics,omitempty"`
}

// Target is a normalized browser target record. Target ID is the authoritative
// selector; title and URL are display and policy inputs only.
type Target struct {
	BrowserID         BrowserID `json:"browser_id"`
	ID                TargetID  `json:"id"`
	Type              string    `json:"type"`
	Title             string    `json:"title,omitempty"`
	URL               string    `json:"url,omitempty"`
	Origin            string    `json:"origin,omitempty"`
	WebSocketURL      string    `json:"web_socket_url,omitempty"`
	Attached          bool      `json:"attached"`
	Eligible          bool      `json:"eligible"`
	EligibilityReason string    `json:"eligibility_reason,omitempty"`
}

// PageKey identifies a target independently of its current page generation.
type PageKey struct {
	BrowserID BrowserID `json:"browser_id"`
	TargetID  TargetID  `json:"target_id"`
}

// PageContext is the selected page state visible to broker callers.
type PageContext struct {
	Key        PageKey   `json:"key"`
	Title      string    `json:"title,omitempty"`
	URL        string    `json:"url,omitempty"`
	Origin     string    `json:"origin,omitempty"`
	Generation uint64    `json:"generation"`
	Connected  bool      `json:"connected"`
	Ready      bool      `json:"ready"`
	SelectedAt time.Time `json:"selected_at,omitempty"`
}

// ToolAnnotations are page-provided hints. They are not authorization
// decisions; page output remains untrusted regardless of these values.
type ToolAnnotations struct {
	ReadOnly         *bool           `json:"read_only,omitempty"`
	UntrustedContent *bool           `json:"untrusted_content,omitempty"`
	AutoSubmit       *bool           `json:"auto_submit,omitempty"`
	Raw              json.RawMessage `json:"raw,omitempty"`
}

// ToolDescriptor is one generation-bound page tool in the broker catalog.
type ToolDescriptor struct {
	Ref           ToolRef         `json:"ref"`
	Name          string          `json:"name"`
	Description   string          `json:"description,omitempty"`
	InputSchema   json.RawMessage `json:"input_schema,omitempty"`
	Annotations   ToolAnnotations `json:"annotations,omitempty"`
	BrowserID     BrowserID       `json:"browser_id"`
	TargetID      TargetID        `json:"target_id"`
	FrameID       FrameID         `json:"frame_id"`
	Origin        string          `json:"origin,omitempty"`
	Generation    uint64          `json:"generation"`
	SchemaDigest  string          `json:"schema_digest,omitempty"`
	AddedSequence uint64          `json:"added_sequence"`
}

// ToolCatalogSnapshot is the immutable view returned by list-tools calls.
type ToolCatalogSnapshot struct {
	Generation uint64           `json:"generation"`
	Tools      []ToolDescriptor `json:"tools"`
}

// DiscoverOptions contains only neutral discovery policy. Concrete endpoint
// parsing and process inspection belong behind BrowserDiscoverer.
type DiscoverOptions struct {
	CDPURL           string     `json:"cdp_url,omitempty"`
	WSEndpoint       string     `json:"ws_endpoint,omitempty"`
	UserDataDir      string     `json:"user_data_dir,omitempty"`
	AllowProcessScan bool       `json:"allow_process_scan,omitempty"`
	AllowRemoteCDP   bool       `json:"allow_remote_cdp,omitempty"`
	EndpointID       EndpointID `json:"endpoint_id,omitempty"`
}

// BrowserSelector describes list-tabs filters. Empty filters mean no filter;
// eligible-only defaults are applied by the caller that constructs it.
type BrowserSelector struct {
	BrowserID            BrowserID `json:"browser_id,omitempty"`
	OriginContains       string    `json:"origin_contains,omitempty"`
	EligibleOnly         bool      `json:"eligible_only,omitempty"`
	IncludeZeroToolPages bool      `json:"include_zero_tool_pages,omitempty"`
}

// TargetSelector identifies the exact target selected for a session. The
// broker may support automatic selection only through its configured policy;
// it must never silently choose another target after a stale selection.
type TargetSelector struct {
	BrowserID  BrowserID      `json:"browser_id,omitempty"`
	TargetID   TargetID       `json:"target_id,omitempty"`
	Origin     string         `json:"origin,omitempty"`
	AutoSelect AutoSelectMode `json:"auto_select,omitempty"`
	Activate   bool           `json:"activate,omitempty"`
}

// ListToolsOptions contains the scalar list-tools filters frozen for the
// model-facing call.
type ListToolsOptions struct {
	Refresh        bool    `json:"refresh,omitempty"`
	NameContains   string  `json:"name_contains,omitempty"`
	IncludeSchemas bool    `json:"include_schemas,omitempty"`
	FrameID        FrameID `json:"frame_id,omitempty"`
}

// InvokeRequest is the neutral form of webmcp_invoke. InputJSON remains a
// string at the model boundary so arbitrary page JSON does not alter the flat
// provider tool definition.
type InvokeRequest struct {
	ToolRef   ToolRef `json:"tool_ref"`
	InputJSON string  `json:"input_json"`
	Reason    string  `json:"reason"`
}

// CancelRequest is the neutral form of webmcp_cancel.
type CancelRequest struct {
	InvocationID InvocationID `json:"invocation_id"`
	Reason       string       `json:"reason,omitempty"`
}

// InvocationResult is the operation-specific data portion of a successful
// tool-result envelope. Output is copied as one JSON value and is never
// coerced into a string.
type InvokeResult struct {
	BrowserID    BrowserID        `json:"browser_id"`
	TargetID     TargetID         `json:"target_id"`
	Generation   uint64           `json:"generation"`
	ToolRef      ToolRef          `json:"tool_ref"`
	ToolName     string           `json:"tool_name"`
	InvocationID InvocationID     `json:"invocation_id,omitempty"`
	Status       InvocationStatus `json:"status"`
	Output       json.RawMessage  `json:"output"`
	Error        *BrokerError     `json:"error,omitempty"`
	DurationMS   int64            `json:"duration_ms,omitempty"`
}

// Invocation is the broker's generation-bound state record. It is a neutral
// contract record, not an instruction to implement a particular queue.
type Invocation struct {
	ID              InvocationID    `json:"id"`
	Tool            ToolDescriptor  `json:"tool"`
	Arguments       json.RawMessage `json:"arguments"`
	State           InvocationState `json:"state"`
	ModelCallID     string          `json:"model_call_id,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	ResponseID      string          `json:"response_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	QueuedAt        time.Time       `json:"queued_at,omitempty"`
	DispatchStarted time.Time       `json:"dispatch_started,omitempty"`
	DispatchedAt    time.Time       `json:"dispatched_at,omitempty"`
	CompletedAt     time.Time       `json:"completed_at,omitempty"`
	CancelRequested bool            `json:"cancel_requested,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	ErrorText       string          `json:"error_text,omitempty"`
}

// RedactionMode records how a browser event payload was retained.
type RedactionMode string

const (
	RedactionNone    RedactionMode = "none"
	RedactionApplied RedactionMode = "redacted"
	RedactionDigest  RedactionMode = "digest"
)

// RedactionMetadata is the fixed metadata portion of a browser event.
type RedactionMetadata struct {
	Mode  RedactionMode `json:"mode"`
	Rules []string      `json:"rules,omitempty"`
}

// BrowserEventType is the semantic event vocabulary used by recording and
// replay. Private CDP frames are intentionally not represented here.
type BrowserEventType string

const (
	EventDiscoveryStarted      BrowserEventType = "browser.discovery.started"
	EventDiscoveryCompleted    BrowserEventType = "browser.discovery.completed"
	EventEndpointVersion       BrowserEventType = "browser.endpoint.version"
	EventTargetsSnapshot       BrowserEventType = "browser.targets.snapshot"
	EventTargetSelected        BrowserEventType = "browser.target.selected"
	EventTargetAttached        BrowserEventType = "browser.chrome.target_attached"
	EventWebMCPEnabled         BrowserEventType = "browser.webmcp.enabled"
	EventCatalogToolAdded      BrowserEventType = "browser.catalog.tool_added"
	EventCatalogToolRemoved    BrowserEventType = "browser.catalog.tool_removed"
	EventCatalogReady          BrowserEventType = "browser.catalog.ready"
	EventInvocationCreated     BrowserEventType = "browser.invocation.created"
	EventInvocationApproval    BrowserEventType = "browser.invocation.approval"
	EventInvocationDispatched  BrowserEventType = "browser.invocation.dispatched"
	EventInvocationCompleted   BrowserEventType = "browser.invocation.completed"
	EventInvocationError       BrowserEventType = "browser.invocation.error"
	EventInvocationCancelAsked BrowserEventType = "browser.invocation.cancel_requested"
	EventInvocationCanceled    BrowserEventType = "browser.invocation.canceled"
	EventPageGenerationChanged BrowserEventType = "browser.page.generation_changed"
	EventTargetDetached        BrowserEventType = "browser.target.detached"
	EventTargetClosed          BrowserEventType = "browser.chrome.target_closed"
)

// BrowserEvent is one semantic JSONL recording entry. Exactly one of Payload
// and PayloadSHA256 is populated by a conforming recorder.
type BrowserEvent struct {
	Version       string            `json:"version"`
	Sequence      uint64            `json:"sequence"`
	MonotonicMS   uint64            `json:"monotonic_ms"`
	Type          BrowserEventType  `json:"type"`
	BrowserID     BrowserID         `json:"browser_id,omitempty"`
	TargetID      TargetID          `json:"target_id,omitempty"`
	Generation    uint64            `json:"generation,omitempty"`
	Payload       json.RawMessage   `json:"payload,omitempty"`
	PayloadSHA256 string            `json:"payload_sha256,omitempty"`
	Redaction     RedactionMetadata `json:"redaction"`
}

// BrokerEvent is an alias because broker watches expose the same semantic
// event values that the recorder persists.
type BrokerEvent = BrowserEvent

// CatalogEvent is an alias for the event stream consumed during initial
// catalog synchronization.
type CatalogEvent = BrowserEvent

// TargetOwnership determines whether close may destroy a browser target.
type TargetOwnership string

const (
	TargetOwnershipExternal     TargetOwnership = "external"
	TargetOwnershipHarnessOwned TargetOwnership = "harness_owned"
)

// InvocationState is the monotonic broker state vocabulary.
type InvocationState string

const (
	InvocationCreated          InvocationState = "created"
	InvocationAwaitingApproval InvocationState = "awaiting_approval"
	InvocationQueued           InvocationState = "queued"
	InvocationDispatching      InvocationState = "dispatching"
	InvocationDispatched       InvocationState = "dispatched"
	InvocationCompleted        InvocationState = "completed"
	InvocationError            InvocationState = "error"
	InvocationCanceled         InvocationState = "canceled"
	InvocationTimedOut         InvocationState = "timed_out"
	InvocationOrphaned         InvocationState = "orphaned"
	InvocationPolicyDenied     InvocationState = "policy_denied"
)

// InvocationStatus is the terminal status exposed in operation result data.
type InvocationStatus string

const (
	InvocationStatusCompleted InvocationStatus = "completed"
	InvocationStatusError     InvocationStatus = "error"
	InvocationStatusCanceled  InvocationStatus = "canceled"
	InvocationStatusTimedOut  InvocationStatus = "timed_out"
	InvocationStatusOrphaned  InvocationStatus = "orphaned"
)

// AutoSelectMode is the frozen automatic-selection policy.
type AutoSelectMode string

const (
	AutoSelectOff       AutoSelectMode = "off"
	AutoSelectSingle    AutoSelectMode = "single"
	AutoSelectPersisted AutoSelectMode = "persisted"
)

// ApprovalMode controls whether a caller asks for user approval.
type ApprovalMode string

const (
	ApprovalAlways ApprovalMode = "always"
	ApprovalWrites ApprovalMode = "writes"
	ApprovalNever  ApprovalMode = "never"
)

// CancelOnInterrupt is the cancellation policy for a session interrupt.
type CancelOnInterrupt string

const (
	CancelOnInterruptNever    CancelOnInterrupt = "never"
	CancelOnInterruptReadOnly CancelOnInterrupt = "read-only"
	CancelOnInterruptAlways   CancelOnInterrupt = "always"
)

// ReplayMode identifies the deterministic browser replay policy.
type ReplayMode string

const (
	ReplayStrict      ReplayMode = "strict"
	ReplayDiagnostic  ReplayMode = "diagnostic"
	ReplayLiveBrowser ReplayMode = "live_browser"
)

// ApprovalScope binds an approval to the exact page generation and tool.
type ApprovalScope struct {
	BrowserID  BrowserID `json:"browser_id"`
	TargetID   TargetID  `json:"target_id"`
	Origin     string    `json:"origin"`
	Generation uint64    `json:"generation"`
	ToolRef    ToolRef   `json:"tool_ref"`
}

// ApprovalDecision is the neutral result of an approval prompt.
type ApprovalDecision struct {
	Allowed   bool          `json:"allowed"`
	Once      bool          `json:"once"`
	Scope     ApprovalScope `json:"scope"`
	DecidedAt time.Time     `json:"decided_at"`
	Source    string        `json:"source"`
}

// CatalogSyncResult records the neutral outcome of the initial quiet-window
// synchronization without prescribing a concrete catalog implementation.
type CatalogSyncResult struct {
	Snapshot   ToolCatalogSnapshot `json:"snapshot"`
	EventCount uint64              `json:"event_count"`
	ReadyAt    time.Time           `json:"ready_at"`
}
