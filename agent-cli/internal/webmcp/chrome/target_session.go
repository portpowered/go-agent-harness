package chrome

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	cdpTarget "github.com/chromedp/cdproto/target"
	cdpWebMCP "github.com/chromedp/cdproto/webmcp"
	"github.com/chromedp/chromedp"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
)

type targetSession struct {
	handle        *handle
	targetContext context.Context
	cancelTarget  context.CancelFunc

	mu               sync.Mutex
	protocolTarget   *chromedp.Target
	protocolSession  cdpTarget.SessionID
	protocolTargetID cdpTarget.ID
	page             webmcp.PageContext
	ownership        webmcp.TargetOwnership
	closed           bool
	err              error
	closeErr         error

	protocolEvents chan any
	events         chan webmcp.BrowserEvent
	done           chan struct{}
	stopRouter     chan struct{}
	routerDone     chan struct{}
	stopOnce       sync.Once
	lifecycleOnce  sync.Once
	closeOnce      sync.Once
	sequence       uint64
}

func newTargetSession(
	handle *handle,
	targetContext context.Context,
	cancelTarget context.CancelFunc,
	targetValue webmcp.Target,
	ownership webmcp.TargetOwnership,
) *targetSession {
	session := &targetSession{
		handle:        handle,
		targetContext: targetContext,
		cancelTarget:  cancelTarget,
		ownership:     ownership,
		page: webmcp.PageContext{
			Key:        webmcp.PageKey{BrowserID: targetValue.BrowserID, TargetID: targetValue.ID},
			Title:      targetValue.Title,
			URL:        targetValue.URL,
			Origin:     targetValue.Origin,
			Generation: 1,
			Connected:  false,
			Ready:      false,
			SelectedAt: time.Now(),
		},
		protocolEvents: make(chan any, handle.eventBuffer),
		events:         make(chan webmcp.BrowserEvent, handle.eventBuffer),
		done:           make(chan struct{}),
		stopRouter:     make(chan struct{}),
		routerDone:     make(chan struct{}),
	}
	go session.routeProtocolEvents()
	return session
}

func (s *targetSession) setProtocolTarget(targetValue *chromedp.Target) {
	s.mu.Lock()
	s.protocolTarget = targetValue
	if targetValue != nil {
		s.protocolSession = targetValue.SessionID
		s.protocolTargetID = targetValue.TargetID
		s.page.Connected = true
	}
	s.mu.Unlock()
}

func (s *targetSession) publishAttached() {
	s.publish(webmcp.BrowserEvent{Type: webmcp.EventTargetAttached})
}

func (s *targetSession) enqueueProtocolEvent(event any) {
	if event == nil {
		return
	}
	select {
	case <-s.stopRouter:
		return
	case s.protocolEvents <- event:
	default:
		s.recordError(webmcp.ErrEventBufferFull)
	}
}

func (s *targetSession) routeProtocolEvents() {
	defer close(s.routerDone)
	for {
		select {
		case <-s.stopRouter:
			return
		case event := <-s.protocolEvents:
			if converted, ok := s.convertProtocolEvent(event); ok {
				s.publish(converted)
			}
		}
	}
}

func (s *targetSession) publish(event webmcp.BrowserEvent) {
	s.mu.Lock()
	if event.Version == "" {
		event.Version = webmcp.BrowserEventsVersion
	}
	if event.BrowserID == "" {
		event.BrowserID = s.page.Key.BrowserID
	}
	if event.TargetID == "" {
		event.TargetID = s.page.Key.TargetID
	}
	if event.Generation == 0 {
		event.Generation = s.page.Generation
	}
	if event.At.IsZero() {
		event.At = time.Now()
	}
	s.sequence++
	event.Sequence = s.sequence
	s.mu.Unlock()

	select {
	case <-s.done:
		return
	case s.events <- cloneEvent(event):
	default:
		s.recordError(webmcp.ErrEventBufferFull)
	}
}

func (s *targetSession) recordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

func (s *targetSession) Context() webmcp.PageContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.page
}

func (s *targetSession) Ownership() webmcp.TargetOwnership {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownership
}

func (s *targetSession) EnableWebMCP(ctx context.Context) error {
	err := s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpWebMCP.Enable().Do(ctx)
	}))
	if err != nil {
		return classifySessionError(s, webmcp.ErrorUnsupportedWebMCP, "enable", err)
	}
	s.mu.Lock()
	s.page.Ready = true
	s.mu.Unlock()
	return nil
}

func (s *targetSession) Events() <-chan webmcp.BrowserEvent {
	return s.events
}

func (s *targetSession) InvokeWebMCP(ctx context.Context, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	if err := validateObjectInput(input); err != nil {
		return "", err
	}
	if frameID == "" || toolName == "" {
		return "", webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, webmcp.DefaultErrorMessage(webmcp.ErrorInvalidToolInput), map[string]any{
			"issues": []map[string]string{{"path": "/tool", "code": "required"}},
		})
	}

	var invocationID string
	err := s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		invocationID, err = cdpWebMCP.InvokeTool(cdp.FrameID(frameID), toolName, jsontext.Value(bytes.Clone(input))).Do(ctx)
		return err
	}))
	if err != nil {
		return "", classifySessionError(s, webmcp.ErrorInvocationFailed, "invoke", err)
	}
	if invocationID == "" {
		return "", classifySessionError(s, webmcp.ErrorInvocationFailed, "invoke", errors.New("browser returned an empty invocation ID"))
	}
	return webmcp.InvocationID(invocationID), nil
}

func (s *targetSession) CancelWebMCP(ctx context.Context, invocationID webmcp.InvocationID) error {
	if invocationID == "" {
		return webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, webmcp.DefaultErrorMessage(webmcp.ErrorInvalidToolInput), map[string]any{
			"issues": []map[string]string{{"path": "/invocation_id", "code": "required"}},
		})
	}
	err := s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdpWebMCP.CancelInvocation(string(invocationID)).Do(ctx)
	}))
	if err != nil {
		return classifySessionError(s, webmcp.ErrorInvocationCanceled, "cancel", err)
	}
	return nil
}

func (s *targetSession) Done() <-chan struct{} {
	return s.done
}

func (s *targetSession) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *targetSession) run(ctx context.Context, action chromedp.Action) error {
	if ctx == nil {
		return context.Canceled
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	targetContext := s.targetContext
	timeout := s.handle.commandTimeout
	s.mu.Unlock()

	commandContext, cancelCommand := context.WithCancel(targetContext)
	if timeout > 0 {
		var cancelTimeout context.CancelFunc
		commandContext, cancelTimeout = context.WithTimeout(commandContext, timeout)
		defer cancelTimeout()
	}
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cancelCommand()
		case <-watchDone:
		}
	}()
	err := chromedp.Run(commandContext, action)
	close(watchDone)
	cancelCommand()
	return err
}

func validateObjectInput(input json.RawMessage) error {
	if len(bytes.TrimSpace(input)) == 0 {
		return invalidInputError("empty")
	}
	trimmed := bytes.TrimSpace(input)
	if trimmed[0] != '{' || !json.Valid(input) {
		return invalidInputError("object_required")
	}
	return nil
}

func invalidInputError(code string) error {
	return webmcp.NewClassifiedError(webmcp.ErrorInvalidToolInput, webmcp.DefaultErrorMessage(webmcp.ErrorInvalidToolInput), map[string]any{
		"input_schema": map[string]any{"type": "object"},
		"issues":       []map[string]string{{"path": "/input_json", "code": code}},
	})
}

func (s *targetSession) Close() error {
	s.closeOnce.Do(func() {
		var joined error
		s.mu.Lock()
		s.closed = true
		targetValue := s.protocolTarget
		sessionID := s.protocolSession
		targetID := s.protocolTargetID
		ownership := s.ownership
		handle := s.handle
		s.mu.Unlock()

		s.stopProtocolRouter()
		if targetValue != nil && sessionID != "" && handle.browser != nil {
			detachContext, cancel := context.WithTimeout(context.Background(), handle.commandTimeout)
			joined = errors.Join(joined, cdpTarget.DetachFromTarget().WithSessionID(sessionID).Do(cdp.WithExecutor(detachContext, handle.browser)))
			cancel()
		}
		if ownership == webmcp.TargetOwnershipHarnessOwned && targetID != "" && handle.browser != nil {
			closeContext, cancel := context.WithTimeout(context.Background(), handle.commandTimeout)
			joined = errors.Join(joined, cdpTarget.CloseTarget(targetID).Do(cdp.WithExecutor(closeContext, handle.browser)))
			cancel()
		}

		// chromedp's cancellation path closes an attached target. Scrub both
		// identifiers only after the explicit detach (and optional owner close)
		// so external sessions can never fall through to CloseTarget.
		if targetValue != nil {
			targetValue.SessionID = ""
			targetValue.TargetID = ""
		}
		if s.cancelTarget != nil {
			s.cancelTarget()
		}
		s.mu.Lock()
		s.closeErr = joined
		s.mu.Unlock()
		s.closeLifecycle(webmcp.BrowserEvent{Type: webmcp.EventSessionClosed, Reason: "closed"})
		handle.unregister(s)
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeErr
}

func (s *targetSession) abortOpen() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		targetValue := s.protocolTarget
		s.mu.Unlock()
		s.stopProtocolRouter()
		if targetValue != nil {
			targetValue.SessionID = ""
			targetValue.TargetID = ""
		}
		if s.cancelTarget != nil {
			s.cancelTarget()
		}
		s.closeLifecycle(webmcp.BrowserEvent{Type: webmcp.EventSessionClosed, Reason: "attach_failed"})
	})
}

func (s *targetSession) transportLost() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		targetValue := s.protocolTarget
		s.err = webmcp.NewClassifiedError(webmcp.ErrorBrowserDisconnected, webmcp.DefaultErrorMessage(webmcp.ErrorBrowserDisconnected), map[string]any{
			"browser_id":         s.page.Key.BrowserID,
			"target_id":          s.page.Key.TargetID,
			"phase":              "connected",
			"reconnect_required": true,
		})
		s.mu.Unlock()
		s.stopProtocolRouter()
		if targetValue != nil {
			targetValue.SessionID = ""
			targetValue.TargetID = ""
		}
		if s.cancelTarget != nil {
			s.cancelTarget()
		}
		s.closeLifecycle(webmcp.BrowserEvent{Type: webmcp.EventBrowserDisconnected, Reason: "browser_disconnected"})
		s.handle.unregister(s)
	})
}

func (s *targetSession) stopProtocolRouter() {
	s.stopOnce.Do(func() { close(s.stopRouter) })
	<-s.routerDone
}

func (s *targetSession) closeLifecycle(event webmcp.BrowserEvent) {
	s.lifecycleOnce.Do(func() {
		if event.Type != "" {
			s.publish(event)
		}
		close(s.done)
		close(s.events)
	})
}

func cloneEvent(event webmcp.BrowserEvent) webmcp.BrowserEvent {
	event.Input = cloneBytes(event.Input)
	event.Output = cloneBytes(event.Output)
	event.RemovedToolNames = append([]string(nil), event.RemovedToolNames...)
	event.Tools = append([]webmcp.ToolDescriptor(nil), event.Tools...)
	for i := range event.Tools {
		event.Tools[i].InputSchema = cloneBytes(event.Tools[i].InputSchema)
		event.Tools[i].Annotations.Raw = cloneBytes(event.Tools[i].Annotations.Raw)
	}
	return event
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return bytes.Clone(value)
}

var _ webmcp.TargetSession = (*targetSession)(nil)
