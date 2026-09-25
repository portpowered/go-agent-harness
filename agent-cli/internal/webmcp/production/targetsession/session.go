// Package targetsession bridges one raw browser target session onto the
// public discovery identity used by the production WebMCP composition. It
// rewrites browser/target IDs and rebases document generations on every
// forwarded event while preserving the raw session's optional capabilities.
package targetsession

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

const eventBufferSize = 128

// Session is the identity/event bridge around one raw target session.
type Session struct {
	raw              webmcp.TargetSession
	target           webmcp.Target
	rawGeneration    uint64
	publicGeneration uint64
	events           chan webmcp.BrowserEvent
	done             chan struct{}
	stop             chan struct{}
	flush            chan chan struct{}
	stopOnce         sync.Once
	closeOnce        sync.Once
	closeErr         error
}

// New wraps raw so its context and events report target's public identity.
func New(raw webmcp.TargetSession, target webmcp.Target) (webmcp.TargetSession, error) {
	if raw == nil {
		return nil, webmcp.NewClassifiedError(webmcp.ErrorTargetAttachFailed, "the selected browser target could not be initialized", nil)
	}
	rawGeneration := raw.Context().Generation
	if rawGeneration == 0 {
		rawGeneration = 1
	}
	publicGeneration := target.Generation
	if publicGeneration == 0 {
		publicGeneration = rawGeneration
	}
	session := &Session{
		raw:              raw,
		target:           target,
		rawGeneration:    rawGeneration,
		publicGeneration: publicGeneration,
		events:           make(chan webmcp.BrowserEvent, eventBufferSize),
		done:             make(chan struct{}),
		stop:             make(chan struct{}),
		flush:            make(chan chan struct{}),
	}
	go session.forwardEvents()
	return session, nil
}

// Context reports the raw page context under the public identity.
func (s *Session) Context() webmcp.PageContext {
	page := s.raw.Context()
	page.Key = webmcp.PageKey{BrowserID: s.target.BrowserID, TargetID: s.target.ID}
	if page.Generation <= s.rawGeneration {
		// Discovery metadata is authoritative only for the document that was
		// attached. After navigation the raw session owns the new URL/title and
		// document state; reapplying this snapshot would resurrect stale values.
		s.applyAttachedMetadata(&page)
	}
	page.Generation = s.publicGenerationForRawGeneration(page.Generation)
	return page
}

func (s *Session) applyAttachedMetadata(page *webmcp.PageContext) {
	if s.target.Title != "" {
		page.Title = s.target.Title
	}
	if s.target.URL != "" {
		page.URL = s.target.URL
	}
	if s.target.Origin != "" {
		page.Origin = s.target.Origin
	}
	if page.DocumentReadyState == "" {
		page.DocumentReadyState = s.target.DocumentReadyState
	}
	if !page.DocumentLoadingKnown && s.target.DocumentLoadingKnown {
		page.DocumentLoading = s.target.DocumentLoading
		page.DocumentLoadingKnown = true
	}
}

// Ownership reports the raw session's ownership.
func (s *Session) Ownership() webmcp.TargetOwnership { return s.raw.Ownership() }

// EnableWebMCP enables the raw session and synchronizes the forwarding hop.
func (s *Session) EnableWebMCP(ctx context.Context) error {
	if err := s.raw.EnableWebMCP(ctx); err != nil {
		return err
	}
	// The neutral broker flushes the session immediately after enablement.
	// Bridge adapters have one additional forwarding hop, so synchronize that
	// hop before returning; otherwise a just-emitted ToolsAdded event could
	// arrive after the broker's flush and make a ready catalog appear empty.
	return s.flushEvents(ctx)
}

// Events returns the rebased event stream.
func (s *Session) Events() <-chan webmcp.BrowserEvent { return s.events }

// InvokeWebMCP forwards one invocation to the raw session.
func (s *Session) InvokeWebMCP(ctx context.Context, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	return s.raw.InvokeWebMCP(ctx, frameID, toolName, input)
}

// CancelWebMCP forwards one cancellation to the raw session.
func (s *Session) CancelWebMCP(ctx context.Context, invocationID webmcp.InvocationID) error {
	return s.raw.CancelWebMCP(ctx, invocationID)
}

// Done closes once event forwarding has stopped.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err reports the raw session's terminal error.
func (s *Session) Err() error { return s.raw.Err() }

// Close stops forwarding and closes the raw session exactly once.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.stopOnce.Do(func() { close(s.stop) })
		s.closeErr = s.raw.Close()
		<-s.done
	})
	return s.closeErr
}

func (s *Session) flushEvents(ctx context.Context) error { //nolint:contextcheck // A nil context from legacy callers falls back to Background.
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ack := make(chan struct{})
	select {
	case s.flush <- ack:
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-ack:
		return nil
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) forwardEvents() {
	defer close(s.events)
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case ack := <-s.flush:
			if !s.drainRawEvents() {
				return
			}
			close(ack)
		case <-s.raw.Done():
			return
		case event, ok := <-s.raw.Events():
			if !ok || !s.forwardEvent(event) {
				return
			}
		}
	}
}

func (s *Session) drainRawEvents() bool {
	for {
		select {
		case <-s.stop:
			return false
		case event, ok := <-s.raw.Events():
			if !ok || !s.forwardEvent(event) {
				return false
			}
		default:
			return true
		}
	}
}

func (s *Session) forwardEvent(event webmcp.BrowserEvent) bool {
	event.BrowserID = s.target.BrowserID
	event.TargetID = s.target.ID
	if event.Generation == 0 {
		event.Generation = s.publicGeneration
	} else {
		event.Generation = s.publicGenerationForRawGeneration(event.Generation)
	}
	if event.PreviousGeneration != 0 {
		event.PreviousGeneration = s.publicGenerationForRawGeneration(event.PreviousGeneration)
	}
	for index := range event.Tools {
		event.Tools[index].BrowserID = s.target.BrowserID
		event.Tools[index].TargetID = s.target.ID
		if event.Tools[index].Generation == 0 {
			event.Tools[index].Generation = event.Generation
		} else {
			event.Tools[index].Generation = s.publicGenerationForRawGeneration(event.Tools[index].Generation)
		}
	}
	select {
	case s.events <- event:
		return true
	case <-s.stop:
		return false
	}
}

// publicGenerationForRawGeneration rebases the Chrome adapter's session-local
// document counter onto the discovery generation carried by the persisted
// selection. A newly attached Chrome target starts its neutral counter at one,
// while a reconnect may intentionally resume at a later public generation.
func (s *Session) publicGenerationForRawGeneration(rawGeneration uint64) uint64 {
	if s == nil || rawGeneration == 0 || s.rawGeneration == 0 {
		return rawGeneration
	}
	if rawGeneration < s.rawGeneration {
		return rawGeneration
	}
	delta := rawGeneration - s.rawGeneration
	if delta > ^uint64(0)-s.publicGeneration {
		return ^uint64(0)
	}
	return s.publicGeneration + delta
}
