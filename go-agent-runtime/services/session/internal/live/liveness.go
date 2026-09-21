package live

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/eventcodec"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

const (
	defaultProviderLivenessTimeout = 10 * time.Second
	silentProviderEmptyResponse    = "silent_provider_empty_response"
	silentProviderTimeout          = "silent_provider_timeout"
)

type providerLivenessError struct {
	failure session.LiveLivenessFailure
	cause   *sessionduration.LivenessError
}

func (e *providerLivenessError) Error() string {
	if e == nil || e.cause == nil {
		return "session liveness failure"
	}
	return e.cause.Error()
}

func (e *providerLivenessError) Unwrap() error {
	if e == nil {
		return nil
	}
	legacy := session.ErrLiveSilentProviderEmptyResponse
	if e.failure.Classification == silentProviderTimeout {
		legacy = session.ErrLiveSilentProviderTimeout
	}
	return errors.Join(e.cause, legacy)
}

func (h *handle) providerLivenessEnabled() bool {
	return h != nil && (h.request.ProviderLiveness.Enabled || h.request.ProviderLiveness.Timeout > 0)
}

// observeProviderDispatch records the provider admission boundary. The
// sessionduration controller owns liveness arming and generation state; this
// host bridge retains only response bookkeeping needed by live completion.
func (h *handle) observeProviderDispatch(msg messages.StreamMessage) {
	if h == nil {
		return
	}
	h.mu.Lock()
	durationController := h.durationController
	h.mu.Unlock()
	if durationController != nil {
		durationController.Observe(msg)
	}
	h.recordMessage(session.LiveRecord{Direction: session.LiveRecordClient, Timestamp: h.now(), Message: msg})
	if msg.Type != messages.StreamTypeResponseCreate && msg.Type != messages.StreamTypeMessageEnd {
		return
	}
	value, hasCreate := msg.Value.(*messages.ResponseCreateValue)
	acknowledgement := hasCreate && value != nil && value.IsToolAcknowledgement()
	if !acknowledgement && msg.Role != messages.RoleTool {
		h.mu.Lock()
		h.responseStarted, h.responsePending = true, true
		if msg.Type == messages.StreamTypeResponseCreate {
			h.responseActive = true
		}
		h.mu.Unlock()
	}
}

func (h *handle) latchProviderLiveness(failure session.LiveLivenessFailure) {
	if h == nil {
		return
	}
	classification := strings.TrimSpace(failure.Classification)
	if classification == "" {
		classification = silentProviderEmptyResponse
	}
	cause := sessionduration.ErrProviderEmptyResponse
	if classification == silentProviderTimeout {
		cause = sessionduration.ErrProviderLivenessTimeout
	}
	causeError := &sessionduration.LivenessError{
		Classification: classification,
		ResponseID:     failure.ResponseID,
		Usage:          failure.Usage,
		Cause:          cause,
	}
	err := &providerLivenessError{failure: failure, cause: causeError}
	h.publish(session.LiveEvent{
		Kind:      string(session.LiveEventLiveness),
		SessionID: h.request.SessionID,
		Error:     err,
		Liveness:  &failure,
		Critical:  true,
	}, false)
	h.Cancel(err)
}

func livenessFailureFromError(err error) *session.LiveLivenessFailure {
	if err == nil {
		return nil
	}
	var typed *sessionduration.LivenessError
	if !errors.As(err, &typed) || typed == nil {
		return nil
	}
	classification := strings.TrimSpace(typed.Classification)
	if classification == "" {
		classification = silentProviderEmptyResponse
	}
	return &session.LiveLivenessFailure{
		Classification:     classification,
		ResponseID:         typed.ResponseID,
		Usage:              typed.Usage,
		TerminalReason:     messages.TerminalReasonTerminalFailure,
		TerminalProvenance: messages.TerminalProvenanceSession,
		OutputState:        messages.TerminalOutputNone,
	}
}

// livenessToolExecutor marks local tool work as outside the provider progress
// budget. The service controller resumes provider observation on the next
// response admission.
type livenessToolExecutor struct {
	inner  messages.ToolExecutor
	handle *handle
}

func (e livenessToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	var controller interface {
		BeginLocalToolExecution()
		EndLocalToolExecution()
	}
	if e.handle != nil {
		e.handle.mu.Lock()
		candidate := e.handle.durationController
		e.handle.mu.Unlock()
		if candidate != nil {
			controller = candidate
			controller.BeginLocalToolExecution()
		}
	}
	if controller != nil {
		defer controller.EndLocalToolExecution()
	}
	if e.inner == nil {
		return messages.ToolCallResponse{}, fmt.Errorf("liveness tool executor is unavailable")
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

func (h *handle) beginDurationController(ctx context.Context) (sessionduration.Controller, error) {
	if h == nil || h.durationService == nil {
		if h != nil && h.request.MaxDuration == 0 && !h.providerLivenessEnabled() && !h.rateLimitRetryEnabled() {
			return nil, nil
		}
		return nil, errors.New("session duration service is unavailable")
	}
	controller, err := h.durationService.Begin(sessionduration.Options{
		Context: ctx, Clock: h.scheduler, MaxDuration: h.request.MaxDuration,
		Liveness: sessionduration.LivenessOptions{Enabled: h.request.ProviderLiveness.Enabled, Timeout: h.request.ProviderLiveness.Timeout},
		Retry:    sessionduration.RetryPolicy{Enabled: h.request.RateLimitRetry.Enabled, MaxRetries: h.request.RateLimitRetry.MaxRetries, DefaultDelay: h.request.RateLimitRetry.DefaultDelay, MaxDelay: h.request.RateLimitRetry.MaxDelay},
		//nolint:contextcheck // this synchronous cause callback records against the invocation context.
		FirstCause: func(cause error) {
			if errors.Is(cause, sessionduration.ErrMaxDurationExceeded) {
				h.Cancel(session.ErrLiveDurationExceeded)
				return
			}
			var liveness *sessionduration.LivenessError
			if !errors.As(cause, &liveness) || liveness == nil {
				return
			}
			h.latchProviderLiveness(session.LiveLivenessFailure{Classification: liveness.Classification, ResponseID: liveness.ResponseID, Usage: liveness.Usage, TerminalReason: messages.TerminalReasonTerminalFailure, TerminalProvenance: messages.TerminalProvenanceSession, OutputState: messages.TerminalOutputNone})
		},
	})
	if errors.Is(err, sessionduration.ErrInvalidDuration) {
		return nil, err
	}
	if errors.Is(err, sessionduration.ErrSchedulerUnavailable) {
		return nil, fmt.Errorf("create live duration timer: %w", session.ErrLiveSchedulerUnavailable)
	}
	return controller, err
}

func (h *handle) ensureCaptureTurnAdmissible() error {
	if h == nil {
		return session.ErrLiveClosed
	}
	h.mu.Lock()
	providerClosed, scheduled := h.providerCloseObserved, h.scheduledAudioCount
	dispatched := h.dispatchedAudioCount
	completed := h.observedResponseTerminals - h.scheduledResponseBase
	terminal := cloneLiveTerminalValue(h.terminalValue)
	h.mu.Unlock()
	if !providerClosed {
		return nil
	}
	if incomplete := newScheduledAudioIncompleteError(scheduled, dispatched, completed, terminal); incomplete != nil {
		return incomplete
	}
	return session.ErrLiveClosed
}

func (h *handle) scheduledAudioError() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	scheduled, dispatched := h.scheduledAudioCount, h.dispatchedAudioCount
	completed := h.observedResponseTerminals - h.scheduledResponseBase
	terminal := cloneLiveTerminalValue(h.terminalValue)
	h.mu.Unlock()
	return newScheduledAudioIncompleteError(scheduled, dispatched, completed, terminal)
}

func (h *handle) observeTerminalValue(msg messages.StreamMessage) {
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
		h.terminalOnce.Do(func() { close(h.terminalObserved) })
	} else if !h.providerCloseObserved {
		h.terminalValue = value
	}
	h.mu.Unlock()
}
