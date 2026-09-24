package service

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

const (
	defaultRetryDelay     = 2 * time.Second
	defaultRetryMaxDelay  = 15 * time.Second
	maxStatusDetailBytes  = 256
	providerRateLimitCode = "rate_limit_exceeded"
)

const retryDelayPattern = `(?i)\bplease\s+try\s+again\s+in\s+((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))s\b`

// EvaluateRetry classifies one provider terminal without consuming retry
// budget or waiting. Controller callers use it to keep provider parsing in
// the sessionduration service while the host owns the actual scheduler wait.
func EvaluateRetry(policy sessionduration.RetryPolicy, terminal *messages.MessageEndValue) sessionduration.RetryDecision {
	if terminal == nil || !policy.Enabled || strings.ToLower(strings.TrimSpace(terminal.Status)) != "failed" || terminal.TerminalReason == messages.TerminalReasonCancellation || retryCode(terminal) != providerRateLimitCode {
		return sessionduration.RetryDecision{}
	}
	defaultDelay := policy.DefaultDelay
	if defaultDelay <= 0 {
		defaultDelay = defaultRetryDelay
	}
	maxDelay := policy.MaxDelay
	if maxDelay <= 0 {
		maxDelay = defaultRetryMaxDelay
	}
	return sessionduration.RetryDecision{Delay: retryDelay(retryMessage(terminal), defaultDelay, maxDelay), Eligible: true}
}

func retryCode(terminal *messages.MessageEndValue) string {
	if value := strings.TrimSpace(terminal.ProviderErrorCode); value != "" {
		return value
	}
	return statusDetail(terminal.StatusDetails, "code")
}

func retryMessage(terminal *messages.MessageEndValue) string {
	if value := strings.TrimSpace(terminal.ProviderErrorMessage); value != "" {
		return value
	}
	return statusDetail(terminal.StatusDetails, "message")
}

func statusDetail(details, wanted string) string {
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
		if len(value) > maxStatusDetailBytes {
			return value[:maxStatusDetailBytes]
		}
		return value
	}
	return ""
}

func retryDelay(message string, defaultDelay, maxDelay time.Duration) time.Duration {
	match := regexp.MustCompile(retryDelayPattern).FindStringSubmatch(message)
	if len(match) != 2 {
		return defaultDelay
	}
	seconds, err := strconv.ParseFloat(match[1], 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return defaultDelay
	}
	if seconds > maxDelay.Seconds() {
		return maxDelay
	}
	delay := time.Duration(math.Round(seconds * float64(time.Second)))
	if delay <= 0 {
		return time.Nanosecond
	}
	return delay
}

func (r *runLoop) retry(msg messages.StreamMessage) error {
	if msg.Type != messages.StreamTypeMessageEnd {
		return nil
	}
	terminal, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || terminal == nil {
		return nil
	}
	decision := r.controller.Retry(sessionduration.RetryRequest{Terminal: terminal})
	if !decision.Eligible {
		return nil
	}
	sender, ok := r.loop.(sessionduration.SessionEventSender)
	if !ok {
		return errors.New("session duration loop does not support provider session events")
	}
	waited, err := r.waitForRetry(decision.Delay)
	if err != nil {
		return err
	}
	if !waited {
		return r.finish(false, nil)
	}
	control := messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()}
	if err := sender.SendSessionEvent(r.runCtx, control); err != nil {
		return fmt.Errorf("send rate-limit retry response: %w", err)
	}
	if r.request.RetryDispatched != nil {
		r.request.RetryDispatched(control)
	}
	return nil
}

func (r *runLoop) waitForRetry(delay time.Duration) (bool, error) {
	if delay <= 0 {
		return true, nil
	}
	if r.request.Clock == nil {
		return false, sessionduration.ErrSchedulerUnavailable
	}
	timer := r.request.Clock.NewTimer(delay)
	if timer == nil {
		return false, errors.New("session duration clock returned a nil retry timer")
	}
	defer timer.Stop()
	select {
	case <-timer.C():
		return true, nil
	case err := <-r.controller.Errors():
		return false, err
	case <-r.request.Done:
		return false, runLoopDoneError(r.request)
	case <-r.ctx.Done():
		return false, r.ctx.Err()
	case <-r.runCtx.Done():
		return false, r.runCtx.Err()
	}
}
