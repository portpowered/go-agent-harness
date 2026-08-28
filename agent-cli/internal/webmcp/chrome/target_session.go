package chrome

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

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
	runAction     func(context.Context, ...chromedp.Action) error

	mu               sync.Mutex
	protocolTarget   *chromedp.Target
	protocolSession  cdpTarget.SessionID
	protocolTargetID cdpTarget.ID
	page             webmcp.PageContext
	ownership        webmcp.TargetOwnership
	closed           bool
	err              error
	closeErr         error

	protocolEvents  chan any
	events          chan webmcp.BrowserEvent
	done            chan struct{}
	stopRouter      chan struct{}
	routerDone      chan struct{}
	eventsMu        sync.Mutex
	lifecycleClosed bool
	finishMu        sync.Mutex
	finished        bool
	finishDone      chan struct{}
	stopOnce        sync.Once
	sequence        uint64
}

func newTargetSession(
	handle *handle,
	targetContext context.Context,
	cancelTarget context.CancelFunc,
	targetValue webmcp.Target,
	ownership webmcp.TargetOwnership,
) *targetSession {
	eventBuffer := handle.eventBuffer
	if eventBuffer <= 0 {
		eventBuffer = 1
	}
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
		protocolEvents: make(chan any, eventBuffer),
		events:         make(chan webmcp.BrowserEvent, eventBuffer),
		done:           make(chan struct{}),
		stopRouter:     make(chan struct{}),
		routerDone:     make(chan struct{}),
		finishDone:     make(chan struct{}),
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

// enqueueBrowserEvent receives browser-scoped target lifecycle events. The
// browser listener is shared by all target sessions, so conversion below
// filters by this session's protocol session/target identifiers.
func (s *targetSession) enqueueBrowserEvent(event any) {
	switch event.(type) {
	case *cdpTarget.EventDetachedFromTarget,
		cdpTarget.EventDetachedFromTarget,
		*cdpTarget.EventTargetDestroyed,
		cdpTarget.EventTargetDestroyed,
		*cdpTarget.EventTargetCrashed,
		cdpTarget.EventTargetCrashed:
		s.enqueueProtocolEvent(event)
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
				if converted.Type == webmcp.EventTargetDetached {
					s.finishFromProtocol(converted)
					return
				}
				s.publish(converted)
			}
		}
	}
}

func (s *targetSession) publish(event webmcp.BrowserEvent) {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	s.publishLocked(event, false)
}

func (s *targetSession) publishLocked(event webmcp.BrowserEvent, terminal bool) {
	if s.lifecycleClosed {
		return
	}
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
	case s.events <- cloneEvent(event):
	default:
		if terminal {
			// Preserve the terminal lifecycle signal without blocking the
			// protocol reader. A full bounded queue may discard its oldest
			// observation, but it must not hide detach/disconnect semantics.
			select {
			case <-s.events:
			default:
			}
			select {
			case s.events <- cloneEvent(event):
			default:
			}
			return
		}
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
		return "", classifyInvocationError(s, invocationID, "invoke", err)
	}
	if invocationID == "" {
		return "", classifyInvocationError(s, invocationID, "invoke", errors.New("browser returned an empty invocation ID"))
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
		return classifyCancellationError(s, string(invocationID), err)
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
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	targetContext := s.targetContext
	timeout := s.handle.commandTimeout
	s.mu.Unlock()
	if targetContext == nil {
		return errors.New("target context is unavailable")
	}

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
	runner := s.runAction
	if runner == nil {
		runner = chromedp.Run
	}
	err := runner(commandContext, action)
	close(watchDone)
	cancelCommand()
	return err
}

func validateObjectInput(input json.RawMessage) error {
	if len(bytes.TrimSpace(input)) == 0 {
		return invalidInputError("empty")
	}
	if len(input) > webmcp.DefaultMaxInputBytes {
		err := invalidInputError("too_large")
		if classified, ok := err.(*webmcp.ClassifiedError); ok {
			classified.Details["observed_bytes"] = len(input)
			classified.Details["max_bytes"] = webmcp.DefaultMaxInputBytes
		}
		return err
	}
	if !utf8.Valid(input) {
		return invalidInputError("invalid_utf8")
	}
	trimmed := bytes.TrimSpace(input)
	if !json.Valid(input) {
		return invalidInputError("malformed")
	}
	if trimmed[0] != '{' {
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
	if !s.beginFinish(true) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeErr
	}

	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	s.stopProtocolRouter()
	cleanupErr := s.cleanupTarget()
	s.mu.Lock()
	s.closeErr = cleanupErr
	s.mu.Unlock()
	s.closeLifecycle(webmcp.BrowserEvent{Type: webmcp.EventSessionClosed, Reason: "closed"})
	s.handle.unregister(s)
	s.completeFinish()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeErr
}

func (s *targetSession) abortOpen() error {
	if !s.beginFinish(true) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closeErr
	}

	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stopProtocolRouter()
	cleanupErr := s.cleanupTarget()
	s.mu.Lock()
	s.closeErr = cleanupErr
	s.mu.Unlock()
	s.closeLifecycle(webmcp.BrowserEvent{Type: webmcp.EventSessionClosed, Reason: "attach_failed"})
	s.handle.unregister(s)
	s.completeFinish()
	return cleanupErr
}

func (s *targetSession) beginFinish(waitForExisting bool) bool {
	s.finishMu.Lock()
	if s.finished {
		done := s.finishDone
		s.finishMu.Unlock()
		if waitForExisting {
			<-done
		}
		return false
	}
	s.finished = true
	s.finishMu.Unlock()
	return true
}

func (s *targetSession) completeFinish() {
	s.finishMu.Lock()
	defer s.finishMu.Unlock()
	close(s.finishDone)
}

// cleanupTarget is the ownership boundary for an attached target. The
// explicit detach must complete (or fail) before the chromedp target
// reference is cleared and the target context is canceled. chromedp v0.16.0
// otherwise follows cancellation with Target.closeTarget.
func (s *targetSession) cleanupTarget() error {
	s.mu.Lock()
	targetValue := s.protocolTarget
	sessionID := s.protocolSession
	targetID := s.protocolTargetID
	ownership := s.ownership
	handle := s.handle
	s.page.Connected = false
	s.mu.Unlock()

	if targetValue == nil {
		if data := chromedp.FromContext(s.targetContext); data != nil {
			targetValue = data.Target
		}
	}
	if targetValue != nil {
		if sessionID == "" {
			sessionID = targetValue.SessionID
		}
		if targetID == "" {
			targetID = targetValue.TargetID
		}
	}

	executor := handle.executor()
	var joined error
	if sessionID != "" {
		if executor == nil {
			joined = errors.Join(joined, classifyTargetCleanupError(s, "detach", errors.New("browser connection is unavailable")))
		} else {
			detachContext, cancel := context.WithTimeout(context.Background(), handle.timeout())
			err := cdpTarget.DetachFromTarget().WithSessionID(sessionID).Do(cdp.WithExecutor(detachContext, executor))
			cancel()
			if err != nil {
				joined = errors.Join(joined, classifyTargetCleanupError(s, "detach", err))
			}
		}
	}
	if ownership == webmcp.TargetOwnershipHarnessOwned && targetID != "" {
		if executor == nil {
			joined = errors.Join(joined, classifyTargetCleanupError(s, "close_target", errors.New("browser connection is unavailable")))
		} else {
			closeContext, cancel := context.WithTimeout(context.Background(), handle.timeout())
			err := cdpTarget.CloseTarget(targetID).Do(cdp.WithExecutor(closeContext, executor))
			cancel()
			if err != nil {
				joined = errors.Join(joined, classifyTargetCleanupError(s, "close_target", err))
			}
		}
	}

	// Clear both the IDs and the Context.Target reference only after protocol
	// cleanup. This prevents chromedp's cancellation goroutine from observing
	// an attached target and issuing its own Target.closeTarget.
	s.clearClientTarget(targetValue)
	if s.cancelTarget != nil {
		s.cancelTarget()
	}
	return joined
}

func (s *targetSession) clearClientTarget(targetValue *chromedp.Target) {
	if data := chromedp.FromContext(s.targetContext); data != nil {
		if targetValue == nil || data.Target == targetValue {
			data.Target = nil
		}
	}
	if targetValue != nil {
		targetValue.SessionID = ""
		targetValue.TargetID = ""
	}
}

func (s *targetSession) transportLost() {
	if !s.beginFinish(true) {
		return
	}

	s.mu.Lock()
	s.closed = true
	targetValue := s.protocolTarget
	s.page.Connected = false
	s.err = webmcp.NewClassifiedError(webmcp.ErrorBrowserDisconnected, webmcp.DefaultErrorMessage(webmcp.ErrorBrowserDisconnected), map[string]any{
		"browser_id":         s.page.Key.BrowserID,
		"target_id":          s.page.Key.TargetID,
		"phase":              "connected",
		"reconnect_required": true,
	})
	s.mu.Unlock()
	s.stopProtocolRouter()
	s.clearClientTarget(targetValue)
	if s.cancelTarget != nil {
		s.cancelTarget()
	}
	s.closeLifecycle(webmcp.BrowserEvent{
		Type:      webmcp.EventBrowserDisconnected,
		ErrorCode: string(webmcp.ErrorBrowserDisconnected),
		Reason:    "browser_disconnected",
	})
	s.handle.unregister(s)
	s.completeFinish()
}

func (s *targetSession) finishFromProtocol(event webmcp.BrowserEvent) {
	if !s.beginFinish(false) {
		return
	}

	s.mu.Lock()
	s.closed = true
	targetValue := s.protocolTarget
	s.page.Connected = false
	s.err = webmcp.NewClassifiedError(webmcp.ErrorTargetDetached, webmcp.DefaultErrorMessage(webmcp.ErrorTargetDetached), map[string]any{
		"browser_id": s.page.Key.BrowserID,
		"target_id":  s.page.Key.TargetID,
		"generation": s.page.Generation,
		"reason":     event.Reason,
	})
	s.mu.Unlock()
	s.clearClientTarget(targetValue)
	if s.cancelTarget != nil {
		s.cancelTarget()
	}
	event.ErrorCode = string(webmcp.ErrorTargetDetached)
	s.closeLifecycle(event)
	s.handle.unregister(s)
	s.completeFinish()
}

func (s *targetSession) stopProtocolRouter() {
	s.stopOnce.Do(func() { close(s.stopRouter) })
	<-s.routerDone
}

func (s *targetSession) closeLifecycle(event webmcp.BrowserEvent) {
	s.eventsMu.Lock()
	defer s.eventsMu.Unlock()
	if s.lifecycleClosed {
		return
	}
	if event.Type != "" {
		s.publishLocked(event, true)
	}
	s.lifecycleClosed = true
	close(s.done)
	close(s.events)
}

func cloneEvent(event webmcp.BrowserEvent) webmcp.BrowserEvent {
	event.Input = cloneBytes(event.Input)
	event.Output = cloneBytes(event.Output)
	event.RemovedToolNames = append([]string(nil), event.RemovedToolNames...)
	event.Tools = append([]webmcp.ToolDescriptor(nil), event.Tools...)
	for i := range event.Tools {
		event.Tools[i].InputSchema = cloneBytes(event.Tools[i].InputSchema)
		event.Tools[i].Annotations.Raw = cloneBytes(event.Tools[i].Annotations.Raw)
		if event.Tools[i].Annotations.ReadOnly != nil {
			value := *event.Tools[i].Annotations.ReadOnly
			event.Tools[i].Annotations.ReadOnly = &value
		}
		if event.Tools[i].Annotations.UntrustedContent != nil {
			value := *event.Tools[i].Annotations.UntrustedContent
			event.Tools[i].Annotations.UntrustedContent = &value
		}
		if event.Tools[i].Annotations.AutoSubmit != nil {
			value := *event.Tools[i].Annotations.AutoSubmit
			event.Tools[i].Annotations.AutoSubmit = &value
		}
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
