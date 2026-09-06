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

// BrowserTabOpener is an optional browser-handle capability for creating a
// page target. It stays outside BrowserHandle so replay and legacy runtimes
// remain source-compatible.
type BrowserTabOpener interface {
	OpenTab(context.Context, string) (Target, error)
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

// TargetCastController is an optional selected-target capability. Cast is a
// page-scoped CDP domain, so tab mirroring must execute on the same attached
// target session used by the broker selection.
type TargetCastController interface {
	ListCastDevices(context.Context) ([]CastDevice, error)
	CastTab(context.Context, string) error
	StopCasting(context.Context, string) error
}

// TargetMediaCastController is the optional native-media companion to tab
// mirroring. It remains separate so existing browser adapters keep satisfying
// TargetCastController until they intentionally support media handoff.
type TargetMediaCastController interface {
	CastMedia(context.Context, string) error
}

// TargetTabNavigator changes the document loaded by an already attached page
// target. Keeping the same target identity is what allows Chrome tab mirroring
// to continue across navigation.
type TargetTabNavigator interface {
	NavigateTab(context.Context, string) error
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

// BrokerTabOpener is the optional model-facing recovery seam used when a
// connected browser has no selectable tabs.
type BrokerTabOpener interface {
	OpenTab(context.Context, OpenTabRequest) (PageContext, error)
}

// BrokerTabCreator creates a visible browser tab without requiring that page
// to expose WebMCP. Managed session bootstrap uses it for about:blank; the
// model-facing OpenTab operation still creates and selects a WebMCP page.
type BrokerTabCreator interface {
	CreateTab(context.Context, OpenTabRequest) (Target, error)
}

// BrokerTabNavigator exposes in-place navigation of the exact selected page.
// It remains optional so replay and legacy browser runtimes stay compatible.
type BrokerTabNavigator interface {
	NavigateSelectedTab(context.Context, string) (PageContext, error)
}

// BrokerCastController exposes Cast operations against the exact selected
// target without widening the frozen Broker interface.
type BrokerCastController interface {
	ListCastDevices(context.Context) ([]CastDevice, error)
	CastSelectedTab(context.Context, string) error
	StopCasting(context.Context, string) error
}

// BrokerMediaCastController exposes native media handoff for the selected
// target without changing the compatibility surface of BrokerCastController.
type BrokerMediaCastController interface {
	CastSelectedMedia(context.Context, string) error
}

// BrowserEventWatcher is an optional broker extension for callers that need
// the adapter-owned semantic browser events, including invocation inputs and
// outputs. The base Broker interface intentionally remains limited to broker
// lifecycle observations; event consumers must use the returned context to
// release this independent subscription.
type BrowserEventWatcher interface {
	WatchBrowserEvents(context.Context) <-chan BrowserEvent
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
