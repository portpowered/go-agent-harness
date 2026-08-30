package cli

import (
	"context"
	"encoding/json"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp/discovery"
	"sync"
)

type productionTargetLister struct{ owner *productionWebMCPComposition }

func (l productionTargetLister) List(ctx context.Context, browser discovery.BrowserCandidate) ([]discovery.TargetDescriptor, error) {
	if l.owner == nil {
		return nil, webmcp.ErrClosed
	}
	return l.owner.listRawTargetDescriptors(ctx, browser)
}

type productionTargetProbe struct{ owner *productionWebMCPComposition }

func (p productionTargetProbe) Probe(ctx context.Context, browser discovery.BrowserCandidate, target discovery.Target) (discovery.TargetCapabilities, error) {
	if p.owner == nil {
		return discovery.TargetCapabilities{}, webmcp.ErrClosed
	}
	return p.owner.probeTarget(ctx, browser, target)
}

type productionWebMCPCatalog struct{ owner *productionWebMCPComposition }

func (c *productionWebMCPCatalog) Version(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserVersion, error) {
	if c == nil || c.owner == nil {
		return webmcp.BrowserVersion{}, webmcp.ErrClosed
	}
	enriched, err := c.owner.enrichCoreCandidate(ctx, candidate)
	if err != nil {
		return webmcp.BrowserVersion{}, err
	}
	if c.owner.catalog != nil {
		return c.owner.catalog.Version(ctx, enriched)
	}
	return webmcp.BrowserVersion{
		Browser:              enriched.Product,
		ProtocolVersion:      enriched.Protocol,
		WebSocketDebuggerURL: enriched.BrowserWSURL,
		BrowserInstanceID:    enriched.BrowserInstanceID,
	}, nil
}

func (c *productionWebMCPCatalog) ListTargets(ctx context.Context, candidate webmcp.BrowserCandidate) ([]webmcp.Target, error) {
	if c == nil || c.owner == nil {
		return nil, webmcp.ErrClosed
	}
	return (&productionWebMCPHandle{owner: c.owner, candidate: candidate, closed: make(chan struct{})}).ListTargets(ctx)
}

type productionTargetSession struct {
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

func newProductionWebMCPSession(raw webmcp.TargetSession, target webmcp.Target) (webmcp.TargetSession, error) {
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
	session := &productionTargetSession{
		raw:              raw,
		target:           target,
		rawGeneration:    rawGeneration,
		publicGeneration: publicGeneration,
		events:           make(chan webmcp.BrowserEvent, 128),
		done:             make(chan struct{}),
		stop:             make(chan struct{}),
		flush:            make(chan chan struct{}),
	}
	go session.forwardEvents()
	return session, nil
}

func (s *productionTargetSession) Context() webmcp.PageContext {
	page := s.raw.Context()
	page.Key = webmcp.PageKey{BrowserID: s.target.BrowserID, TargetID: s.target.ID}
	if s.target.Title != "" {
		page.Title = s.target.Title
	}
	if s.target.URL != "" {
		page.URL = s.target.URL
	}
	if s.target.Origin != "" {
		page.Origin = s.target.Origin
	}
	if s.target.Generation > 0 {
		page.Generation = s.target.Generation
	}
	if page.DocumentReadyState == "" {
		page.DocumentReadyState = s.target.DocumentReadyState
	}
	if !page.DocumentLoadingKnown && s.target.DocumentLoadingKnown {
		page.DocumentLoading = s.target.DocumentLoading
		page.DocumentLoadingKnown = true
	}
	return page
}

func (s *productionTargetSession) Ownership() webmcp.TargetOwnership { return s.raw.Ownership() }

func (s *productionTargetSession) EnableWebMCP(ctx context.Context) error {
	if err := s.raw.EnableWebMCP(ctx); err != nil {
		return err
	}
	// The neutral broker flushes the session immediately after enablement.
	// Bridge adapters have one additional forwarding hop, so synchronize that
	// hop before returning; otherwise a just-emitted ToolsAdded event could
	// arrive after the broker's flush and make a ready catalog appear empty.
	return s.flushEvents(ctx)
}

func (s *productionTargetSession) CapturePageScreenshot(ctx context.Context) (webmcp.PageScreenshot, error) {
	if s == nil || s.raw == nil {
		return webmcp.PageScreenshot{}, webmcp.ErrClosed
	}
	capturer, ok := s.raw.(webmcp.PageScreenshotter)
	if !ok {
		return webmcp.PageScreenshot{}, webmcp.NewClassifiedError(
			webmcp.ErrorUnsupportedWebMCP,
			"the selected browser page does not support screenshot capture",
			map[string]any{"capability": webmcp.PageCaptureScreenshotMethod},
		)
	}
	screenshot, err := capturer.CapturePageScreenshot(ctx)
	if err != nil {
		return webmcp.PageScreenshot{}, err
	}
	// The raw Chrome adapter reports the protocol target ID. This bridge owns
	// the public discovery identity, so remap the successful capture before it
	// reaches the neutral broker's exact-selection check.
	screenshot.BrowserID = s.target.BrowserID
	screenshot.TargetID = s.target.ID
	screenshot.Bytes = append([]byte(nil), screenshot.Bytes...)
	return screenshot, nil
}

func (s *productionTargetSession) Events() <-chan webmcp.BrowserEvent { return s.events }

func (s *productionTargetSession) InvokeWebMCP(ctx context.Context, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	return s.raw.InvokeWebMCP(ctx, frameID, toolName, input)
}

func (s *productionTargetSession) CancelWebMCP(ctx context.Context, invocationID webmcp.InvocationID) error {
	return s.raw.CancelWebMCP(ctx, invocationID)
}

func (s *productionTargetSession) Done() <-chan struct{} { return s.done }

func (s *productionTargetSession) Err() error { return s.raw.Err() }

func (s *productionTargetSession) Close() error {
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

func (s *productionTargetSession) flushEvents(ctx context.Context) error {
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

func (s *productionTargetSession) forwardEvents() {
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
			if !ok {
				return
			}
			if !s.forwardEvent(event) {
				return
			}
		}
	}
}

func (s *productionTargetSession) drainRawEvents() bool {
	for {
		select {
		case <-s.stop:
			return false
		case event, ok := <-s.raw.Events():
			if !ok {
				return false
			}
			if !s.forwardEvent(event) {
				return false
			}
		default:
			return true
		}
	}
}

func (s *productionTargetSession) forwardEvent(event webmcp.BrowserEvent) bool {
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
func (s *productionTargetSession) publicGenerationForRawGeneration(rawGeneration uint64) uint64 {
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
