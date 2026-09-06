package live

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

const (
	defaultFirstTurnTimeout       = 30 * time.Second
	defaultRateLimitRetryDelay    = 2 * time.Second
	defaultRateLimitRetryMaxDelay = 15 * time.Second
	rateLimitRetryCode            = "rate_limit_exceeded"
)

var rateLimitRetryDelayPattern = regexp.MustCompile(`(?i)\bplease\s+try\s+again\s+in\s+((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))s\b`)

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
	delay, eligible := rateLimitRetryDecision(terminal, h.request.RateLimitRetry)
	if !eligible {
		return
	}
	if !h.claimRateLimitRetry() {
		h.Cancel(fmt.Errorf("%w: provider returned %s", session.ErrLiveRateLimitRetryExhausted, rateLimitRetryCode))
		return
	}
	request := retryRequest{loop: loop, deadline: h.scheduler.Now().Add(delay)}
	select {
	case h.retryRequests <- request:
	case <-h.parentContext().Done():
	}
}

func (h *handle) claimRateLimitRetry() bool {
	if h == nil {
		return false
	}
	h.retryMu.Lock()
	defer h.retryMu.Unlock()
	maxRetries := h.request.RateLimitRetry.MaxRetries
	if maxRetries == 0 {
		maxRetries = 1
	}
	if h.retriesUsed >= maxRetries {
		return false
	}
	h.retriesUsed++
	return true
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

func rateLimitRetryDecision(terminal *messages.MessageEndValue, policy session.LiveRateLimitRetryPolicy) (time.Duration, bool) {
	if terminal == nil || strings.ToLower(strings.TrimSpace(terminal.Status)) != "failed" {
		return 0, false
	}
	if strings.EqualFold(strings.TrimSpace(string(terminal.TerminalReason)), string(messages.TerminalReasonCancellation)) {
		return 0, false
	}
	if !strings.EqualFold(rateLimitErrorCode(terminal), rateLimitRetryCode) {
		return 0, false
	}
	defaultDelay := policy.DefaultDelay
	if defaultDelay <= 0 {
		defaultDelay = defaultRateLimitRetryDelay
	}
	maxDelay := policy.MaxDelay
	if maxDelay <= 0 {
		maxDelay = defaultRateLimitRetryMaxDelay
	}
	delay := parseRateLimitRetryDelay(rateLimitErrorMessage(terminal), defaultDelay, maxDelay)
	return delay, true
}

func rateLimitErrorCode(terminal *messages.MessageEndValue) string {
	if terminal == nil {
		return ""
	}
	if value := strings.TrimSpace(terminal.ProviderErrorCode); value != "" {
		return value
	}
	return statusDetailField(terminal.StatusDetails, "code")
}

func rateLimitErrorMessage(terminal *messages.MessageEndValue) string {
	if terminal == nil {
		return ""
	}
	if value := strings.TrimSpace(terminal.ProviderErrorMessage); value != "" {
		return value
	}
	return statusDetailField(terminal.StatusDetails, "message")
}

func statusDetailField(details, wanted string) string {
	parts := strings.Split(details, ",")
	for index, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), wanted) {
			continue
		}
		value = strings.TrimSpace(value)
		if wanted == "message" && index+1 < len(parts) {
			value = strings.TrimSpace(strings.Join(append([]string{value}, parts[index+1:]...), ","))
		}
		return value
	}
	return ""
}

func parseRateLimitRetryDelay(message string, defaultDelay, maxDelay time.Duration) time.Duration {
	match := rateLimitRetryDelayPattern.FindStringSubmatch(message)
	if len(match) != 2 {
		return defaultDelay
	}
	seconds, err := strconv.ParseFloat(match[1], 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return defaultDelay
	}
	delay := time.Duration(math.Round(seconds * float64(time.Second)))
	if delay <= 0 {
		delay = time.Nanosecond
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
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
