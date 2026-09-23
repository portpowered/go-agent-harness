package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionduration"
)

const (
	defaultLivenessTimeout  = 10 * time.Second
	controllerErrorCapacity = 8
)

type scheduledRetry struct {
	delay    time.Duration
	dispatch func(context.Context) error
}

type controller struct {
	mu      sync.Mutex
	armMu   sync.Mutex
	options sessionduration.Options
	ctx     context.Context
	cancel  context.CancelFunc
	errors  chan error

	maxTimer             sessionTimer
	firstResponseTimer   sessionTimer
	firstResponseVersion uint64
	firstResponseStarted bool
	firstResponseSeen    bool
	timerWake            chan struct{}
	timerWorkerOnce      sync.Once
	timerWorkerWG        sync.WaitGroup

	livenessTimer      sessionTimer
	livenessGeneration uint64
	livenessArmed      bool
	livenessStopped    bool
	livenessFailure    error
	livenessReported   bool
	firstCauseOnce     sync.Once
	responseOutput     bool
	responseComplete   bool
	toolObligation     bool
	localToolActive    bool

	terminalWritten bool
	expired         bool
	outputState     messages.TerminalOutputState
	retriesUsed     int
	retryRequests   chan scheduledRetry
	closed          bool

	durationReported bool
	startOnce        sync.Once
	startErr         error
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
	options, needsClock, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	if needsClock && options.Clock == nil {
		return nil, sessionduration.ErrSchedulerUnavailable
	}
	ctx, cancel := context.WithCancel(options.Context)
	c := &controller{
		options:     options,
		ctx:         ctx,
		cancel:      cancel,
		errors:      make(chan error, controllerErrorCapacity),
		timerWake:   make(chan struct{}, 1),
		outputState: messages.TerminalOutputNone,
	}
	if options.Retry.Enabled {
		c.retryRequests = make(chan scheduledRetry, 1)
	}
	if !options.DeferStart {
		if err := c.Start(); err != nil {
			cancel()
			return nil, err
		}
	}
	return c, nil
}

func (c *controller) Start() error {
	if c == nil {
		return nil
	}
	c.startOnce.Do(func() {
		c.startTimerWorker()
		c.startErr = c.startMaxDuration(c.options.MaxDuration)
	})
	return c.startErr
}

func (c *controller) needsTimerWorker() bool {
	return c.options.MaxDuration > 0 || c.options.Liveness.Enabled || c.options.Liveness.RequireFirstResponse ||
		(c.options.Retry.Enabled && c.options.Clock != nil)
}

func (c *controller) startTimerWorker() {
	if c == nil || !c.needsTimerWorker() {
		return
	}
	c.timerWorkerOnce.Do(func() {
		c.timerWorkerWG.Add(1)
		go c.watchTimers()
	})
}

func (c *controller) notifyTimerWorker() {
	if c == nil || c.timerWake == nil {
		return
	}
	select {
	case c.timerWake <- struct{}{}:
	default:
	}
}

func (c *controller) ExpectProviderProgress() {
	if c != nil {
		c.armLiveness(false)
	}
}

func (c *controller) Observe(msg messages.StreamMessage) sessionduration.Admission {
	if c == nil {
		return sessionduration.Admission{Message: msg}
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return sessionduration.Admission{Message: msg, OutputState: messages.TerminalOutputNone}
	}
	if msg.Type == messages.StreamTypeSessionClose {
		admission, disarm := c.observeCloseLocked(msg)
		c.mu.Unlock()
		if disarm {
			c.stopLiveness()
		}
		c.stopFirstResponse()
		return admission
	}
	if c.expired {
		admission := sessionduration.Admission{Message: msg, OutputState: c.outputState}
		c.mu.Unlock()
		return admission
	}
	return c.observeActiveLocked(msg)
}

func (c *controller) observeActiveLocked(msg messages.StreamMessage) sessionduration.Admission {
	c.observeOutputLocked(msg)
	armFirstResponse, stopFirstResponse := c.observeFirstResponseLocked(msg)
	arm, reset, disarm, failure := c.observeLivenessLocked(msg)
	admission := sessionduration.Admission{Message: msg, Accepted: true, OutputState: c.outputState}
	if failure != nil && c.livenessFailure == nil {
		c.livenessFailure = failure
		admission.LivenessErr = failure
	}
	c.mu.Unlock()

	if armFirstResponse {
		c.armFirstResponse()
	}
	if stopFirstResponse {
		c.stopFirstResponse()
	}
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
		c.reportLiveness(failure)
	}
	return admission
}

func (c *controller) ObserveDrain(msg messages.StreamMessage) sessionduration.Admission {
	if c == nil {
		return sessionduration.Admission{Message: msg}
	}
	c.mu.Lock()
	if !c.expired {
		c.mu.Unlock()
		return c.Observe(msg)
	}
	if msg.Type == messages.StreamTypeSessionClose {
		admission, disarm := c.observeCloseLocked(msg)
		c.mu.Unlock()
		if disarm {
			c.stopLiveness()
		}
		return admission
	}
	if c.closed {
		c.mu.Unlock()
		return sessionduration.Admission{Message: msg}
	}
	if msg.Type == messages.StreamTypeError {
		admission := sessionduration.Admission{Message: msg, OutputState: c.outputState}
		c.mu.Unlock()
		return admission
	}
	c.observeOutputLocked(msg)
	admission := sessionduration.Admission{Message: msg, Accepted: true, OutputState: c.outputState}
	c.mu.Unlock()
	return admission
}

func (c *controller) observeCloseLocked(msg messages.StreamMessage) (sessionduration.Admission, bool) {
	provider := c.options.Terminal.Matches == nil || c.options.Terminal.Matches(msg)
	if c.terminalWritten || (c.expired && !provider) {
		return sessionduration.Admission{Message: msg, OutputState: c.outputState, TerminalSeen: provider}, false
	}
	c.terminalWritten = true
	return sessionduration.Admission{Message: msg, Accepted: true, OutputState: c.outputState, TerminalSeen: true}, true
}

func (c *controller) startMaxDuration(maxDuration time.Duration) error {
	if maxDuration <= 0 {
		return nil
	}
	timer := c.options.Clock.NewTimer(maxDuration)
	if timer == nil {
		return fmt.Errorf("session duration clock returned a nil timer: %w", sessionduration.ErrSchedulerUnavailable)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		timer.Stop()
		return nil
	}
	c.maxTimer = timer
	c.mu.Unlock()
	c.notifyTimerWorker()
	return nil
}

func (c *controller) SetToolObligation(obligation bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.toolObligation = obligation
	c.mu.Unlock()
}

func (c *controller) BeginLocalToolExecution() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.localToolActive = true
	c.mu.Unlock()
	c.stopLiveness()
}

func (c *controller) EndLocalToolExecution() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.localToolActive = false
	c.mu.Unlock()
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
		c.firstCauseOnce.Do(func() { c.options.FirstCause(err) })
	}
}

func (c *controller) reportLiveness(err error) {
	c.report(err)
	c.mu.Lock()
	c.livenessReported = true
	c.mu.Unlock()
}

func (c *controller) Expire() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed || c.expired {
		c.mu.Unlock()
		return sessionduration.ErrMaxDurationExceeded
	}
	c.expired = true
	timer := c.maxTimer
	c.maxTimer = nil
	report := !c.durationReported
	c.durationReported = true
	c.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	c.notifyTimerWorker()
	if report {
		c.report(sessionduration.ErrMaxDurationExceeded)
	}
	return sessionduration.ErrMaxDurationExceeded
}

func (c *controller) Retry(request sessionduration.RetryRequest) sessionduration.RetryDecision {
	if c == nil || request.Terminal == nil {
		return sessionduration.RetryDecision{}
	}
	if request.Dispatch != nil && c.options.Clock == nil {
		c.report(sessionduration.ErrSchedulerUnavailable)
		return sessionduration.RetryDecision{}
	}
	c.mu.Lock()
	if c.closed || c.expired || !c.options.Retry.Enabled {
		c.mu.Unlock()
		return sessionduration.RetryDecision{}
	}
	decision := EvaluateRetry(c.options.Retry, request.Terminal)
	if !decision.Eligible {
		c.mu.Unlock()
		return decision
	}
	maxRetries := c.options.Retry.MaxRetries
	if maxRetries == 0 {
		maxRetries = 1
	}
	if c.retriesUsed >= maxRetries {
		c.mu.Unlock()
		c.report(sessionduration.ErrRateLimitRetryExhausted)
		return sessionduration.RetryDecision{Exhausted: true}
	}
	c.retriesUsed++
	c.mu.Unlock()
	if request.Dispatch != nil {
		select {
		case c.retryRequests <- scheduledRetry{delay: decision.Delay, dispatch: request.Dispatch}:
			c.notifyTimerWorker()
		case <-c.ctx.Done():
			return sessionduration.RetryDecision{}
		}
	}
	return decision
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
