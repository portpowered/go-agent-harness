// Package toolpublication defines the transport-neutral live tool-surface
// publication contract. Browser, provider, and agent-loop adapters belong at
// the composition edge; the service owns publication policy and lifecycle.
package toolpublication

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

var (
	// ErrSessionDynamicToolPublication identifies a failed live page-tool
	// refresh or session-update delivery. A successful surface is retained
	// when this error is reported.
	ErrSessionDynamicToolPublication = errors.New("session dynamic tool publication failed")
	// ErrInvalidOptions identifies a publisher that cannot be safely started.
	ErrInvalidOptions = errors.New("invalid tool publication options")
)

// SettleWindow is the bounded coalescing interval for one browser change.
// Hosts may inject a deterministic timer factory for tests.
const SettleWindow = 10 * time.Millisecond

// EventType is the small semantic event vocabulary needed by publication.
type EventType string

const (
	EventSelected          EventType = "selected"
	EventCatalogChanged    EventType = "catalog_changed"
	EventGenerationChanged EventType = "generation_changed"
)

// Event is a transport-neutral browser/catalog observation. IDs remain opaque
// strings so the service does not depend on a browser implementation.
type Event struct {
	Type       EventType
	Sequence   uint64
	BrowserID  string
	TargetID   string
	Generation uint64
}

// EventWatch owns one subscription for one publisher instance.
type EventWatch func(context.Context) <-chan Event

// DefinitionRefresher returns the complete current dynamic tool surface. The
// service merges it with the immutable stable definitions.
type DefinitionRefresher func(context.Context) ([]messages.ToolDefinition, error)

// Timer is the only clock primitive required by the coalescing controller.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(time.Duration) bool
}

// TimerFactory creates timers for one publisher. Production construction
// supplies a wall-clock implementation; tests can inject a deterministic one.
type TimerFactory interface {
	NewTimer(time.Duration) Timer
}

// SessionUpdateSink is the provider-neutral delivery port for a complete
// session tool surface.
type SessionUpdateSink interface {
	SendSessionUpdate(context.Context, []messages.ToolDefinition) error
}

// SessionUpdateSinkFunc adapts a function to SessionUpdateSink.
type SessionUpdateSinkFunc func(context.Context, []messages.ToolDefinition) error

func (f SessionUpdateSinkFunc) SendSessionUpdate(ctx context.Context, definitions []messages.ToolDefinition) error {
	if f == nil {
		return errors.New("session update sink is nil")
	}
	return f(ctx, definitions)
}

// Options contains the explicit per-session ports and initial surface.
// Construction is inert until Publisher.Start is called.
type Options struct {
	StaticStableDefinitions []messages.ToolDefinition
	InitialDefinitions      []messages.ToolDefinition
	Watch                   EventWatch
	Refresh                 DefinitionRefresher
	TimerFactory            TimerFactory
}

// Service constructs request-scoped publishers. It is assembled by the
// dedicated Wire package so the private implementation never crosses the
// public service boundary.
type Service interface {
	NewPublisher(Options) (Publisher, error)
}

// Publisher owns one watch, one coalescing timer, and one publication state.
// Ready releases dynamic publication after the host has delivered its initial
// provider session configuration. Stop is idempotent.
type Publisher interface {
	Start(context.Context, SessionUpdateSink)
	Ready()
	Errors() <-chan error
	Snapshot() State
	Done() <-chan struct{}
	Stop()
}

// Lifecycle is the bounded state vocabulary for one publisher.
type Lifecycle string

const (
	LifecycleCreated   Lifecycle = "created"
	LifecycleReady     Lifecycle = "ready"
	LifecycleFailed    Lifecycle = "failed"
	LifecycleStopped   Lifecycle = "stopped"
	LifecycleWatchGone Lifecycle = "watch_closed"
)

// State is a diagnostic snapshot. Definition slices are copied by the
// implementation, so callers cannot mutate controller state.
type State struct {
	StaticStableDefinitions     []messages.ToolDefinition
	LastSuccessfulDefinitions   []messages.ToolDefinition
	LastSuccessfulDigest        string
	LastSuccessfulBrowserID     string
	LastSuccessfulTargetID      string
	LastSuccessfulGeneration    uint64
	LastSuccessfulEventSequence uint64
	LatestEventSequence         uint64
	Lifecycle                   Lifecycle
	Err                         error
	PublicationCount            uint64
}

// PublicationError keeps phase and sequence metadata bounded while retaining
// the publication sentinel and exact underlying cause for errors.Is/errors.As.
type PublicationError struct {
	Phase    string
	Sequence uint64
	Err      error
}

func (e *PublicationError) Error() string {
	if e == nil {
		return ErrSessionDynamicToolPublication.Error()
	}
	message := e.ErrString()
	if message == "" {
		message = "unknown error"
	}
	return "session dynamic tool publication failed: phase=" + e.Phase +
		" sequence=" + formatSequence(e.Sequence) + ": " + message
}

func (e *PublicationError) ErrString() string {
	if e == nil || e.Err == nil {
		return ""
	}
	message := strings.TrimSpace(e.Err.Error())
	const maxPublicationErrorText = 256
	if len(message) > maxPublicationErrorText {
		return message[:maxPublicationErrorText] + "..."
	}
	return message
}

func (e *PublicationError) Unwrap() error {
	if e == nil {
		return ErrSessionDynamicToolPublication
	}
	return errors.Join(ErrSessionDynamicToolPublication, e.Err)
}

// formatSequence avoids pulling formatting policy into the private controller
// while keeping the diagnostic string deterministic.
func formatSequence(sequence uint64) string {
	if sequence == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for sequence > 0 {
		index--
		digits[index] = byte('0' + sequence%10)
		sequence /= 10
	}
	return string(digits[index:])
}
