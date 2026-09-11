package live

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/input"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/eventcodec"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func (i *liveInvocation) bindPlaybackController() {
	if i == nil || i.endpoints.Inbound == nil {
		return
	}
	var controller sharedaudio.PlaybackController
	if provider, ok := i.ports.Playback.(devices.PlaybackControllerProvider); ok {
		controller = provider.PlaybackController()
	}
	if controller == nil && i.options.Request.ReplayPlan != nil {
		controller = replayVirtualPlaybackController{}
	}
	if controlled, ok := i.endpoints.Inbound.(sharedaudio.PlaybackControlledInbound); ok && controller != nil {
		controlled.SetPlaybackController(controller)
	}
}

type replayVirtualPlaybackController struct{}

func (replayVirtualPlaybackController) StartPlayback(sharedaudio.PlaybackResponse) {}
func (replayVirtualPlaybackController) InterruptPlayback(sharedaudio.PlaybackResponse) (int, bool) {
	return 0, true
}

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
	if h == nil || msg.Type != messages.StreamTypeMessageEnd || msg.Role == messages.RoleTool || (msg.Role != "" && msg.Role != messages.RoleAssistant) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.scheduledAudioCount <= 0 || (!h.activeScheduledAudio && h.observedResponseTerminals-h.scheduledResponseBase >= h.scheduledAudioCount) {
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
	if !h.activeScheduledAudio && h.observedResponseTerminals-h.scheduledResponseBase >= h.scheduledAudioCount {
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
	event := eventcodec.FromMessage(h.request.SessionID, msg)
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
	value := eventcodec.TerminalValue(msg)
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

// observeSessionLifecycle admits the scheduler-backed watchdog at SESSION.OPEN; pre-OPEN UPDATE state suppresses the timer.
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

func (h *handle) observeOutput(msg messages.StreamMessage) {
	if h == nil || !eventcodec.OutputMessage(msg) {
		return
	}
	h.mu.Lock()
	h.outputObserved = true
	h.mu.Unlock()
}

func (h *handle) observeFiniteResponse(msg messages.StreamMessage, complete ...bool) bool {
	if h == nil || !h.request.FinishAfterResponse {
		return false
	}
	h.mu.Lock()
	deferredResponseComplete := len(complete) > 0 && complete[0]
	if h.isToolResponseEnd(msg) {
		h.retireCompletedToolCallsLocked()
		if deferredResponseComplete {
			h.replayResponses++
		}
		finish := deferredResponseComplete && h.canFinishFiniteResponse()
		h.mu.Unlock()
		if finish {
			h.stopGracefully()
		}
		return finish
	}
	// A completed continuation retires the prior tool result; the following assistant boundary may contain another provider tool call, so keep pending calls intact.
	h.observeFiniteResponseMessage(msg)
	if deferredResponseComplete && h.activeScheduledAudio && h.scheduledAudioCount > 0 && h.interruptedScheduledResponses > h.scheduledContinuationTerminals {
		h.scheduledContinuationTerminals++
	}
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
		h.notePendingToolCallLocked(msg)
		return
	}
	if msg.Type == messages.StreamTypeMessageEnd && msg.Role != messages.RoleTool {
		responseWasOpen := h.responseActive || h.responsePending
		h.responseActive, h.responsePending = false, false
		interrupted := h.finiteResponseWasInterrupted(msg)
		if h.pendingToolCalls > 0 || interrupted {
			if h.pendingToolCalls > 0 && !h.pendingToolCallsForResponse(msg.ResponseID) {
				h.replayResponses++
			}
			if interrupted && responseWasOpen && h.activeScheduledAudio && h.scheduledAudioCount > 0 {
				h.interruptedScheduledResponses++
			}
			return
		}
		h.replayResponses++
	}
}

// notePendingToolCall records a provider call by identity when available. A
// single aggregate counter cannot represent two active provider responses: a
// RoleTool boundary for the first batch must not retire the second batch.
func (h *handle) notePendingToolCall(msg messages.StreamMessage) {
	if h == nil {
		return
	}
	callID, _ := providerToolCallIdentity(msg)
	responseID := strings.TrimSpace(msg.ResponseID)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notePendingToolCallLockedWithID(callID, responseID)
}

// notePendingToolCallLocked is used while observeFiniteResponse already owns
// the lifecycle mutex. Keep the lock-free state transition separate so a
// TOOLCALL.END cannot deadlock the delta consumer by reacquiring h.mu.
func (h *handle) notePendingToolCallLocked(msg messages.StreamMessage) {
	callID, _ := providerToolCallIdentity(msg)
	h.notePendingToolCallLockedWithID(callID, strings.TrimSpace(msg.ResponseID))
}

func (h *handle) notePendingToolCallLockedWithID(callID, responseID string) {
	if h.pendingToolCallIDs == nil {
		// Keep manually-constructed test handles and legacy callers on the
		// historical aggregate path; production handles initialize the map.
		h.pendingToolCalls++
		return
	}
	if callID != "" {
		h.pendingToolCallIDs[callID] = struct{}{}
		if responseID != "" {
			if h.pendingToolCallResponses == nil {
				h.pendingToolCallResponses = make(map[string]string)
			}
			h.pendingToolCallResponses[callID] = responseID
		}
	}
	h.pendingToolCalls = len(h.pendingToolCallIDs)
}

// pendingToolCallsForResponse keeps one provider response's terminal from
// being blocked by calls belonging to a different active response. Providers
// that omit response IDs retain the aggregate behavior as a conservative
// compatibility fallback.
func (h *handle) pendingToolCallsForResponse(responseID string) bool {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" || h.pendingToolCallIDs == nil || h.pendingToolCallResponses == nil {
		return h.pendingToolCalls > 0
	}
	for callID := range h.pendingToolCallIDs {
		callResponseID, known := h.pendingToolCallResponses[callID]
		if !known || callResponseID == "" || callResponseID == responseID {
			return true
		}
	}
	return false
}

// retireCompletedToolCalls removes only calls whose local tool result batch has
// reached MESSAGE.END. Calls from another still-active provider response remain
// pending and continue to block finite-session shutdown.
func (h *handle) retireCompletedToolCalls() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.retireCompletedToolCallsLocked()
}

// retireCompletedToolCallsLocked is used while observeFiniteResponse already
// owns the lifecycle mutex. The tool-state snapshot is protected separately
// because provider observation and local tool execution run on different
// workers.
func (h *handle) retireCompletedToolCallsLocked() {
	h.toolMu.Lock()
	completed := make([]string, 0)
	for callID, state := range h.toolContinuations {
		if state != nil && state.toolResponseComplete && strings.TrimSpace(callID) != "" {
			completed = append(completed, callID)
		}
	}
	h.toolMu.Unlock()

	if h.pendingToolCallIDs == nil {
		h.pendingToolCalls = 0
		return
	}
	for _, callID := range completed {
		delete(h.pendingToolCallIDs, callID)
		delete(h.pendingToolCallResponses, callID)
	}
	h.pendingToolCalls = len(h.pendingToolCallIDs)
}
