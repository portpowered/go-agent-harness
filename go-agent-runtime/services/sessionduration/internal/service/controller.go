package service

import (
	"context"
	"errors"
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

type controller struct {
	mu                sync.Mutex
	armMu             sync.Mutex
	livenessWatchOnce sync.Once
	options           sessionduration.Options
	ctx               context.Context
	cancel            context.CancelFunc
	errors            chan error

	maxTimer sessionTimer

	livenessTimer      sessionTimer
	livenessWake       chan struct{}
	livenessGeneration uint64
	livenessArmed      bool
	livenessStopped    bool
	livenessFailure    error
	livenessReported   bool
	livenessResponseID string
	livenessCancelled  bool
	livenessCancelID   string
	firstCauseOnce     sync.Once
	responseOutput     bool
	responseComplete   bool
	toolObligation     bool
	localToolActive    bool

	terminalWritten bool
	expired         bool
	outputState     messages.TerminalOutputState
	retriesUsed     int
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
		options:      options,
		ctx:          ctx,
		cancel:       cancel,
		errors:       make(chan error, controllerErrorCapacity),
		livenessWake: make(chan struct{}, 1),
		outputState:  messages.TerminalOutputNone,
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
		c.startErr = c.startMaxDuration(c.options.MaxDuration)
	})
	return c.startErr
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
	go c.watchMaxDuration(timer)
	return nil
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
		return admission
	}
	if c.expired {
		admission := sessionduration.Admission{Message: msg, OutputState: c.outputState}
		c.mu.Unlock()
		return admission
	}

	c.observeOutputLocked(msg)
	arm, reset, disarm, failure := c.observeLivenessLocked(msg)
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
		c.reportLiveness(failure)
	}
	return admission
}

func (c *controller) ObserveDrain(msg messages.StreamMessage) sessionduration.Admission {
	if c == nil {
		return sessionduration.Admission{Message: msg}
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return sessionduration.Admission{Message: msg}
	}
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

func (c *controller) SetToolObligation(obligation bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.toolObligation = obligation
	c.mu.Unlock()
}

func (c *controller) observeCloseLocked(msg messages.StreamMessage) (sessionduration.Admission, bool) {
	provider := c.options.Terminal.Matches == nil || c.options.Terminal.Matches(msg)
	if c.terminalWritten || (c.expired && !provider) {
		return sessionduration.Admission{Message: msg, OutputState: c.outputState, TerminalSeen: provider}, false
	}
	c.terminalWritten = true
	return sessionduration.Admission{Message: msg, Accepted: true, OutputState: c.outputState, TerminalSeen: true}, true
}

func (c *controller) observeLivenessLocked(msg messages.StreamMessage) (arm, reset, disarm bool, failure error) {
	if handled, disarm := c.prepareLivenessObservationLocked(msg); handled {
		return false, false, disarm, nil
	}
	c.observeLivenessResponseIDLocked(msg)
	if !c.options.Liveness.Enabled {
		return false, false, false, nil
	}
	switch {
	case msg.Type == messages.StreamTypeMessageStart:
		arm = true
	case msg.Type == messages.StreamTypeResponseCreate:
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
	default:
		// Provider metadata and unrelated stream messages do not affect liveness.
	}
	return arm, reset, disarm, failure
}

func (c *controller) observeOutputLocked(msg messages.StreamMessage) {
	if isNonProviderRole(msg.Role) {
		return
	}
	//nolint:exhaustive // only response output boundaries affect this state.
	switch msg.Type {
	case messages.StreamTypeMessageStart:
		c.responseOutput = false
		c.responseComplete = false
		c.toolObligation = false
	case messages.StreamTypeResponseCreate:
		if !isToolAcknowledgementResponse(msg) {
			c.responseOutput = false
			c.responseComplete = false
			c.toolObligation = false
		}
	case messages.StreamTypeTextDelta, messages.StreamTypeReasoningDelta, messages.StreamTypeAudioDelta, messages.StreamTypeImageDelta, messages.StreamTypeVideoDelta, messages.StreamTypeFileDelta, messages.StreamTypeEmbeddingDelta, messages.StreamTypeToolCallDelta, messages.StreamTypeToolCallEnd, messages.StreamTypeRefusal:
		c.responseOutput = true
	case messages.StreamTypeTranscriptDelta:
		c.responseOutput = true
	case messages.StreamTypeToolCallStart:
		c.toolObligation = true
	case messages.StreamTypeMessageEnd:
		c.responseComplete = true
	default:
		// Non-response messages do not change terminal output state.
	}
	if !c.responseOutput {
		c.outputState = messages.TerminalOutputNone
	} else if c.responseComplete {
		c.outputState = messages.TerminalOutputComplete
	} else {
		c.outputState = messages.TerminalOutputPartial
	}
}

func (c *controller) Errors() <-chan error {
	if c == nil {
		return nil
	}
	return c.errors
}

func (c *controller) LivenessFailure() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.livenessReported {
		return nil
	}
	return c.livenessFailure
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
	wasActive := c.localToolActive
	c.localToolActive = false
	rearm := wasActive && !c.livenessCancelled
	c.mu.Unlock()
	if rearm {
		c.armLiveness(false)
	}
}
