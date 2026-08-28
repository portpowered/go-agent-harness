package discovery

import (
	"context"
	"net/http"
)

const (
	// BrowserEventsVersion is the semantic event stream version frozen by C0.
	BrowserEventsVersion = "webmcp.browser-events.v1"

	// DefaultMaxVersionBytes bounds one /json/version response before it is
	// decoded. A discovery response is small by contract; the bound also
	// prevents an endpoint from turning discovery into an unbounded read.
	DefaultMaxVersionBytes int64 = 64 << 10
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
	EventDiscoveryStarted   EventType = "browser.discovery.started"
	EventDiscoveryCompleted EventType = "browser.discovery.completed"
	EventEndpointVersion    EventType = "browser.endpoint.version"
)

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
