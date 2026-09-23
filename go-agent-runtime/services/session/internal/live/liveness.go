package live

import (
	"context"
	"errors"
	"fmt"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

const (
	silentProviderEmptyResponse = sessionduration.LivenessClassificationEmptyResponse
	silentProviderTimeout       = sessionduration.LivenessClassificationTimeout
)

// providerLivenessError translates the service-owned liveness failure into the
// session contract while retaining both error identities.
type providerLivenessError struct {
	failure session.LiveLivenessFailure
	cause   error
}

func (e *providerLivenessError) Error() string {
	if e == nil {
		return "session liveness failure"
	}
	classification := e.failure.Classification
	if classification == "" {
		classification = silentProviderEmptyResponse
	}
	return fmt.Sprintf("%s: provider response produced no observable output", classification)
}

func (e *providerLivenessError) Unwrap() []error {
	if e == nil {
		return nil
	}
	causes := make([]error, 0, 2)
	switch e.failure.Classification {
	case silentProviderTimeout:
		causes = append(causes, session.ErrLiveSilentProviderTimeout)
	case silentProviderEmptyResponse:
		causes = append(causes, session.ErrLiveSilentProviderEmptyResponse)
	}
	if e.cause != nil {
		causes = append(causes, e.cause)
	}
	return causes
}

func (h *handle) providerLivenessEnabled() bool {
	return h != nil && (h.request.ProviderLiveness.Enabled || h.request.ProviderLiveness.Timeout > 0)
}

func (h *handle) beginDurationController(ctx context.Context) (sessionduration.Controller, error) {
	if h == nil || h.durationService == nil {
		if h != nil && h.request.MaxDuration == 0 && !h.providerLivenessEnabled() && !h.rateLimitRetryEnabled() {
			return nil, nil
		}
		return nil, errors.New("session duration service is unavailable")
	}
	controller, err := h.durationService.Begin(sessionduration.Options{
		Context: ctx, Clock: h.scheduler, LivenessClock: h.scheduler, MaxDuration: h.request.MaxDuration,
		Liveness: sessionduration.LivenessOptions{
			Enabled: h.providerLivenessEnabled(),
			Timeout: h.request.ProviderLiveness.Timeout,
		},
		Retry: sessionduration.RetryPolicy{
			Enabled:      h.request.RateLimitRetry.Enabled,
			MaxRetries:   h.request.RateLimitRetry.MaxRetries,
			DefaultDelay: h.request.RateLimitRetry.DefaultDelay,
			MaxDelay:     h.request.RateLimitRetry.MaxDelay,
		},
		//nolint:contextcheck // this synchronous cause callback records against the invocation context.
		FirstCause: func(cause error) {
			if errors.Is(cause, sessionduration.ErrMaxDurationExceeded) {
				h.Cancel(session.ErrLiveDurationExceeded)
				return
			}
			h.publishProviderLiveness(cause)
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

func (h *handle) durationControllerSnapshot() sessionduration.Controller {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.durationController
}

func (h *handle) publishProviderLiveness(cause error) {
	failure := livenessFailureFromError(cause)
	if h == nil || failure == nil {
		return
	}
	err := &providerLivenessError{failure: *failure, cause: cause}
	h.publish(session.LiveEvent{
		Kind:      string(session.LiveEventLiveness),
		SessionID: h.request.SessionID,
		Error:     err,
		Liveness:  failure,
		Critical:  true,
	}, false)
	h.Cancel(err)
}

func livenessFailureFromError(err error) *session.LiveLivenessFailure {
	if err == nil {
		return nil
	}
	var liveErr *providerLivenessError
	if errors.As(err, &liveErr) && liveErr != nil {
		copy := liveErr.failure
		return &copy
	}
	var durationErr *sessionduration.LivenessError
	if !errors.As(err, &durationErr) || durationErr == nil {
		return nil
	}
	return &session.LiveLivenessFailure{
		Classification:     durationErr.Classification,
		ResponseID:         durationErr.ResponseID,
		Usage:              durationErr.Usage,
		TerminalReason:     durationErr.TerminalReason,
		TerminalProvenance: durationErr.TerminalProvenance,
		OutputState:        durationErr.OutputState,
	}
}

// durationToolExecutor reports local work to the duration controller so the
// provider watchdog remains service-owned while tools run.
type durationToolExecutor struct {
	inner  messages.ToolExecutor
	handle *handle
}

func (e durationToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	controller := e.handle.durationControllerSnapshot()
	if controller != nil {
		controller.BeginLocalToolExecution()
		defer controller.EndLocalToolExecution()
	}
	return e.inner.Execute(ctx, call)
}
