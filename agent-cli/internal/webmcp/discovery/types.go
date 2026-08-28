package discovery

import (
	"context"
	"net/http"
	"time"
)

const (
	// BrowserEventsVersion is the semantic event stream version frozen by C0.
	BrowserEventsVersion = "webmcp.browser-events.v1"

	// DefaultMaxVersionBytes bounds one /json/version response before it is
	// decoded. A discovery response is small by contract; the bound also
	// prevents an endpoint from turning discovery into an unbounded read.
	DefaultMaxVersionBytes int64 = 64 << 10

	// DefaultMaxTargetBytes bounds one /json/list response before it is
	// decoded. Target metadata is untrusted page/browser input and must not
	// turn a refresh into an unbounded read.
	DefaultMaxTargetBytes int64 = 256 << 10
)

// Source identifies the source selected for a browser candidate. The values
// are stable diagnostic names and never contain an endpoint.
type Source string

const (
	SourceExplicitCDPHTTP    Source = "explicit_cdp_http"
	SourceExplicitBrowserWS  Source = "explicit_browser_websocket"
	SourceDevToolsActivePort Source = "devtools_active_port"
	SourceConfigured         Source = "configured"
	SourceProcess            Source = "process"
)

// EndpointKind identifies the endpoint shape used by a discovery attempt.
// It is intentionally less specific than a URL and is safe in classified
// error details.
type EndpointKind string

const (
	EndpointKindCDPHTTP          EndpointKind = "cdp_http"
	EndpointKindBrowserWebSocket EndpointKind = "browser_websocket"
	EndpointKindActivePort       EndpointKind = "devtools_active_port"
	EndpointKindConfigured       EndpointKind = "configured"
	EndpointKindProcess          EndpointKind = "process"
)

// ConnectionInputs are already-resolved C0 values. Configuration parsing and
// CLI flag registration intentionally live outside this package.
type ConnectionInputs struct {
	// CDPURL is the first discovery source when non-empty. It may be either a
	// CDP base URL or its /json/version URL; query and fragment data is never
	// used as identity or sent to the probe.
	CDPURL string
	// BrowserWSEndpoint is the second source when non-empty. It must identify a
	// browser websocket under /devtools/browser/, not a page websocket.
	BrowserWSEndpoint string
	// UserDataDir enables the DevToolsActivePort source when non-empty.
	UserDataDir string
	// ConfiguredSources are tried in slice order after the explicit and
	// active-port sources.
	ConfiguredSources []ConfiguredSource
	// AllowProcessScan gates the optional process enumerator. A process
	// enumerator is never called when this is false.
	AllowProcessScan bool
	// AllowRemoteCDP is required for every non-loopback HTTP or websocket
	// endpoint, including endpoints returned by another source.
	AllowRemoteCDP bool
}

// Inputs is a concise alias for callers that prefer the shorter name.
type Inputs = ConnectionInputs

// Endpoint contains raw connection values at an injected source seam. It is
// not returned in BrowserCandidate, classified errors, or events.
type Endpoint struct {
	CDPURL            string
	BrowserWSEndpoint string
}

// ActivePortRecord is the neutral result of reading Chrome's
// <profile>/DevToolsActivePort file. BrowserWebSocketPath may be either the
// path written by Chrome or a complete websocket URL supplied by a fake.
type ActivePortRecord struct {
	Port                 int
	BrowserWebSocketPath string
}

// ProcessInfo is the minimum neutral process-discovery record. A process is
// usable only when DebuggingEnabled proves that its endpoint is intentional.
type ProcessInfo struct {
	PID              int
	Name             string
	UserDataDir      string
	DebuggingEnabled bool
	Endpoint         Endpoint
}

// BrowserVersion is the subset of Chrome's /json/version response consumed by
// discovery. The JSON tags intentionally match the wire names, including the
// hyphenated Protocol-Version field.
type BrowserVersion struct {
	Browser              string `json:"Browser"`
	ProtocolVersion      string `json:"Protocol-Version"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// BrowserCandidate is the safe normalized discovery result. ID is the only
// identity used by later selection layers. No transport URL is present here.
type BrowserCandidate struct {
	ID       string `json:"id"`
	Source   Source `json:"source"`
	Product  string `json:"product"`
	Protocol string `json:"protocol"`
	Loopback bool   `json:"loopback"`
}

// TargetDescriptor is the small, browser-neutral shape of one /json/list
// record. The ID and websocket URL are transport values used only at the
// discovery seam; normalized Target values never expose either raw value.
// Optional capability fields are accepted for hermetic fakes and forward
// compatible endpoints. Real browser adapters may instead provide a
// TargetCapabilityProbe.
type TargetDescriptor struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	// ContinuityMarker and DocumentID are optional neutral-fake metadata. A
	// browser adapter may use either value to prove that a target is still the
	// same page across a process restart; neither value is exposed directly.
	ContinuityMarker string `json:"continuityMarker,omitempty"`
	Continuity       string `json:"continuity,omitempty"`
	DocumentID       string `json:"documentId,omitempty"`
	WebMCPSupported  *bool  `json:"webmcpSupported,omitempty"`
	WebMCP           *bool  `json:"webmcp,omitempty"`
	ToolCount        *int   `json:"toolCount,omitempty"`
	Tools            []any  `json:"tools,omitempty"`
}

// DevToolsTarget and RawTarget are descriptive aliases for callers that
// already use those terms for the injected /json/list seam.
type DevToolsTarget = TargetDescriptor
type RawTarget = TargetDescriptor

// Target is a normalized, safe browser target record. ID is the authoritative
// selector; the Chrome target ID and page websocket URL are deliberately not
// part of this public value. URL is query/fragment-free and Origin is a
// canonical origin, so neither field can carry transport credentials.
type Target struct {
	BrowserID string `json:"browser_id"`
	ID        string `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Origin    string `json:"origin"`
	// ContinuityMarker is an opaque digest of adapter-provided continuity
	// metadata. It is safe to retain in selection state and persistence.
	ContinuityMarker  string `json:"continuity_marker,omitempty"`
	Generation        uint64 `json:"generation,omitempty"`
	WebSocketPresent  bool   `json:"websocket_present"`
	WebMCP            bool   `json:"webmcp"`
	WebMCPKnown       bool   `json:"webmcp_known"`
	ToolCount         int    `json:"tool_count"`
	ToolCountKnown    bool   `json:"tool_count_known"`
	Eligible          bool   `json:"eligible"`
	EligibilityReason string `json:"eligibility_reason,omitempty"`
}

// TargetCapabilities is returned by an injected target-runtime seam. A
// capability probe can establish WebMCP support and the current tool count
// without importing a browser protocol package into discovery.
type TargetCapabilities struct {
	WebMCP         bool
	ToolCount      int
	ToolCountKnown bool
}

// TargetCapabilityProbe verifies the capability that /json/list cannot
// describe. It is called only after the target has a valid external page URL,
// page websocket, and an admitted origin.
type TargetCapabilityProbe interface {
	Probe(context.Context, BrowserCandidate, Target) (TargetCapabilities, error)
}

// TargetCapabilityProbeFunc adapts a function to TargetCapabilityProbe.
type TargetCapabilityProbeFunc func(context.Context, BrowserCandidate, Target) (TargetCapabilities, error)

// Probe implements TargetCapabilityProbe.
func (f TargetCapabilityProbeFunc) Probe(ctx context.Context, browser BrowserCandidate, target Target) (TargetCapabilities, error) {
	if f == nil {
		return TargetCapabilities{}, nil
	}
	return f(ctx, browser, target)
}

// TargetLister is an optional runtime seam for browser websocket-only
// discovery. HTTP endpoints use the standard /json/list request automatically;
// a target lister lets a neutral fake or a later adapter supply the same
// records without exposing protocol types here.
type TargetLister interface {
	List(context.Context, BrowserCandidate) ([]TargetDescriptor, error)
}

// TargetListerFunc adapts a function to TargetLister.
type TargetListerFunc func(context.Context, BrowserCandidate) ([]TargetDescriptor, error)

// List implements TargetLister.
func (f TargetListerFunc) List(ctx context.Context, browser BrowserCandidate) ([]TargetDescriptor, error) {
	if f == nil {
		return nil, nil
	}
	return f(ctx, browser)
}

// OriginPolicy admits or rejects canonical page origins before target
// eligibility is computed. Policy receives no query, fragment, credentials,
// or raw transport URL.
type OriginPolicy interface {
	Allows(string) bool
}

// OriginPolicyFunc adapts a function to OriginPolicy.
type OriginPolicyFunc func(string) bool

// Allows implements OriginPolicy.
func (f OriginPolicyFunc) Allows(origin string) bool {
	return f == nil || f(origin)
}

// TargetListOptions are the model-facing list filters. EligibleOnly is a
// pointer because C0 distinguishes an omitted value (default true) from an
// explicit false. IncludeZeroToolPages defaults to false.
type TargetListOptions struct {
	BrowserID            string `json:"browser_id,omitempty"`
	TargetID             string `json:"target_id,omitempty"`
	OriginContains       string `json:"origin_contains,omitempty"`
	EligibleOnly         *bool  `json:"eligible_only,omitempty"`
	IncludeZeroToolPages bool   `json:"include_zero_tool_pages,omitempty"`
}

// TargetFilters is a descriptive alias for callers that name these values as
// filters rather than list options.
type TargetFilters = TargetListOptions

// Bool returns a pointer suitable for optional boolean list fields.
func Bool(value bool) *bool { return &value }

// WithEligibleOnly makes the C0 default explicit for callers constructing
// options programmatically.
func WithEligibleOnly(value bool) TargetListOptions {
	return TargetListOptions{EligibleOnly: Bool(value)}
}

// TargetSnapshot is the deterministic result of one target refresh.
type TargetSnapshot struct {
	Browsers       []BrowserCandidate `json:"browsers"`
	Targets        []Target           `json:"targets"`
	CandidateCount int                `json:"candidate_count"`
	EligibleCount  int                `json:"eligible_count"`
	Filters        TargetListOptions  `json:"filters"`
}

// TargetListResult is a descriptive alias for TargetSnapshot.
type TargetListResult = TargetSnapshot

// TargetOwnership describes who is allowed to close a browser target. Lane B
// selections are external by default and therefore use detach-only cleanup.
// The ownership value is retained in the neutral contract so a later
// harness-owned launcher can make its stronger cleanup rights explicit.
type TargetOwnership string

const (
	TargetOwnershipExternal     TargetOwnership = "external"
	TargetOwnershipHarnessOwned TargetOwnership = "harness_owned"
)

// TargetDetacher is the only lifecycle operation Lane B accepts from an
// attached target. Keeping close-target and process operations out of this
// interface makes it impossible for the neutral selection service to turn
// closing an external handle into closing the customer's tab or browser.
type TargetDetacher interface {
	Detach(context.Context) error
}

// TargetDetacherFunc adapts a detach function to TargetDetacher.
type TargetDetacherFunc func(context.Context) error

// Detach implements TargetDetacher.
func (f TargetDetacherFunc) Detach(ctx context.Context) error {
	if f == nil {
		return nil
	}
	return f(ctx)
}

// TargetAttacher attaches a neutral selection to a target. The returned
// resource must expose detach only; browser-specific attach implementations
// remain outside this package.
type TargetAttacher interface {
	Attach(context.Context, BrowserCandidate, Target) (TargetDetacher, error)
}

// TargetRuntime is a descriptive alias for callers that name the injected
// attach seam as a runtime.
type TargetRuntime = TargetAttacher

// TargetAttacherFunc adapts an attach function to TargetAttacher.
type TargetAttacherFunc func(context.Context, BrowserCandidate, Target) (TargetDetacher, error)

// TargetRuntimeFunc is a descriptive alias for TargetAttacherFunc.
type TargetRuntimeFunc = TargetAttacherFunc

// Attach implements TargetAttacher.
func (f TargetAttacherFunc) Attach(ctx context.Context, browser BrowserCandidate, target Target) (TargetDetacher, error) {
	if f == nil {
		return nil, nil
	}
	return f(ctx, browser, target)
}

// TargetActivator foregrounds an exact target when selection explicitly asks
// for it. Selection itself never calls this seam unless Activate is true.
type TargetActivator interface {
	Activate(context.Context, BrowserCandidate, Target) error
}

// TargetActivatorFunc adapts an activation function to TargetActivator.
type TargetActivatorFunc func(context.Context, BrowserCandidate, Target) error

// Activate implements TargetActivator.
func (f TargetActivatorFunc) Activate(ctx context.Context, browser BrowserCandidate, target Target) error {
	if f == nil {
		return nil
	}
	return f(ctx, browser, target)
}

// SelectionOptions controls the state-changing part of an exact selection.
// Reason is diagnostic metadata only; it never changes which target is used.
type SelectionOptions struct {
	Activate bool
	Reason   string
}

// TargetSelectionRequest is the neutral exact-selection request. Browser may
// carry an already discovered candidate for callers that do not retain a
// browser catalog on the service. BrowserID and TargetID are the only
// authoritative selectors; title, URL, origin, and list order are ignored.
type TargetSelectionRequest struct {
	Browser   BrowserCandidate `json:"-"`
	BrowserID string           `json:"browser_id"`
	TargetID  string           `json:"target_id"`
	Activate  bool             `json:"activate,omitempty"`
	Reason    string           `json:"reason,omitempty"`
}

// SelectionRequest is a concise alias for TargetSelectionRequest.
type SelectionRequest = TargetSelectionRequest

// Selection is the current exact page selection. Its identity and generation
// are copied into returned values, so an older caller retains the old target
// identity even after the service selects a different page.
type Selection struct {
	BrowserID  string        `json:"browser_id"`
	TargetID   string        `json:"target_id"`
	Title      string        `json:"title"`
	URL        string        `json:"url"`
	Origin     string        `json:"origin"`
	Generation uint64        `json:"generation"`
	SelectedAt time.Time     `json:"selected_at"`
	Target     Target        `json:"target"`
	Handle     *TargetHandle `json:"-"`

	// statusSet distinguishes a service-owned selection from a value assembled
	// by a caller. These fields are intentionally private: callers validate an
	// exact selection through Service.ValidateSelection instead of changing
	// readiness or connection state on a copied value.
	statusSet bool
	connected bool
	ready     bool
}

// SelectedContext is a descriptive alias for the selected page state.
type SelectedContext = Selection

// Context returns a broker-neutral context copy for callers that prefer the
// method-shaped API used by later broker layers.
func (s Selection) Context() PageContext {
	connected, ready := true, true
	if s.statusSet {
		connected = s.connected
		ready = s.ready
	}
	return PageContext{
		BrowserID:  s.BrowserID,
		TargetID:   s.TargetID,
		Title:      s.Title,
		URL:        s.URL,
		Origin:     s.Origin,
		Generation: s.Generation,
		SelectedAt: s.SelectedAt,
		Connected:  connected,
		Ready:      ready,
	}
}

// PageContext is the safe context returned by Selection.Context. It contains
// no endpoint or target websocket values.
type PageContext struct {
	BrowserID  string    `json:"browser_id"`
	TargetID   string    `json:"target_id"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	Origin     string    `json:"origin"`
	Generation uint64    `json:"generation"`
	Connected  bool      `json:"connected"`
	Ready      bool      `json:"ready"`
	SelectedAt time.Time `json:"selected_at"`
}

// SelectionValidationRequest identifies the exact selection generation an
// operation intends to use. Browser and target display metadata are never
// accepted as selectors.
type SelectionValidationRequest struct {
	BrowserID  string `json:"browser_id"`
	TargetID   string `json:"target_id"`
	Generation uint64 `json:"generation"`
}

// SelectionGeneration is a concise alias for callers that model the
// generation-bearing identity separately from a full Selection value.
type SelectionGeneration = SelectionValidationRequest

// DiscoveryResult is provided for callers that prefer a named result object
// while Discover continues to return the candidate directly.
type DiscoveryResult struct {
	Browser BrowserCandidate `json:"browser"`
}

// HTTPClient is the narrow HTTP seam used for /json/version. *http.Client
// satisfies it, and deterministic fakes can implement it without opening a
// socket.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// ActivePortReader reads a profile's DevToolsActivePort record.
type ActivePortReader interface {
	Read(context.Context, string) (ActivePortRecord, error)
}

// ProcessEnumerator is intentionally optional and is called only when
// ConnectionInputs.AllowProcessScan is true.
type ProcessEnumerator interface {
	List(context.Context) ([]ProcessInfo, error)
}

// WebSocketProbe validates or obtains version metadata from a browser
// websocket endpoint. A websocket implementation belongs outside this
// package; the default probe validates the browser path and treats the
// endpoint itself as the available version metadata.
type WebSocketProbe interface {
	Probe(context.Context, string) (BrowserVersion, error)
}

// IDMapper converts normalized endpoint identity into an opaque public ID.
// BrowserIdentity is already query/fragment-free and contains only the
// normalized authority and browser websocket path.
type IDMapper interface {
	BrowserID(BrowserIdentity) string
}

// TargetIDMapper converts a browser plus raw target identity into an opaque
// public target ID. RawID is accepted only at the injected seam and never
// appears in normalized records, errors, or events.
type TargetIDMapper interface {
	TargetID(TargetIdentity) string
}

// TargetIdentity is the private-to-the-boundary identity input for a target
// ID mapper. BrowserID scopes target IDs so identical browser target IDs on
// different endpoints cannot collide.
type TargetIdentity struct {
	BrowserID string
	RawID     string
}

// BrowserIdentity is the safe input to an IDMapper. It contains no userinfo,
// query, or fragment. Host and path are used only to derive an opaque ID and
// never appear in the public event payload.
type BrowserIdentity struct {
	Scheme string
	Host   string
	Port   string
	Path   string
}

// EventType is the Lane B semantic discovery event vocabulary.
type EventType string

const (
	EventDiscoveryStarted      EventType = "browser.discovery.started"
	EventDiscoveryCompleted    EventType = "browser.discovery.completed"
	EventEndpointVersion       EventType = "browser.endpoint.version"
	EventTargetsSnapshot       EventType = "browser.targets.snapshot"
	EventTargetSelected        EventType = "browser.target.selected"
	EventTargetAttached        EventType = "browser.chrome.target_attached"
	EventPageGenerationChanged EventType = "browser.page.generation_changed"
	EventTargetDetached        EventType = "browser.target.detached"
)

// LifecycleEventType identifies normalized target lifecycle observations from
// a browser adapter or deterministic fake.
type LifecycleEventType string

const (
	LifecycleNavigation       LifecycleEventType = "navigation"
	LifecycleDocumentReplaced LifecycleEventType = "document_replaced"
	LifecycleTargetClosed     LifecycleEventType = "target_closed"
	LifecycleTargetDetached   LifecycleEventType = "target_detached"
)

// LifecycleEvent is the neutral lifecycle seam used to invalidate exact page
// selections. EventID, Sequence, DocumentID, or an explicit Generation can
// make repeated delivery of the same adapter event idempotent. A lifecycle
// event never carries transport URLs or credentials.
type LifecycleEvent struct {
	Type               LifecycleEventType
	BrowserID          string
	TargetID           string
	EventID            string
	Sequence           uint64
	DocumentID         string
	PreviousGeneration uint64
	Generation         uint64
	Reason             string
	Capabilities       *TargetCapabilities
	WebMCP             *bool
	ToolCount          *int
}

// PageLifecycleEvent and TargetLifecycleEvent are descriptive aliases for
// adapters that keep page and target notifications in separate streams.
type PageLifecycleEvent = LifecycleEvent
type TargetLifecycleEvent = LifecycleEvent

// Redaction describes the safe representation of a semantic event. Discovery
// never emits raw CDP frames or transport URLs, so the mode is always
// redacted.
type Redaction struct {
	Mode  string   `json:"mode"`
	Rules []string `json:"rules,omitempty"`
}

// Event is a normalized semantic event. Payload is limited to stable facts
// and is copied before delivery to prevent a sink from mutating later state.
type Event struct {
	Version     string         `json:"version"`
	Sequence    uint64         `json:"sequence"`
	MonotonicMS uint64         `json:"monotonic_ms"`
	Type        EventType      `json:"type"`
	BrowserID   string         `json:"browser_id,omitempty"`
	TargetID    string         `json:"target_id,omitempty"`
	Generation  uint64         `json:"generation,omitempty"`
	Payload     map[string]any `json:"payload"`
	Redaction   Redaction      `json:"redaction"`
}

// EventSink receives semantic discovery events. EventFunc adapts a function
// to this interface.
type EventSink interface {
	Emit(Event)
}

// EventFunc is a convenient EventSink adapter.
type EventFunc func(Event)

// Emit implements EventSink.
func (f EventFunc) Emit(event Event) {
	if f != nil {
		f(event)
	}
}

// ConfiguredSource resolves one already-configured endpoint profile. The
// source name is included only as a bounded diagnostic label.
type ConfiguredSource interface {
	Name() string
	Resolve(context.Context) (Endpoint, error)
}

// ConfiguredSourceFunc adapts functions to ConfiguredSource.
type ConfiguredSourceFunc struct {
	SourceName  string
	ResolveFunc func(context.Context) (Endpoint, error)
}

// Name implements ConfiguredSource.
func (s ConfiguredSourceFunc) Name() string { return s.SourceName }

// Resolve implements ConfiguredSource.
func (s ConfiguredSourceFunc) Resolve(ctx context.Context) (Endpoint, error) {
	if s.ResolveFunc == nil {
		return Endpoint{}, nil
	}
	return s.ResolveFunc(ctx)
}

// StaticConfiguredSource is useful for resolved configuration and deterministic
// tests.
type StaticConfiguredSource struct {
	SourceName string
	Value      Endpoint
}

// Name implements ConfiguredSource.
func (s StaticConfiguredSource) Name() string { return s.SourceName }

// Resolve implements ConfiguredSource.
func (s StaticConfiguredSource) Resolve(context.Context) (Endpoint, error) {
	return s.Value, nil
}
