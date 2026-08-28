package webmcp

import (
	"encoding/json"
	"time"
)

// DiscoverySource identifies the source that produced a browser candidate.
// It is deliberately transport-neutral; discovery adapters own the details
// of how a source is queried.
type DiscoverySource string

const (
	DiscoverySourceExplicit   DiscoverySource = "explicit"
	DiscoverySourceActivePort DiscoverySource = "active_port"
	DiscoverySourceConfigured DiscoverySource = "configured"
	DiscoverySourceProcess    DiscoverySource = "process"
	DiscoverySourceReplay     DiscoverySource = "replay"
)

// Diagnostic is safe, bounded metadata intended for logs and diagnostics. It
// must not contain endpoint credentials or raw page input/output.
type Diagnostic struct {
	Code    string         `json:"code"`
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// BrowserCandidate is the normalized identity and connection metadata for a
// browser endpoint. Browser-specific protocol values stay at the adapter
// boundary and are represented here only as neutral strings.
type BrowserCandidate struct {
	ID           BrowserID
	Source       DiscoverySource
	Product      string
	Protocol     string
	HTTPURL      string
	BrowserWSURL string
	UserDataDir  string
	PID          int
	Loopback     bool
	Explicit     bool
	HarnessOwned bool
	Diagnostics  []Diagnostic
}

// BrowserVersion is the small protocol version response consumed by the
// neutral runtime. It intentionally does not expose a generated CDP type.
type BrowserVersion struct {
	Browser              string
	ProtocolVersion      string
	WebSocketDebuggerURL string
}

// Target is a normalized browser target. Title and URL are display metadata;
// ID is the authoritative selector.
type Target struct {
	BrowserID BrowserID
	ID        TargetID
	Type      string
	Title     string
	URL       string
	Origin    string
	// ContinuityMarker and Generation are normalized selection state carried
	// across the CLI composition boundary. They contain no browser transport
	// values; adapters derive the marker from the target's continuity claim.
	ContinuityMarker string
	Generation       uint64
	WebSocketURL     string
	Attached         bool
	// WebMCPDomainSupported and PageToolsReady are separate observations. A
	// browser protocol domain can be available even when this page has not
	// materialized a tool producer/catalog.
	WebMCPDomainSupported bool
	PageToolsReady        bool
	PageToolsKnown        bool
	PageToolsEvidence     string
	Eligible              bool
	EligibilityReason     string
}

type PageKey struct {
	BrowserID BrowserID
	TargetID  TargetID
}

// PageContext identifies the document currently owned by a target session.
// Generation changes invalidate page-provided catalog references.
type PageContext struct {
	Key                   PageKey
	Title                 string
	URL                   string
	Origin                string
	Generation            uint64
	Connected             bool
	WebMCPDomainSupported bool
	CatalogReady          bool
	CatalogEvidence       string
	Ready                 bool
	SelectedAt            time.Time
}

type ToolAnnotations struct {
	ReadOnly         *bool
	UntrustedContent *bool
	AutoSubmit       *bool
	Raw              json.RawMessage
}

// ToolDescriptor is a complete page-tool descriptor. InputSchema is copied
// as JSON bytes so callers cannot mutate the session's catalog through a
// shared backing array.
type ToolDescriptor struct {
	Ref           ToolRef
	Name          string
	Description   string
	InputSchema   json.RawMessage
	Annotations   ToolAnnotations
	BrowserID     BrowserID
	TargetID      TargetID
	FrameID       FrameID
	Origin        string
	Generation    uint64
	SchemaDigest  string
	AddedSequence uint64
}

// BrowserEventType is the semantic event vocabulary shared by adapters and
// deterministic fakes. Event payloads are neutral copies of browser data.
type BrowserEventType string

const (
	EventToolsAdded   BrowserEventType = "tools_added"
	EventToolsRemoved BrowserEventType = "tools_removed"
	// EventCatalogReady is an affirmative page capability observation. It is
	// intentionally distinct from tools_added so an empty page catalog can be
	// proven ready without treating the absence of tools as evidence.
	EventCatalogReady        BrowserEventType = "catalog_ready"
	EventToolInvoked         BrowserEventType = "tool_invoked"
	EventToolResponded       BrowserEventType = "tool_responded"
	EventPageNavigated       BrowserEventType = "page_navigated"
	EventFrameNavigated      BrowserEventType = "frame_navigated"
	EventTargetAttached      BrowserEventType = "target_attached"
	EventTargetDetached      BrowserEventType = "target_detached"
	EventBrowserDisconnected BrowserEventType = "browser_disconnected"
	EventSessionClosed       BrowserEventType = "session_closed"
)

// BrowserEvent is the adapter-to-broker event contract. Optional payloads
// are populated according to Type. Output and Input are copied JSON values;
// they are not interpreted as trusted instructions by this package.
type BrowserEvent struct {
	Version            string
	Type               BrowserEventType
	Sequence           uint64
	At                 time.Time
	BrowserID          BrowserID
	TargetID           TargetID
	FrameID            FrameID
	Generation         uint64
	PreviousGeneration uint64
	// CatalogReady and ToolCountKnown let an adapter explicitly prove an empty
	// catalog. A tools_added event with one or more valid descriptors is also
	// affirmative catalog evidence.
	CatalogReady     bool
	ToolCount        int
	ToolCountKnown   bool
	ToolName         string
	Tools            []ToolDescriptor
	RemovedToolNames []string
	InvocationID     InvocationID
	Status           string
	Input            json.RawMessage
	Output           json.RawMessage
	ErrorCode        string
	Reason           string
}

// InvocationState is shared by broker implementations and test fixtures.
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

type OperationClass string

const (
	OperationReadOnly OperationClass = "read_only"
	OperationMutating OperationClass = "mutating"
	OperationUnknown  OperationClass = "unknown"
)

// Invocation is the neutral correlation record. The broker owns its state;
// the type is also useful to fakes and recorders that need to inspect the
// lifecycle without importing a provider package.
type Invocation struct {
	ID                InvocationID
	Tool              ToolDescriptor
	Arguments         json.RawMessage
	State             InvocationState
	Operation         OperationClass
	ModelCallID       string
	SessionID         string
	ResponseID        string
	CreatedAt         time.Time
	QueuedAt          time.Time
	DispatchStarted   time.Time
	DispatchedAt      time.Time
	Deadline          time.Time
	CompletedAt       time.Time
	CancelRequested   bool
	TerminalDelivered bool
	Result            json.RawMessage
	ErrorCode         string
}

// BrokerEvent is intentionally smaller than BrowserEvent. It is the
// broker-observation seam used by recorders and lifecycle diagnostics.
type BrokerEventType string

const (
	BrokerEventSelected           BrokerEventType = "selected"
	BrokerEventCatalogChanged     BrokerEventType = "catalog_changed"
	BrokerEventGenerationChanged  BrokerEventType = "generation_changed"
	BrokerEventInvocationCreated  BrokerEventType = "invocation_created"
	BrokerEventInvocationTerminal BrokerEventType = "invocation_terminal"
	BrokerEventSessionClosed      BrokerEventType = "session_closed"
)

// Stream termination reasons are bounded, machine-readable values carried by
// the existing Reason field. They let a caller distinguish an incomplete
// stream from an ordinary clean close without adding fields to the watch wire
// shape.
const (
	BrokerWatchBufferFullReason  = "watch_buffer_full"
	BrowserEventBufferFullReason = "event_buffer_full"
)

type BrokerEvent struct {
	Version      string
	Type         BrokerEventType
	Sequence     uint64
	At           time.Time
	BrowserID    BrowserID
	TargetID     TargetID
	Generation   uint64
	InvocationID InvocationID
	ToolRef      ToolRef
	State        InvocationState
	Reason       string
}
