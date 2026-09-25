package sessionturn

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// PublicationSettleWindow collects the selection, generation, and catalog
// notifications of one browser change before one refresh, while keeping a
// genuine later change responsive.
const PublicationSettleWindow = 10 * time.Millisecond

// MaxPublicationErrorText bounds the cause text carried by a publication
// error.
const MaxPublicationErrorText = 256

// BrowserEventType is the bounded browser lifecycle vocabulary that can
// change the advertised tool surface.
type BrowserEventType string

const (
	BrowserEventSelected          BrowserEventType = "selected"
	BrowserEventCatalogChanged    BrowserEventType = "catalog_changed"
	BrowserEventGenerationChanged BrowserEventType = "generation_changed"
)

// BrowserEvent is one host browser observation projected onto the fields the
// publisher orders by. Other event types are ignored.
type BrowserEvent struct {
	Type       BrowserEventType
	BrowserID  string
	TargetID   string
	Generation uint64
	Sequence   uint64
}

// Timer is the settle-boundary timer contract.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(time.Duration) bool
}

// TimerFactory creates settle timers. Nil selects the wall clock.
type TimerFactory interface {
	NewTimer(time.Duration) Timer
}

// PublicationLifecycle is the bounded publication lifecycle vocabulary.
type PublicationLifecycle string

const (
	PublicationCreated     PublicationLifecycle = "created"
	PublicationReady       PublicationLifecycle = "ready"
	PublicationFailed      PublicationLifecycle = "failed"
	PublicationStopped     PublicationLifecycle = "stopped"
	PublicationWatchClosed PublicationLifecycle = "watch_closed"
)

// PublicationState is a diagnostic snapshot. Definitions are copies.
type PublicationState struct {
	StaticStableDefinitions     []messages.ToolDefinition
	LastSuccessfulDefinitions   []messages.ToolDefinition
	LastSuccessfulDigest        string
	LastSuccessfulBrowserID     string
	LastSuccessfulTargetID      string
	LastSuccessfulGeneration    uint64
	LastSuccessfulEventSequence uint64
	LatestEventSequence         uint64
	Lifecycle                   PublicationLifecycle
	Err                         error
	PublicationCount            uint64
}

// PublicationRequest configures one session's dynamic tool publication. A
// nil Watch or Refresh produces an inert publication.
type PublicationRequest struct {
	// StaticStableDefinitions is the immutable base surface retained across
	// every page catalog change.
	StaticStableDefinitions []messages.ToolDefinition
	// InitialDefinitions is the surface advertised at session start.
	InitialDefinitions []messages.ToolDefinition
	// Watch is an independent subscription to browser observations.
	Watch func(context.Context) <-chan BrowserEvent
	// Refresh returns the complete current tool surface.
	Refresh func(context.Context) ([]messages.ToolDefinition, error)
	// Publish delivers a full definition replacement to the provider.
	Publish      func(context.Context, []messages.ToolDefinition) error
	TimerFactory TimerFactory
}

// Publication is one running dynamic tool publication.
type Publication interface {
	// MarkSessionReady releases publication after the provider's initial
	// configuration boundary. Events seen earlier are reconciled by the first
	// refresh.
	MarkSessionReady()
	// Errors reports the first publication failure.
	Errors() <-chan error
	State() PublicationState
	// Stop cancels the watch and waits for the publisher to exit.
	Stop()
}

// PublicationService starts dynamic tool publication.
type PublicationService interface {
	// StartPublication begins watching before the session is ready. It never
	// returns nil.
	StartPublication(context.Context, PublicationRequest) Publication
	// MergeToolDefinitions keeps every base definition and adds the named
	// page definitions that do not collide with it, in canonical order.
	MergeToolDefinitions(base, definitions []messages.ToolDefinition) []messages.ToolDefinition
	// ToolDefinitionDigest identifies a canonical definition surface.
	ToolDefinitionDigest([]messages.ToolDefinition) (string, error)
}

// PublicationError carries bounded phase and event metadata while retaining
// the cause for classification.
type PublicationError struct {
	Phase    string
	Sequence uint64
	Err      error
}

func (e *PublicationError) Error() string {
	if e == nil {
		return ErrPublication.Error()
	}
	message := strings.TrimSpace(e.CauseText())
	if message == "" {
		message = "unknown error"
	}
	return fmt.Sprintf("%s: phase=%s sequence=%d: %s", ErrPublication, e.Phase, e.Sequence, message)
}

// CauseText returns the bounded cause text.
func (e *PublicationError) CauseText() string {
	if e == nil || e.Err == nil {
		return ""
	}
	message := strings.TrimSpace(e.Err.Error())
	if len(message) > MaxPublicationErrorText {
		return message[:MaxPublicationErrorText] + "..."
	}
	return message
}

// Unwrap exposes both the publication identity and the cause.
func (e *PublicationError) Unwrap() []error {
	if e == nil {
		return []error{ErrPublication}
	}
	return []error{ErrPublication, e.Err}
}
