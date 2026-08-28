package webmcp

import (
	"context"
	"encoding/json"
	"time"
)

// BrowserDiscoverer discovers verified browser candidates in deterministic
// precedence order. It does not expose a browser or CDP implementation.
type BrowserDiscoverer interface {
	Discover(context.Context, DiscoverOptions) ([]BrowserCandidate, error)
}

// DevToolsCatalog reads browser version and normalized target snapshots from a
// browser edge.
type DevToolsCatalog interface {
	Version(context.Context, BrowserCandidate) (BrowserVersion, error)
	ListTargets(context.Context, BrowserCandidate) ([]Target, error)
}

// BrowserRuntime opens a browser-scoped handle for one candidate.
type BrowserRuntime interface {
	Open(context.Context, BrowserCandidate) (BrowserHandle, error)
}

// BrowserHandle owns browser-scoped enumeration and target attachment. Close
// must never terminate an externally owned browser process.
type BrowserHandle interface {
	Candidate() BrowserCandidate
	ListTargets(context.Context) ([]Target, error)
	Activate(context.Context, TargetID) error
	Attach(context.Context, TargetID, TargetOwnership) (TargetSession, error)
	Close() error
}

// TargetSession is the transport-neutral selected-page seam. External target
// ownership requires detach-only close semantics; generated browser values
// must remain behind the concrete adapter.
type TargetSession interface {
	Context() PageContext
	Ownership() TargetOwnership
	EnableWebMCP(context.Context) error
	Events() <-chan BrowserEvent
	InvokeWebMCP(context.Context, FrameID, string, json.RawMessage) (InvocationID, error)
	CancelWebMCP(context.Context, InvocationID) error
	Done() <-chan struct{}
	Err() error
	Close() error
}

// Broker is the neutral unit consumed by direct CLI commands and session
// composition. It owns selection/catalog/invocation semantics but not provider
// continuation or browser transport details.
type Broker interface {
	Discover(context.Context, DiscoverOptions) ([]BrowserCandidate, error)
	ListTargets(context.Context, BrowserSelector) ([]Target, error)
	Select(context.Context, TargetSelector) (PageContext, error)
	Selected(context.Context) (PageContext, error)
	ListTools(context.Context, ListToolsOptions) (ToolCatalogSnapshot, error)
	Invoke(context.Context, InvokeRequest) (InvokeResult, error)
	Cancel(context.Context, CancelRequest) error
	Watch(context.Context) <-chan BrokerEvent
	Close() error
}

// CatalogSynchronizer waits for the quiet-window initial catalog state. The
// implementation must also allow a valid zero-tool page to become ready.
type CatalogSynchronizer interface {
	WaitReady(context.Context, <-chan CatalogEvent) (CatalogSyncResult, error)
}

// SemanticRecorder receives redacted semantic browser events and flushes them
// into the existing top-level recording bundle.
type SemanticRecorder interface {
	Record(BrowserEvent) error
	Flush() error
}

// Clock is the deterministic time seam used by broker state and recordings.
type Clock interface {
	Now() time.Time
}

// IDGenerator is the deterministic identifier seam. Callers supply a stable
// namespace prefix and convert the returned opaque value to the appropriate
// typed identifier at the boundary.
type IDGenerator interface {
	NewID(prefix string) string
}
