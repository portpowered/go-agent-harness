package live

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/sessionwrap"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type mediaRequirements struct{ inbound, outbound bool }

func deviceRequestHasDirection(request devices.Request) bool {
	return request.CaptureEnabled || request.PlaybackEnabled
}

func (r mediaRequirements) satisfiedBy(endpoints sharedaudio.MediaEndpoints) bool {
	return (!r.inbound || endpoints.Inbound != nil) && (!r.outbound || endpoints.Outbound != nil)
}

// SatisfiedBy retains the package-local contract used by the session tests
// while the live host keeps the runtime check unexported.
func (r mediaRequirements) SatisfiedBy(endpoints sharedaudio.MediaEndpoints) bool {
	return r.satisfiedBy(endpoints)
}

func (h *handle) configureMediaRequirements(inbound, outbound bool) {
	h.mu.Lock()
	h.mediaRequirements = mediaRequirements{inbound: inbound, outbound: outbound}
	h.mu.Unlock()
}

func (i *liveInvocation) attachRecorder() {
	if i == nil || i.options.Recorder == nil {
		return
	}
	setter, ok := i.handle.(interface{ setRecorder(session.LiveRecorder) })
	if ok {
		setter.setRecorder(i.options.Recorder)
	}
}

func (i *liveInvocation) validateDeviceAdmission() error {
	if i == nil {
		return errors.New("live invocation is unavailable")
	}
	if (len(i.options.CaptureTurns) > 0 || len(i.options.CaptureInterruptions) > 0) && i.options.Devices == nil {
		return errors.New("finite capture inputs require a device service")
	}
	return nil
}

func (h *handle) start(runCtx context.Context) error {
	defer h.startFinish.Do(func() { close(h.startDone) })
	durationController, err := h.beginDurationController(runCtx)
	if err != nil {
		return h.failStart(runCtx, err)
	}
	h.mu.Lock()
	h.durationController = durationController
	h.mu.Unlock()
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
	h.prepareReplayCompletion()
	h.publish(session.LiveEvent{Kind: string(session.LiveEventStarted), SessionID: h.request.SessionID, Critical: true}, false) //nolint:contextcheck // start publication uses the invocation evidence context.
	watchEvents := capabilityEventStream(runCtx, capabilityWatch)
	if h.captureInterruptionsEnabled() && watchEvents == nil {
		return h.failStart(runCtx, errors.New("capture interruptions require browser invocation events"))
	}
	h.launchWorkers(runCtx, loop, watchEvents)
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
	capturing := sessionwrap.NewCapturingInferencer(sessionwrap.CapturingInferencerOptions{
		Inner: inferencer, Media: h.media, Continuous: h.request.OutputAudioContinuous,
		FlushOutbound:  h.request.FinishAfterResponse,
		RequireInbound: h.mediaRequirements.inbound, RequireOutbound: h.mediaRequirements.outbound,
		OnDispatch: h.observeProviderDispatch, OnToolResult: h.beginToolResultAdmission,
		OnContinuation:    h.beginContinuationAdmission,
		OnOpeningAdmitted: func() { h.markOpeningAdmitted(nil) },
		OnProviderDone:    h.providerDone, OnMediaAttached: h.setProviderMediaAttached,
	})
	h.providerTerminalError = capturing.TerminalError
	options := []agentloop.Option{
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(capturing),
		agentloop.WithBufferCapacity(h.eventCapacity),
	}
	if h.scheduler != nil {
		options = append(options, agentloop.WithClock(h.scheduler))
	}
	if toolExecutor == nil {
		// An absent host tool capability must not expose the loop's defaults.
		options = append(options, agentloop.WithToolExecutionDisabled())
		return agentloop.New(options...)
	}
	if h.request.ToolExecutionTimeout > 0 {
		toolExecutor = newTimedToolExecutor(toolExecutor, h.scheduler, h.request.ToolExecutionTimeout)
	}
	if h.durationControllerSnapshot() != nil {
		toolExecutor = durationToolExecutor{inner: toolExecutor, handle: h}
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

type workerPlan struct {
	capabilityEvents <-chan session.LiveCapabilityEvent
	replay           bool
	watchSession     bool
}

func (h *handle) makeWorkerPlan(capabilityEvents <-chan session.LiveCapabilityEvent) workerPlan {
	return workerPlan{
		capabilityEvents: capabilityEvents,
		replay:           h.request.ReplayPlan != nil && len(h.request.ReplayPlan.AudioTurns) > 0,
		watchSession:     h.request.RequireSessionUpdated,
	}
}

func (p workerPlan) count() int {
	count := 2
	if p.watchSession {
		count++
	}
	if p.capabilityEvents != nil {
		count++
	}
	if p.replay {
		count++
	}
	return count
}

func (p workerPlan) launch(h *handle, ctx context.Context, loop *agentloop.AgentLoop) {
	if p.watchSession {
		go h.watchSessionUpdated(ctx)
	}
	if p.capabilityEvents != nil {
		go h.consumeCapabilityEvents(ctx, loop, p.capabilityEvents)
	}
	if p.replay {
		go h.runReplay(ctx)
	}
}

func (h *handle) launchWorkers(
	ctx context.Context,
	loop *agentloop.AgentLoop,
	capabilityEvents <-chan session.LiveCapabilityEvent,
) {
	plan := h.makeWorkerPlan(capabilityEvents)
	h.runWG.Add(plan.count())
	go h.runLoop(ctx, loop)
	go h.consumeDeltas(ctx, loop)
	plan.launch(h, ctx, loop)
	go h.finishWhenStopped() //nolint:contextcheck // lifecycle join owns the invocation evidence context.
}
