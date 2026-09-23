package service

import (
	"context"
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

func normalizeOptions(options sessionduration.Options) (sessionduration.Options, bool, error) {
	if options.MaxDuration < 0 {
		return options, false, &sessionduration.InvalidDurationError{Duration: options.MaxDuration}
	}
	if err := validatePolicyValues(options); err != nil {
		return options, false, err
	}
	options.Liveness = normalizeLivenessPolicy(options.Liveness)
	options.Retry = normalizeRetryPolicy(options.Retry)
	if options.Context == nil {
		options.Context = context.Background()
	}
	if options.LivenessClock == nil {
		options.LivenessClock = options.Clock
	}
	if requiresLivenessClock(options.Liveness) && options.LivenessClock == nil {
		return options, false, sessionduration.ErrSchedulerUnavailable
	}
	return options, options.MaxDuration > 0, nil
}

func validatePolicyValues(options sessionduration.Options) error {
	if options.Liveness.Timeout < 0 || options.Liveness.FirstResponseTimeout < 0 ||
		options.Retry.MaxRetries < 0 || options.Retry.DefaultDelay < 0 || options.Retry.MaxDelay < 0 {
		return fmt.Errorf("session duration policy values must be non-negative")
	}
	return nil
}

func normalizeLivenessPolicy(policy sessionduration.LivenessOptions) sessionduration.LivenessOptions {
	if policy.Timeout > 0 {
		policy.Enabled = true
	}
	if policy.FirstResponseTimeout > 0 {
		policy.RequireFirstResponse = true
	}
	if policy.RequireFirstResponse && policy.FirstResponseTimeout == 0 {
		policy.FirstResponseTimeout = defaultFirstResponseTimeout
	}
	return policy
}

func normalizeRetryPolicy(policy sessionduration.RetryPolicy) sessionduration.RetryPolicy {
	if policy.MaxRetries != 0 || policy.DefaultDelay != 0 || policy.MaxDelay != 0 {
		policy.Enabled = true
	}
	return policy
}

func requiresLivenessClock(policy sessionduration.LivenessOptions) bool {
	return policy.Enabled || policy.RequireFirstResponse
}

const (
	defaultFirstResponseTimeout = 30 * time.Second
	defaultRetryDelay           = 2 * time.Second
	defaultRetryMaxDelay        = 15 * time.Second
	maxStatusDetailBytes        = 256
	providerRateLimitCode       = "rate_limit_exceeded"
)

const retryDelayPattern = `(?i)\bplease\s+try\s+again\s+in\s+((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))s\b`

func (c *controller) observeFirstResponseLocked(msg messages.StreamMessage) (arm, stop bool) {
	if !c.options.Liveness.RequireFirstResponse {
		return false, false
	}
	if isFirstResponseBoundary(msg) {
		c.firstResponseSeen = true
		stop = c.firstResponseTimer != nil
	}
	if msg.Type == messages.StreamTypeSessionOpen && !c.firstResponseStarted && !c.firstResponseSeen {
		c.firstResponseStarted = true
		arm = true
	}
	return arm, stop
}

func isFirstResponseBoundary(msg messages.StreamMessage) bool {
	switch msg.Type {
	case messages.StreamTypeMessageStart, messages.StreamTypeMessageEnd, messages.StreamTypeTextStart,
		messages.StreamTypeAudioStart, messages.StreamTypeImageStart, messages.StreamTypeToolCallStart,
		messages.StreamTypeReasoningStart, messages.StreamTypeTranscriptStart, messages.StreamTypeError:
		return true
	default:
		return false
	}
}

func (c *controller) armFirstResponse() {
	timer := c.options.LivenessClock.NewTimer(c.options.Liveness.FirstResponseTimeout)
	if timer == nil {
		c.mu.Lock()
		c.firstResponseSeen = true
		c.mu.Unlock()
		c.report(sessionduration.ErrSchedulerUnavailable)
		return
	}
	c.mu.Lock()
	if c.closed || c.firstResponseSeen || !c.firstResponseStarted {
		c.mu.Unlock()
		timer.Stop()
		return
	}
	previous := c.firstResponseTimer
	c.firstResponseTimer = timer
	c.firstResponseVersion++
	c.mu.Unlock()
	if previous != nil {
		previous.Stop()
	}
	c.notifyTimerWorker()
}

func (c *controller) stopFirstResponse() {
	if c == nil {
		return
	}
	c.mu.Lock()
	timer := c.firstResponseTimer
	c.firstResponseTimer = nil
	c.firstResponseVersion++
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
		c.notifyTimerWorker()
	}
}

func (c *controller) expireFirstResponse(version uint64) {
	c.mu.Lock()
	if c.closed || c.firstResponseSeen || !c.firstResponseStarted || c.firstResponseVersion != version || c.firstResponseTimer == nil {
		c.mu.Unlock()
		return
	}
	timer := c.firstResponseTimer
	c.firstResponseTimer = nil
	c.firstResponseVersion++
	c.firstResponseSeen = true
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	c.notifyTimerWorker()
	c.report(sessionduration.ErrFirstResponseTimeout)
}

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

func (s *Service) Complete(request sessionduration.CompletionRequest) error {
	err := request.RunError
	completionFacts := completionFacts(request)
	observerActive := request.Observer != nil && request.Observer.Active()
	if observerActive {
		completionFacts = request.Observer.CompletionFacts()
	}
	roomBoundCancellation := request.RoomBoundCancellation || channelClosed(request.BoundCancellation)
	checkResponse := responseCheckRequired(request, completionFacts, observerActive, roomBoundCancellation)
	if checkResponse {
		err = completeAssistantResponse(err, request, completionFacts)
	}
	return completeScheduledAudio(err, request, completionFacts)
}

func completionFacts(request sessionduration.CompletionRequest) sessionduration.CompletionFacts {
	return sessionduration.CompletionFacts{
		ProviderToolCallObserved:     request.ProviderToolCallObserved,
		AssistantResponseCompleted:   request.AssistantResponseCompleted,
		ScheduledAudioIncomplete:     request.ScheduledAudioIncomplete,
		ScheduledAudioCompleted:      request.ScheduledAudioCompleted,
		ScheduledAudioDispatched:     request.ScheduledAudioDispatched,
		ScheduledAudioCount:          request.ScheduledAudioCount,
		ProviderScheduledStatus:      request.ProviderScheduledStatus,
		ProviderScheduledErrorCode:   request.ProviderScheduledErrorCode,
		ProviderScheduledErrorDetail: request.ProviderScheduledErrorDetails,
	}
}

func responseCheckRequired(request sessionduration.CompletionRequest, facts sessionduration.CompletionFacts, observerActive, roomBoundCancellation bool) bool {
	if roomBoundCancellation || request.DurationExpired {
		return false
	}
	if request.HasAudioInput || request.RequireAssistantResponse || request.RequireTerminalAssistantReply {
		return true
	}
	if observerActive {
		return facts.ProviderToolCallObserved
	}
	return request.ProviderToolCallObserved
}

func completeAssistantResponse(err error, request sessionduration.CompletionRequest, facts sessionduration.CompletionFacts) error {
	if request.AudioOutputError != nil {
		err = errors.Join(err, request.AudioOutputError())
	}
	if facts.AssistantResponseCompleted || (!request.RequireTerminalAssistantReply && !facts.ProviderToolCallObserved) {
		return err
	}
	incomplete := request.AssistantResponseIncomplete
	if incomplete == nil {
		incomplete = sessionduration.ErrAssistantResponseIncomplete
	}
	return errors.Join(err, incomplete)
}

func completeScheduledAudio(err error, request sessionduration.CompletionRequest, facts sessionduration.CompletionFacts) error {
	scheduledCause := request.ScheduledAudioIncompleteCause
	if scheduledCause == nil {
		scheduledCause = sessionduration.ErrScheduledAudioIncomplete
	}
	if !request.DurationExpired && request.CloseAfterScheduledAudio && facts.ScheduledAudioIncomplete &&
		!errors.Is(err, scheduledCause) && !errors.Is(err, sessionduration.ErrScheduledAudioIncomplete) {
		err = errors.Join(err, &sessionduration.ScheduledAudioIncompleteError{
			Completed:         facts.ScheduledAudioCompleted,
			Dispatched:        facts.ScheduledAudioDispatched,
			Scheduled:         facts.ScheduledAudioCount,
			ProviderStatus:    facts.ProviderScheduledStatus,
			ProviderErrorCode: facts.ProviderScheduledErrorCode,
			ProviderDetails:   facts.ProviderScheduledErrorDetail,
			Cause:             scheduledCause,
		})
	}
	return err
}

func channelClosed(signal <-chan struct{}) bool {
	if signal == nil {
		return false
	}
	select {
	case <-signal:
		return true
	default:
		return false
	}
}
