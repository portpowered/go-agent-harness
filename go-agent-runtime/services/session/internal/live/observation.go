package live

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/input"
)

func (h *handle) consumeDeltas(ctx context.Context, loop *agentloop.AgentLoop) {
	defer h.runWG.Done()
	for {
		msg, err := loop.Deltas().ReadContext(ctx)
		if err != nil {
			for {
				pending, ok := loop.Deltas().Read()
				if !ok {
					return
				}
				h.consumeMessage(h.evidenceContext(), loop, pending, false) //nolint:contextcheck // drain uses the invocation evidence context after runner cancellation.
			}
		}
		h.consumeMessage(ctx, loop, msg, true)
	}
}

func (h *handle) consumeCapabilityEvents(ctx context.Context, loop *agentloop.AgentLoop, events <-chan session.LiveCapabilityEvent) {
	defer h.runWG.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if err := h.handleCapabilityEvent(ctx, loop, events, event); err != nil {
				if ctx.Err() == nil {
					h.Cancel(err)
				}
				return
			}
		}
	}
}

func (h *handle) consumeMessage(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage, allowOpening bool) bool {
	h.observeOutput(msg)
	h.observeTerminalValue(msg)
	h.observeResponseTerminal(msg)
	h.observeProviderLiveness(ctx, msg)
	if allowOpening {
		h.observeOpeningPolicies(ctx, loop, msg)
	}
	continuationErr, toolContinuationComplete := h.observeToolLifecycle(msg)
	h.publishMessage(msg) //nolint:contextcheck // recording owns the invocation evidence context.
	if continuationErr != nil {
		h.Cancel(continuationErr)
	}
	responseComplete := h.observeFiniteResponse(msg, toolContinuationComplete)
	h.finishMessageObservation(msg)
	if allowOpening && msg.Type == messages.StreamTypeSessionOpen {
		h.sendOpeningMessage(ctx, loop)
	}
	if responseComplete || (msg.Type == messages.StreamTypeMessageEnd && msg.Role != messages.RoleTool) {
		h.signalResponseWake()
	}
	return responseComplete
}

func (h *handle) observeResponseTerminal(msg messages.StreamMessage) {
	if h == nil || msg.Type != messages.StreamTypeMessageEnd || msg.Role == messages.RoleTool ||
		(msg.Role != "" && msg.Role != messages.RoleAssistant) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.scheduledAudioCount <= 0 || h.observedResponseTerminals-h.scheduledResponseBase >= h.scheduledAudioCount {
		return
	}
	if responseID := strings.TrimSpace(msg.ResponseID); responseID != "" {
		if h.observedResponseIDs == nil {
			h.observedResponseIDs = make(map[string]struct{})
		}
		if _, seen := h.observedResponseIDs[responseID]; seen {
			return
		}
		h.observedResponseIDs[responseID] = struct{}{}
	}
	h.observedResponseTerminals++
	wake := h.responseTerminalWake
	if wake == nil {
		wake = make(chan struct{})
	}
	h.responseTerminalWake = make(chan struct{})
	if h.observedResponseTerminals-h.scheduledResponseBase >= h.scheduledAudioCount {
		h.observedResponseIDs = nil
	}
	close(wake)
}

func (h *handle) signalResponseWake() {
	if h == nil {
		return
	}
	h.mu.Lock()
	close(h.replayResponseWake)
	h.replayResponseWake = make(chan struct{})
	h.mu.Unlock()
}

func (h *handle) observeOpeningPolicies(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage) {
	h.observeSessionLifecycle(ctx, msg)
	if msg.Type == messages.StreamTypeSessionUpdated {
		h.replayReadyOnce.Do(func() { close(h.replayReady) })
	}
	h.observeFirstTurn(ctx, msg)
	h.observeRateLimit(loop, msg)
}

func (h *handle) publishMessage(msg messages.StreamMessage) {
	event := eventFromMessage(h.request.SessionID, msg)
	h.recordMessage(session.LiveRecord{Direction: session.LiveRecordAgent, Timestamp: event.Timestamp, Message: msg})
	h.publish(event, false)
}

func (h *handle) sendOpeningMessage(ctx context.Context, loop *agentloop.AgentLoop) {
	prompt, parts, responseMode, ok := h.claimOpeningMessage()
	if !ok {
		return
	}
	if len(parts) > 0 {
		content := make([]messages.ContentPart, 0, len(parts)+1)
		if prompt != "" {
			content = append(content, messages.TextPart{Text: prompt})
		}
		content = append(content, parts...)
		requestResponse := responseMode != session.LiveOpeningMessageQueued
		if err := loop.SendSessionMessage(ctx, messages.Message{Role: messages.RoleUser, ContentParts: content}, requestResponse); err != nil {
			h.failOpeningMessage(err)
			return
		}
		if requestResponse && h.request.FinishAfterResponse && !h.captureSourceIsActive() {
			h.markCaptureComplete()
		}
		return
	}
	if err := loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, prompt)}); err != nil {
		h.failOpeningMessage(err)
		return
	}
	if h.request.FinishAfterResponse && !h.captureSourceIsActive() {
		h.markCaptureComplete()
	}
}

const deferredImageOpeningPrompt = "Use the attached image to answer the user's next spoken question."

func (h *handle) claimOpeningMessage() (string, []messages.ContentPart, session.LiveOpeningMessageResponse, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.openingSent || h.closed ||
		(!h.request.OpeningPromptPresent && h.request.OpeningPrompt == "" && len(h.request.OpeningContentParts) == 0) {
		return "", nil, session.LiveOpeningMessageQueued, false
	}
	h.openingSent = true
	prompt := h.request.OpeningPrompt
	parts := input.CloneContentParts(h.request.OpeningContentParts)
	if prompt == "" && h.request.OpeningMessageResponse == session.LiveOpeningMessageQueued && hasImageContentPart(parts) {
		prompt = deferredImageOpeningPrompt
	}
	return prompt, parts, h.request.OpeningMessageResponse, true
}

func hasImageContentPart(parts []messages.ContentPart) bool {
	for _, part := range parts {
		switch part.(type) {
		case messages.ImagePart:
			return true
		}
	}
	return false
}

func (h *handle) failOpeningMessage(err error) {
	if err == nil {
		return
	}
	h.markOpeningAdmitted(err)
	h.mu.Lock()
	h.pumpErr = err
	h.mu.Unlock()
	h.Cancel(err)
}

func (h *handle) observeTerminalValue(msg messages.StreamMessage) {
	if h == nil {
		return
	}
	value := terminalValueForMessage(msg)
	if value == nil {
		return
	}
	h.mu.Lock()
	if msg.Type == messages.StreamTypeSessionClose {
		if !h.providerCloseObserved || h.terminalValue == nil {
			h.terminalValue = value
		}
		h.providerCloseObserved = true
	} else if !h.providerCloseObserved {
		h.terminalValue = value
	}
	h.mu.Unlock()
	h.terminalOnce.Do(func() { close(h.terminalObserved) })
}

func (h *handle) markCaptureComplete() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if !h.captureComplete {
		h.captureComplete = true
		if h.captureSourceActive && h.scheduledAudioCount == 0 && h.request.FinishAfterResponse {
			h.captureResponseTarget = h.replayResponses + 1
		}
	}
	shouldFinish := h.canFinishFiniteResponse()
	h.mu.Unlock()
	if shouldFinish {
		h.stopGracefully()
	}
}

func (h *handle) waitForResponseStart(ctx context.Context) error {
	if h == nil {
		return context.Canceled
	}
	if ctx == nil {
		return errors.New("response start context is required")
	}
	for {
		h.mu.Lock()
		if h.responseObserved > 0 {
			h.mu.Unlock()
			return nil
		}
		wake := h.responseStartWake
		h.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// responseIsActive snapshots provider ownership; cancellation uses ordered control.
func (h *handle) responseIsActive() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return (h.responseActive || h.responsePending) && !h.cancelRequested && !h.closed
}

// observeSessionLifecycle admits the scheduler-backed watchdog at SESSION.OPEN.
// Pre-OPEN UPDATE state suppresses the timer.
func (h *handle) observeSessionLifecycle(ctx context.Context, msg messages.StreamMessage) {
	if msg.Type == messages.StreamTypeSessionUpdated {
		h.policyMu.Lock()
		h.sessionUpdatedSeen = true
		h.policyMu.Unlock()
		h.sessionUpdatedOnce.Do(func() { close(h.sessionUpdatedSignal) })
		return
	}
	if msg.Type != messages.StreamTypeSessionOpen || !h.request.RequireSessionUpdated {
		return
	}
	h.policyMu.Lock()
	if h.sessionUpdatedSeen || h.sessionUpdatedTimerScheduled {
		h.policyMu.Unlock()
		return
	}
	h.sessionUpdatedTimerScheduled = true
	h.policyMu.Unlock()

	timeout := h.request.SessionUpdatedTimeout
	if timeout == 0 {
		timeout = defaultSessionUpdatedTimeout
	}
	timer := h.scheduler.NewTimer(timeout)
	if timer == nil {
		h.Cancel(fmt.Errorf("create session.updated timer: %w", session.ErrLiveSchedulerUnavailable))
		return
	}
	select {
	case h.sessionUpdatedTimerReady <- timer:
	case <-ctx.Done():
		timer.Stop()
	}
}

func (h *handle) observeFiniteResponse(msg messages.StreamMessage, complete ...bool) bool {
	if h == nil || !h.request.FinishAfterResponse {
		return false
	}
	h.mu.Lock()
	if h.isToolResponseEnd(msg) {
		h.pendingToolCalls = 0
		h.mu.Unlock()
		return false
	}
	// A completed continuation only retires the preceding tool result.  A
	// single assistant boundary may immediately contain the next provider tool
	// call, whose TOOLCALL.END already incremented pendingToolCalls before this
	// MESSAGE.END arrived.  Resetting the aggregate here would let a finite
	// invocation stop after the first continuation and strand that next call.
	// The RoleTool MESSAGE.END path above retires the previous result; leave the
	// current response's pending calls intact.
	h.observeFiniteResponseMessage(msg)
	finish := h.shouldFinishFiniteResponse(msg)
	h.mu.Unlock()
	if finish {
		h.stopGracefully()
	}
	return msg.Type == messages.StreamTypeMessageEnd && msg.Role != messages.RoleTool
}

func (h *handle) observeFiniteResponseMessage(msg messages.StreamMessage) {
	if msg.Type == messages.StreamTypeMessageStart {
		if msg.Role != messages.RoleTool {
			h.responseStarted, h.responseActive = true, true
			h.responseObserved++
			close(h.responseStartWake)
			h.responseStartWake = make(chan struct{})
		}
		return
	}
	if msg.Type == messages.StreamTypeToolCallEnd {
		h.pendingToolCalls++
		return
	}
	if msg.Type == messages.StreamTypeMessageEnd && msg.Role != messages.RoleTool {
		h.responseActive, h.responsePending = false, false
		if h.pendingToolCalls > 0 || finiteResponseWasInterrupted(msg) {
			return
		}
		h.replayResponses++
	}
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
