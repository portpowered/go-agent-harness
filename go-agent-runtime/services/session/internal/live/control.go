package live

import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"reflect"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

func (h *handle) Send(ctx context.Context, control session.LiveControl) error {
	if h == nil {
		return session.ErrLiveClosed
	}
	if ctx == nil {
		return errors.New("live control context is required")
	}
	h.mu.Lock()
	if !h.started || h.loop == nil {
		h.mu.Unlock()
		return session.ErrLiveNotStarted
	}
	if h.closed {
		h.mu.Unlock()
		return session.ErrLiveClosed
	}
	loop := h.loop
	h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := h.refreshLiveTools(ctx, loop); err != nil {
		return err
	}
	// Close is teardown, so it must cancel the provider and media bridges before
	// waiting on any in-flight media write. Waiting behind a blocked writer here
	// would make a close control unable to stop that writer.
	if control.Kind == session.LiveControlClose {
		h.Cancel(context.Canceled)
		return nil
	}

	// Reserve the control's place in the same admission sequence used by PCM
	// frames and automatic provider sends. Reservation is non-blocking; waiting
	// for an earlier operation while holding a mutex would deadlock the
	// agent-loop runner when that earlier operation is itself a provider send.
	ackID, ack, err := h.media.RegisterAck()
	if err != nil {
		return err
	}
	event, err := liveControlEvent(control)
	if err != nil {
		h.media.AbortAck(ackID)
		return err
	}
	event.ActorProvidedID = ackID
	if err := loop.SendSessionEvent(ctx, event); err != nil {
		h.media.AbortAck(ackID)
		return err
	}
	select {
	case accepted := <-ack:
		if !accepted {
			return fmt.Errorf("live provider rejected control %q", control.Kind)
		}
		return nil
	case <-ctx.Done():
		h.media.CancelAck(ackID)
		return ctx.Err()
	}
}

// refreshLiveTools publishes a participant's current tool surface before a
// new control enters the provider wire. The update uses the same marked
// admission barrier as text/commit/cancel, so a page-discovery refresh cannot
// overtake already admitted PCM or be overtaken by the following control.
func (h *handle) refreshLiveTools(ctx context.Context, loop *agentloop.AgentLoop) error {
	h.mu.Lock()
	refresh := h.capabilityRefresh
	h.mu.Unlock()
	if refresh == nil {
		return nil
	}
	h.capabilityMu.Lock()
	defer h.capabilityMu.Unlock()
	// A concurrent Send may have refreshed the definitions while this call was
	// waiting for the capability mutex. Re-read the current snapshot before
	// deciding whether another provider update is needed.
	h.mu.Lock()
	current := append([]messages.ToolDefinition(nil), h.toolDefinitions...)
	h.mu.Unlock()
	refreshed, err := refresh(ctx)
	if err != nil {
		return fmt.Errorf("refresh live capabilities: %w", err)
	}
	refreshed = messages.CanonicalToolDefinitions(refreshed)
	if reflect.DeepEqual(current, refreshed) {
		return nil
	}
	ackID, ack, err := h.media.RegisterAck()
	if err != nil {
		return err
	}
	event := messages.StreamMessage{
		Type:            messages.StreamTypeSessionUpdate,
		Value:           messages.NewSessionUpdateValue(&messages.SessionUpdateConfig{Tools: refreshed}),
		ActorProvidedID: ackID,
	}
	if err := loop.SendSessionEvent(ctx, event); err != nil {
		h.media.AbortAck(ackID)
		return err
	}
	select {
	case accepted := <-ack:
		if !accepted {
			return errors.New("live provider rejected capability refresh")
		}
		h.mu.Lock()
		h.toolDefinitions = append([]messages.ToolDefinition(nil), refreshed...)
		if h.request.Capabilities != nil {
			binding := *h.request.Capabilities
			binding.Definitions = append([]messages.ToolDefinition(nil), refreshed...)
			h.request.Capabilities = &binding
		}
		h.mu.Unlock()
		return nil
	case <-ctx.Done():
		h.media.CancelAck(ackID)
		return ctx.Err()
	}
}

func liveControlEvent(control session.LiveControl) (messages.StreamMessage, error) {
	switch control.Kind {
	case session.LiveControlText:
		// The session runner's text admission path ultimately sends this same
		// provider TEXT.DELTA event. Using its ordered session ingress lets the
		// live boundary wait for provider admission without adding a second
		// untracked text queue.
		return messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(control.Text)}, nil
	case session.LiveControlAudioCommit:
		return messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, nil
	case session.LiveControlResponseCancel:
		return messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue()}, nil
	case session.LiveControlResponseCreate:
		return messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()}, nil
	case session.LiveControlClose:
		return messages.StreamMessage{}, errors.New("close control is handled by the live lifecycle")
	default:
		return messages.StreamMessage{}, fmt.Errorf("unsupported live control %q", control.Kind)
	}
}

func (h *handle) Cancel(err error) {
	if h == nil {
		return
	}
	if err == nil {
		err = context.Canceled
	}
	h.mu.Lock()
	h.cancelRequested = true
	// Preserve the first cancellation cause. Provider/media teardown often
	// reports context.Canceled after a typed liveness, persistence, or device
	// failure; replacing that cause would make the public Wait result lose the
	// actionable root error.
	if h.cancelCause == nil {
		h.cancelCause = err
	}
	cancel := h.cancel
	started := h.started
	h.mu.Unlock()
	if started && cancel != nil {
		cancel(err)
	}
}

func (h *handle) stopGracefully() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed || h.gracefulStop || h.cancelRequested ||
		(h.parentCtx != nil && h.parentCtx.Err() != nil && errors.Is(context.Cause(h.parentCtx), session.ErrLiveUserCancellation)) {
		h.mu.Unlock()
		return
	}
	h.gracefulStop = true
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel(nil)
	}
}

func (h *handle) Wait() error {
	if h == nil {
		return session.ErrLiveNotStarted
	}
	h.mu.Lock()
	started := h.started
	startDone := h.startDone
	done := h.done
	h.mu.Unlock()
	if !started {
		return session.ErrLiveNotStarted
	}
	<-startDone
	<-done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.terminalErr
}

func (h *handle) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if h.closed {
		started := h.started
		done := h.done
		h.mu.Unlock()
		if started {
			<-done
		}
		return nil
	}
	h.closed = true
	started := h.started
	cancel := h.cancel
	if started {
		h.cancelRequested = true
		// Close is teardown. Preserve a typed cause already recorded by the
		// session (for example liveness, replay, or device failure) so the
		// provider/media cancellation it triggers cannot mask the first fault.
		if h.cancelCause == nil {
			h.cancelCause = context.Canceled
		}
	}
	h.mu.Unlock()
	if started && cancel != nil {
		cancel(context.Canceled)
	}
	mediaErr := h.media.Close()
	if !started {
		close(h.startDone)
		h.finish(nil)
		return mediaErr
	}
	<-h.done
	return mediaErr
}

func (h *handle) noteCaptureDispatched() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.dispatchedAudioCount < h.scheduledAudioCount {
		h.dispatchedAudioCount++
	}
	wake := h.captureTurnWake
	h.captureTurnWake = make(chan struct{})
	h.mu.Unlock()
	if wake != nil {
		close(wake)
	}
}

func (h *handle) observeToolResult(callID, name string, requestsContinuation bool) {
	_ = h.beginToolResultAdmission(callID, name, requestsContinuation)
}

func providerContinuationFailed(value *messages.MessageEndValue) bool {
	if value == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(value.Status))
	return status == continuationStatusFailed || status == "error"
}

func drainLiveEvents(events <-chan session.LiveEvent, sink session.LiveEventSink, ctx context.Context, sinkErr *error, handle session.LiveHandle) {
	if events == nil {
		return
	}
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			if sink == nil || *sinkErr != nil {
				continue
			}
			if err := sink.Publish(ctx, event); err != nil {
				*sinkErr = fmt.Errorf("publish live event: %w", err)
				handle.Cancel(*sinkErr)
			}
		default:
			return
		}
	}
}

func sessionSendOutcomeForError(ctx context.Context, err error) messages.SessionSendOutcome {
	if err == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if errors.Is(err, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: err}
	}
	if errors.Is(err, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: err}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
}

func finiteResponseWasInterrupted(msg messages.StreamMessage) bool {
	value, ok := msg.Value.(*messages.MessageEndValue)
	return ok && value != nil && value.TerminalReason == messages.TerminalReasonPartialOutput
}

func (h *handle) isToolResponseEnd(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeMessageEnd && msg.Role == messages.RoleTool
}

func (h *handle) shouldFinishFiniteResponse(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeMessageEnd && msg.Role != messages.RoleTool && !finiteResponseWasInterrupted(msg) && h.canFinishFiniteResponse()
}

func (h *handle) canFinishFiniteResponse() bool {
	if !h.request.FinishAfterResponse || h.responseActive || h.responsePending {
		return false
	}
	providerCloseExpected := h.request.ReplayPlan != nil && h.request.ReplayPlan.ProviderCloseExpected
	responseCount, responseTarget := h.replayResponses, h.replayResponseTarget()
	if h.captureResponseTarget > 0 {
		responseTarget = h.captureResponseTarget
	}
	if h.scheduledAudioCount > 0 {
		// Scheduled barge-in resolves input at its owned partial terminal; count
		// every scheduled terminal after the optional opening response.
		responseCount = h.observedResponseTerminals
		responseTarget = h.scheduledResponseBase + h.scheduledAudioCount
	}
	return h.captureComplete && h.responseStarted && h.pendingToolCalls == 0 && responseCount >= responseTarget && !h.gracefulStop && !h.cancelRequested && !providerCloseExpected
}

func (h *handle) replayResponseTarget() int {
	target := 1
	if h.request.ReplayPlan != nil && len(h.request.ReplayPlan.AudioTurns) > 0 {
		target = len(h.request.ReplayPlan.AudioTurns)
	}
	if h.request.ExpectedResponses > 0 {
		target = h.request.ExpectedResponses
	}
	return target
}

func shouldCancelMediaPumpFor(name string, pumpErr error, ctx context.Context) bool {
	if name == "playback" && errors.Is(pumpErr, devices.ErrPlaybackInput) {
		return false
	}
	return shouldCancelMediaPump(pumpErr, ctx)
}
