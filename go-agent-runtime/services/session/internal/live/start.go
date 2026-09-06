package live

import (
	"context"
	"errors"
	"fmt"

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
		inner:           inferencer,
		media:           h.media,
		onDispatch:      h.observeProviderDispatch,
		onToolResult:    h.observeToolResult,
		onContinuation:  h.observeContinuationRequested,
		onProviderDone:  h.providerDone,
		onMediaAttached: h.setProviderMediaAttached,
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
	options = append(options, agentloop.WithToolExecutor(toolExecutor))
	if len(toolDefinitions) > 0 {
		options = append(options, agentloop.WithTools(toolDefinitions))
	}
	// An injected session inferencer receives provider configuration through the
	// same loop boundary as a native provider. Forward the admitted catalog so
	// fixture providers and shipped sessions observe the exact surface that the
	// runtime will execute.
	if h.request.ReplayPlan == nil && (len(toolDefinitions) > 0 || h.request.Capabilities != nil) {
		options = append(options, agentloop.WithSessionConfig(messages.SessionUpdateConfig{
			Instructions: h.request.Instructions,
			Model:        h.request.Model,
			Tools:        toolDefinitions,
		}))
	}
	return agentloop.New(options...)
}

// restrictToolExecutor keeps provider issued calls inside the capability
// surface advertised for this invocation. A registry executor can still be
// non-nil when one configured tool has been disabled, so relying on executor
// presence alone would execute an unadvertised call through its generic
// "not found" path. Returning a normal correlated tool result lets the
// provider apply its own continuation/replay validation and prevents an
// unavailable call from being mistaken for a clean assistant completion.
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
	plan := h.request.ReplayPlan
	if plan != nil && len(plan.AudioTurns) > 0 {
		return
	}
	if plan != nil && plan.OpeningPromptPresent && h.request.FinishAfterResponse {
		h.markCaptureComplete()
	}
	// A raw provider replay may contain a response without a client opening
	// prompt (for example a capture that starts with session.update and then
	// records provider output). Such captures do not always include an explicit
	// session.closed frame, so the response terminal boundary must be allowed
	// to finish the invocation once the replayed response is complete.
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
