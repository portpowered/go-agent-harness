package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

// BrowserScriptAdapter exposes a validated browser-script runtime through the
// browser-neutral broker interfaces. It is deliberately synchronous at the
// fixture boundary: every browser command consumes exactly one scripted
// operation and every emitted fixture event is copied into the broker event
// stream without a socket or a wall-clock wait.
type BrowserScriptAdapter struct {
	runtime   *BrowserScriptRuntime
	candidate webmcp.BrowserCandidate
	target    webmcp.Target

	mu     sync.Mutex
	handle *BrowserScriptHandle
	closed bool
}

// NewBrowserScriptAdapter constructs a broker adapter for one validated
// browser script and its run-scoped runtime.
func NewBrowserScriptAdapter(script BrowserScript, runtime *BrowserScriptRuntime) (*BrowserScriptAdapter, error) {
	if runtime == nil {
		return nil, fmt.Errorf("webmcp testkit: browser script runtime is nil")
	}
	if err := script.Validate(); err != nil {
		return nil, err
	}
	fixtureTarget := runtime.Target()
	if fixtureTarget.ID == "" {
		return nil, errors.New("webmcp testkit: browser script has no target")
	}
	browserID := runtime.BrowserID()
	if browserID == "" {
		return nil, errors.New("webmcp testkit: browser script has no browser ID")
	}
	origin := targetOrigin(fixtureTarget.URL)
	target := webmcp.Target{
		BrowserID:    webmcp.BrowserID(browserID),
		ID:           webmcp.TargetID(fixtureTarget.ID),
		Type:         fixtureTarget.Type,
		Title:        fixtureTarget.Title,
		URL:          fixtureTarget.URL,
		Origin:       origin,
		Generation:   runtime.Generation(),
		WebSocketURL: fixtureTarget.WebSocketDebuggerURL,
		Eligible:     true,
	}
	candidate := webmcp.BrowserCandidate{
		ID:           webmcp.BrowserID(browserID),
		Source:       webmcp.DiscoverySourceReplay,
		Product:      script.Endpoint.Version.Browser,
		Protocol:     script.Endpoint.Version.ProtocolVersion,
		BrowserWSURL: script.Endpoint.Version.WebSocketDebuggerURL,
		Loopback:     true,
		Explicit:     true,
		HarnessOwned: true,
	}
	return &BrowserScriptAdapter{
		runtime:   runtime,
		candidate: candidate,
		target:    target,
	}, nil
}

// NewFixtureBrowserAdapter is a descriptive constructor alias.
func NewFixtureBrowserAdapter(script BrowserScript, runtime *BrowserScriptRuntime) (*BrowserScriptAdapter, error) {
	return NewBrowserScriptAdapter(script, runtime)
}

// Runtime returns the run-scoped fixture runtime for evidence inspection.
func (a *BrowserScriptAdapter) Runtime() *BrowserScriptRuntime {
	if a == nil {
		return nil
	}
	return a.runtime
}

// Discover implements webmcp.BrowserDiscoverer. A fixture exposes exactly one
// explicitly selected replay candidate and never performs process scanning.
func (a *BrowserScriptAdapter) Discover(ctx context.Context, options webmcp.DiscoverOptions) ([]webmcp.BrowserCandidate, error) {
	if err := adapterContextError(ctx); err != nil {
		return nil, err
	}
	if a == nil {
		return nil, webmcp.ErrClosed
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.runtime.Outcome().Status != BrowserScriptOpen {
		if a.runtime.Outcome().OK() {
			// A completed fixture remains a valid discovery result for callers
			// that inspect final evidence after execution.
		} else {
			return nil, webmcp.ErrClosed
		}
	}
	if options.BrowserID != "" && options.BrowserID != a.candidate.ID {
		return nil, webmcp.ErrBrowserNotFound
	}
	if options.ExplicitOnly && !a.candidate.Explicit {
		return nil, webmcp.ErrBrowserNotFound
	}
	return []webmcp.BrowserCandidate{cloneFixtureCandidate(a.candidate)}, nil
}

// Open implements webmcp.BrowserRuntime. The adapter owns one handle for the
// run, so repeated opens retain the same target/session state.
func (a *BrowserScriptAdapter) Open(ctx context.Context, candidate webmcp.BrowserCandidate) (webmcp.BrowserHandle, error) {
	if err := adapterContextError(ctx); err != nil {
		return nil, err
	}
	if a == nil {
		return nil, webmcp.ErrClosed
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, webmcp.ErrClosed
	}
	if candidate.ID != "" && candidate.ID != a.candidate.ID {
		return nil, webmcp.ErrBrowserNotFound
	}
	if a.handle == nil {
		a.handle = &BrowserScriptHandle{adapter: a, candidate: cloneFixtureCandidate(a.candidate)}
	}
	return a.handle, nil
}

// Navigate consumes a scripted navigate operation and emits its page
// generation transition through the attached session.
func (a *BrowserScriptAdapter) Navigate(ctx context.Context, targetURL string) error {
	if a == nil || a.runtime == nil {
		return webmcp.ErrClosed
	}
	previous := a.runtime.Generation()
	execution, err := a.runtime.Execute(ctx, OperationRequest{Type: OperationNavigate, URL: targetURL})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.target = a.currentTargetLocked()
	handle := a.handle
	var session *BrowserScriptSession
	if handle != nil {
		handle.mu.Lock()
		session = handle.session
		handle.mu.Unlock()
	}
	a.mu.Unlock()
	if session == nil {
		return nil
	}
	target := a.runtime.Target()
	session.mu.Lock()
	session.context.URL = target.URL
	session.context.Origin = targetOrigin(target.URL)
	session.context.Generation = a.runtime.Generation()
	session.context.Ready = false
	session.mu.Unlock()
	if err := session.emit(webmcp.BrowserEvent{
		Type:               webmcp.EventPageNavigated,
		BrowserID:          webmcp.BrowserID(a.runtime.BrowserID()),
		TargetID:           webmcp.TargetID(target.ID),
		Generation:         a.runtime.Generation(),
		PreviousGeneration: previous,
		At:                 fixtureEventTime(a.runtime.LastExecution().MonotonicMS),
		Reason:             "fixture_navigation",
	}); err != nil {
		return err
	}
	for _, event := range execution.Events {
		if err := session.emitFixtureEvent(event); err != nil {
			return err
		}
	}
	return nil
}

// Disconnect closes the attached target without consuming an extra fixture
// operation. Scripts that need a protocol detach should use browser_disconnect
// followed by an explicit detach_target operation in their browser fixture.
func (a *BrowserScriptAdapter) Disconnect(ctx context.Context) error {
	if err := adapterContextError(ctx); err != nil {
		return err
	}
	if a == nil {
		return webmcp.ErrClosed
	}
	a.mu.Lock()
	handle := a.handle
	a.mu.Unlock()
	if handle == nil {
		return nil
	}
	return handle.Close()
}

func (a *BrowserScriptAdapter) currentTargetLocked() webmcp.Target {
	target := a.target
	if a.runtime != nil {
		runtimeTarget := a.runtime.Target()
		if runtimeTarget.ID != "" {
			target.URL = runtimeTarget.URL
			target.Title = runtimeTarget.Title
			target.WebSocketURL = runtimeTarget.WebSocketDebuggerURL
		}
		target.Generation = a.runtime.Generation()
	}
	target.BrowserID = a.candidate.ID
	target.Origin = targetOrigin(target.URL)
	target.Eligible = true
	return target
}

// BrowserScriptHandle is the single broker-facing handle for an adapter.
type BrowserScriptHandle struct {
	adapter   *BrowserScriptAdapter
	candidate webmcp.BrowserCandidate

	mu      sync.Mutex
	session *BrowserScriptSession
	closed  bool
}

func (h *BrowserScriptHandle) Candidate() webmcp.BrowserCandidate {
	if h == nil {
		return webmcp.BrowserCandidate{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return cloneFixtureCandidate(h.candidate)
}

func (h *BrowserScriptHandle) ListTargets(ctx context.Context) ([]webmcp.Target, error) {
	if err := adapterContextError(ctx); err != nil {
		return nil, err
	}
	if h == nil || h.adapter == nil {
		return nil, webmcp.ErrClosed
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, webmcp.ErrClosed
	}
	session := h.session
	h.mu.Unlock()
	h.adapter.mu.Lock()
	target := h.adapter.currentTargetLocked()
	h.adapter.mu.Unlock()
	if session != nil && !session.isClosed() {
		target.Attached = true
	}
	return []webmcp.Target{target}, nil
}

func (h *BrowserScriptHandle) Activate(ctx context.Context, targetID webmcp.TargetID) error {
	if err := adapterContextError(ctx); err != nil {
		return err
	}
	if h == nil || h.adapter == nil {
		return webmcp.ErrClosed
	}
	if targetID == "" || targetID != webmcp.TargetID(h.adapter.runtime.Target().ID) {
		return webmcp.ErrTargetNotFound
	}
	return nil
}

func (h *BrowserScriptHandle) Attach(ctx context.Context, targetID webmcp.TargetID, ownership webmcp.TargetOwnership) (webmcp.TargetSession, error) {
	if err := adapterContextError(ctx); err != nil {
		return nil, err
	}
	if h == nil || h.adapter == nil {
		return nil, webmcp.ErrClosed
	}
	if ownership == "" {
		ownership = webmcp.TargetOwnershipExternal
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, webmcp.ErrClosed
	}
	if targetID == "" || targetID != webmcp.TargetID(h.adapter.runtime.Target().ID) {
		h.mu.Unlock()
		return nil, webmcp.ErrTargetNotFound
	}
	if h.session != nil && !h.session.isClosed() {
		if h.session.Ownership() != ownership {
			h.mu.Unlock()
			return nil, errors.New("webmcp testkit: target already attached")
		}
		session := h.session
		h.mu.Unlock()
		return session, nil
	}
	h.adapter.mu.Lock()
	target := h.adapter.currentTargetLocked()
	h.adapter.mu.Unlock()
	session := newBrowserScriptSession(h, target, ownership)
	h.session = session
	h.mu.Unlock()
	if err := session.emit(webmcp.BrowserEvent{
		Type:       webmcp.EventTargetAttached,
		BrowserID:  target.BrowserID,
		TargetID:   target.ID,
		Generation: target.Generation,
		At:         fixtureEventTime(0),
		Reason:     "fixture_attach",
	}); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}

func (h *BrowserScriptHandle) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	session := h.session
	h.mu.Unlock()
	if session != nil {
		return session.Close()
	}
	return nil
}

// BrowserScriptSession adapts the synchronous fixture observations to one
// broker target session.
type BrowserScriptSession struct {
	handle    *BrowserScriptHandle
	runtime   *BrowserScriptRuntime
	target    webmcp.Target
	ownership webmcp.TargetOwnership

	mu      sync.Mutex
	context webmcp.PageContext
	events  chan webmcp.BrowserEvent
	done    chan struct{}
	closed  bool
	err     error
}

func newBrowserScriptSession(handle *BrowserScriptHandle, target webmcp.Target, ownership webmcp.TargetOwnership) *BrowserScriptSession {
	capacity := 32
	if handle != nil && handle.adapter != nil {
		capacity = countScriptEvents(handle.adapter.script()) + 8
		if capacity < 32 {
			capacity = 32
		}
	}
	page := webmcp.PageContext{
		Key:        webmcp.PageKey{BrowserID: target.BrowserID, TargetID: target.ID},
		Title:      target.Title,
		URL:        target.URL,
		Origin:     target.Origin,
		Generation: target.Generation,
		Connected:  true,
		Ready:      false,
	}
	return &BrowserScriptSession{
		handle:    handle,
		runtime:   handle.adapter.runtime,
		target:    target,
		ownership: ownership,
		context:   page,
		events:    make(chan webmcp.BrowserEvent, capacity),
		done:      make(chan struct{}),
	}
}

func (s *BrowserScriptSession) Context() webmcp.PageContext {
	if s == nil {
		return webmcp.PageContext{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.context
}

func (s *BrowserScriptSession) Ownership() webmcp.TargetOwnership {
	if s == nil {
		return ""
	}
	return s.ownership
}

func (s *BrowserScriptSession) EnableWebMCP(ctx context.Context) error {
	if s == nil || s.runtime == nil {
		return webmcp.ErrClosed
	}
	if err := adapterContextError(ctx); err != nil {
		return err
	}
	if s.isClosed() {
		return webmcp.ErrClosed
	}
	if operation, ok := s.runtime.NextExpectedOperationType(); ok && operation == OperationEnableLifecycle {
		if _, err := s.runtime.Execute(ctx, OperationRequest{Type: OperationEnableLifecycle}); err != nil {
			return err
		}
	}
	if operation, ok := s.runtime.NextExpectedOperationType(); !ok || operation != OperationEnableWebMCP {
		return fmt.Errorf("webmcp testkit: expected enable_webmcp operation")
	}
	execution, err := s.runtime.Execute(ctx, OperationRequest{Type: OperationEnableWebMCP})
	if err != nil {
		return err
	}
	emitted := false
	for _, event := range execution.Events {
		if event.Type == EmittedToolsAdded {
			emitted = true
		}
		if err := s.emitFixtureEvent(event); err != nil {
			return err
		}
	}
	if !emitted {
		// A zero-tool page is still ready. The empty catalog event wakes the
		// broker's bounded initial-catalog wait without inventing a tool.
		if err := s.emit(webmcp.BrowserEvent{
			Type:       webmcp.EventToolsAdded,
			BrowserID:  webmcp.BrowserID(s.runtime.BrowserID()),
			TargetID:   webmcp.TargetID(s.runtime.Target().ID),
			Generation: s.runtime.Generation(),
			At:         fixtureEventTime(execution.MonotonicMS),
		}); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.context.Ready = true
	s.mu.Unlock()
	return nil
}

func (s *BrowserScriptSession) Events() <-chan webmcp.BrowserEvent {
	if s == nil {
		closed := make(chan webmcp.BrowserEvent)
		close(closed)
		return closed
	}
	return s.events
}

func (s *BrowserScriptSession) InvokeWebMCP(ctx context.Context, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	return s.invoke(ctx, "", frameID, toolName, input)
}

// InvokeWebMCPWithID preserves the broker's public invocation identity when
// the fixture result does not supply a separate browser correlation ID.
func (s *BrowserScriptSession) InvokeWebMCPWithID(ctx context.Context, id webmcp.InvocationID, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	return s.invoke(ctx, id, frameID, toolName, input)
}

func (s *BrowserScriptSession) invoke(ctx context.Context, publicID webmcp.InvocationID, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	if s == nil || s.runtime == nil {
		return "", webmcp.ErrClosed
	}
	if err := adapterContextError(ctx); err != nil {
		return "", err
	}
	if s.isClosed() {
		return "", webmcp.ErrClosed
	}
	execution, err := s.runtime.Execute(ctx, OperationRequest{
		Type:     OperationInvokeTool,
		FrameID:  string(frameID),
		ToolName: toolName,
		Input:    cloneRaw(input),
	})
	if err != nil {
		return "", err
	}
	invocationID := webmcp.InvocationID(execution.InvocationID)
	if invocationID == "" {
		invocationID = publicID
	}
	if err := s.emit(webmcp.BrowserEvent{
		Type:         webmcp.EventToolInvoked,
		BrowserID:    webmcp.BrowserID(s.runtime.BrowserID()),
		TargetID:     webmcp.TargetID(s.runtime.Target().ID),
		FrameID:      frameID,
		ToolName:     toolName,
		InvocationID: invocationID,
		Input:        cloneRaw(input),
		Generation:   execution.Generation,
		At:           fixtureEventTime(execution.MonotonicMS),
	}); err != nil {
		return "", err
	}
	for _, event := range execution.Events {
		if err := s.emitFixtureEvent(event); err != nil {
			return "", err
		}
	}
	return invocationID, nil
}

func (s *BrowserScriptSession) CancelWebMCP(ctx context.Context, id webmcp.InvocationID) error {
	if s == nil || s.runtime == nil {
		return webmcp.ErrClosed
	}
	if err := adapterContextError(ctx); err != nil {
		return err
	}
	if s.isClosed() {
		return webmcp.ErrClosed
	}
	if err := s.runtime.CancelTool(ctx, string(id)); err != nil {
		return err
	}
	execution := s.runtime.LastExecution()
	for _, event := range execution.Events {
		if err := s.emitFixtureEvent(event); err != nil {
			return err
		}
	}
	return nil
}

func (s *BrowserScriptSession) Done() <-chan struct{} {
	if s == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return s.done
}

func (s *BrowserScriptSession) Err() error {
	if s == nil {
		return webmcp.ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *BrowserScriptSession) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		err := s.err
		s.mu.Unlock()
		return err
	}
	s.closed = true
	s.mu.Unlock()

	var closeErr error
	if operation, ok := s.runtime.NextExpectedOperationType(); ok {
		switch operation {
		case OperationCloseTarget:
			closeErr = s.runtime.CloseTarget(context.Background())
		case OperationDetachTarget:
			closeErr = s.runtime.DetachTarget(context.Background())
		}
	}
	s.mu.Lock()
	s.err = closeErr
	close(s.done)
	close(s.events)
	s.mu.Unlock()
	return closeErr
}

func (s *BrowserScriptSession) isClosed() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *BrowserScriptSession) emit(event webmcp.BrowserEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return webmcp.ErrClosed
	}
	if event.BrowserID == "" {
		event.BrowserID = s.context.Key.BrowserID
	}
	if event.TargetID == "" {
		event.TargetID = s.context.Key.TargetID
	}
	if event.Generation == 0 {
		event.Generation = s.context.Generation
	}
	select {
	case s.events <- cloneFixtureBrowserEvent(event):
		return nil
	default:
		return webmcp.ErrEventBufferFull
	}
}

func (s *BrowserScriptSession) emitFixtureEvent(event FixtureEvent) error {
	converted := webmcp.BrowserEvent{
		BrowserID:    webmcp.BrowserID(event.BrowserID),
		TargetID:     webmcp.TargetID(event.TargetID),
		Generation:   event.Generation,
		InvocationID: webmcp.InvocationID(event.InvocationID),
		Status:       event.Status,
		Output:       cloneRaw(event.Output),
		At:           fixtureEventTime(event.MonotonicMS),
	}
	switch event.Type {
	case EmittedToolsAdded:
		converted.Type = webmcp.EventToolsAdded
		converted.Tools = make([]webmcp.ToolDescriptor, 0, len(event.Tools))
		for _, tool := range event.Tools {
			converted.Tools = append(converted.Tools, fixtureToolDescriptor(tool))
		}
	case EmittedToolResponded:
		converted.Type = webmcp.EventToolResponded
	default:
		return fmt.Errorf("webmcp testkit: unsupported fixture event %q", event.Type)
	}
	return s.emit(converted)
}

func (a *BrowserScriptAdapter) script() BrowserScript {
	if a == nil || a.runtime == nil {
		return BrowserScript{}
	}
	return a.runtime.script
}

func fixtureToolDescriptor(tool ToolDescriptor) webmcp.ToolDescriptor {
	return webmcp.ToolDescriptor{
		Name:        tool.Name,
		Description: tool.Description,
		FrameID:     webmcp.FrameID(tool.FrameID),
		InputSchema: cloneRaw(tool.InputSchema),
		Annotations: webmcp.ToolAnnotations{Raw: cloneRaw(tool.Annotations)},
	}
}

func cloneFixtureCandidate(candidate webmcp.BrowserCandidate) webmcp.BrowserCandidate {
	candidate.Diagnostics = append([]webmcp.Diagnostic(nil), candidate.Diagnostics...)
	return candidate
}

func cloneFixtureBrowserEvent(event webmcp.BrowserEvent) webmcp.BrowserEvent {
	event.Tools = append([]webmcp.ToolDescriptor(nil), event.Tools...)
	for index := range event.Tools {
		event.Tools[index].InputSchema = cloneRaw(event.Tools[index].InputSchema)
		event.Tools[index].Annotations.Raw = cloneRaw(event.Tools[index].Annotations.Raw)
	}
	event.Input = cloneRaw(event.Input)
	event.Output = cloneRaw(event.Output)
	return event
}

func targetOrigin(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func fixtureEventTime(monotonicMS uint64) time.Time {
	return time.Unix(0, int64(monotonicMS)*int64(time.Millisecond)).UTC()
}

func adapterContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

var (
	_ webmcp.BrowserDiscoverer = (*BrowserScriptAdapter)(nil)
	_ webmcp.BrowserRuntime    = (*BrowserScriptAdapter)(nil)
	_ webmcp.BrowserHandle     = (*BrowserScriptHandle)(nil)
	_ webmcp.TargetSession     = (*BrowserScriptSession)(nil)
)
