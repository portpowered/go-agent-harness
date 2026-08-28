package webmcp

import (
	"context"
	"encoding/json"
	"time"
)

type TargetOwnership string

const (
	TargetOwnershipExternal     TargetOwnership = "external"
	TargetOwnershipHarnessOwned TargetOwnership = "harness_owned"
)

type BrowserDiscoverer interface {
	Discover(context.Context, DiscoverOptions) ([]BrowserCandidate, error)
}

type DevToolsCatalog interface {
	Version(context.Context, BrowserCandidate) (BrowserVersion, error)
	ListTargets(context.Context, BrowserCandidate) ([]Target, error)
}

type BrowserRuntime interface {
	Open(context.Context, BrowserCandidate) (BrowserHandle, error)
}

type BrowserHandle interface {
	Candidate() BrowserCandidate
	ListTargets(context.Context) ([]Target, error)
	Activate(context.Context, TargetID) error
	Attach(context.Context, TargetID, TargetOwnership) (TargetSession, error)
	Close() error
}

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

// Clock is the minimum time seam required by broker state and deadline
// handling. Implementations should return a stable timezone-normalized value
// when deterministic replay is desired.
type Clock interface {
	Now() time.Time
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(time.Duration) bool
}

type TimerFactory interface {
	NewTimer(time.Duration) Timer
}

// IDSource owns all broker-generated opaque references and invocation IDs.
// Keeping both methods on one seam makes a replay's identifier stream easy to
// reproduce while allowing production to use a cryptographically random
// implementation later.
type IDSource interface {
	NewToolRef() (ToolRef, error)
	NewInvocationID() (InvocationID, error)
}

// These aliases keep the seam discoverable for callers that use the longer
// terminology without introducing a second contract.
type IdentifierSource = IDSource
type IDGenerator = IDSource
