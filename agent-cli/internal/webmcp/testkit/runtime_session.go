package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"sync"
	"sync/atomic"
)

type ScriptedTargetSession struct {
	handle    *ScriptedBrowserHandle
	runtime   *ScriptedBrowserRuntime
	entry     *scriptedTargetEntry
	target    webmcp.Target
	mu        sync.Mutex
	context   webmcp.PageContext
	ownership webmcp.TargetOwnership
	options   ScriptedTargetSessionOptions

	events          chan webmcp.BrowserEvent
	done            chan struct{}
	change          chan struct{}
	enableChanges   chan struct{}
	terminalChanges chan struct{}

	closed              bool
	closedState         atomic.Bool
	closeErr            error
	closeResult         error
	removeTargetOnClose bool
	blocked             bool
	enableBlocked       bool
	sequence            uint64
	tools               map[string]webmcp.ToolDescriptor
	invokes             map[webmcp.InvocationID]*InvocationRecord
	order               []webmcp.InvocationID
	observed            map[webmcp.InvocationID]bool
	terminalObserved    map[webmcp.InvocationID]TerminalObservation
	terminalHistory     []TerminalObservation
}

func newScriptedTargetSession(handle *ScriptedBrowserHandle, entry *scriptedTargetEntry, target webmcp.Target, page webmcp.PageContext, ownership webmcp.TargetOwnership, options ScriptedTargetSessionOptions) *ScriptedTargetSession {
	if options.EventBuffer <= 0 {
		options.EventBuffer = 64
	}
	if options.IDs == nil {
		options.IDs = handle.runtime.ids
	}
	if options.Clock == nil {
		options.Clock = handle.runtime.clock
	}
	if options.AcknowledgeCancellation == nil {
		options.AcknowledgeCancellation = boolPointer(true)
	}
	if options.EmitCancellationResponse == nil {
		options.EmitCancellationResponse = boolPointer(true)
	}
	if options.AutoResponseStatus == "" {
		options.AutoResponseStatus = "Completed"
	}
	session := &ScriptedTargetSession{
		handle:           handle,
		runtime:          handle.runtime,
		entry:            entry,
		target:           cloneTarget(target),
		context:          page,
		ownership:        ownership,
		options:          options,
		events:           make(chan webmcp.BrowserEvent, options.EventBuffer),
		done:             make(chan struct{}),
		change:           make(chan struct{}),
		enableChanges:    make(chan struct{}),
		terminalChanges:  make(chan struct{}),
		tools:            make(map[string]webmcp.ToolDescriptor),
		invokes:          make(map[webmcp.InvocationID]*InvocationRecord),
		observed:         make(map[webmcp.InvocationID]bool),
		enableBlocked:    options.BlockEnable,
		terminalObserved: make(map[webmcp.InvocationID]TerminalObservation),
	}
	for _, tool := range options.InitialCatalog {
		session.tools[toolKey(tool.FrameID, tool.Name)] = cloneTool(tool)
	}
	return session
}

func (s *ScriptedTargetSession) Context() webmcp.PageContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.context
}

func (s *ScriptedTargetSession) Target() webmcp.Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneTarget(s.target)
}

func (s *ScriptedTargetSession) Ownership() webmcp.TargetOwnership {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownership
}

func (s *ScriptedTargetSession) Events() <-chan webmcp.BrowserEvent { return s.events }

func (s *ScriptedTargetSession) Done() <-chan struct{} { return s.done }

func (s *ScriptedTargetSession) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeErr
}

func (s *ScriptedTargetSession) EnableWebMCP(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	err := s.options.EnableError
	blocked := s.enableBlocked
	enableChanges := s.enableChanges
	done := s.done
	generation := s.context.Generation
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.runtime.record(Operation{Kind: OperationEnableWebMCP, BrowserID: s.target.BrowserID, TargetID: s.target.ID, Generation: generation})
	if blocked {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-enableChanges:
		case <-done:
			err := s.Err()
			if err != nil {
				return err
			}
			return webmcp.ErrClosed
		}
	}
	s.mu.Lock()
	if s.closed {
		err := s.closeErr
		s.mu.Unlock()
		if err != nil {
			return err
		}
		return webmcp.ErrClosed
	}
	s.mu.Unlock()

	events := append([]webmcp.BrowserEvent(nil), s.options.EnableEvents...)
	if len(events) == 0 && len(s.options.InitialCatalog) > 0 {
		events = append(events, webmcp.BrowserEvent{Type: webmcp.EventToolsAdded, Tools: cloneTools(s.options.InitialCatalog)})
	}
	for _, event := range events {
		if err := s.Emit(event); err != nil {
			return err
		}
	}
	s.runtime.record(Operation{Kind: OperationEnableAcknowledged, BrowserID: s.target.BrowserID, TargetID: s.target.ID, Generation: generation})
	return nil
}

func (s *ScriptedTargetSession) InvokeWebMCP(ctx context.Context, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	s.mu.Lock()
	id, err := s.options.IDs.NewInvocationID()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return s.invokeWebMCPWithID(ctx, id, frameID, toolName, input)
}

// InvokeWebMCPWithID is an optional deterministic-session seam used by the
// broker tests. Real adapters can continue to allocate their protocol IDs in
// InvokeWebMCP; the fake accepts the broker's public ID so records prove the
// two correlation surfaces agree.
func (s *ScriptedTargetSession) InvokeWebMCPWithID(ctx context.Context, id webmcp.InvocationID, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	return s.invokeWebMCPWithID(ctx, id, frameID, toolName, input)
}

func (s *ScriptedTargetSession) invokeWebMCPWithID(ctx context.Context, id webmcp.InvocationID, frameID webmcp.FrameID, toolName string, input json.RawMessage) (webmcp.InvocationID, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("webmcp testkit: invocation ID is empty")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", webmcp.ErrClosed
	}
	if s.options.InvokeError != nil {
		err := s.options.InvokeError
		s.mu.Unlock()
		return "", err
	}
	if _, exists := s.invokes[id]; exists {
		s.mu.Unlock()
		return "", fmt.Errorf("webmcp testkit: invocation %q already exists", id)
	}
	record := &InvocationRecord{
		ID:         id,
		BrowserID:  s.target.BrowserID,
		TargetID:   s.target.ID,
		Generation: s.context.Generation,
		FrameID:    frameID,
		ToolName:   toolName,
		Input:      cloneBytes(input),
		State:      webmcp.InvocationDispatched,
	}
	s.invokes[id] = record
	s.order = append(s.order, id)
	s.notifyLocked()
	blocked := s.blocked
	autoRespond := s.options.AutoRespond && !blocked
	autoStatus := s.options.AutoResponseStatus
	autoOutput := cloneBytes(s.options.AutoResponseOutput)
	generation := record.Generation
	s.mu.Unlock()

	s.runtime.record(Operation{
		Kind:         OperationInvoke,
		BrowserID:    s.target.BrowserID,
		TargetID:     s.target.ID,
		Generation:   generation,
		FrameID:      frameID,
		ToolName:     toolName,
		InvocationID: id,
		Input:        input,
		Arguments:    input,
	})
	if err := s.emit(webmcp.BrowserEvent{Type: webmcp.EventToolInvoked, FrameID: frameID, ToolName: toolName, InvocationID: id, Input: input, Generation: generation}); err != nil {
		return id, err
	}
	if autoRespond {
		if err := s.EmitToolResponse(id, autoStatus, autoOutput); err != nil {
			return id, err
		}
	}
	return id, nil
}

func (s *ScriptedTargetSession) CancelWebMCP(ctx context.Context, id webmcp.InvocationID) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return webmcp.ErrClosed
	}
	cancelErr := s.options.CancelError
	acknowledged := *s.options.AcknowledgeCancellation
	emitResponse := *s.options.EmitCancellationResponse && acknowledged
	s.mu.Unlock()
	if cancelErr != nil {
		return cancelErr
	}
	owner, record := s.findInvocation(id)
	if owner == nil || record == nil {
		return fmt.Errorf("%w: %s", webmcp.ErrInvocationNotFound, id)
	}
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return webmcp.ErrClosed
	}
	record.CancelRequested = true
	record.CancellationAcknowledged = acknowledged
	record.Terminal = acknowledged
	record.State = webmcp.InvocationCanceled
	generation := record.Generation
	owner.notifyLocked()
	owner.mu.Unlock()

	s.runtime.record(Operation{
		Kind:                     OperationCancel,
		BrowserID:                s.target.BrowserID,
		TargetID:                 s.target.ID,
		Generation:               generation,
		InvocationID:             id,
		Reason:                   "cancel",
		CancellationAcknowledged: acknowledged,
	})
	if !acknowledged {
		return ErrCancellationNotAcknowledged
	}
	if emitResponse {
		return owner.EmitToolResponse(id, "Canceled", nil)
	}
	if acknowledged {
		s.markTerminalObserved(id, PublishedEvent{})
	}
	return nil
}

// findInvocation models the browser-owned invocation registry. A direct
// cancellation request may arrive through a different target session than
// the one that dispatched the call, while local invocation APIs remain
// scoped to each client session.
func (s *ScriptedTargetSession) findInvocation(id webmcp.InvocationID) (*ScriptedTargetSession, *InvocationRecord) {
	s.mu.Lock()
	if record := s.invokes[id]; record != nil {
		s.mu.Unlock()
		return s, record
	}
	entry := s.entry
	s.mu.Unlock()
	if entry == nil {
		return nil, nil
	}
	entry.mu.Lock()
	sessions := make([]*ScriptedTargetSession, 0, len(entry.sessions))
	for session := range entry.sessions {
		sessions = append(sessions, session)
	}
	entry.mu.Unlock()
	for _, session := range sessions {
		if session == s {
			continue
		}
		session.mu.Lock()
		record := session.invokes[id]
		session.mu.Unlock()
		if record != nil {
			return session, record
		}
	}
	return nil, nil
}

// BlockInvocations prevents configured automatic responses. It does not
// prevent admission or ID assignment, which lets tests exercise cancellation
// and late-result races against a known invocation ID.
func (s *ScriptedTargetSession) BlockInvocations() {
	s.mu.Lock()
	s.blocked = true
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) UnblockInvocations() {
	s.mu.Lock()
	s.blocked = false
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) SetAutoResponse(status string, output json.RawMessage) {
	s.mu.Lock()
	s.options.AutoRespond = true
	if status != "" {
		s.options.AutoResponseStatus = status
	}
	s.options.AutoResponseOutput = cloneBytes(output)
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) SetCancellationAcknowledgement(acknowledged bool) {
	s.mu.Lock()
	s.options.AcknowledgeCancellation = boolPointer(acknowledged)
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) SetCancellationResponse(enabled bool) {
	s.mu.Lock()
	s.options.EmitCancellationResponse = boolPointer(enabled)
	s.mu.Unlock()
}

func (s *ScriptedTargetSession) Close() error {
	s.mu.Lock()
	if s.closed {
		err := s.closeResult
		s.mu.Unlock()
		return err
	}
	ownership := s.ownership
	s.mu.Unlock()
	if ownership == webmcp.TargetOwnershipHarnessOwned {
		return s.terminate(webmcp.EventTargetDetached, "close", webmcp.ErrClosed)
	}
	return s.terminate(webmcp.EventTargetDetached, "detach", webmcp.ErrClosed)
}

func (s *ScriptedTargetSession) terminate(eventType webmcp.BrowserEventType, reason string, terminalErr error) error {
	return s.terminateWithOptions(eventType, reason, terminalErr, eventType == webmcp.EventTargetDetached && s.Ownership() == webmcp.TargetOwnershipHarnessOwned, false)
}

func (s *ScriptedTargetSession) emit(event webmcp.BrowserEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return webmcp.ErrClosed
	}
	return s.emitLocked(event)
}

func (s *ScriptedTargetSession) emitLocked(event webmcp.BrowserEvent) error {
	_, err := s.emitPublishedLocked(event, false)
	return err
}

// emitPublishedLocked publishes a shared browser event, or a local terminal
// lifecycle event when terminal is true. The variadic form keeps the helper
// compatible with older testkit callers that did not need the terminal flag.
func (s *ScriptedTargetSession) emitLocal(event webmcp.BrowserEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return webmcp.ErrClosed
	}
	_, err := s.emitLocalPublishedLocked(event, false)
	return err
}

func (entry *scriptedTargetEntry) broadcast(event webmcp.BrowserEvent) error {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	var result error
	for session := range entry.sessions {
		select {
		case session.events <- cloneEvent(event):
		default:
			result = webmcp.ErrEventBufferFull
		}
	}
	return result
}

func (s *ScriptedTargetSession) notifyLocked() {
	close(s.change)
	s.change = make(chan struct{})
}

func (s *ScriptedTargetSession) isClosed() bool {
	return s.closedState.Load()
}
