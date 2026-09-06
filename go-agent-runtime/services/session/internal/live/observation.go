package live

import (
	"context"
	"errors"
	"fmt"

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
			// AgentLoop.Run establishes a publication barrier before it
			// returns, but its context is cancelled as part of that return.
			// Drain the already published kernel deltas before terminal
			// delivery so a fast provider cannot lose its final text/audio
			// boundary merely because the consumer was one scheduling step
			// behind.
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

// consumeCapabilityEvents drains the participant-owned browser/tool watcher
// on its own bounded worker. A stalled room or CLI observer cannot block the
// broker watcher, provider reader, or media admission path; publish records a
// bounded overflow when the public event consumer falls behind.
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

func (h *handle) consumeMessage(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage, allowOpening bool) {
	h.observeTerminalValue(msg)
	h.observeProviderLiveness(ctx, msg)
	if allowOpening {
		h.observeOpeningPolicies(ctx, loop, msg)
	}
	continuationErr := h.observeToolLifecycle(msg)
	h.publishMessage(msg) //nolint:contextcheck // recording owns the invocation evidence context.
	if continuationErr != nil {
		h.Cancel(continuationErr)
	}
	h.observeFiniteResponse(msg)
	h.finishMessageObservation(msg)
	if allowOpening && msg.Type == messages.StreamTypeSessionOpen {
		h.sendOpeningMessage(ctx, loop)
	}
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

func (h *handle) finishMessageObservation(msg messages.StreamMessage) {
	// SESSION.CLOSE is the provider's terminal application boundary. Publish
	// it before initiating cleanup; transport Done remains the join signal,
	// not a prerequisite for asking an otherwise-open connection to close.
	if msg.Type == messages.StreamTypeSessionClose {
		h.stopGracefully()
	}
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
		// A rich message that requested a response is itself the finite turn
		// boundary. A queued rich message remains open for the following audio
		// commit, which owns the response boundary instead.
		if requestResponse && h.request.FinishAfterResponse {
			h.markCaptureComplete()
		}
		return
	}
	if err := loop.Send(ctx, []messages.Message{messages.NewTextMessage(messages.RoleUser, prompt)}); err != nil {
		h.failOpeningMessage(err)
	}
}

func (h *handle) claimOpeningMessage() (string, []messages.ContentPart, session.LiveOpeningMessageResponse, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.openingSent || h.closed ||
		(!h.request.OpeningPromptPresent && h.request.OpeningPrompt == "" && len(h.request.OpeningContentParts) == 0) {
		return "", nil, session.LiveOpeningMessageQueued, false
	}
	h.openingSent = true
	return h.request.OpeningPrompt, input.CloneContentParts(h.request.OpeningContentParts), h.request.OpeningMessageResponse, true
}

func (h *handle) failOpeningMessage(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	h.pumpErr = err
	h.mu.Unlock()
	h.Cancel(err)
}

// observeTerminalValue retains the most specific provider-authored terminal
// value seen during the invocation. The final lifecycle event is emitted
// after all workers join, so keeping this typed value here lets hosts observe
// provider-close metadata even when graceful replay cleanup cancels the loop
// before a second transport path can synthesize it.
func (h *handle) observeTerminalValue(msg messages.StreamMessage) {
	if h == nil {
		return
	}
	value := terminalValueForMessage(msg)
	if value == nil {
		return
	}
	h.mu.Lock()
	h.terminalValue = value
	h.mu.Unlock()
	h.terminalOnce.Do(func() { close(h.terminalObserved) })
}

func terminalValueForMessage(msg messages.StreamMessage) *messages.SessionCloseValue {
	if msg.Type == messages.StreamTypeSessionClose {
		candidate, ok := msg.Value.(*messages.SessionCloseValue)
		if !ok || candidate == nil {
			return nil
		}
		copy := *candidate
		return &copy
	}
	if msg.Type != messages.StreamTypeMessageEnd || msg.Role == messages.RoleTool {
		return nil
	}
	candidate, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || candidate == nil {
		return nil
	}
	return sessionCloseValueFromMessageEnd(candidate)
}

func sessionCloseValueFromMessageEnd(value *messages.MessageEndValue) *messages.SessionCloseValue {
	if value == nil {
		return nil
	}
	return &messages.SessionCloseValue{
		Type:               "session_close",
		Classification:     "",
		TerminalReason:     value.TerminalReason,
		TerminalProvenance: value.TerminalProvenance,
		OutputState:        value.OutputState,
	}
}

func (h *handle) markCaptureComplete() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.captureComplete = true
	shouldFinish := h.canFinishFiniteResponse()
	h.mu.Unlock()
	if shouldFinish {
		h.stopGracefully()
	}
}

func (h *handle) observeFiniteResponse(msg messages.StreamMessage) {
	if h == nil || !h.request.FinishAfterResponse {
		return
	}
	h.mu.Lock()
	if h.isToolResponseEnd(msg) {
		h.pendingToolCalls = 0
		h.mu.Unlock()
		return
	}
	h.observeFiniteResponseMessage(msg)
	shouldFinish := h.shouldFinishFiniteResponse(msg)
	h.mu.Unlock()
	if shouldFinish {
		h.stopGracefully()
	}
}

func (h *handle) observeFiniteResponseMessage(msg messages.StreamMessage) {
	if msg.Type == messages.StreamTypeMessageStart {
		if msg.Role != messages.RoleTool {
			h.responseStarted = true
			h.responseActive = true
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
		h.responseActive = false
		h.responsePending = false
		h.replayResponses++
		close(h.replayResponseWake)
		h.replayResponseWake = make(chan struct{})
	}
}

func (h *handle) isToolResponseEnd(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeMessageEnd && msg.Role == messages.RoleTool
}

func (h *handle) shouldFinishFiniteResponse(msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeMessageEnd || msg.Role == messages.RoleTool {
		return false
	}
	return h.canFinishFiniteResponse()
}

// canFinishFiniteResponse is called under mu both when response completion
// arrives and when EOF arrives after an already completed response.
func (h *handle) canFinishFiniteResponse() bool {
	if !h.request.FinishAfterResponse || h.responseActive || h.responsePending {
		return false
	}
	providerCloseExpected := h.request.ReplayPlan != nil && h.request.ReplayPlan.ProviderCloseExpected
	return h.captureComplete && h.responseStarted && h.pendingToolCalls == 0 &&
		h.replayResponses >= h.replayResponseTarget() && !h.gracefulStop &&
		!h.cancelRequested && !providerCloseExpected
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

// waitForResponseStart waits for the provider's first response lifecycle
// event. A client-owned MESSAGE.END can be accepted into an asynchronous
// response-intent queue before the provider has written response.created; a
// finite barge turn must wait for that authoritative boundary before sending
// RESPONSE.CANCEL, otherwise its input can win the race and trigger an
// implicit cancellation after the new audio has already crossed the wire.
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

// responseIsActive reports whether the provider currently owns a non-terminal
// assistant response. It is intentionally a snapshot: the caller must send a
// cancellation through the ordered control path so a response boundary cannot
// race a following finite audio turn.
func (h *handle) responseIsActive() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return (h.responseActive || h.responsePending) && !h.cancelRequested && !h.closed
}

// observeSessionLifecycle admits the scheduler-backed configuration watchdog
// at the provider's SESSION.OPEN boundary. The timer is created before the
// OPEN observation is published, which gives deterministic hosts a stable
// point at which advancing their clock starts the bounded wait. An UPDATE
// received before OPEN is retained and suppresses the timer entirely.
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
