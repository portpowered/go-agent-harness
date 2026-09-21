package live

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/eventcodec"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
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
		classification = sessionduration.LivenessClassificationEmptyResponse
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

func terminalDrainFactory(factory session.LiveInferencerFactory) session.LiveInferencerFactory {
	return func(ctx context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
		inner, err := factory(ctx, request)
		if err != nil || inner == nil {
			return inner, err
		}
		if request.Replay.Kind == session.LiveReplayKindTurn {
			inner = turnReplayMediaInferencer{inner: inner, sampleRate: request.OutputAudioSampleRate, continuous: request.OutputAudioContinuous}
		}
		return terminalDrainInferencer{inner: inner, continuous: request.OutputAudioContinuous}, nil
	}
}

type terminalDrainInferencer struct {
	inner      messages.SessionInferencer
	continuous bool
}

func (i terminalDrainInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	s, err := i.inner.ConnectSession(ctx)
	if err != nil || s == nil {
		return s, err
	}
	if provider, ok := s.(sharedaudio.MediaSession); ok {
		captureMediaEndpoints(s, provider, i.continuous)
	}
	source := s.Receive()
	capacity := defaultEventCapacity
	if source != nil && source.Cap() > 0 {
		capacity = source.Cap()
	}
	d := &terminalDrainSession{inner: s, receive: messages.NewTypedBuffer[messages.StreamMessage](capacity), done: make(chan struct{}), stop: make(chan struct{})}
	go d.forward(context.WithoutCancel(ctx), source, s.Done())
	return d, nil
}

func (i terminalDrainInferencer) FlushCapture() error {
	if flusher, ok := i.inner.(interface{ FlushCapture() error }); ok {
		return flusher.FlushCapture()
	}
	return nil
}

func (s *terminalDrainSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if requester, ok := s.inner.(messages.SessionResponseRequester); ok {
		return requester.RequestResponse(ctx)
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
}

func (s *terminalDrainSession) SupportsResponseRequests() bool {
	if capability, ok := s.inner.(messages.SessionResponseCapability); ok {
		return capability.SupportsResponseRequests()
	}
	_, ok := s.inner.(messages.SessionResponseRequester)
	return ok
}

func (s *terminalDrainSession) FlushOutbound(ctx context.Context) error {
	if flusher, ok := s.inner.(messages.SessionOutboundFlusher); ok {
		return flusher.FlushOutbound(ctx)
	}
	return nil
}

func (s *terminalDrainSession) TerminalError() error {
	provider, ok := s.inner.(interface{ TerminalError() error })
	if !ok {
		return nil
	}
	return provider.TerminalError()
}

type terminalDrainSession struct {
	inner      messages.Session
	receive    *messages.TypedBuffer[messages.StreamMessage]
	done, stop chan struct{}
	close      sync.Once
	closeErr   error
}

func (s *terminalDrainSession) forward(ctx context.Context, source *messages.TypedBuffer[messages.StreamMessage], sourceDone <-chan struct{}) {
	defer close(s.done)
	if source == nil {
		return
	}
	for {
		select {
		case msg, ok := <-source.Chan():
			if !ok || !s.forwardMessage(ctx, msg) {
				return
			}
		case <-sourceDone:
			s.drain(ctx, source)
			return
		case <-s.stop:
			return
		}
	}
}

func (s *terminalDrainSession) drain(ctx context.Context, source *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := source.Read()
		if !ok || !s.forwardMessage(ctx, msg) {
			return
		}
	}
}

func (s *terminalDrainSession) forwardMessage(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionClose {
		msg.ResponseID = ""
	}
	return s.receive.WriteWaitContextOrDone(ctx, s.stop, msg).OK()
}

func (s *terminalDrainSession) InitialSessionConfigSent() bool {
	marker, ok := s.inner.(interface{ InitialSessionConfigSent() bool })
	return ok && marker.InitialSessionConfigSent()
}

func captureResponseTarget(request session.LiveRequest) int {
	if openingMessageRequestsResponse(request) {
		return 1
	}
	return 0
}

func shouldWaitForCaptureResponse(index int, admission session.AudioTurnAdmission) bool {
	return admission == session.AudioTurnAdmissionCompletionGated || index > 0
}
