package webmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	maxInvocationIDMintAttempts = 64
	maxEarlyTerminalResults     = 64
	maxTerminalResults          = 64
)

// brokerInvocation is the broker's private lease for one admitted call. The
// broker owns the public ID from admission onward; browserID is the separate
// protocol correlation ID returned by the target session. The lease stays
// pinned to its session and is never resolved through the current selection.
type brokerInvocation struct {
	selected *brokerSession
	ctx      context.Context

	invocation Invocation

	// browserID is the protocol invocation ID returned by the target session.
	// It is kept separately so browser events can be reconciled without exposing
	// a provider-owned identifier as the broker's public invocation ID.
	browserID     InvocationID
	timer         Timer
	cancelSent    bool
	cancelDone    chan struct{}
	cancelPending bool

	dispatchDone chan invocationDispatch
	terminal     chan struct{}
	finalResult  InvokeResult
	terminalized bool
	reported     bool
	admissionSeq uint64
}

type invocationDispatch struct {
	result InvokeResult
	err    error
}

type terminalInvocation struct {
	invocation Invocation
	result     InvokeResult
}

type targetCancellation struct {
	session TargetSession
	id      InvocationID
	done    chan struct{}
}

// terminalObservation is deliberately smaller than BrowserEvent. In
// particular, an early response whose output is too large is represented by
// its byte count rather than retained in the broker buffer.
type terminalObservation struct {
	status        string
	output        json.RawMessage
	outputBytes   int
	outputPresent bool
	errorCode     string
	at            time.Time
}

// targetSessionInvokerWithID is an optional test/replay seam. Production
// adapters may keep generating their own protocol ID; the broker then maps it
// to the public ID. Deterministic sessions can accept the broker ID directly,
// making operation records demonstrate end-to-end correlation.
type targetSessionInvokerWithID interface {
	InvokeWebMCPWithID(context.Context, InvocationID, FrameID, string, json.RawMessage) (InvocationID, error)
}

func invokeWebMCP(ctx context.Context, session TargetSession, publicID InvocationID, frameID FrameID, toolName string, input json.RawMessage) (InvocationID, error) {
	if invoker, ok := session.(targetSessionInvokerWithID); ok {
		return invoker.InvokeWebMCPWithID(ctx, publicID, frameID, toolName, input)
	}
	return session.InvokeWebMCP(ctx, frameID, toolName, input)
}

func (b *StatefulBroker) admitInvocation(ctx context.Context, request InvokeRequest) (InvokeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return InvokeResult{}, err
	}
	if b == nil {
		return InvokeResult{}, ErrClosed
	}
	if err := validateToolRefSyntax(request.ToolRef); err != nil {
		return InvokeResult{}, invalidToolRefError(request.ToolRef, err)
	}

	// Catalog events are asynchronous. Flush them before resolving the
	// descriptor, then repeat the state check while admitting the queue item.
	b.flushSelected()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return InvokeResult{}, ErrClosed
	}
	selected := b.selected
	if selected == nil || !selected.active || !selected.context.Connected {
		err := staleSelectionForSession(selected, "selection_not_connected")
		b.mu.Unlock()
		return InvokeResult{}, err
	}
	record, ok := b.refs[request.ToolRef]
	if !ok || !refCurrentLocked(selected, record) {
		generation := selected.context.Generation
		err := staleToolRefError(request.ToolRef, generation)
		b.mu.Unlock()
		return InvokeResult{}, err
	}
	descriptor := cloneToolDescriptor(record.descriptor)
	maxInputBytes := b.maxInputBytes
	invocationTimeout := b.invocationTimeout
	b.mu.Unlock()

	if issues := validatePageToolInput(request.Input, descriptor.InputSchema, maxInputBytes); len(issues) > 0 {
		return InvokeResult{}, invalidPageInputError(request.ToolRef, descriptor, issues)
	}
	if err := contextError(ctx); err != nil {
		return InvokeResult{}, err
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return InvokeResult{}, ErrClosed
	}
	if b.selected != selected || !selected.active || !selected.context.Connected {
		err := staleSelectionForSession(selected, "selection_changed_before_admission")
		b.mu.Unlock()
		return InvokeResult{}, err
	}
	record, ok = b.refs[request.ToolRef]
	if !ok || !refCurrentLocked(selected, record) {
		err := staleToolRefError(request.ToolRef, selected.context.Generation)
		b.mu.Unlock()
		return InvokeResult{}, err
	}

	id, err := b.mintInvocationIDLocked()
	if err != nil {
		b.mu.Unlock()
		return InvokeResult{}, err
	}
	now := b.clock.Now()
	invocation := &brokerInvocation{
		selected: selected,
		ctx:      ctx,
		invocation: Invocation{
			ID:          id,
			Tool:        cloneToolDescriptor(descriptor),
			Arguments:   cloneJSON(request.Input),
			State:       InvocationQueued,
			Operation:   classifyOperation(descriptor),
			ModelCallID: request.ModelCallID,
			SessionID:   request.SessionID,
			ResponseID:  request.ResponseID,
			CreatedAt:   now,
			QueuedAt:    now,
			Deadline:    now.Add(invocationTimeout),
		},
		dispatchDone: make(chan invocationDispatch, 1),
		terminal:     make(chan struct{}),
		admissionSeq: b.eventSequence + 1,
	}
	b.invocations[id] = invocation
	selected.queue = append(selected.queue, invocation)
	b.startInvocationTimerLocked(invocation)
	b.emitLocked(BrokerEvent{
		Type:         BrokerEventInvocationCreated,
		BrowserID:    descriptor.BrowserID,
		TargetID:     descriptor.TargetID,
		Generation:   descriptor.Generation,
		ToolRef:      request.ToolRef,
		InvocationID: id,
		State:        InvocationQueued,
		Reason:       "admitted",
	})
	signalInvocationQueueLocked(selected)
	b.mu.Unlock()

	select {
	case outcome := <-invocation.dispatchDone:
		return cloneInvokeResult(outcome.result), outcome.err
	case <-ctx.Done():
		b.cancelAdmission(invocation)
		return InvokeResult{}, ctx.Err()
	case <-b.closedCh:
		b.cancelAdmission(invocation)
		return InvokeResult{}, ErrClosed
	}
}

func (b *StatefulBroker) mintInvocationIDLocked() (InvocationID, error) {
	for attempt := 0; attempt < maxInvocationIDMintAttempts; attempt++ {
		id, err := b.ids.NewInvocationID()
		if err != nil {
			return "", err
		}
		if id == "" {
			continue
		}
		if _, active := b.invocations[id]; active {
			continue
		}
		if _, terminal := b.terminalResults[id]; terminal {
			continue
		}
		if _, seen := b.terminalSeen[id]; seen {
			continue
		}
		if _, active := b.browserInvocations[id]; active {
			continue
		}
		if _, seen := b.browserTerminalSeen[id]; seen {
			continue
		}
		return id, nil
	}
	return "", errors.New("webmcp: invocation ID source did not produce a unique non-empty ID")
}

func (b *StatefulBroker) startInvocationTimerLocked(invocation *brokerInvocation) {
	if invocation == nil || b.timers == nil || b.invocationTimeout <= 0 {
		return
	}
	timer := b.timers.NewTimer(b.invocationTimeout)
	if timer == nil {
		return
	}
	invocation.timer = timer
	b.wg.Add(1)
	go b.watchInvocationDeadline(invocation, timer)
}

func (b *StatefulBroker) watchInvocationDeadline(invocation *brokerInvocation, timer Timer) {
	defer b.wg.Done()
	select {
	case <-timer.C():
		b.timeoutInvocation(invocation)
	case <-invocation.terminal:
	case <-b.closedCh:
	}
}

func (b *StatefulBroker) timeoutInvocation(invocation *brokerInvocation) {
	if invocation == nil {
		return
	}
	b.mu.Lock()
	if invocation.terminalized {
		b.mu.Unlock()
		return
	}
	if invocation.invocation.State != InvocationQueued {
		invocation.invocation.CancelRequested = true
		invocation.cancelPending = true
	} else {
		removeQueuedInvocationLocked(invocation.selected, invocation)
	}
	action := b.claimTargetCancellationLocked(invocation)
	wait := b.cancellationWaitLocked(invocation, action)
	phase := "queue"
	if invocation.invocation.State == InvocationDispatching {
		phase = "dispatch"
	} else if invocation.invocation.State == InvocationDispatched {
		phase = "result"
	}
	timeoutMilliseconds := b.invocationTimeout.Milliseconds()
	result := invocationFailureResult(invocation, InvocationTimedOut, ErrorInvocationTimedOut, map[string]any{
		"invocation_id":       string(invocation.invocation.ID),
		"timeout_ms":          timeoutMilliseconds,
		"phase":               phase,
		"side_effect_unknown": true,
	})
	if action != nil || wait != nil {
		b.mu.Unlock()
		if action != nil {
			performTargetCancellation(action)
		} else {
			<-wait
		}
		b.mu.Lock()
	}
	b.finishInvocationLocked(invocation, result)
	b.mu.Unlock()
}

func (b *StatefulBroker) claimTargetCancellationLocked(invocation *brokerInvocation) *targetCancellation {
	if invocation == nil || invocation.cancelSent || !invocation.invocation.CancelRequested || invocation.browserID == "" {
		return nil
	}
	invocation.cancelSent = true
	invocation.cancelDone = make(chan struct{})
	return &targetCancellation{session: invocation.selected.session, id: invocation.browserID, done: invocation.cancelDone}
}

func (b *StatefulBroker) cancellationWaitLocked(invocation *brokerInvocation, action *targetCancellation) <-chan struct{} {
	if action != nil || invocation == nil || !invocation.cancelSent {
		return nil
	}
	return invocation.cancelDone
}

func performTargetCancellation(action *targetCancellation) {
	if action == nil {
		return
	}
	defer close(action.done)
	if action.session == nil || action.id == "" {
		return
	}
	// Cancellation is best effort after the broker has claimed the request.
	// A target that has already detached or replied is still
	// reconciled by the broker's bounded browser-terminal cache.
	_ = action.session.CancelWebMCP(context.Background(), action.id)
}

// runInvocationQueue owns one target-local FIFO. It intentionally waits for
// terminal reconciliation before taking the next item, which makes the
// default policy safe for both mutating tools and descriptors without a
// trusted read-only annotation.
func (b *StatefulBroker) runInvocationQueue(selected *brokerSession) {
	defer b.wg.Done()
	defer close(selected.queueWorkerDone)
	for {
		invocation := b.nextQueuedInvocation(selected)
		if invocation == nil {
			select {
			case <-selected.queueStop:
				return
			case <-selected.queueWake:
			}
			continue
		}
		b.dispatchQueuedInvocation(invocation)
		b.clearCurrentInvocation(selected, invocation)
	}
}

func (b *StatefulBroker) nextQueuedInvocation(selected *brokerSession) *brokerInvocation {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(selected.queue) > 0 {
		invocation := selected.queue[0]
		selected.queue = selected.queue[1:]
		if invocation.terminalized {
			continue
		}
		if b.closed || selected.queueClosed || !selected.active {
			b.finishInvocationLocked(invocation, invocationFailureResult(invocation, InvocationOrphaned, ErrorInvocationOrphaned, nil))
			continue
		}
		selected.current = invocation
		invocation.invocation.State = InvocationDispatching
		invocation.invocation.DispatchStarted = b.clock.Now()
		return invocation
	}
	return nil
}

func (b *StatefulBroker) dispatchQueuedInvocation(invocation *brokerInvocation) {
	selected := invocation.selected
	selected.dispatchMu.Lock()
	b.dispatchQueuedInvocationWithLock(invocation)
	b.mu.Lock()
	action := b.claimTargetCancellationLocked(invocation)
	b.mu.Unlock()
	selected.dispatchMu.Unlock()
	performTargetCancellation(action)
	b.waitForInvocationLane(invocation)
}

func (b *StatefulBroker) dispatchQueuedInvocationWithLock(invocation *brokerInvocation) {
	selected := invocation.selected
	b.mu.Lock()
	if invocation.terminalized {
		b.reportDispatchLocked(invocation, invocation.finalResult, nil)
		b.mu.Unlock()
		return
	}
	if b.closed || b.selected != selected || !selected.active || !selected.context.Connected {
		err := staleSelectionForSession(selected, "selection_changed_before_dispatch")
		result := invocationFailureResult(invocation, InvocationError, errorCodeFor(err, ErrorStaleSelection), nil)
		b.finishInvocationLocked(invocation, result)
		b.reportDispatchLocked(invocation, result, err)
		b.mu.Unlock()
		return
	}
	record, ok := b.refs[invocation.invocation.Tool.Ref]
	if !ok || !refCurrentLocked(selected, record) {
		err := staleToolRefError(invocation.invocation.Tool.Ref, selected.context.Generation)
		result := invocationFailureResult(invocation, InvocationError, ErrorStaleToolRef, nil)
		b.finishInvocationLocked(invocation, result)
		b.reportDispatchLocked(invocation, result, err)
		b.mu.Unlock()
		return
	}
	handle := selected.handle
	session := selected.session
	descriptor := cloneToolDescriptor(invocation.invocation.Tool)
	ctx := invocation.ctx
	b.mu.Unlock()

	// The target check is repeated for every dequeued call. This prevents a
	// target disappearing while an earlier invocation occupied the lane.
	targets, err := handle.ListTargets(ctx)
	if err != nil || !targetPresent(targets, descriptor.TargetID) {
		failure := staleSelectionError(descriptor.BrowserID, descriptor.TargetID, descriptor.Generation, "target_not_current")
		b.mu.Lock()
		result := invocationFailureResult(invocation, InvocationError, ErrorStaleSelection, nil)
		b.finishInvocationLocked(invocation, result)
		b.reportDispatchLocked(invocation, result, failure)
		b.mu.Unlock()
		return
	}

	b.mu.Lock()
	if invocation.terminalized {
		b.reportDispatchLocked(invocation, invocation.finalResult, nil)
		b.mu.Unlock()
		return
	}
	if b.closed || b.selected != selected || !selected.active || !selected.context.Connected {
		err = staleSelectionError(descriptor.BrowserID, descriptor.TargetID, descriptor.Generation, "selection_changed_before_dispatch")
		result := invocationFailureResult(invocation, InvocationError, ErrorStaleSelection, nil)
		b.finishInvocationLocked(invocation, result)
		b.reportDispatchLocked(invocation, result, err)
		b.mu.Unlock()
		return
	}
	record, ok = b.refs[invocation.invocation.Tool.Ref]
	if !ok || !refCurrentLocked(selected, record) {
		err = staleToolRefError(invocation.invocation.Tool.Ref, selected.context.Generation)
		result := invocationFailureResult(invocation, InvocationError, ErrorStaleToolRef, nil)
		b.finishInvocationLocked(invocation, result)
		b.reportDispatchLocked(invocation, result, err)
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	id, invokeErr := invokeWebMCP(ctx, session, invocation.invocation.ID, descriptor.FrameID, descriptor.Name, cloneJSON(invocation.invocation.Arguments))

	b.mu.Lock()
	if id == "" {
		code := ErrorInvocationFailed
		state := InvocationError
		if errors.Is(invokeErr, context.Canceled) {
			code = ErrorInvocationCanceled
			state = InvocationCanceled
		} else if errors.Is(invokeErr, context.DeadlineExceeded) {
			code = ErrorInvocationTimedOut
			state = InvocationTimedOut
		}
		result := invocationFailureResult(invocation, state, code, nil)
		b.finishInvocationLocked(invocation, result)
		b.reportDispatchLocked(invocation, result, invokeErr)
		b.mu.Unlock()
		return
	}

	if existing, ok := b.browserInvocations[id]; ok && existing != invocation {
		err = fmt.Errorf("webmcp: duplicate target invocation ID %q", id)
		result := invocationFailureResult(invocation, InvocationError, ErrorInvocationFailed, map[string]any{
			"invocation_id":       string(id),
			"phase":               "correlation",
			"side_effect_unknown": true,
		})
		b.finishInvocationLocked(invocation, result)
		b.reportDispatchLocked(invocation, result, err)
		b.mu.Unlock()
		return
	}
	if _, terminal := b.browserTerminalSeen[id]; terminal {
		err = fmt.Errorf("webmcp: reused terminal target invocation ID %q", id)
		result := invocationFailureResult(invocation, InvocationError, ErrorInvocationFailed, map[string]any{
			"invocation_id":       string(id),
			"phase":               "correlation",
			"side_effect_unknown": true,
		})
		b.finishInvocationLocked(invocation, result)
		b.reportDispatchLocked(invocation, result, err)
		b.mu.Unlock()
		return
	}
	invocation.browserID = id

	if invocation.terminalized {
		b.recordBrowserTerminalIDLocked(id)
		b.takeEarlyTerminalLocked(id)
		b.rebindTerminalInvocationLocked(invocation)
		b.reportDispatchLocked(invocation, invocation.finalResult, nil)
		b.mu.Unlock()
		return
	}
	invocation.invocation.State = InvocationDispatched
	invocation.invocation.DispatchedAt = b.clock.Now()
	b.browserInvocations[id] = invocation
	result := InvokeResult{InvocationID: invocation.invocation.ID, State: InvocationDispatched}
	if early, ok := b.takeEarlyTerminalLocked(id); ok {
		b.applyTerminalObservationLocked(invocation, early)
		result = cloneInvokeResult(invocation.finalResult)
	}
	if invokeErr != nil && !invocation.terminalized {
		result = invocationFailureResult(invocation, InvocationError, ErrorInvocationFailed, map[string]any{
			"invocation_id":       string(invocation.invocation.ID),
			"phase":               "invoke",
			"side_effect_unknown": true,
		})
		b.finishInvocationLocked(invocation, result)
	}
	b.reportDispatchLocked(invocation, result, nil)
	b.mu.Unlock()
}

func (b *StatefulBroker) clearCurrentInvocation(selected *brokerSession, invocation *brokerInvocation) {
	b.mu.Lock()
	if selected.current == invocation {
		selected.current = nil
	}
	b.mu.Unlock()
}

func (b *StatefulBroker) waitForInvocationLane(invocation *brokerInvocation) {
	select {
	case <-invocation.terminal:
	case <-invocation.ctx.Done():
		b.cancelContextInvocation(invocation)
	case <-invocation.selected.queueStop:
	}
}

func (b *StatefulBroker) reportDispatchLocked(invocation *brokerInvocation, result InvokeResult, err error) {
	if invocation.reported {
		return
	}
	invocation.reported = true
	invocation.dispatchDone <- invocationDispatch{result: cloneInvokeResult(result), err: err}
}

func (b *StatefulBroker) cancelAdmission(invocation *brokerInvocation) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if invocation.terminalized || invocation.invocation.State != InvocationQueued {
		return
	}
	removeQueuedInvocationLocked(invocation.selected, invocation)
	result := invocationFailureResult(invocation, InvocationCanceled, ErrorInvocationCanceled, map[string]any{
		"invocation_id": string(invocation.invocation.ID),
		"cancel_source": "context",
	})
	b.finishInvocationLocked(invocation, result)
}

func (b *StatefulBroker) cancelContextInvocation(invocation *brokerInvocation) {
	b.mu.Lock()
	if invocation.terminalized {
		b.mu.Unlock()
		return
	}
	if !b.cancelOnInterruptAllowsLocked(invocation) {
		done := invocation.terminal
		b.mu.Unlock()
		<-done
		return
	}
	if invocation.invocation.State == InvocationQueued {
		removeQueuedInvocationLocked(invocation.selected, invocation)
	}
	invocation.invocation.CancelRequested = true
	invocation.cancelPending = true
	action := b.claimTargetCancellationLocked(invocation)
	wait := b.cancellationWaitLocked(invocation, action)
	result := invocationFailureResult(invocation, InvocationCanceled, ErrorInvocationCanceled, map[string]any{
		"invocation_id": string(invocation.invocation.ID),
		"cancel_source": "context",
	})
	if action != nil || wait != nil {
		b.mu.Unlock()
		if action != nil {
			performTargetCancellation(action)
		} else {
			<-wait
		}
		b.mu.Lock()
	}
	b.finishInvocationLocked(invocation, result)
	b.mu.Unlock()
}

func (b *StatefulBroker) cancelInvocation(ctx context.Context, request CancelRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if b == nil {
		return ErrClosed
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrClosed
	}
	invocation := b.invocations[request.InvocationID]
	if invocation == nil {
		if _, done := b.terminalSeen[request.InvocationID]; done {
			b.mu.Unlock()
			return nil
		}
		err := classified(ErrorInvocationFailed, "the invocation is not registered", map[string]any{
			"invocation_id": string(request.InvocationID),
			"phase":         "cancel",
		}, ErrInvocationNotFound)
		b.mu.Unlock()
		return err
	}
	if invocation.terminalized {
		b.mu.Unlock()
		return nil
	}
	if invocation.invocation.State == InvocationQueued {
		removeQueuedInvocationLocked(invocation.selected, invocation)
		result := invocationFailureResult(invocation, InvocationCanceled, ErrorInvocationCanceled, map[string]any{
			"invocation_id": string(request.InvocationID),
			"cancel_source": "broker",
		})
		b.finishInvocationLocked(invocation, result)
		b.mu.Unlock()
		return nil
	}
	invocation.invocation.CancelRequested = true
	invocation.cancelPending = true
	action := b.claimTargetCancellationLocked(invocation)
	wait := b.cancellationWaitLocked(invocation, action)
	result := invocationFailureResult(invocation, InvocationCanceled, ErrorInvocationCanceled, map[string]any{
		"invocation_id": string(request.InvocationID),
		"cancel_source": "broker",
	})
	if action != nil || wait != nil {
		b.mu.Unlock()
		if action != nil {
			performTargetCancellation(action)
		} else {
			<-wait
		}
		b.mu.Lock()
	}
	b.finishInvocationLocked(invocation, result)
	b.mu.Unlock()
	return nil
}

func (b *StatefulBroker) cancelOnInterruptAllowsLocked(invocation *brokerInvocation) bool {
	switch b.cancelOnInterrupt {
	case CancelOnInterruptNever:
		return false
	case CancelOnInterruptAlways:
		return true
	case CancelOnInterruptReadOnly:
		return invocation.invocation.Operation == OperationReadOnly
	default:
		return invocation.invocation.Operation == OperationReadOnly
	}
}

func (b *StatefulBroker) reconcileBrowserResponseLocked(selected *brokerSession, event BrowserEvent) {
	if event.InvocationID == "" {
		return
	}
	if invocation, ok := b.browserInvocations[event.InvocationID]; ok {
		if invocation.selected != selected {
			return
		}
		b.applyTerminalObservationLocked(invocation, terminalObservationFromEvent(event, b.maxResultBytes))
		return
	}
	if _, done := b.browserTerminalSeen[event.InvocationID]; done {
		return
	}
	if _, alreadyBuffered := b.earlyTerminals[event.InvocationID]; alreadyBuffered {
		return
	}
	b.bufferEarlyTerminalLocked(event)
}

func terminalObservationFromEvent(event BrowserEvent, maxResultBytes int) terminalObservation {
	output := bytes.TrimSpace(event.Output)
	observation := terminalObservation{
		status:        event.Status,
		outputBytes:   len(output),
		outputPresent: true,
		errorCode:     event.ErrorCode,
		at:            event.At,
	}
	if len(output) <= maxResultBytes {
		observation.output = cloneJSON(output)
	}
	return observation
}

func (b *StatefulBroker) bufferEarlyTerminalLocked(event BrowserEvent) {
	if len(b.earlyTerminals) >= maxEarlyTerminalResults {
		b.evictOldestEarlyTerminalLocked()
	}
	observation := terminalObservationFromEvent(event, b.maxResultBytes)
	b.earlyTerminals[event.InvocationID] = observation
	b.earlyTerminalOrder = append(b.earlyTerminalOrder, event.InvocationID)
}

func (b *StatefulBroker) evictOldestEarlyTerminalLocked() {
	for len(b.earlyTerminalOrder) > 0 {
		id := b.earlyTerminalOrder[0]
		b.earlyTerminalOrder = b.earlyTerminalOrder[1:]
		if _, ok := b.earlyTerminals[id]; ok {
			delete(b.earlyTerminals, id)
			return
		}
	}
}

func (b *StatefulBroker) takeEarlyTerminalLocked(id InvocationID) (terminalObservation, bool) {
	observation, ok := b.earlyTerminals[id]
	if !ok {
		return terminalObservation{}, false
	}
	delete(b.earlyTerminals, id)
	b.removeEarlyTerminalOrderIDLocked(id)
	return observation, true
}

func (b *StatefulBroker) removeEarlyTerminalOrderIDLocked(id InvocationID) {
	for i, candidate := range b.earlyTerminalOrder {
		if candidate != id {
			continue
		}
		b.earlyTerminalOrder = append(b.earlyTerminalOrder[:i], b.earlyTerminalOrder[i+1:]...)
		return
	}
}

func (b *StatefulBroker) applyTerminalObservationLocked(invocation *brokerInvocation, observation terminalObservation) {
	if invocation.terminalized || invocation.cancelPending {
		return
	}
	state, success := terminalState(observation.status)
	if !success {
		code := ErrorInvocationFailed
		details := map[string]any{
			"invocation_id":       string(invocation.invocation.ID),
			"tool_ref":            string(invocation.invocation.Tool.Ref),
			"phase":               "result",
			"page_error_code":     safePageErrorCode(observation.errorCode),
			"side_effect_unknown": true,
		}
		if state == InvocationCanceled || observation.errorCode == string(ErrorInvocationCanceled) {
			code = ErrorInvocationCanceled
			state = InvocationCanceled
			details = map[string]any{
				"invocation_id": string(invocation.invocation.ID),
				"cancel_source": "browser",
			}
		}
		result := invocationFailureResult(invocation, state, code, details)
		b.finishInvocationLocked(invocation, result)
		return
	}

	output := observation.output
	if !observation.outputPresent {
		output = json.RawMessage("null")
	}
	if observation.outputBytes > b.maxResultBytes {
		b.finishInvocationLocked(invocation, resultTooLargeResult(invocation, estimatedInvocationResultSize(invocation, output, observation.outputBytes), b.maxResultBytes))
		return
	}
	output, err := oneJSONValue(output)
	if err != nil {
		result := invocationFailureResult(invocation, InvocationError, ErrorInvocationFailed, map[string]any{
			"invocation_id":       string(invocation.invocation.ID),
			"tool_ref":            string(invocation.invocation.Tool.Ref),
			"phase":               "result_serialization",
			"page_error_code":     "invalid_json",
			"side_effect_unknown": true,
		})
		b.finishInvocationLocked(invocation, result)
		return
	}
	observedBytes := invocationResultSize(invocation, output)
	if observedBytes > b.maxResultBytes {
		b.finishInvocationLocked(invocation, resultTooLargeResult(invocation, observedBytes, b.maxResultBytes))
		return
	}
	result := InvokeResult{
		InvocationID: invocation.invocation.ID,
		State:        InvocationCompleted,
		Output:       cloneJSON(output),
	}
	b.finishInvocationLocked(invocation, result)
}

func terminalState(status string) (InvocationState, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "completed", "complete", "success", "succeeded", "ok":
		return InvocationCompleted, true
	case "canceled", "cancelled":
		return InvocationCanceled, false
	default:
		return InvocationError, false
	}
}

func oneJSONValue(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return json.RawMessage("null"), nil
	}
	if !json.Valid(trimmed) {
		return nil, errors.New("webmcp: page result is not valid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	var value json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("webmcp: page result contains multiple JSON values")
		}
		return nil, err
	}
	return cloneJSON(trimmed), nil
}

type invocationResultData struct {
	InvocationID InvocationID    `json:"invocation_id"`
	ToolRef      ToolRef         `json:"tool_ref"`
	Status       string          `json:"status"`
	Output       json.RawMessage `json:"output"`
}

func invocationResultSize(invocation *brokerInvocation, output json.RawMessage) int {
	data, err := json.Marshal(invocationResultData{
		InvocationID: invocation.invocation.ID,
		ToolRef:      invocation.invocation.Tool.Ref,
		Status:       string(InvocationCompleted),
		Output:       output,
	})
	if err != nil {
		return 0
	}
	wire, err := json.Marshal(ToolResultEnvelope{
		Version: ToolResultVersion,
		OK:      true,
		Data:    data,
		Error:   nil,
	})
	if err != nil {
		return 0
	}
	return len(wire)
}

func estimatedInvocationResultSize(invocation *brokerInvocation, output json.RawMessage, outputBytes int) int {
	placeholder := invocationResultSize(invocation, json.RawMessage("null"))
	if placeholder == 0 {
		return outputBytes
	}
	return placeholder + outputBytes - len("null")
}

func resultTooLargeResult(invocation *brokerInvocation, observedBytes, limitBytes int) InvokeResult {
	return invocationFailureResult(invocation, InvocationError, ErrorResultTooLarge, map[string]any{
		"tool_ref":       string(invocation.invocation.Tool.Ref),
		"limit_bytes":    limitBytes,
		"observed_bytes": observedBytes,
	})
}

func invocationFailureResult(invocation *brokerInvocation, state InvocationState, code ErrorCode, details map[string]any) InvokeResult {
	return InvokeResult{
		InvocationID: invocation.invocation.ID,
		State:        state,
		ErrorCode:    string(code),
		ErrorDetails: cloneDetails(details),
	}
}

func (b *StatefulBroker) finishInvocationLocked(invocation *brokerInvocation, result InvokeResult) {
	if invocation.terminalized {
		return
	}
	result.InvocationID = invocation.invocation.ID
	if result.State == "" {
		result.State = InvocationError
	}
	invocation.terminalized = true
	invocation.finalResult = cloneInvokeResult(result)
	invocation.invocation.State = result.State
	invocation.invocation.CompletedAt = b.clock.Now()
	invocation.invocation.Result = cloneJSON(result.Output)
	invocation.invocation.ErrorCode = result.ErrorCode
	invocation.invocation.TerminalDelivered = false
	if invocation.timer != nil {
		invocation.timer.Stop()
		invocation.timer = nil
	}
	if invocation.invocation.ID != "" {
		b.recordTerminalIDLocked(invocation.invocation.ID)
		delete(b.invocations, invocation.invocation.ID)
		b.terminalResults[invocation.invocation.ID] = terminalInvocation{
			invocation: cloneInvocation(invocation.invocation),
			result:     cloneInvokeResult(result),
		}
		b.terminalOrder = append(b.terminalOrder, invocation.invocation.ID)
		b.trimTerminalResultsLocked()
	}
	if invocation.browserID != "" {
		delete(b.browserInvocations, invocation.browserID)
		b.recordBrowserTerminalIDLocked(invocation.browserID)
	}
	if !invocation.reported {
		b.reportDispatchLocked(invocation, invocation.finalResult, nil)
	}
	close(invocation.terminal)
	b.emitLocked(BrokerEvent{
		Type:         BrokerEventInvocationTerminal,
		At:           invocation.invocation.CompletedAt,
		BrowserID:    invocation.invocation.Tool.BrowserID,
		TargetID:     invocation.invocation.Tool.TargetID,
		Generation:   invocation.invocation.Tool.Generation,
		InvocationID: invocation.invocation.ID,
		ToolRef:      invocation.invocation.Tool.Ref,
		State:        result.State,
		Reason:       result.ErrorCode,
	})
}

func (b *StatefulBroker) recordTerminalIDLocked(id InvocationID) {
	if id == "" {
		return
	}
	if _, exists := b.terminalSeen[id]; exists {
		return
	}
	b.terminalSeen[id] = struct{}{}
	b.terminalSeenOrder = append(b.terminalSeenOrder, id)
	for len(b.terminalSeen) > maxTerminalResults && len(b.terminalSeenOrder) > 0 {
		oldest := b.terminalSeenOrder[0]
		b.terminalSeenOrder = b.terminalSeenOrder[1:]
		delete(b.terminalSeen, oldest)
	}
}

func (b *StatefulBroker) recordBrowserTerminalIDLocked(id InvocationID) {
	if id == "" {
		return
	}
	if _, exists := b.browserTerminalSeen[id]; exists {
		return
	}
	b.browserTerminalSeen[id] = struct{}{}
	b.browserTerminalOrder = append(b.browserTerminalOrder, id)
	for len(b.browserTerminalSeen) > maxTerminalResults && len(b.browserTerminalOrder) > 0 {
		oldest := b.browserTerminalOrder[0]
		b.browserTerminalOrder = b.browserTerminalOrder[1:]
		delete(b.browserTerminalSeen, oldest)
	}
}

func (b *StatefulBroker) rebindTerminalInvocationLocked(invocation *brokerInvocation) {
	if invocation.invocation.ID == "" {
		return
	}
	if terminal, ok := b.terminalResults[invocation.invocation.ID]; ok {
		terminal.invocation.ID = invocation.invocation.ID
		terminal.result.InvocationID = invocation.invocation.ID
		b.terminalResults[invocation.invocation.ID] = terminal
		return
	}
	b.terminalResults[invocation.invocation.ID] = terminalInvocation{
		invocation: cloneInvocation(invocation.invocation),
		result:     cloneInvokeResult(invocation.finalResult),
	}
	b.terminalOrder = append(b.terminalOrder, invocation.invocation.ID)
	b.trimTerminalResultsLocked()
}

func (b *StatefulBroker) trimTerminalResultsLocked() {
	for len(b.terminalResults) > maxTerminalResults && len(b.terminalOrder) > 0 {
		id := b.terminalOrder[0]
		b.terminalOrder = b.terminalOrder[1:]
		if terminal, ok := b.terminalResults[id]; ok {
			terminal.invocation.TerminalDelivered = true
			delete(b.terminalResults, id)
		}
	}
}

func (b *StatefulBroker) finishLifecycleInvocationLocked(invocation *brokerInvocation, state InvocationState, code ErrorCode, reason string) {
	var details map[string]any
	switch code {
	case ErrorTargetDetached:
		if reason == "" {
			reason = "target_detached"
		}
		details = map[string]any{
			"browser_id": string(invocation.invocation.Tool.BrowserID),
			"target_id":  string(invocation.invocation.Tool.TargetID),
			"generation": invocation.invocation.Tool.Generation,
			"reason":     reason,
		}
	case ErrorPageNavigated:
		currentGeneration := invocation.selected.context.Generation
		previousGeneration := invocation.invocation.Tool.Generation
		if previousGeneration >= currentGeneration && currentGeneration > 0 {
			previousGeneration = currentGeneration - 1
		}
		details = map[string]any{
			"browser_id":          string(invocation.invocation.Tool.BrowserID),
			"target_id":           string(invocation.invocation.Tool.TargetID),
			"previous_generation": previousGeneration,
			"current_generation":  currentGeneration,
		}
	case ErrorBrowserDisconnected:
		details = map[string]any{
			"browser_id":         string(invocation.invocation.Tool.BrowserID),
			"target_id":          string(invocation.invocation.Tool.TargetID),
			"phase":              "lifecycle",
			"reconnect_required": true,
		}
	case ErrorInvocationOrphaned:
		details = map[string]any{
			"invocation_id":     string(invocation.invocation.ID),
			"target_id":         string(invocation.invocation.Tool.TargetID),
			"generation":        invocation.invocation.Tool.Generation,
			"terminal_observed": false,
		}
	default:
		details = map[string]any{
			"browser_id": string(invocation.invocation.Tool.BrowserID),
			"target_id":  string(invocation.invocation.Tool.TargetID),
			"generation": invocation.invocation.Tool.Generation,
			"reason":     reason,
		}
	}
	b.finishInvocationLocked(invocation, invocationFailureResult(invocation, state, code, details))
}

func (b *StatefulBroker) terminalizeSessionInvocationsLocked(selected *brokerSession, code ErrorCode, reason string) {
	state := InvocationError
	if code == ErrorInvocationOrphaned {
		state = InvocationOrphaned
	}
	if selected.current != nil && !selected.current.terminalized {
		b.finishLifecycleInvocationLocked(selected.current, state, code, reason)
	}
	for _, invocation := range selected.queue {
		if invocation == nil || invocation.terminalized {
			continue
		}
		b.finishLifecycleInvocationLocked(invocation, state, code, reason)
	}
	for _, invocation := range b.invocations {
		if invocation.selected != selected || invocation.terminalized {
			continue
		}
		b.finishLifecycleInvocationLocked(invocation, state, code, reason)
	}
	signalInvocationQueueLocked(selected)
}
func closeInvocationQueueLocked(selected *brokerSession) {
	if selected == nil || selected.queueClosed {
		return
	}
	selected.queueClosed = true
	selected.queue = nil // queued entries were terminalized before lane close.
	close(selected.queueStop)
	signalInvocationQueueLocked(selected)
}

func removeQueuedInvocationLocked(selected *brokerSession, target *brokerInvocation) bool {
	if selected == nil || target == nil {
		return false
	}
	for i, invocation := range selected.queue {
		if invocation != target {
			continue
		}
		copy(selected.queue[i:], selected.queue[i+1:])
		selected.queue[len(selected.queue)-1] = nil
		selected.queue = selected.queue[:len(selected.queue)-1]
		signalInvocationQueueLocked(selected)
		return true
	}
	return false
}

func signalInvocationQueueLocked(selected *brokerSession) {
	if selected == nil {
		return
	}
	select {
	case selected.queueWake <- struct{}{}:
	default:
	}
}

func classifyOperation(descriptor ToolDescriptor) OperationClass {
	if descriptor.Annotations.ReadOnly == nil {
		return OperationUnknown
	}
	if *descriptor.Annotations.ReadOnly {
		return OperationReadOnly
	}
	return OperationMutating
}

func lifecycleInvocationErrorCode(reason string, fallback ErrorCode) ErrorCode {
	switch strings.ToLower(reason) {
	case "disconnect", "disconnected", "browser_disconnected":
		return ErrorBrowserDisconnected
	case "detach", "detached", "target_detached":
		return ErrorTargetDetached
	default:
		return fallback
	}
}

func errorCodeFor(err error, fallback ErrorCode) ErrorCode {
	var classifiedError *ClassifiedError
	if errors.As(err, &classifiedError) && classifiedError != nil && IsKnownErrorCode(classifiedError.Code) {
		return classifiedError.Code
	}
	return fallback
}

func safePageErrorCode(code string) string {
	if code == "" {
		return ""
	}
	if len(code) > 64 {
		return code[:64]
	}
	for _, character := range code {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' && character != '.' {
			return "unknown"
		}
	}
	return code
}

func cloneInvokeResult(result InvokeResult) InvokeResult {
	result.Output = cloneJSON(result.Output)
	result.ErrorDetails = cloneDetails(result.ErrorDetails)
	return result
}

func cloneInvocation(invocation Invocation) Invocation {
	invocation.Tool = cloneToolDescriptor(invocation.Tool)
	invocation.Arguments = cloneJSON(invocation.Arguments)
	invocation.Result = cloneJSON(invocation.Result)
	return invocation
}

func cloneDetails(details map[string]any) map[string]any {
	if details == nil {
		return nil
	}
	cloned := make(map[string]any, len(details))
	for key, value := range details {
		switch typed := value.(type) {
		case json.RawMessage:
			cloned[key] = cloneJSON(typed)
		case []byte:
			cloned[key] = append([]byte(nil), typed...)
		default:
			cloned[key] = value
		}
	}
	return cloned
}

// Invocation returns a defensive snapshot of an active or recently terminal
// call. The terminal cache is bounded and exists only to close the race
// between browser completion and a consumer asking for the result.
func (b *StatefulBroker) Invocation(id InvocationID) (Invocation, bool) {
	if b == nil {
		return Invocation{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if invocation, ok := b.invocations[id]; ok {
		return cloneInvocation(invocation.invocation), true
	}
	if terminal, ok := b.terminalResults[id]; ok {
		return cloneInvocation(terminal.invocation), true
	}
	return Invocation{}, false
}

// PendingInvocations returns active registry entries in admission order. It
// is an observation seam for tests and diagnostics, not a provider API.
func (b *StatefulBroker) PendingInvocations() []Invocation {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entries := make([]*brokerInvocation, 0, len(b.invocations))
	for _, invocation := range b.invocations {
		entries = append(entries, invocation)
	}
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].admissionSeq < entries[j-1].admissionSeq; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	result := make([]Invocation, 0, len(entries))
	for _, invocation := range entries {
		result = append(result, cloneInvocation(invocation.invocation))
	}
	return result
}

// WaitInvocation waits for one terminal broker result and consumes its
// bounded terminal cache entry. Invoke itself remains non-blocking after
// dispatch so the existing tool executor can preserve its current behavior.
func (b *StatefulBroker) WaitInvocation(ctx context.Context, id InvocationID) (InvokeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return InvokeResult{}, err
	}
	if b == nil {
		return InvokeResult{}, ErrClosed
	}
	b.mu.Lock()
	if terminal, ok := b.terminalResults[id]; ok {
		terminal.invocation.TerminalDelivered = true
		result := cloneInvokeResult(terminal.result)
		delete(b.terminalResults, id)
		b.removeTerminalOrderIDLocked(id)
		b.mu.Unlock()
		return result, nil
	}
	invocation := b.invocations[id]
	if invocation == nil {
		b.mu.Unlock()
		return InvokeResult{}, ErrInvocationNotFound
	}
	done := invocation.terminal
	b.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		return InvokeResult{}, ctx.Err()
	case <-b.closedCh:
		select {
		case <-done:
		default:
			return InvokeResult{}, ErrClosed
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	terminal, ok := b.terminalResults[id]
	if !ok {
		return cloneInvokeResult(invocation.finalResult), nil
	}
	terminal.invocation.TerminalDelivered = true
	delete(b.terminalResults, id)
	b.removeTerminalOrderIDLocked(id)
	return cloneInvokeResult(terminal.result), nil
}

func (b *StatefulBroker) removeTerminalOrderIDLocked(id InvocationID) {
	for i, candidate := range b.terminalOrder {
		if candidate != id {
			continue
		}
		b.terminalOrder = append(b.terminalOrder[:i], b.terminalOrder[i+1:]...)
		return
	}
}
