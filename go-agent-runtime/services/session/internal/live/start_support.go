package live

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

func (h *handle) validateTimingPolicy() error {
	if h.request.MaxDuration < 0 {
		return errors.New("live session maximum duration must not be negative")
	}
	if h.request.SessionUpdatedTimeout < 0 {
		return errors.New("live session.updated timeout must not be negative")
	}
	if h.request.FirstTurnTimeout < 0 {
		return errors.New("live first-turn timeout must not be negative")
	}
	if h.request.ToolExecutionTimeout < 0 {
		return errors.New("live tool execution timeout must not be negative")
	}
	if h.request.ProviderLiveness.Timeout < 0 {
		return errors.New("live provider liveness timeout must not be negative")
	}
	if h.request.RateLimitRetry.MaxRetries < 0 {
		return errors.New("live rate-limit retry count must not be negative")
	}
	if h.request.RateLimitRetry.DefaultDelay < 0 || h.request.RateLimitRetry.MaxDelay < 0 {
		return errors.New("live rate-limit retry delays must not be negative")
	}
	if h.requiresScheduler() && h.scheduler == nil {
		return fmt.Errorf("%w: request requires a scheduler", session.ErrLiveSchedulerUnavailable)
	}
	return nil
}

func (h *handle) requiresScheduler() bool {
	return h.request.MaxDuration > 0 || h.request.RequireSessionUpdated || h.firstTurnPolicyEnabled() || h.rateLimitRetryEnabled() || h.request.ToolExecutionTimeout > 0 || h.providerLivenessEnabled()
}

func cloneLiveTerminalValue(value *messages.SessionCloseValue) *messages.SessionCloseValue {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func terminalForLiveness(sessionID string, value *messages.SessionCloseValue, liveness *session.LiveLivenessFailure) *messages.SessionCloseValue {
	if value == nil {
		return messages.NewSessionCloseValueWithTerminal(
			sessionID,
			liveness.Classification,
			liveness.Classification,
			liveness.TerminalReason,
			liveness.TerminalProvenance,
			liveness.OutputState,
		)
	}
	copy := *value
	copy.Classification = liveness.Classification
	copy.TerminalReason = liveness.TerminalReason
	copy.TerminalProvenance = liveness.TerminalProvenance
	copy.OutputState = liveness.OutputState
	if copy.Reason == "" {
		copy.Reason = liveness.Classification
	}
	return &copy
}

func successfulLiveTerminal(request session.LiveRequest, value *messages.SessionCloseValue) *messages.SessionCloseValue {
	if value != nil && value.TerminalReason != "" {
		return value
	}
	if request.Replay.InputCapturePath != "" &&
		(request.ReplayPlan == nil || request.ReplayPlan.StopAfterResponse) {
		return messages.NewSessionCloseValueWithTerminal(
			request.SessionID,
			"",
			string(messages.TerminalReasonReplayComplete),
			messages.TerminalReasonReplayComplete,
			messages.TerminalProvenanceReplay,
			messages.TerminalOutputComplete,
		)
	}
	if value != nil && value.Reason != "" {
		return value
	}
	return messages.NewSessionCloseValueWithTerminal(
		request.SessionID,
		"",
		string(messages.TerminalReasonProviderAuthoredCompletion),
		messages.TerminalReasonProviderAuthoredCompletion,
		messages.TerminalProvenanceProvider,
		messages.TerminalOutputComplete,
	)
}

func (h *handle) configureCaptureSource(active bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.captureSourceActive = active
	h.mu.Unlock()
}

func (h *handle) captureSourceIsActive() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.captureSourceActive
}

// A recorder can fail on the terminal observation itself. That failure must
// affect Wait and the delivered terminal even though the failed recorder
// cannot be asked recursively to record its own error.
func (h *handle) includeTerminalRecordingError(event *session.LiveEvent) {
	recordErr := h.recorderError()
	if recordErr == nil {
		return
	}
	if !errors.Is(event.Error, recordErr) {
		event.Error = errors.Join(event.Error, recordErr)
	}
	event.Terminal = finalizeLiveTerminalValue(h.request, event.Error, event.Terminal, event.Liveness)
	h.mu.Lock()
	h.terminalErr = event.Error
	h.mu.Unlock()
}

func (h *handle) watchDuration(ctx context.Context, timer platformclock.Timer) {
	defer h.runWG.Done()
	if timer == nil {
		return
	}
	defer timer.Stop()
	select {
	case <-timer.C():
		h.Cancel(session.ErrLiveDurationExceeded)
	case <-ctx.Done():
	}
}

func (h *handle) watchSessionUpdated(ctx context.Context) {
	defer h.runWG.Done()
	var timer platformclock.Timer
	select {
	case timer = <-h.sessionUpdatedTimerReady:
	case <-ctx.Done():
		return
	}
	if timer == nil {
		return
	}
	defer timer.Stop()
	select {
	case <-timer.C():
		h.Cancel(session.ErrLiveSessionUpdatedTimeout)
	case <-h.sessionUpdatedSignal:
	case <-ctx.Done():
	}
}

// admitCapabilities resolves participant-scoped tools exactly once, before
// the provider session is constructed. A factory result with
// InheritDefaults=false is authoritative even when its executor and
// definitions are empty, which lets rooms disable tools for one participant.
func (h *handle) admitCapabilities(ctx context.Context) (messages.ToolExecutor, []messages.ToolDefinition, error) {
	executor := h.toolExecutor
	definitions := append([]messages.ToolDefinition(nil), h.toolDefinitions...)
	binding, err := h.resolveCapabilityBinding(ctx)
	if err != nil {
		return nil, nil, err
	}
	if binding == nil {
		return executor, definitions, nil
	}
	normalizeCapabilityLifecycle(binding)
	if binding.Initialize != nil {
		if err := binding.Initialize(ctx); err != nil {
			return nil, nil, closeFailedCapability(binding, fmt.Errorf("initialize live capabilities: %w", err))
		}
	}
	if !binding.InheritDefaults {
		executor = binding.Executor
		definitions = messages.CanonicalToolDefinitions(binding.Definitions)
	}
	if binding.RefreshDefinitions != nil {
		refreshed, err := binding.RefreshDefinitions(ctx)
		if err != nil {
			return nil, nil, closeFailedCapability(binding, fmt.Errorf("refresh live capabilities: %w", err))
		}
		definitions = messages.CanonicalToolDefinitions(refreshed)
	}
	if err := h.retainCapability(binding, executor, definitions); err != nil {
		return nil, nil, closeFailedCapability(binding, err)
	}
	return executor, definitions, nil
}

func (h *handle) resolveCapabilityBinding(ctx context.Context) (*session.LiveCapabilities, error) {
	if h.request.Capabilities != nil {
		binding := *h.request.Capabilities
		return &binding, nil
	}
	if h.capabilityFactory == nil {
		return nil, nil
	}
	binding, err := h.capabilityFactory(ctx, h.request)
	if err != nil {
		return nil, fmt.Errorf("resolve live capabilities: %w", err)
	}
	return &binding, nil
}

// A promoted handle owns all lifecycle callbacks. Legacy callbacks cannot
// initialize or close the same capability a second time.
func normalizeCapabilityLifecycle(binding *session.LiveCapabilities) {
	if binding.Handle == nil {
		return
	}
	binding.Initialize = binding.Handle.Initialize
	binding.RefreshDefinitions = binding.Handle.RefreshDefinitions
	binding.Close = binding.Handle.Close
	binding.BrowserWatch = nil
	if watcher, ok := binding.Handle.(session.LiveCapabilityWatcher); ok {
		binding.BrowserWatch = watcher.BrowserWatch
	}
}

func closeFailedCapability(binding *session.LiveCapabilities, cause error) error {
	if binding.Close == nil {
		return cause
	}
	return errors.Join(cause, binding.Close())
}

func (h *handle) retainCapability(binding *session.LiveCapabilities, executor messages.ToolExecutor, definitions []messages.ToolDefinition) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return session.ErrLiveClosed
	}
	h.capabilityClose = binding.Close
	h.capabilityRefresh = binding.RefreshDefinitions
	h.capabilityWatch = binding.BrowserWatch
	h.toolExecutor = executor
	h.toolDefinitions = append([]messages.ToolDefinition(nil), definitions...)
	binding.Definitions = append([]messages.ToolDefinition(nil), definitions...)
	h.request.Capabilities = binding
	return nil
}

func (h *handle) failStart(ctx context.Context, err error) error {
	err = errors.Join(err, h.media.Close())
	h.mu.Lock()
	h.startErr = err
	h.terminalErr = err
	h.mu.Unlock()
	h.publish(session.LiveEvent{Kind: string(session.LiveEventError), SessionID: h.request.SessionID, Error: err, Critical: true}, false) //nolint:contextcheck // start failure publication uses the invocation evidence context.
	h.finish(err)                                                                                                                         //nolint:contextcheck // finish owns the invocation evidence context.
	return err
}

func (h *handle) runLoop(ctx context.Context, loop *agentloop.AgentLoop) {
	defer h.runWG.Done()
	err := loop.Run(ctx)
	h.mu.Lock()
	h.runErr = err
	h.mu.Unlock()
	// Stop the delta consumer once Run has joined all participant workers.
	// A caller supplied cancel cause remains authoritative and cannot be
	// overwritten by this internal cleanup cancellation.
	h.mu.Lock()
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel(nil)
	}
}

// providerDone is called by the provider-session adapter when its transport
// closes. ModelRunner emits the transport SessionClose value from its Done
// branch; waiting for that value before cancelling AgentLoop preserves the
// final provider classification and avoids leaving AgentLoop's hot loop alive
// after its model participant has returned.
func (h *handle) providerDone(terminalErr error) {
	if h == nil {
		return
	}
	if terminalErr != nil {
		h.mu.Lock()
		if h.providerErr == nil {
			h.providerErr = terminalErr
		}
		h.mu.Unlock()
	}
	h.providerDoneOnce.Do(func() { close(h.providerDoneSignal) })
	select {
	case <-h.terminalObserved:
		if !h.deferProviderClose() {
			h.stopGracefully()
		}
	case <-h.done:
	}
}

// handleCapabilityEvent publishes every watcher observation and coalesces
// catalog changes that arrive while the ordered provider update is in flight.
// The browser stream stays bounded, while the latest refresh observes the
// complete catalog after a burst of page changes.
func (h *handle) handleCapabilityEvent(
	ctx context.Context,
	loop *agentloop.AgentLoop,
	events <-chan session.LiveCapabilityEvent,
	event session.LiveCapabilityEvent,
) error {
	h.publishCapabilityEvent(event) //nolint:contextcheck // event publication owns its bounded recorder path.
	if !capabilityEventRequiresRefresh(event) {
		return nil
	}
	for {
		if err := h.refreshLiveTools(ctx, loop); err != nil {
			return fmt.Errorf("refresh live capabilities after browser event: %w", err)
		}
		_, refreshAgain, err := h.nextCapabilityRefresh(ctx, events)
		if err != nil || !refreshAgain {
			return err
		}
	}
}

func (h *handle) publishCapabilityEvent(event session.LiveCapabilityEvent) {
	h.publish(capabilityEvent(h.request.SessionID, h.request.ParticipantID, event), false)
}

func (h *handle) nextCapabilityRefresh(ctx context.Context, events <-chan session.LiveCapabilityEvent) (session.LiveCapabilityEvent, bool, error) {
	var latest session.LiveCapabilityEvent
	refreshAgain := false
	for {
		select {
		case <-ctx.Done():
			return session.LiveCapabilityEvent{}, false, ctx.Err()
		case next, ok := <-events:
			if !ok {
				return latest, refreshAgain, nil
			}
			h.publishCapabilityEvent(next) //nolint:contextcheck // event publication owns its bounded recorder path.
			if capabilityEventRequiresRefresh(next) {
				latest, refreshAgain = next, true
			}
		default:
			return latest, refreshAgain, nil
		}
	}
}

func capabilityEventRequiresRefresh(event session.LiveCapabilityEvent) bool {
	kind := strings.ToLower(strings.TrimSpace(event.Type))
	return event.CatalogReady || strings.Contains(kind, "catalog") || strings.Contains(kind, "generation")
}
