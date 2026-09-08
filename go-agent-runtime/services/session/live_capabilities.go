package session

import (
	"context"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// LiveInferencerFactory constructs the provider session at Start time. Keeping
// this edge as a function lets embedders inject a fake session and lets hosts
// choose a provider, transport, or replay implementation without exposing any
// provider configuration or concrete session type in this package.
type LiveInferencerFactory func(context.Context, LiveRequest) (messages.SessionInferencer, error)

// LiveTurnDetection is the provider-neutral VAD policy carried into a live
// session. Provider adapters translate it into their wire representation.
type LiveTurnDetection struct {
	Type              string
	Threshold         float64
	PrefixPaddingMs   int
	SilenceDurationMs int
	CreateResponse    *bool
	InterruptResponse *bool
	Eagerness         string
}

// LiveCapabilities is one participant's owned tool surface. A factory may
// return an explicit empty binding (nil Executor and empty Definitions) to
// disable tools for one participant, or set InheritDefaults to use the
// service-level binding. RefreshDefinitions is sampled by the live owner at
// each provider session admission and may be used by a host that publishes
// updates between turns. Close releases participant-scoped resources.
type LiveCapabilities struct {
	Executor        messages.ToolExecutor
	Definitions     []messages.ToolDefinition
	InheritDefaults bool
	// Handle is the preferred lifecycle owner for a request-scoped capability
	// surface. When present, the live owner calls its Initialize,
	// RefreshDefinitions, and Close methods and does not independently invoke
	// the callback fields below. The callback fields remain an adapter seam for
	// hosts that have not yet promoted their capability owner to a handle.
	Handle             LiveCapabilityHandle
	RefreshDefinitions func(context.Context) ([]messages.ToolDefinition, error)
	Initialize         func(context.Context) error
	BrowserWatch       func(context.Context) <-chan LiveCapabilityEvent
	Close              func() error
}

// LiveCapabilityHandle owns one participant's admitted tool/browser
// resources. Implementations must make Close idempotent. A handle may also
// implement LiveCapabilityWatcher to expose the bounded browser lifecycle
// stream without coupling this contract to a broker package.
type LiveCapabilityHandle interface {
	Initialize(context.Context) error
	RefreshDefinitions(context.Context) ([]messages.ToolDefinition, error)
	Close() error
}

// LiveCapabilityWatcher is an optional extension of LiveCapabilityHandle for
// hosts that need browser invocation and page lifecycle observations.
type LiveCapabilityWatcher interface {
	BrowserWatch(context.Context) <-chan LiveCapabilityEvent
}

// LiveCapabilityEvent is the provider-neutral browser/tool lifecycle event
// forwarded by a participant-local capability binding. Hosts may omit the
// watch port when they only need tool invocation.
type LiveCapabilityEvent struct {
	Type               string
	Sequence           uint64
	Timestamp          time.Time
	BrowserID          string
	TargetID           string
	Generation         uint64
	PreviousGeneration uint64
	InvocationID       string
	ToolName           string
	State              string
	Status             string
	ErrorCode          string
	Reason             string
	CatalogReady       bool
	ToolCount          int
	ToolCountKnown     bool
}

// LiveCapabilityFactory creates an invocation-scoped capability binding. The
// request includes ParticipantID and an opaque CredentialReference, allowing
// room hosts to select independent tools and credentials without mutable
// process-wide state.
type LiveCapabilityFactory func(context.Context, LiveRequest) (LiveCapabilities, error)

// LiveClock is injected by a host so event timestamps share the same clock as
// device, recording, and room evidence. A nil clock leaves timestamps zero.
type LiveClock func() time.Time

// LiveControlKind identifies an ordered control or input operation.
type LiveControlKind string

const (
	LiveControlText           LiveControlKind = "text"
	LiveControlAudioCommit    LiveControlKind = "audio_commit"
	LiveControlResponseCancel LiveControlKind = "response_cancel"
	LiveControlResponseCreate LiveControlKind = "response_create"
	LiveControlClose          LiveControlKind = "close"
)

// LiveControl is admitted through one bounded ordered ingress so text, audio,
// and turn boundaries cannot overtake one another on the provider wire.
type LiveControl struct {
	Kind LiveControlKind
	Text string
}
