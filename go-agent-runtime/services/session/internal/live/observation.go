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

func (h *handle) consumeMessage(ctx context.Context, loop *agentloop.AgentLoop, msg messages.StreamMessage, allowOpening bool) bool {
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
	if responseComplete {
		h.signalResponseWake()
	}
	return responseComplete
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

func (h *handle) finishMessageObservation(msg messages.StreamMessage) {
	// SESSION.CLOSE is the provider's terminal application boundary. Publish
	// it before initiating cleanup; transport Done remains the join signal,
	// not a prerequisite for asking an otherwise-open connection to close.
	if msg.Type == messages.StreamTypeSessionClose && !h.deferProviderClose() {
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
		// A plain opening prompt is a finite turn boundary only when this
		// invocation has no separately admitted capture source. Persistent and
		// scheduled audio must reach their own EOF/boundary policy first.
		h.markCaptureComplete()
	}
}

// openingAdmissionRequired protects rich opening turns, such as an image
// queued ahead of a finite audio source. The capture owner must not race that
// message into the provider's ordered ingress before the opening content has
// crossed the provider adapter.
func (h *handle) openingAdmissionRequired() bool {
	return h != nil && len(h.request.OpeningContentParts) > 0
}

func (h *handle) markOpeningAdmitted(err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	if err != nil && h.openingAdmissionErr == nil {
		h.openingAdmissionErr = err
	}
	h.mu.Unlock()
	h.openingReadyOnce.Do(func() { close(h.openingReady) })
}

func (h *handle) waitOpeningReady(ctx context.Context) error {
	if !h.openingAdmissionRequired() {
		return nil
	}
	if ctx == nil {
		return errors.New("opening admission context is required")
	}
	select {
	case <-h.openingReady:
		h.mu.Lock()
		err := h.openingAdmissionErr
		h.mu.Unlock()
		return err
	case <-h.done:
		h.mu.Lock()
		err := h.terminalErr
		h.mu.Unlock()
		if err != nil {
			return err
		}
		return session.ErrLiveClosed
	case <-ctx.Done():
		return ctx.Err()
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
	if msg.Type == messages.StreamTypeSessionClose {
		// A provider SESSION.CLOSE is an irreversible admission boundary. Keep
		// its concrete reason/classification even when a queued MESSAGE.END is
		// observed afterward and would otherwise synthesize a blank summary.
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
	h.captureComplete = true
	shouldFinish := h.canFinishFiniteResponse()
	h.mu.Unlock()
	if shouldFinish {
		h.stopGracefully()
	}
}

func (h *handle) observeFiniteResponse(msg messages.StreamMessage, toolContinuationComplete ...bool) bool {
	if h == nil || !h.request.FinishAfterResponse {
		return false
	}
	h.mu.Lock()
	if h.isToolResponseEnd(msg) {
		h.pendingToolCalls = 0
		h.mu.Unlock()
		return false
	}
	h.observeFiniteResponseMessage(msg, len(toolContinuationComplete) > 0 && toolContinuationComplete[0])
	shouldFinish := h.shouldFinishFiniteResponse(msg)
	h.mu.Unlock()
	if shouldFinish {
		h.stopGracefully()
	}
	return msg.Type == messages.StreamTypeMessageEnd && msg.Role != messages.RoleTool
}

func (h *handle) observeFiniteResponseMessage(msg messages.StreamMessage, toolContinuationComplete bool) {
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
		// The provider's first response can terminate after emitting a tool
		// call. That boundary is the tool-request response, not the finite
		// customer-facing response; the tool result and its continuation still
		// belong to the same invocation.
		if h.pendingToolCalls > 0 {
			return
		}
		if toolContinuationComplete {
			return
		}
		// A barge-in cancellation closes the interrupted response's wire
		// boundary, but it is not a completed finite response. Keep the
		// invocation open for the provider's replacement response; otherwise
		// an output-time correction can be stopped between its replacement
		// TOOLCALL.START and the tool result admission.
		if finiteResponseWasInterrupted(msg) {
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
