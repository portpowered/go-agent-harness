package live

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const defaultFirstTurnTimeout = 30 * time.Second

func (h *handle) finalizeDuration(err error) error {
	h.mu.Lock()
	durationController := h.durationController
	h.mu.Unlock()
	if durationController != nil {
		_, durationErr := durationController.Finalize(context.WithoutCancel(h.evidenceContext()), sessionduration.FinalizeRequest{Primary: err})
		return durationErr
	}
	return err
}

func (h *handle) joinPumpError(err error) error {
	h.mu.Lock()
	if !isContextTermination(h.pumpErr) {
		err = errors.Join(err, h.pumpErr)
	}
	h.mu.Unlock()
	return err
}

// observeProviderDispatch arms the participant watchdog when the loop admits
// an operation that asks the provider to produce a response. The provider may
// emit MESSAGE.START asynchronously, so arming at dispatch closes the gap
// between a successful input admission and the first provider observation.
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
	// Admission establishes pending work even before the provider's response
	// starts. Keep that obligation separate from observed streaming progress.
	if !acknowledgement && msg.Role != messages.RoleTool {
		h.mu.Lock()
		h.responseStarted, h.responsePending = true, true
		if msg.Type == messages.StreamTypeResponseCreate {
			h.responseActive = true
		}
		h.mu.Unlock()
	}
	if durationController != nil && h.providerLivenessEnabled() && (msg.Type == messages.StreamTypeMessageEnd || !acknowledgement) {
		durationController.ExpectProviderProgress()
	}
}

type retryRequest struct {
	loop     *agentloop.AgentLoop
	deadline time.Time
}

func (h *handle) firstTurnPolicyEnabled() bool {
	return h != nil && (h.request.RequireFirstTurn || h.request.FirstTurnTimeout > 0)
}
func (h *handle) firstTurnTimeout() time.Duration {
	if h == nil || h.request.FirstTurnTimeout <= 0 {
		return defaultFirstTurnTimeout
	}
	return h.request.FirstTurnTimeout
}
func (h *handle) rateLimitRetryEnabled() bool {
	return h != nil && h.request.RateLimitRetry.Enabled
}
func (h *handle) observeFirstTurn(ctx context.Context, msg messages.StreamMessage) {
	if h == nil || !h.firstTurnPolicyEnabled() {
		return
	}
	if msg.Type == messages.StreamTypeSessionOpen {
		h.policyMu.Lock()
		if h.firstTurnTimerScheduled || h.firstTurnSeen {
			h.policyMu.Unlock()
			return
		}
		h.firstTurnTimerScheduled = true
		h.policyMu.Unlock()
		timer := h.scheduler.NewTimer(h.firstTurnTimeout())
		if timer == nil {
			h.Cancel(fmt.Errorf("create first-turn timer: %w", session.ErrLiveSchedulerUnavailable))
			return
		}
		select {
		case h.firstTurnTimerReady <- timer:
		case <-ctx.Done():
			timer.Stop()
		}
		return
	}
	if !isFirstTurnResponseBoundary(msg) {
		return
	}
	h.policyMu.Lock()
	if h.firstTurnSeen {
		h.policyMu.Unlock()
		return
	}
	h.firstTurnSeen = true
	h.policyMu.Unlock()
	h.firstTurnOnce.Do(func() { close(h.firstTurnSignal) })
}

func (h *handle) watchFirstTurn(ctx context.Context) {
	defer h.runWG.Done()
	var timer platformclock.Timer
	select {
	case timer = <-h.firstTurnTimerReady:
	case <-h.firstTurnSignal:
		return
	case <-ctx.Done():
		return
	}
	if timer == nil {
		return
	}
	defer timer.Stop()
	select {
	case <-h.firstTurnSignal:
	case <-timer.C():
		h.Cancel(session.ErrLiveFirstTurnTimeout)
	case <-ctx.Done():
	}
}
func isFirstTurnResponseBoundary(msg messages.StreamMessage) bool {
	return msg.Type == messages.StreamTypeMessageStart ||
		msg.Type == messages.StreamTypeMessageEnd ||
		msg.Type == messages.StreamTypeTextStart ||
		msg.Type == messages.StreamTypeAudioStart ||
		msg.Type == messages.StreamTypeImageStart ||
		msg.Type == messages.StreamTypeToolCallStart ||
		msg.Type == messages.StreamTypeReasoningStart ||
		msg.Type == messages.StreamTypeTranscriptStart ||
		msg.Type == messages.StreamTypeError
}
func (h *handle) observeRateLimit(loop *agentloop.AgentLoop, msg messages.StreamMessage) {
	if h == nil || loop == nil || !h.rateLimitRetryEnabled() || msg.Type != messages.StreamTypeMessageEnd {
		return
	}
	terminal, ok := msg.Value.(*messages.MessageEndValue)
	if !ok {
		return
	}
	h.mu.Lock()
	controller := h.durationController
	h.mu.Unlock()
	if controller == nil {
		return
	}
	decision := controller.Retry(sessionduration.RetryRequest{Terminal: terminal})
	if decision.Exhausted {
		h.Cancel(fmt.Errorf("%w: provider returned rate_limit_exceeded", session.ErrLiveRateLimitRetryExhausted))
		return
	}
	if !decision.Eligible {
		return
	}
	request := retryRequest{loop: loop, deadline: h.scheduler.Now().Add(decision.Delay)}
	select {
	case h.retryRequests <- request:
	case <-h.parentContext().Done():
	}
}

func (h *handle) parentContext() context.Context {
	if h == nil {
		return context.Background()
	}
	h.mu.Lock()
	ctx := h.parentCtx
	h.mu.Unlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
func (h *handle) runRateLimitRetry(ctx context.Context, defaultLoop *agentloop.AgentLoop) {
	defer h.runWG.Done()
	for {
		request, ok := h.nextRetryRequest(ctx)
		if !ok {
			return
		}
		if err := h.sendRateLimitRetry(ctx, defaultLoop, request); err != nil {
			h.Cancel(err)
		}
	}
}

func (h *handle) nextRetryRequest(ctx context.Context) (retryRequest, bool) {
	select {
	case <-ctx.Done():
		return retryRequest{}, false
	case request := <-h.retryRequests:
		return request, true
	}
}

func (h *handle) sendRateLimitRetry(ctx context.Context, defaultLoop *agentloop.AgentLoop, request retryRequest) error {
	if err := h.waitForRetry(ctx, request.deadline); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return err
	}
	loop := request.loop
	if loop == nil {
		loop = defaultLoop
	}
	if loop == nil {
		return errors.New("rate-limit retry loop is unavailable")
	}
	if err := loop.SendSessionEvent(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	}); err != nil {
		return fmt.Errorf("send rate-limit retry response: %w", err)
	}
	return nil
}

func (h *handle) waitForRetry(ctx context.Context, deadline time.Time) error {
	if !deadline.After(h.scheduler.Now()) {
		return nil
	}
	timer := h.scheduler.NewTimer(deadline.Sub(h.scheduler.Now()))
	if timer == nil {
		return fmt.Errorf("create rate-limit retry timer: %w", session.ErrLiveSchedulerUnavailable)
	}
	defer timer.Stop()
	select {
	case <-timer.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type timedToolExecutor struct {
	inner     messages.ToolExecutor
	scheduler platformclock.Scheduler
	timeout   time.Duration
}

func newTimedToolExecutor(inner messages.ToolExecutor, scheduler platformclock.Scheduler, timeout time.Duration) messages.ToolExecutor {
	if inner == nil || timeout <= 0 {
		return inner
	}
	return timedToolExecutor{inner: inner, scheduler: scheduler, timeout: timeout}
}

func (e timedToolExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	if err := ctx.Err(); err != nil {
		return messages.ToolCallResponse{}, err
	}
	if e.scheduler == nil {
		return messages.ToolCallResponse{}, session.ErrLiveSchedulerUnavailable
	}
	toolCtx, cancel := e.scheduler.WithTimeout(ctx, e.timeout)
	defer cancel()
	response, err := e.inner.Execute(toolCtx, call)
	if errors.Is(toolCtx.Err(), context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.Canceled) {
		if err == nil {
			err = context.DeadlineExceeded
		}
		return response, errors.Join(session.ErrLiveToolExecutionTimeout, err)
	}
	return response, err
}

func (h *handle) openingAdmissionRequired() bool {
	return h != nil && len(h.request.OpeningContentParts) > 0
}

func newScheduledAudioIncompleteError(scheduled, dispatched, completed int, terminal *messages.SessionCloseValue) error {
	if scheduled <= 0 {
		return nil
	}
	if completed < 0 {
		completed = 0
	}
	if completed > scheduled {
		completed = scheduled
	}
	if dispatched > scheduled {
		dispatched = scheduled
	}
	if completed >= scheduled && dispatched >= scheduled {
		return nil
	}
	incomplete := &session.LiveScheduledAudioIncompleteError{Completed: completed, Dispatched: dispatched, Scheduled: scheduled}
	if terminal != nil {
		incomplete.ProviderStatus = terminal.Classification
		incomplete.ProviderDetails = terminal.Reason
	}
	return incomplete
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
