package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

const (
	defaultLivenessTimeout = 10 * time.Second
	defaultRetryDelay      = 2 * time.Second
	defaultRetryMaxDelay   = 15 * time.Second
	maxStatusDetailBytes   = 256
	providerRetryCode      = "rate_limit_exceeded"
)

var retryDelayPattern = regexp.MustCompile(`(?i)\bplease\s+try\s+again\s+in\s+((?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+))s\b`)

type controller struct {
	mu      sync.Mutex
	options sessionduration.Options
	ctx     context.Context
	cancel  context.CancelFunc
	errors  chan error

	maxTimer sessionTimer

	livenessTimer      sessionTimer
	livenessWake       chan struct{}
	livenessGeneration uint64
	livenessArmed      bool
	livenessStopped    bool
	livenessFailure    error
	responseOutput     bool
	responseComplete   bool
	toolObligation     bool

	terminalWritten  bool
	expired          bool
	expiryPublishing bool
	outputState      messages.TerminalOutputState
	retriesUsed      int
	closed           bool

	durationReported bool
	finalizeOnce     sync.Once
	finalizeResult   sessionduration.Result
	finalizeErr      error
}

type sessionTimer interface {
	C() <-chan time.Time
	Stop() bool
}

var _ sessionduration.Controller = (*controller)(nil)

func (s *Service) Begin(options sessionduration.Options) (sessionduration.Controller, error) {
	if options.MaxDuration < 0 {
		return nil, &sessionduration.InvalidDurationError{Duration: options.MaxDuration}
	}
	if options.Liveness.Timeout < 0 || options.Retry.MaxRetries < 0 || options.Retry.DefaultDelay < 0 || options.Retry.MaxDelay < 0 {
		return nil, fmt.Errorf("session duration policy values must be non-negative")
	}
	if options.Liveness.Timeout > 0 {
		options.Liveness.Enabled = true
	}
	if options.Retry.MaxRetries != 0 || options.Retry.DefaultDelay != 0 || options.Retry.MaxDelay != 0 {
		options.Retry.Enabled = true
	}
	if options.Context == nil {
		options.Context = context.Background()
	}
	needsClock := options.MaxDuration > 0 || options.Liveness.Enabled || options.Retry.Enabled
	if needsClock && options.Clock == nil {
		return nil, sessionduration.ErrSchedulerUnavailable
	}
	ctx, cancel := context.WithCancel(options.Context)
	c := &controller{
		options:      options,
		ctx:          ctx,
		cancel:       cancel,
		errors:       make(chan error, 8),
		livenessWake: make(chan struct{}, 1),
		outputState:  messages.TerminalOutputNone,
	}
	if options.MaxDuration > 0 {
		timer := options.Clock.NewTimer(options.MaxDuration)
		if timer == nil {
			cancel()
			return nil, sessionduration.ErrSchedulerUnavailable
		}
		c.maxTimer = timer
		go c.watchMaxDuration(timer)
	}
	return c, nil
}

func (c *controller) watchMaxDuration(timer sessionTimer) {
	select {
	case <-timer.C():
		if err := c.Expire(); err != nil && !errors.Is(err, sessionduration.ErrMaxDurationExceeded) {
			c.report(err)
		}
	case <-c.ctx.Done():
	}
}

func (c *controller) Observe(msg messages.StreamMessage) sessionduration.Admission {
	if c == nil {
		return sessionduration.Admission{Message: msg}
	}
	var arm, reset, disarm bool
	var failure error
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return sessionduration.Admission{Message: msg, OutputState: messages.TerminalOutputNone}
	}
	if msg.Type == messages.StreamTypeSessionClose {
		provider := c.options.Terminal.Matches == nil || c.options.Terminal.Matches(msg)
		if c.terminalWritten || (c.expired && !provider) {
			admission := sessionduration.Admission{Message: msg, OutputState: c.outputState, TerminalSeen: provider}
			c.mu.Unlock()
			return admission
		}
		c.terminalWritten = true
		disarm = true
		admission := sessionduration.Admission{Message: msg, Accepted: true, OutputState: c.outputState, TerminalSeen: true}
		c.mu.Unlock()
		if disarm {
			c.stopLiveness()
		}
		return admission
	}
	if c.expired && msg.Type != messages.StreamTypeError {
		admission := sessionduration.Admission{Message: msg, OutputState: c.outputState}
		c.mu.Unlock()
		return admission
	}

	c.observeOutputLocked(msg)
	if c.options.Liveness.Enabled && msg.Role != messages.RoleTool && msg.ResponsePurpose != messages.ResponsePurposeToolAcknowledgement {
		switch {
		case msg.Type == messages.StreamTypeMessageStart:
			arm = true
		case msg.Type == messages.StreamTypeMessageEnd:
			if c.isEmptyResponseLocked(msg) {
				failure = c.makeLivenessErrorLocked(msg, false)
			}
			disarm = true
		case msg.Type == messages.StreamTypeSessionOpen:
			// The provider has not started a response yet.
		case isProviderOutput(msg) || msg.Type == messages.StreamTypeToolCallStart || msg.Type == messages.StreamTypeToolCallDelta || msg.Type == messages.StreamTypeToolCallEnd:
			reset = true
		case msg.Type == messages.StreamTypeError:
			disarm = true
		}
	}
	admission := sessionduration.Admission{Message: msg, Accepted: true, OutputState: c.outputState}
	if failure != nil && c.livenessFailure == nil {
		c.livenessFailure = failure
		admission.LivenessErr = failure
	}
	c.mu.Unlock()

	if arm {
		c.armLiveness(false)
	}
	if reset {
		c.armLiveness(true)
	}
	if disarm {
		c.stopLiveness()
	}
	if failure != nil {
		c.report(failure)
	}
	return admission
}

func (c *controller) observeOutputLocked(msg messages.StreamMessage) {
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		c.responseOutput = false
		c.responseComplete = false
		c.toolObligation = false
	case messages.StreamTypeTextDelta, messages.StreamTypeReasoningDelta, messages.StreamTypeAudioDelta, messages.StreamTypeImageDelta, messages.StreamTypeVideoDelta, messages.StreamTypeFileDelta, messages.StreamTypeEmbeddingDelta, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd, messages.StreamTypeRefusal:
		if msg.Role != messages.RoleUser && msg.Role != messages.RoleTool {
			c.responseOutput = true
		}
	case messages.StreamTypeTranscriptDelta:
		if msg.Role != messages.RoleUser && msg.Role != messages.RoleTool {
			c.responseOutput = true
		}
	case messages.StreamTypeToolCallStart:
		c.toolObligation = true
	case messages.StreamTypeMessageEnd:
		c.responseComplete = true
	}
	if !c.responseOutput {
		c.outputState = messages.TerminalOutputNone
	} else if c.responseComplete {
		c.outputState = messages.TerminalOutputComplete
	} else {
		c.outputState = messages.TerminalOutputPartial
	}
}

func (c *controller) isEmptyResponseLocked(msg messages.StreamMessage) bool {
	value, ok := msg.Value.(*messages.MessageEndValue)
	if !ok || value == nil || c.responseOutput || c.toolObligation || msg.ResponsePurpose == messages.ResponsePurposeToolAcknowledgement {
		return false
	}
	if value.TerminalReason != messages.TerminalReasonPartialOutput || value.OutputState != messages.TerminalOutputNone || value.Usage.CompletionTokens != 0 {
		return false
	}
	return value.TerminalReason != messages.TerminalReasonCancellation && !strings.EqualFold(strings.TrimSpace(value.Status), "cancelled")
}

func (c *controller) makeLivenessErrorLocked(msg messages.StreamMessage, timeout bool) error {
	classification := "silent_provider_empty_response"
	cause := sessionduration.ErrProviderEmptyResponse
	if timeout {
		classification = "silent_provider_timeout"
		cause = sessionduration.ErrProviderLivenessTimeout
	}
	err := &sessionduration.LivenessError{Classification: classification, ResponseID: strings.TrimSpace(msg.ResponseID), Cause: cause}
	if value, ok := msg.Value.(*messages.MessageEndValue); ok && value != nil {
		err.Usage = value.Usage
	}
	return err
}

func isProviderOutput(msg messages.StreamMessage) bool {
	if msg.Role == messages.RoleUser || msg.Role == messages.RoleTool {
		return false
	}
	switch msg.Type {
	case messages.StreamTypeTextDelta, messages.StreamTypeAudioDelta, messages.StreamTypeImageDelta, messages.StreamTypeVideoDelta, messages.StreamTypeFileDelta, messages.StreamTypeEmbeddingDelta, messages.StreamTypeReasoningDelta, messages.StreamTypeTranscriptDelta, messages.StreamTypeTextEnd, messages.StreamTypeAudioEnd, messages.StreamTypeImageEnd, messages.StreamTypeVideoEnd, messages.StreamTypeFileEnd, messages.StreamTypeEmbeddingEnd, messages.StreamTypeReasoningEnd, messages.StreamTypeTranscriptEnd:
		return true
	default:
		return false
	}
}

func (c *controller) Errors() <-chan error {
	if c == nil {
		return nil
	}
	return c.errors
}

func (c *controller) armLiveness(onlyIfArmed bool) {
	if c == nil || !c.options.Liveness.Enabled || c.options.Clock == nil {
		return
	}
	timeout := c.options.Liveness.Timeout
	if timeout <= 0 {
		timeout = defaultLivenessTimeout
	}
	c.mu.Lock()
	if c.closed || c.livenessStopped || (onlyIfArmed && !c.livenessArmed) {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	timer := c.options.Clock.NewTimer(timeout)
	if timer == nil {
		c.report(sessionduration.ErrSchedulerUnavailable)
		return
	}
	c.mu.Lock()
	if c.closed || c.livenessStopped || (onlyIfArmed && !c.livenessArmed) {
		c.mu.Unlock()
		timer.Stop()
		return
	}
	old := c.livenessTimer
	c.livenessTimer = timer
	c.livenessArmed = true
	c.livenessGeneration++
	wake := c.livenessWake
	c.mu.Unlock()
	if old != nil {
		old.Stop()
	}
	select {
	case wake <- struct{}{}:
	default:
	}
	go c.watchLiveness()
}

func (c *controller) watchLiveness() {
	for {
		c.mu.Lock()
		if c.closed || c.livenessStopped || !c.livenessArmed || c.livenessTimer == nil {
			c.mu.Unlock()
			return
		}
		generation := c.livenessGeneration
		timer := c.livenessTimer
		wake := c.livenessWake
		ctx := c.ctx
		c.mu.Unlock()
		select {
		case <-timer.C():
			c.expireLiveness(generation)
			return
		case <-wake:
		case <-ctx.Done():
			return
		}
	}
}

func (c *controller) expireLiveness(generation uint64) {
	c.mu.Lock()
	if c.closed || c.livenessStopped || !c.livenessArmed || c.livenessGeneration != generation || c.livenessFailure != nil {
		c.mu.Unlock()
		return
	}
	err := c.makeLivenessErrorLocked(messages.StreamMessage{}, true)
	c.livenessFailure = err
	c.livenessArmed = false
	timer := c.livenessTimer
	c.livenessTimer = nil
	c.livenessGeneration++
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	c.report(err)
}

func (c *controller) stopLiveness() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.livenessArmed && c.livenessTimer == nil {
		c.mu.Unlock()
		return
	}
	c.livenessArmed = false
	c.livenessGeneration++
	timer := c.livenessTimer
	c.livenessTimer = nil
	wake := c.livenessWake
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (c *controller) report(err error) {
	if err == nil || c == nil {
		return
	}
	select {
	case c.errors <- err:
	default:
	}
	if c.options.FirstCause != nil {
		c.options.FirstCause(err)
	}
}

func (c *controller) Expire() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed || c.terminalWritten || c.expiryPublishing {
		c.mu.Unlock()
		return nil
	}
	c.expired = true
	c.expiryPublishing = true
	publication := c.options.Publication
	output := c.outputState
	c.mu.Unlock()
	err := publishMaxDuration(publication, output)
	c.mu.Lock()
	c.expiryPublishing = false
	if err == nil {
		c.terminalWritten = true
	}
	if !c.durationReported {
		c.durationReported = true
		c.mu.Unlock()
		c.report(sessionduration.ErrMaxDurationExceeded)
	} else {
		c.mu.Unlock()
	}
	return errors.Join(sessionduration.ErrMaxDurationExceeded, err)
}

func (c *controller) Retry(request sessionduration.RetryRequest) sessionduration.RetryDecision {
	if c == nil || request.Terminal == nil {
		return sessionduration.RetryDecision{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.options.Retry.Enabled || strings.ToLower(strings.TrimSpace(request.Terminal.Status)) != "failed" || request.Terminal.TerminalReason == messages.TerminalReasonCancellation || providerErrorCode(request.Terminal) != providerRetryCode {
		return sessionduration.RetryDecision{}
	}
	maxRetries := c.options.Retry.MaxRetries
	if maxRetries == 0 {
		maxRetries = 1
	}
	if c.retriesUsed >= maxRetries {
		return sessionduration.RetryDecision{Exhausted: true}
	}
	c.retriesUsed++
	defaultDelay := c.options.Retry.DefaultDelay
	if defaultDelay <= 0 {
		defaultDelay = defaultRetryDelay
	}
	maxDelay := c.options.Retry.MaxDelay
	if maxDelay <= 0 {
		maxDelay = defaultRetryMaxDelay
	}
	return sessionduration.RetryDecision{Delay: parseRetryDelay(providerErrorMessage(request.Terminal), defaultDelay, maxDelay), Eligible: true}
}

func providerErrorCode(terminal *messages.MessageEndValue) string {
	if value := strings.TrimSpace(terminal.ProviderErrorCode); value != "" {
		return value
	}
	return statusDetail(terminal.StatusDetails, "code")
}

func providerErrorMessage(terminal *messages.MessageEndValue) string {
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

func parseRetryDelay(message string, defaultDelay, maxDelay time.Duration) time.Duration {
	match := retryDelayPattern.FindStringSubmatch(message)
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

func (c *controller) OutputState() messages.TerminalOutputState {
	if c == nil {
		return messages.TerminalOutputNone
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.outputState
}

func (c *controller) TerminalWritten() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terminalWritten
}

func (c *controller) Finalize(ctx context.Context, request sessionduration.FinalizeRequest) (result sessionduration.Result, err error) {
	if c == nil {
		return sessionduration.Result{}, request.Primary
	}
	c.finalizeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.livenessStopped = true
		maxTimer := c.maxTimer
		liveTimer := c.livenessTimer
		c.maxTimer = nil
		c.livenessTimer = nil
		c.mu.Unlock()
		if maxTimer != nil {
			maxTimer.Stop()
		}
		if liveTimer != nil {
			liveTimer.Stop()
		}
		c.cancel()

		var failures []error
		appendFailure := func(label string, cleanup func() error) {
			if cleanup == nil {
				return
			}
			if cleanupErr := invokeCleanup(cleanup); cleanupErr != nil {
				failures = append(failures, fmt.Errorf("%s: %w", label, cleanupErr))
			}
		}
		if ctx == nil {
			ctx = c.ctx
		}
		if request.Drain != nil {
			appendFailure("drain session", func() error { return request.Drain(ctx) })
		}
		appendFailure("close session", request.Close)
		appendFailure("close device binding", request.Binding)
		artifacts := request.Artifacts
		if artifacts == nil {
			artifacts = c.options.Artifacts
		}
		if artifacts != nil {
			appendFailure("flush artifacts", artifacts.Flush)
			appendFailure("close artifacts", artifacts.Close)
		}
		c.mu.Lock()
		c.finalizeResult = sessionduration.Result{OutputState: c.outputState, TerminalWritten: c.terminalWritten, Expired: c.expired}
		c.finalizeErr = errors.Join(appendError(request.Primary), errors.Join(failures...))
		c.mu.Unlock()
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.finalizeResult, c.finalizeErr
}

func appendError(err error) error { return err }

func invokeCleanup(cleanup func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("session finalization panicked: %v", recovered)
		}
	}()
	return cleanup()
}
