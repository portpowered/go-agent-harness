package webmcp

import (
	"context"
	"errors"
	"strings"
	"time"
)

type directCancellationPhase string

const (
	directCancellationTargetSelected directCancellationPhase = "target_selected"
	directCancellationObserving      directCancellationPhase = "observing"
	directCancellationDispatched     directCancellationPhase = "cancel_dispatched"
	directCancellationTerminal       directCancellationPhase = "terminal"
)

// directCancellation is a short-lived waiter owned by one selected broker
// session. It deliberately stores no page output: direct cancellation only
// needs a bounded terminal classification and exact identity correlation.
type directCancellation struct {
	target       TargetSelector
	invocationID InvocationID
	session      *brokerSession
	phase        directCancellationPhase
	terminal     chan directCancellationObservation
}

type directCancellationObservation struct {
	eventType  BrowserEventType
	status     string
	errorCode  string
	reason     string
	generation uint64
	at         time.Time
}

func (b *StatefulBroker) cancelDirectInvocation(ctx context.Context, request DirectCancelRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if request.InvocationID == "" {
		return classified(ErrorInvalidToolInput, "the browser invocation ID is required", map[string]any{
			"issues": []ToolResultIssue{{Path: "/invocation_id", Code: "required"}},
		}, ErrInvalidToolInput)
	}
	if request.Target.BrowserID == "" || request.Target.TargetID == "" {
		return staleSelectionError(request.Target.BrowserID, request.Target.TargetID, 0, "exact_browser_and_target_required")
	}
	if b == nil {
		return ErrClosed
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrClosed
	}
	selected := b.selected
	if selected == nil || !selected.active || !selected.context.Connected {
		err := staleSelectionForSession(selected, "selection_not_connected")
		b.mu.Unlock()
		return err
	}
	if selected.context.Key.BrowserID != request.Target.BrowserID || selected.context.Key.TargetID != request.Target.TargetID {
		err := staleSelectionError(request.Target.BrowserID, request.Target.TargetID, selected.context.Generation, "exact_target_not_selected")
		b.mu.Unlock()
		return err
	}
	session := selected.session
	b.mu.Unlock()
	selected.dispatchMu.Lock()

	// Revalidate while holding the dispatch linearization point. Close and
	// target lifecycle paths may retire a selection without waiting for a page
	// command, so a check made before this lock is not sufficient.
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		selected.dispatchMu.Unlock()
		return ErrClosed
	}
	if b.selected != selected || !selected.active || !selected.context.Connected {
		err := staleSelectionForSession(selected, "selection_not_connected")
		b.mu.Unlock()
		selected.dispatchMu.Unlock()
		return err
	}
	if selected.context.Key.BrowserID != request.Target.BrowserID || selected.context.Key.TargetID != request.Target.TargetID {
		err := staleSelectionError(request.Target.BrowserID, request.Target.TargetID, selected.context.Generation, "exact_target_not_selected")
		b.mu.Unlock()
		selected.dispatchMu.Unlock()
		return err
	}
	if session == nil {
		b.mu.Unlock()
		selected.dispatchMu.Unlock()
		return targetAttachError(request.Target, "cancel", ErrClosed)
	}
	// The broker selection and the target session are separate state holders.
	// Recheck the session identity at the command boundary so a stale or
	// accidentally substituted session can never receive a direct cancel for a
	// different target.
	sessionContext := session.Context()
	if sessionContext.Key.BrowserID != request.Target.BrowserID || sessionContext.Key.TargetID != request.Target.TargetID {
		err := staleSelectionError(request.Target.BrowserID, request.Target.TargetID, selected.context.Generation, "exact_target_session_mismatch")
		b.mu.Unlock()
		selected.dispatchMu.Unlock()
		return err
	}
	operation, err := b.beginDirectCancellationLocked(selected, request)
	b.mu.Unlock()
	if err != nil {
		selected.dispatchMu.Unlock()
		return err
	}

	// Keep dispatch and event reconciliation serialized. An adapter may enqueue
	// the terminal response synchronously while returning from CancelWebMCP;
	// the observer is already registered, but it must not be processed as a
	// half-dispatched outcome.
	if err := session.CancelWebMCP(ctx, request.InvocationID); err != nil {
		b.mu.Lock()
		observation, observed := takeDirectCancellationObservation(operation)
		if !observed {
			// The CDP command was attempted even when the browser rejected it.
			// Keep the operation in the dispatched phase so an unconfirmed
			// result cannot be mistaken for a pre-dispatch validation failure.
			operation.phase = directCancellationDispatched
		}
		b.finishDirectCancellationLocked(operation)
		b.mu.Unlock()
		selected.dispatchMu.Unlock()
		if observed {
			return directCancellationResult(operation, observation)
		}
		return directCancellationDispatchFailure(operation, err)
	}
	b.mu.Lock()
	operation.phase = directCancellationDispatched
	b.mu.Unlock()
	selected.dispatchMu.Unlock()

	defer func() {
		b.mu.Lock()
		b.finishDirectCancellationLocked(operation)
		b.mu.Unlock()
	}()
	return b.waitForDirectCancellation(ctx, operation)
}

func (b *StatefulBroker) beginDirectCancellationLocked(selected *brokerSession, request DirectCancelRequest) (*directCancellation, error) {
	if selected == nil {
		return nil, ErrStaleSelection
	}
	if selected.directCancellations == nil {
		selected.directCancellations = make(map[InvocationID]*directCancellation)
	}
	if existing := selected.directCancellations[request.InvocationID]; existing != nil {
		return nil, classified(ErrorInvocationFailed, "a direct cancellation for this invocation is already in progress", map[string]any{
			"browser_id":          string(request.Target.BrowserID),
			"target_id":           string(request.Target.TargetID),
			"invocation_id":       string(request.InvocationID),
			"phase":               string(existing.phase),
			"outcome":             "cancellation_in_progress",
			"side_effect_unknown": true,
		}, nil)
	}
	operation := &directCancellation{
		target:       request.Target,
		invocationID: request.InvocationID,
		session:      selected,
		phase:        directCancellationTargetSelected,
		terminal:     make(chan directCancellationObservation, 1),
	}
	selected.directCancellations[request.InvocationID] = operation
	operation.phase = directCancellationObserving
	return operation, nil
}

func (b *StatefulBroker) finishDirectCancellationLocked(operation *directCancellation) {
	if operation == nil || operation.session == nil || operation.session.directCancellations == nil {
		return
	}
	if current := operation.session.directCancellations[operation.invocationID]; current == operation {
		delete(operation.session.directCancellations, operation.invocationID)
	}
}

func (b *StatefulBroker) observeDirectCancellationLocked(selected *brokerSession, event BrowserEvent) {
	if selected == nil || len(selected.directCancellations) == 0 {
		return
	}
	if event.BrowserID != "" && event.BrowserID != selected.context.Key.BrowserID {
		return
	}
	if event.TargetID != "" && event.TargetID != selected.context.Key.TargetID {
		return
	}
	if event.Type == EventToolResponded {
		operation := selected.directCancellations[event.InvocationID]
		if operation != nil {
			markDirectCancellationTerminal(operation, directCancellationObservationFromEvent(event))
		}
		return
	}
	if !isDirectCancellationLifecycleEvent(event.Type) {
		return
	}
	for _, operation := range selected.directCancellations {
		markDirectCancellationTerminal(operation, directCancellationObservationFromEvent(event))
	}
}

func directCancellationObservationFromEvent(event BrowserEvent) directCancellationObservation {
	return directCancellationObservation{
		eventType:  event.Type,
		status:     event.Status,
		errorCode:  event.ErrorCode,
		reason:     event.Reason,
		generation: event.Generation,
		at:         event.At,
	}
}

func markDirectCancellationTerminal(operation *directCancellation, observation directCancellationObservation) {
	if operation == nil || operation.phase == directCancellationTerminal {
		return
	}
	operation.phase = directCancellationTerminal
	select {
	case operation.terminal <- observation:
	default:
	}
}

func isDirectCancellationLifecycleEvent(eventType BrowserEventType) bool {
	switch eventType {
	case EventPageNavigated, EventFrameNavigated, EventTargetDetached, EventBrowserDisconnected, EventSessionClosed:
		return true
	default:
		return false
	}
}

func (b *StatefulBroker) waitForDirectCancellation(ctx context.Context, operation *directCancellation) error {
	if ctx == nil {
		ctx = context.Background()
	}
	waitCtx, cancel := context.WithTimeout(ctx, DefaultDirectCancellationTimeout)
	defer cancel()

	var sessionDone <-chan struct{}
	if operation != nil && operation.session != nil && operation.session.session != nil {
		sessionDone = operation.session.session.Done()
	}
	for {
		select {
		case observation := <-operation.terminal:
			return directCancellationResult(operation, observation)
		case <-waitCtx.Done():
			if observation, ok := takeDirectCancellationObservation(operation); ok {
				return directCancellationResult(operation, observation)
			}
			return directCancellationUnconfirmedError(operation, waitCtx.Err(), "cancellation_unconfirmed")
		case <-sessionDone:
			if observation, ok := takeDirectCancellationObservation(operation); ok {
				return directCancellationResult(operation, observation)
			}
			var lifecycleCode string
			b.mu.Lock()
			if failure := sessionLifecycleFailure(operation.session); failure != nil {
				if classified, ok := lifecycleClassifiedError(failure); ok {
					lifecycleCode = string(classified.Code)
				}
			}
			b.mu.Unlock()
			return directCancellationLifecycleError(operation, EventSessionClosed, "session_done", lifecycleCode)
		case <-b.closedCh:
			if observation, ok := takeDirectCancellationObservation(operation); ok {
				return directCancellationResult(operation, observation)
			}
			return directCancellationUnconfirmedError(operation, ErrClosed, "cancellation_unconfirmed")
		}
	}
}

func takeDirectCancellationObservation(operation *directCancellation) (directCancellationObservation, bool) {
	if operation == nil || operation.terminal == nil {
		return directCancellationObservation{}, false
	}
	select {
	case observation := <-operation.terminal:
		return observation, true
	default:
		return directCancellationObservation{}, false
	}
}

func directCancellationResult(operation *directCancellation, observation directCancellationObservation) error {
	if directCancellationWasCanceled(observation) {
		return nil
	}
	if isDirectCancellationLifecycleEvent(observation.eventType) {
		return directCancellationLifecycleError(operation, observation.eventType, observation.reason, observation.errorCode)
	}
	details := directCancellationDetails(operation, observation.eventType, "completed_anyway")
	if errorCode := safePageErrorCode(observation.errorCode); errorCode != "" {
		details["page_error_code"] = errorCode
	}
	return classified(ErrorInvocationFailed, "the browser invocation completed despite the cancellation request", details, nil)
}

func directCancellationWasCanceled(observation directCancellationObservation) bool {
	status := strings.ToLower(strings.TrimSpace(observation.status))
	return status == "canceled" || status == "cancelled" || observation.errorCode == string(ErrorInvocationCanceled)
}

func directCancellationUnconfirmedError(operation *directCancellation, cause error, outcome string) error {
	details := directCancellationDetails(operation, "", outcome)
	return classified(ErrorInvocationFailed, "the browser did not provide a correlated terminal cancellation result", details, cause)
}

func directCancellationDispatchFailure(operation *directCancellation, cause error) error {
	// Lifecycle failures retain their dedicated C0 classifications. A caller
	// context failure also keeps its meaning. The adapter's explicit cancel
	// protocol rejection is otherwise represented as invocation_canceled, but
	// that only describes the failed command—not a confirmed page invocation
	// cancellation—so convert it to bounded, non-retryable uncertainty here.
	var classifiedErr *ClassifiedError
	if errors.As(cause, &classifiedErr) && classifiedErr != nil {
		if _, lifecycle := lifecycleClassifiedError(classifiedErr); lifecycle {
			return cause
		}
		if classifiedErr.Code != ErrorInvocationCanceled || classifiedErr.Details["cancel_source"] == "caller" {
			return cause
		}
	}
	return directCancellationUnconfirmedError(operation, cause, "cancellation_unconfirmed")
}

func directCancellationLifecycleError(operation *directCancellation, eventType BrowserEventType, reason, errorCode string) error {
	code := ErrorInvocationOrphaned
	outcome := "session_closed"
	message := "the target session closed before cancellation was confirmed"
	if eventType == EventSessionClosed {
		switch ErrorCode(errorCode) {
		case ErrorBrowserDisconnected:
			eventType = EventBrowserDisconnected
		case ErrorTargetDetached:
			eventType = EventTargetDetached
		case ErrorBrowserProtocol:
			eventType = EventSessionClosed
		}
	}
	switch eventType {
	case EventPageNavigated, EventFrameNavigated:
		code = ErrorPageNavigated
		outcome = "page_navigated"
		message = "the page navigated before cancellation was confirmed"
	case EventTargetDetached:
		code = ErrorTargetDetached
		outcome = "target_detached"
		message = "the target detached before cancellation was confirmed"
	case EventBrowserDisconnected:
		code = ErrorBrowserDisconnected
		outcome = "browser_disconnected"
		message = "the browser disconnected before cancellation was confirmed"
	case EventSessionClosed:
		if reason == BrowserEventBufferFullReason {
			code = ErrorBrowserProtocol
			outcome = "event_stream_closed"
			message = "the browser event stream closed before cancellation was confirmed"
		}
	}
	details := directCancellationDetails(operation, eventType, outcome)
	if safeReason := safePageErrorCode(reason); safeReason != "" {
		details["lifecycle_reason"] = safeReason
	}
	return classified(code, message, details, nil)
}

func directCancellationDetails(operation *directCancellation, eventType BrowserEventType, outcome string) map[string]any {
	details := map[string]any{
		"browser_id":          string(operation.target.BrowserID),
		"target_id":           string(operation.target.TargetID),
		"invocation_id":       string(operation.invocationID),
		"phase":               "cancel",
		"cancel_phase":        string(directCancellationDispatched),
		"outcome":             outcome,
		"terminal_observed":   eventType != "",
		"side_effect_unknown": true,
	}
	if eventType != "" {
		details["terminal_event"] = string(eventType)
	}
	return details
}
