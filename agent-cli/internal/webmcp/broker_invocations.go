package webmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	// It is kept separately so browser events can be reconciled without changing
	// the broker's public invocation ID. Direct CLI handoff may expose this
	// opaque ID explicitly so a fresh process can cancel the exact target call.
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

	// invokedObserved and invokedSequence are the broker's proof that the
	// target published the invocation which this dispatch admitted. A terminal
	// event is never sufficient on its own: protocol IDs can be reused or
	// replayed by a target-local event stream.
	invokedObserved bool
	invokedSequence uint64
}

type invocationDispatch struct {
	result InvokeResult
	err    error
}

type terminalInvocation struct {
	invocation Invocation
	result     InvokeResult
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
	reason        string
	generation    uint64
	browserID     BrowserID
	targetID      TargetID
	sequence      uint64
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
	b.mu.Unlock()
	if err := b.selectedStateError(selected, "lifecycle", "selection_not_connected"); err != nil {
		return InvokeResult{}, err
	}
	b.mu.Lock()
	record, err := b.admissionRecordLocked(selected, request.ToolRef, "selection_not_connected")
	if err != nil {
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

	input := request.Input
	if input == nil {
		input = json.RawMessage(`{}`)
	}
	b.mu.Lock()
	if _, err := b.admissionRecordLocked(selected, request.ToolRef, "selection_changed_before_admission"); err != nil {
		b.mu.Unlock()
		return InvokeResult{}, err
	}

	id, err := b.mintInvocationIDLocked()
	if err != nil {
		b.mu.Unlock()
		return InvokeResult{}, err
	}
	invocation := b.newBrokerInvocationLocked(ctx, selected, id, request, descriptor, input, invocationTimeout)
	b.invocations[id] = invocation
	selected.queue = append(selected.queue, invocation)
	b.startInvocationTimerLocked(invocation)
	b.emitLocked(BrokerEvent{
		Type:         BrokerEventInvocationCreated,
		BrowserID:    descriptor.BrowserID,
		TargetID:     descriptor.TargetID,
		Generation:   descriptor.Generation,
		ToolRef:      request.ToolRef,
		ToolName:     descriptor.Name,
		InvocationID: id,
		State:        InvocationQueued,
		Reason:       "admitted",
	})
	signalInvocationQueueLocked(selected)
	b.mu.Unlock()

	return b.waitForAdmissionDispatch(ctx, invocation)
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
	action := b.claimTargetCancellationLocked(invocation, context.Background())
	wait := b.cancellationWaitLocked(invocation, action)
	phase := invocationTimeoutPhase(invocation.invocation.State)
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

// invocationTimeoutPhase names the lane phase an invocation timed out in.
func invocationTimeoutPhase(state InvocationState) string {
	if state == InvocationDispatching {
		return "dispatch"
	}
	if state == InvocationDispatched {
		return "result"
	}
	return "queue"
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
	action := b.claimTargetCancellationLocked(invocation, context.Background())
	b.mu.Unlock()
	selected.dispatchMu.Unlock()
	performTargetCancellation(action)
	b.waitForInvocationLane(invocation)
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
	if result.BrowserInvocationID == "" {
		result.BrowserInvocationID = invocation.browserID
	}
	invocation.reported = true
	invocation.dispatchDone <- invocationDispatch{result: cloneInvokeResult(result), err: err}
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
	action := b.claimTargetCancellationLocked(invocation, context.Background())
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
	action := b.claimTargetCancellationLocked(invocation, ctx)
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
		observation := terminalObservationFromEvent(event, b.maxResultBytes)
		if !invocation.invokedObserved {
			b.bufferEarlyTerminalLocked(event)
			return
		}
		if reason := terminalObservationFreshnessReason(invocation, observation); reason != "" {
			b.finishInvocationLocked(invocation, freshnessFailureResult(invocation, "terminal_provenance", reason, true))
			return
		}
		if reason := invocationCatalogFreshnessReasonLocked(b, invocation); reason != "" {
			b.finishInvocationLocked(invocation, freshnessFailureResult(invocation, "catalog_provenance", reason, true))
			return
		}
		b.applyTerminalObservationLocked(invocation, observation)
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
		reason:        event.Reason,
		generation:    event.Generation,
		browserID:     event.BrowserID,
		targetID:      event.TargetID,
		sequence:      event.Sequence,
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

func (b *StatefulBroker) takeEarlyTerminalLocked(id InvocationID, generation uint64) (terminalObservation, bool) {
	observation, ok := b.earlyTerminals[id]
	if !ok {
		return terminalObservation{}, false
	}
	if observation.generation != 0 && generation != 0 && observation.generation != generation {
		delete(b.earlyTerminals, id)
		b.removeEarlyTerminalOrderIDLocked(id)
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
	if result.BrowserInvocationID == "" {
		result.BrowserInvocationID = invocation.browserID
	}
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
		var dispatchErr error
		if invocation.browserID == "" {
			switch ErrorCode(result.ErrorCode) {
			case ErrorBrowserDisconnected:
				dispatchErr = browserDisconnectedErrorForSession(invocation.selected, "list_targets", sessionLifecycleFailure(invocation.selected))
			case ErrorTargetDetached, ErrorPageNavigated, ErrorInvocationOrphaned:
				dispatchErr = classified(ErrorCode(result.ErrorCode), DefaultErrorMessage(ErrorCode(result.ErrorCode)), result.ErrorDetails, nil)
			}
		}
		b.reportDispatchLocked(invocation, invocation.finalResult, dispatchErr)
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
		ToolName:     invocation.invocation.Tool.Name,
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
		terminal.result.BrowserInvocationID = invocation.browserID
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

func (b *StatefulBroker) finishLifecycleInvocationLocked(invocation *brokerInvocation, state InvocationState, code ErrorCode, reason string, previousGeneration uint64) {
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
		if previousGeneration == 0 {
			previousGeneration = invocation.invocation.Tool.Generation
			if previousGeneration >= currentGeneration && currentGeneration > 0 {
				previousGeneration = currentGeneration - 1
			}
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

func (b *StatefulBroker) terminalizeSessionInvocationsLocked(selected *brokerSession, code ErrorCode, reason string, transitionPrevious ...uint64) {
	state := InvocationError
	if code == ErrorInvocationOrphaned {
		state = InvocationOrphaned
	}
	previousGeneration := uint64(0)
	if len(transitionPrevious) > 0 {
		previousGeneration = transitionPrevious[0]
	}
	if selected.current != nil && !selected.current.terminalized {
		b.finishLifecycleInvocationLocked(selected.current, state, code, reason, previousGeneration)
	}
	for _, invocation := range selected.queue {
		if invocation == nil || invocation.terminalized {
			continue
		}
		b.finishLifecycleInvocationLocked(invocation, state, code, reason, previousGeneration)
	}
	for _, invocation := range b.invocations {
		if invocation.selected != selected || invocation.terminalized {
			continue
		}
		b.finishLifecycleInvocationLocked(invocation, state, code, reason, previousGeneration)
	}
	signalInvocationQueueLocked(selected)
}

// WaitInvocation waits for one terminal broker result and consumes its
// bounded terminal cache entry. Invoke itself remains non-blocking after
// dispatch; terminal-aware adapters wait here before returning a tool result.
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
