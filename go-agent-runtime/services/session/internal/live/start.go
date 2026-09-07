package live

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func (h *handle) start(runCtx context.Context) error {
	defer h.startFinish.Do(func() { close(h.startDone) })
	toolExecutor, toolDefinitions, inferencer, err := h.prepareStart(runCtx)
	if err != nil {
		return h.failStart(runCtx, err)
	}
	loop, err := h.buildLoop(inferencer, toolExecutor, toolDefinitions)
	if err != nil {
		return h.failStart(runCtx, err)
	}
	capabilityWatch, err := h.installLoop(loop)
	if err != nil {
		return h.failStart(runCtx, err)
	}
	durationTimer, err := h.newDurationTimer()
	if err != nil {
		return h.failStart(runCtx, err)
	}
	h.prepareReplayCompletion()
	h.publish(session.LiveEvent{Kind: string(session.LiveEventStarted), SessionID: h.request.SessionID, Critical: true}, false) //nolint:contextcheck // start publication uses the invocation evidence context.
	watchEvents := capabilityEventStream(runCtx, capabilityWatch)
	h.launchWorkers(runCtx, loop, durationTimer, watchEvents)
	return nil
}

func (h *handle) prepareStart(runCtx context.Context) (messages.ToolExecutor, []messages.ToolDefinition, messages.SessionInferencer, error) {
	if err := runCtx.Err(); err != nil {
		return nil, nil, nil, err
	}
	if err := h.validateTimingPolicy(); err != nil {
		return nil, nil, nil, err
	}
	toolExecutor, toolDefinitions, err := h.admitCapabilities(runCtx)
	if err != nil {
		return nil, nil, nil, err
	}
	inferencer, err := h.factory(runCtx, h.request)
	if err != nil {
		return nil, nil, nil, err
	}
	if inferencer == nil {
		return nil, nil, nil, errors.New("live inferencer factory returned nil")
	}
	if flusher, ok := inferencer.(interface{ FlushCapture() error }); ok {
		h.mu.Lock()
		h.captureFlush = flusher.FlushCapture
		h.mu.Unlock()
	}
	if err := runCtx.Err(); err != nil {
		return nil, nil, nil, err
	}
	return toolExecutor, toolDefinitions, inferencer, nil
}

func (h *handle) buildLoop(inferencer messages.SessionInferencer, toolExecutor messages.ToolExecutor, toolDefinitions []messages.ToolDefinition) (*agentloop.AgentLoop, error) {
	capturing := &capturingInferencer{
		inner:             inferencer,
		media:             h.media,
		continuous:        h.request.OutputAudioContinuous,
		flushOutbound:     h.request.FinishAfterResponse,
		onDispatch:        h.observeProviderDispatch,
		onToolResult:      h.beginToolResultAdmission,
		onContinuation:    h.beginContinuationAdmission,
		onOpeningAdmitted: func() { h.markOpeningAdmitted(nil) },
		onProviderDone:    h.providerDone,
		onMediaAttached:   h.setProviderMediaAttached,
	}
	options := []agentloop.Option{
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(capturing),
		agentloop.WithBufferCapacity(h.eventCapacity),
	}
	if h.scheduler != nil {
		options = append(options, agentloop.WithClock(h.scheduler))
	}
	if toolExecutor == nil {
		// Explicitly suppress definitions when a host has no tool edge. This
		// keeps an empty embedded capability set empty and avoids the loop's
		// default tool executor becoming observable in a live session.
		options = append(options, agentloop.WithToolExecutionDisabled())
		return agentloop.New(options...)
	}
	if h.request.ToolExecutionTimeout > 0 {
		toolExecutor = newTimedToolExecutor(toolExecutor, h.scheduler, h.request.ToolExecutionTimeout)
	}
	if h.providerLivenessEnabled() {
		toolExecutor = livenessToolExecutor{inner: toolExecutor, handle: h}
	}
	h.mu.Lock()
	explicitCapability := h.request.Capabilities != nil && !h.request.Capabilities.InheritDefaults
	h.mu.Unlock()
	toolExecutor = restrictToolExecutor(toolExecutor, toolDefinitions, explicitCapability)
	toolExecutor = activeCaptureToolExecutor{inner: toolExecutor, wait: h.waitForActiveCaptureTurn}
	options = append(options, agentloop.WithToolExecutor(toolExecutor))
	if len(toolDefinitions) > 0 {
		options = append(options, agentloop.WithTools(toolDefinitions))
	}
	// An injected session inferencer receives provider configuration through the same
	// loop boundary as a native provider. Forward the admitted catalog so fixtures
	// observe the exact surface that the runtime executes.
	if h.request.ReplayPlan == nil && h.request.Replay.InputCapturePath == "" && (len(toolDefinitions) > 0 || h.request.Capabilities != nil) {
		options = append(options, agentloop.WithSessionConfig(messages.SessionUpdateConfig{
			Instructions: h.request.Instructions,
			Model:        h.request.Model,
			Tools:        toolDefinitions,
		}))
	}
	return agentloop.New(options...)
}

// activeCaptureToolExecutor keeps tool results behind the next active audio turn.
type activeCaptureToolExecutor struct {
	inner messages.ToolExecutor
	wait  func(context.Context) error
}

func (e activeCaptureToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if e.wait != nil {
		if err := e.wait(ctx); err != nil {
			return messages.ToolCallResponse{}, err
		}
	}
	return e.inner.Execute(ctx, call)
}
func configureActiveScheduledAudio(handle session.LiveHandle, active bool) {
	if runtimeHandle, ok := handle.(interface{ configureActiveScheduledAudio(bool) }); ok {
		runtimeHandle.configureActiveScheduledAudio(active)
	}
}
func (h *handle) configureActiveScheduledAudio(active bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.activeScheduledAudio = active
	h.mu.Unlock()
}
func (h *handle) waitForActiveCaptureTurn(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("active capture turn context is required")
	}
	for {
		h.mu.Lock()
		waiting := h.activeScheduledAudio && h.scheduledAudioCount > 1 && h.dispatchedAudioCount > 0 && h.dispatchedAudioCount < h.scheduledAudioCount
		wake := h.captureTurnWake
		h.mu.Unlock()
		if !waiting {
			return nil
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (h *handle) finiteAudioResponseError() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	incomplete := h.captureSourceActive && (h.request.FinishAfterResponse || h.request.ExpectedResponses > 0) && !h.gracefulStop && h.scheduledAudioCount == 0
	h.mu.Unlock()
	if incomplete {
		return session.ErrLiveAudioResponseIncomplete
	}
	return nil
}

func (h *handle) emitSynthesizedSessionClose() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if !h.gracefulStop || h.cancelRequested || h.providerCloseObserved || h.localCloseObserved || h.scheduledAudioCount == 0 || h.request.ReplayPlan != nil || h.request.Replay.InputCapturePath != "" {
		h.mu.Unlock()
		return
	}
	value := messages.NewSessionCloseValueWithTerminal(h.request.SessionID, "client_close", string(messages.TerminalReasonLoopSynthesizedCompletion), messages.TerminalReasonLoopSynthesizedCompletion, messages.TerminalProvenanceLoop, messages.TerminalOutputComplete)
	h.localCloseObserved = true
	h.terminalValue = cloneLiveTerminalValue(value)
	h.mu.Unlock()
	h.publishMessage(messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: value})
}

// restrictToolExecutor keeps provider calls inside the invocation capability surface.
func restrictToolExecutor(executor messages.ToolExecutor, definitions []messages.ToolDefinition, enforceEmpty bool) messages.ToolExecutor {
	if replacement, ok := executor.(interface{ AllowUnadvertisedTools() bool }); ok && replacement.AllowUnadvertisedTools() {
		return executor
	}
	if executor == nil || (!enforceEmpty && len(definitions) == 0) {
		return executor
	}
	allowed := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.Name != "" {
			allowed[definition.Name] = struct{}{}
		}
	}
	if len(allowed) == 0 && !enforceEmpty {
		return executor
	}
	return allowlistedToolExecutor{inner: executor, allowed: allowed}
}

type allowlistedToolExecutor struct {
	inner   messages.ToolExecutor
	allowed map[string]struct{}
}

func (e allowlistedToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if _, ok := e.allowed[call.Name]; !ok {
		return messages.ToolCallResponse{
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    fmt.Sprintf("tool %q is not available in the current capability set", call.Name),
		}, nil
	}
	return e.inner.Execute(ctx, call)
}

func (h *handle) installLoop(loop *agentloop.AgentLoop) (func(context.Context) <-chan session.LiveCapabilityEvent, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, session.ErrLiveClosed
	}
	h.loop = loop
	return h.capabilityWatch, nil
}

func (h *handle) newDurationTimer() (platformclock.Timer, error) {
	if h.request.MaxDuration <= 0 {
		return nil, nil
	}
	timer := h.scheduler.NewTimer(h.request.MaxDuration)
	if timer == nil {
		return nil, fmt.Errorf("create live duration timer: %w", session.ErrLiveSchedulerUnavailable)
	}
	return timer, nil
}

func (h *handle) prepareReplayCompletion() {
	// An explicit capture source owns the boundary and must send its bytes first.
	if h.captureSourceIsActive() {
		return
	}
	plan := h.request.ReplayPlan
	if plan != nil && len(plan.AudioTurns) > 0 {
		return
	}
	if plan != nil && plan.OpeningPromptPresent && h.request.FinishAfterResponse {
		h.markCaptureComplete()
	}
	// Raw replays without an opening prompt may lack session.closed; let the
	// response terminal boundary finish the invocation when it completes.
	if h.request.FinishAfterResponse && h.request.Replay.InputCapturePath != "" &&
		(plan == nil || plan.StopAfterResponse) {
		h.markCaptureComplete()
	}
}

func capabilityEventStream(ctx context.Context, watch func(context.Context) <-chan session.LiveCapabilityEvent) <-chan session.LiveCapabilityEvent {
	if watch == nil {
		return nil
	}
	return watch(ctx)
}

type workerPlan struct {
	durationTimer     platformclock.Timer
	capabilityEvents  <-chan session.LiveCapabilityEvent
	replay            bool
	watchSession      bool
	watchFirstTurn    bool
	watchRateLimit    bool
	watchProviderLive bool
}

func (h *handle) makeWorkerPlan(durationTimer platformclock.Timer, capabilityEvents <-chan session.LiveCapabilityEvent) workerPlan {
	return workerPlan{
		durationTimer:     durationTimer,
		capabilityEvents:  capabilityEvents,
		replay:            h.request.ReplayPlan != nil && len(h.request.ReplayPlan.AudioTurns) > 0,
		watchSession:      h.request.RequireSessionUpdated,
		watchFirstTurn:    h.firstTurnPolicyEnabled(),
		watchRateLimit:    h.rateLimitRetryEnabled(),
		watchProviderLive: h.providerLivenessEnabled(),
	}
}

func (p workerPlan) count() int {
	count := 2
	if p.durationTimer != nil {
		count++
	}
	if p.watchSession {
		count++
	}
	if p.watchFirstTurn {
		count++
	}
	if p.watchRateLimit {
		count++
	}
	if p.capabilityEvents != nil {
		count++
	}
	if p.watchProviderLive {
		count++
	}
	if p.replay {
		count++
	}
	return count
}

func (p workerPlan) launch(h *handle, ctx context.Context, loop *agentloop.AgentLoop) {
	if p.durationTimer != nil {
		go h.watchDuration(ctx, p.durationTimer)
	}
	if p.watchSession {
		go h.watchSessionUpdated(ctx)
	}
	if p.watchFirstTurn {
		go h.watchFirstTurn(ctx)
	}
	if p.watchRateLimit {
		go h.runRateLimitRetry(ctx, loop)
	}
	if p.capabilityEvents != nil {
		go h.consumeCapabilityEvents(ctx, loop, p.capabilityEvents)
	}
	if p.watchProviderLive {
		go h.watchProviderLiveness(ctx)
	}
	if p.replay {
		go h.runReplay(ctx)
	}
}

func (h *handle) launchWorkers(
	ctx context.Context,
	loop *agentloop.AgentLoop,
	durationTimer platformclock.Timer,
	capabilityEvents <-chan session.LiveCapabilityEvent,
) {
	plan := h.makeWorkerPlan(durationTimer, capabilityEvents)
	h.runWG.Add(plan.count())
	go h.runLoop(ctx, loop)
	go h.consumeDeltas(ctx, loop)
	plan.launch(h, ctx, loop)
	go h.finishWhenStopped() //nolint:contextcheck // lifecycle join owns the invocation evidence context.
}

func capabilityEvent(sessionID, participantID string, value session.LiveCapabilityEvent) session.LiveEvent {
	copy := value
	return session.LiveEvent{
		Kind:          "browser." + strings.TrimSpace(value.Type),
		SessionID:     sessionID,
		ParticipantID: participantID,
		Timestamp:     value.Timestamp,
		BrowserID:     value.BrowserID,
		TargetID:      value.TargetID,
		Generation:    value.Generation,
		InvocationID:  value.InvocationID,
		State:         value.State,
		Reason:        value.Reason,
		Capability:    &copy,
		Critical:      capabilityEventCritical(value),
	}
}

func capabilityEventCritical(value session.LiveCapabilityEvent) bool {
	typeName := strings.ToLower(strings.TrimSpace(value.Type))
	state := strings.ToLower(strings.TrimSpace(value.State))
	return strings.Contains(typeName, "closed") || strings.Contains(typeName, "disconnect") ||
		strings.Contains(typeName, "error") || strings.Contains(typeName, "failed") ||
		strings.Contains(state, "error") || strings.Contains(state, "failed") ||
		strings.Contains(state, "canceled") || strings.Contains(state, "timed_out")
}
