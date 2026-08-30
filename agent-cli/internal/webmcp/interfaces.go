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

// BrowserHandleHealth is an optional BrowserHandle extension. A handle that
// has observed transport loss reports Disconnected()=true so callers holding
// a cached handle can discard it and re-dial the (possibly healthy) endpoint
// instead of failing every subsequent operation against a dead connection.
type BrowserHandleHealth interface {
	Disconnected() bool
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

// PageScreenshot is the browser-adapter result for a screenshot of the exact
// target session that received the request. Bytes stay inside the adapter and
// tool boundary; callers must validate and project them before exposing any
// model-facing result.
type PageScreenshot struct {
	BrowserID BrowserID
	TargetID  TargetID
	MIMEType  string
	Bytes     []byte
	Width     int
	Height    int
}

// PageScreenshotter is an optional target/broker capability. Keeping page
// capture outside the frozen interfaces preserves compatibility with browser
// implementations that only support the original WebMCP operations.
type PageScreenshotter interface {
	CapturePageScreenshot(context.Context) (PageScreenshot, error)
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

// DirectCanceller is an optional broker extension for a fresh direct CLI
// process. It cancels only the supplied browser invocation ID against the
// already exact-selected target and does not consult the local invocation
// registry. A nil return means the exact target session emitted a correlated
// terminal Canceled event; protocol acceptance alone is not success.
type DirectCanceller interface {
	CancelDirect(context.Context, DirectCancelRequest) error
}

// InvocationWaiter is an optional broker capability for callers that need the
// terminal page result rather than the dispatch acknowledgement returned by
// Broker.Invoke. Keeping it separate preserves the frozen Broker interface for
// lightweight and legacy broker implementations.
type InvocationWaiter interface {
	WaitInvocation(context.Context, InvocationID) (InvokeResult, error)
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
