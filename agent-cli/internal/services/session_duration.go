package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// SessionMaxDurationReason is the stable terminal reason for a planned
// duration cutoff.
const SessionMaxDurationReason messages.TerminalReason = "max_duration"

// ErrInvalidSessionMaxDuration identifies a negative --max-duration value.
var ErrInvalidSessionMaxDuration = errors.New("invalid session max duration")

// SessionMaxDurationError describes a duration that cannot be used as a
// session bound. It is returned before runtime planning or session startup.
type SessionMaxDurationError struct {
	Duration time.Duration
}

// InvalidSessionDurationError is retained as a descriptive alias for callers
// that use the validation error by its general duration name.
type InvalidSessionDurationError = SessionMaxDurationError

// Error returns an actionable validation message for the CLI.
func (e *SessionMaxDurationError) Error() string {
	if e == nil {
		return ErrInvalidSessionMaxDuration.Error()
	}
	return fmt.Sprintf("--max-duration must be non-negative, got %s", e.Duration)
}

// Unwrap preserves a stable errors.Is identity for duration validation.
func (e *SessionMaxDurationError) Unwrap() error {
	return ErrInvalidSessionMaxDuration
}

// ValidateSessionMaxDuration validates the optional session duration before
// any provider, session, or output resource is planned.
func ValidateSessionMaxDuration(duration time.Duration) error {
	if duration < 0 {
		return &SessionMaxDurationError{Duration: duration}
	}
	return nil
}

// SessionDurationTimer is the timer contract owned by the session duration
// controller. The small interface gives deterministic tests a clock seam while
// ensuring the real timer is stopped on every termination path.
type SessionDurationTimer interface {
	C() <-chan time.Time
	Stop() bool
}

// SessionDurationClock creates one timer for a positive session bound.
type SessionDurationClock interface {
	NewTimer(time.Duration) SessionDurationTimer
}

type realSessionDurationClock struct{}

func (realSessionDurationClock) NewTimer(duration time.Duration) SessionDurationTimer {
	return realSessionDurationTimer{timer: time.NewTimer(duration)}
}

type realSessionDurationTimer struct {
	timer *time.Timer
}

func (t realSessionDurationTimer) C() <-chan time.Time {
	return t.timer.C
}

func (t realSessionDurationTimer) Stop() bool {
	return t.timer.Stop()
}

const sessionDurationAdmissionBufferCapacity = 1024

// sessionDurationAdmission is the single admission boundary for provider
// events. Closing it prevents a provider event from entering the loop after
// the logical deadline while preserving events accepted before the close.
type sessionDurationAdmission struct {
	mu     sync.Mutex
	closed bool
	done   chan struct{}
	once   sync.Once
}

func newSessionDurationAdmission() *sessionDurationAdmission {
	return &sessionDurationAdmission{done: make(chan struct{})}
}

func (a *sessionDurationAdmission) close() {
	a.closeWithDrain(nil, nil)
}

func (a *sessionDurationAdmission) closeWithDrain(receive, source *messages.TypedBuffer[messages.StreamMessage]) {
	a.once.Do(func() {
		a.mu.Lock()
		if receive != nil && source != nil {
			for {
				msg, ok := source.Read()
				if !ok || !receive.Write(context.Background(), msg) {
					break
				}
			}
		}
		a.closed = true
		a.mu.Unlock()
		close(a.done)
	})
}

func (a *sessionDurationAdmission) admit(receive *messages.TypedBuffer[messages.StreamMessage], msg messages.StreamMessage) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	return receive.Write(context.Background(), msg)
}

// sessionDurationAdmissionInferencer inserts the admission boundary between
// the provider session and the agent loop. The public Session interface exposes
// a concrete receive buffer, so the wrapper forwards through its own buffer and
// can stop admitting provider events without changing the shared interface.
type sessionDurationAdmissionInferencer struct {
	inner      messages.SessionInferencer
	admission  *sessionDurationAdmission
	mu         sync.Mutex
	runtimeErr error
	closeErr   error
	connected  bool
	session    *sessionDurationAdmissionSession
	closeDone  chan struct{}
	closeOnce  sync.Once
}

func (i *sessionDurationAdmissionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		i.mu.Lock()
		i.runtimeErr = err
		i.mu.Unlock()
		return nil, err
	}
	i.mu.Lock()
	i.connected = true
	wrapped := newSessionDurationAdmissionSession(ctx, session, i.admission, i.recordCloseError)
	i.session = wrapped
	i.mu.Unlock()
	return wrapped, nil
}

func (i *sessionDurationAdmissionInferencer) recordCloseError(err error) {
	i.mu.Lock()
	i.closeErr = err
	i.mu.Unlock()
	if i.closeDone != nil {
		i.closeOnce.Do(func() { close(i.closeDone) })
	}
}

func (i *sessionDurationAdmissionInferencer) closeError() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.closeErr
}

func (i *sessionDurationAdmissionInferencer) runtimeError() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.runtimeErr
}

func (i *sessionDurationAdmissionInferencer) waitForClose() {
	i.mu.Lock()
	connected := i.connected
	closeDone := i.closeDone
	i.mu.Unlock()
	if connected && closeDone != nil {
		<-closeDone
	}
}

func (i *sessionDurationAdmissionInferencer) closeAdmission() {
	i.mu.Lock()
	session := i.session
	i.mu.Unlock()
	if session != nil {
		session.closeAdmission()
		return
	}
	i.admission.close()
}

type sessionDurationAdmissionSession struct {
	inner     messages.Session
	admission *sessionDurationAdmission
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
	closeMu   sync.Mutex
	closeErr  error
	onClose   func(error)
}

func newSessionDurationAdmissionSession(ctx context.Context, inner messages.Session, admission *sessionDurationAdmission, onClose func(error)) *sessionDurationAdmissionSession {
	s := &sessionDurationAdmissionSession{
		inner:     inner,
		admission: admission,
		receive:   messages.NewTypedBuffer[messages.StreamMessage](sessionDurationAdmissionBufferCapacity),
		done:      make(chan struct{}),
		onClose:   onClose,
	}
	go s.forward(ctx)
	return s
}

func (s *sessionDurationAdmissionSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.inner.Send(ctx, msg)
}

func (s *sessionDurationAdmissionSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionDurationAdmissionSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionDurationAdmissionSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeAdmission()
		err := s.inner.Close()
		s.closeMu.Lock()
		s.closeErr = err
		s.closeMu.Unlock()
		if s.onClose != nil {
			s.onClose(err)
		}
	})
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeErr
}

func (s *sessionDurationAdmissionSession) closeAdmission() {
	s.admission.closeWithDrain(s.receive, s.inner.Receive())
}

func (s *sessionDurationAdmissionSession) forward(ctx context.Context) {
	source := s.inner.Receive()
	sourceCh := source.Chan()
	admissionDone := s.admission.done
	for {
		select {
		case <-s.inner.Done():
			s.drainSource(source)
			s.closeDone()
			return
		case <-ctx.Done():
			s.closeDone()
			return
		case <-admissionDone:
			// The deadline closes admission, but the provider session must stay
			// alive long enough to receive the graceful close request. Disable
			// source forwarding and wait for the provider/session lifecycle.
			admissionDone = nil
			sourceCh = nil
		case msg, ok := <-sourceCh:
			if !ok {
				s.closeDone()
				return
			}
			if !s.admission.admit(s.receive, msg) {
				admissionDone = nil
				sourceCh = nil
			}
		}
	}
}

func (s *sessionDurationAdmissionSession) drainSource(source *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := source.Read()
		if !ok || !s.admission.admit(s.receive, msg) {
			return
		}
	}
}

func (s *sessionDurationAdmissionSession) closeDone() {
	s.doneOnce.Do(func() { close(s.done) })
}

var _ messages.SessionInferencer = (*sessionDurationAdmissionInferencer)(nil)
var _ messages.Session = (*sessionDurationAdmissionSession)(nil)

// RunSessionWithMaxDuration runs a session with an optional graceful duration
// bound. A zero duration disables the controller; a positive duration requests
// a session close and drains the accepted output before finalization.
func RunSessionWithMaxDuration(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration) error {
	return RunSessionWithMaxDurationClock(ctx, out, opts, maxDuration, realSessionDurationClock{})
}

// RunSessionWithMaxDurationClock is the deterministic-clock seam for the
// duration path. Production callers should use RunSessionWithMaxDuration.
func RunSessionWithMaxDurationClock(ctx context.Context, out io.Writer, opts SessionRunOptions, maxDuration time.Duration, durationClock SessionDurationClock) error {
	if err := ValidateSessionMaxDuration(maxDuration); err != nil {
		return err
	}
	if err := validateSessionRunOptions(opts); err != nil {
		return err
	}

	plan, err := planSessionRuntime(opts)
	if err != nil {
		return err
	}
	// Zero disables this controller. Preserve the runtime's existing safety
	// behavior for replay and injected sessions when the flag is omitted.
	if maxDuration == 0 {
		return plan.run(ctx, out)
	}
	if durationClock == nil {
		durationClock = realSessionDurationClock{}
	}
	return runSessionDurationPlan(ctx, out, plan, maxDuration, durationClock)
}

func runSessionDurationPlan(ctx context.Context, out io.Writer, plan sessionRuntimePlan, maxDuration time.Duration, durationClock SessionDurationClock) error {
	if plan.announce != "" {
		if _, err := fmt.Fprintln(out, plan.announce); err != nil {
			return err
		}
	}

	loopOut := out
	if plan.loopOut != nil {
		loopOut = plan.loopOut
	}
	if plan.inferencer != nil {
		if err := runAgentLoopSessionWithDurationClock(ctx, loopOut, plan.inferencer, plan.loop, maxDuration, durationClock); err != nil {
			if plan.flushCapture != nil {
				flushErr := plan.flushCapture()
				return wrapSessionRuntimeError(plan, errors.Join(
					wrapSessionPhaseError("run session loop", err),
					wrapSessionPhaseError("flush capture", flushErr),
				))
			}
			return wrapSessionRuntimeError(plan, err)
		}
	}

	if plan.flushCapture != nil {
		if err := plan.flushCapture(); err != nil {
			return wrapSessionRuntimeError(plan, wrapSessionPhaseError("flush capture", err))
		}
	}
	if plan.finalize != nil {
		if err := plan.finalize(ctx, out); err != nil {
			return wrapSessionRuntimeError(plan, err)
		}
	}
	return nil
}

func runAgentLoopSessionWithDurationClock(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions, maxDuration time.Duration, durationClock SessionDurationClock) error {
	if maxDuration <= 0 {
		return runAgentLoopSession(ctx, out, sessionInferencer, opts)
	}

	admission := newSessionDurationAdmission()
	admittedInferencer := &sessionDurationAdmissionInferencer{
		inner:     sessionInferencer,
		admission: admission,
		closeDone: make(chan struct{}),
	}
	observedInferencer := newObservedSessionInferencer(admittedInferencer)
	loop, err := agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(observedInferencer),
	)
	if err != nil {
		return fmt.Errorf("create session agent loop: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- loop.Run(runCtx)
	}()

	timer := durationClock.NewTimer(maxDuration)
	if timer == nil {
		admittedInferencer.closeAdmission()
		cancel()
		<-runErrCh
		admittedInferencer.waitForClose()
		return errors.New("session duration clock returned a nil timer")
	}
	defer timer.Stop()
	timerCh := timer.C()

	promptSent := false
	closeSent := false
	durationExpired := false
	durationTerminalWritten := false

	finish := func(planned bool, preferredErr error) error {
		var preCancelDrainErr error
		if preferredErr == nil {
			preCancelDrainErr = drainDurationSessionLoopMessagesUntilQuiet(out, loop, planned, &durationTerminalWritten)
		}
		cancel()
		runErr := <-runErrCh
		admittedInferencer.waitForClose()
		if drainErr := drainDurationSessionLoopMessages(out, loop, planned, &durationTerminalWritten); drainErr != nil {
			return drainErr
		}
		runtimeErr := admittedInferencer.runtimeError()
		closeErr := admittedInferencer.closeError()
		if preferredErr != nil {
			if runtimeErr != nil || closeErr != nil {
				return errors.Join(preferredErr, sessionDurationLifecycleError(runtimeErr, closeErr))
			}
			return preferredErr
		}
		if preCancelDrainErr != nil {
			return preCancelDrainErr
		}
		if runtimeErr != nil {
			return wrapSessionPhaseError("session runtime", runtimeErr)
		}
		if closeErr != nil {
			return wrapSessionPhaseError("close session", closeErr)
		}
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			return fmt.Errorf("session error: %w", runErr)
		}
		if planned && !durationTerminalWritten {
			if err := writeMaxDurationTerminal(out); err != nil {
				return err
			}
		}
		return nil
	}

	expire := func() error {
		if durationExpired {
			return nil
		}
		durationExpired = true
		timerCh = nil
		admittedInferencer.closeAdmission()
		if closeSent {
			return nil
		}
		closeSent = true
		return sendSessionClose(runCtx, loop)
	}

	for {
		// Prefer a deadline that is already ready over a simultaneously ready
		// provider-close signal. Once this branch wins, the planned reason is
		// retained and the close is still drained normally.
		if !durationExpired && sessionDurationTimerReady(timerCh) {
			if err := expire(); err != nil {
				return finish(false, err)
			}
			continue
		}

		select {
		case <-timerCh:
			if err := expire(); err != nil {
				return finish(false, err)
			}
		case <-ctx.Done():
			if err := finish(false, nil); err != nil {
				return err
			}
			return ctx.Err()
		case <-opts.Done:
			doneErr := error(nil)
			if opts.DoneErr != nil {
				doneErr = opts.DoneErr()
			}
			return finish(durationExpired && doneErr == nil, doneErr)
		case <-observedInferencer.Done():
			doneErr := error(nil)
			if opts.DoneErr != nil {
				doneErr = opts.DoneErr()
			}
			return finish(durationExpired && doneErr == nil, doneErr)
		case err := <-runErrCh:
			admittedInferencer.waitForClose()
			if drainErr := drainDurationSessionLoopMessages(out, loop, durationExpired, &durationTerminalWritten); drainErr != nil {
				return drainErr
			}
			if runtimeErr := admittedInferencer.runtimeError(); runtimeErr != nil {
				return wrapSessionPhaseError("session runtime", runtimeErr)
			}
			if closeErr := admittedInferencer.closeError(); closeErr != nil {
				return wrapSessionPhaseError("close session", closeErr)
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("session error: %w", err)
			}
			if durationExpired && !durationTerminalWritten {
				if err := writeMaxDurationTerminal(out); err != nil {
					return err
				}
			}
			return nil
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return finish(durationExpired, nil)
			}
			if durationExpired && msg.Type == messages.StreamTypeSessionClose {
				msg, durationTerminalWritten = maxDurationTerminalMessage(msg)
			}
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return finish(false, err)
			}
			if msg.Type == messages.StreamTypeSessionOpen && !durationExpired {
				if opts.Prompt != "" && !promptSent {
					promptSent = true
					userMsg := messages.NewTextMessage(messages.RoleUser, opts.Prompt)
					if err := loop.Send(runCtx, []messages.Message{userMsg}); err != nil {
						return finish(false, fmt.Errorf("send session message: %w", err))
					}
				}
				if opts.CloseAfterOpen && opts.Prompt == "" && !closeSent {
					closeSent = true
					if err := sendSessionClose(runCtx, loop); err != nil {
						return finish(false, err)
					}
				}
			}
			if !durationExpired && opts.CloseAfterOpen && opts.Prompt != "" && msg.Type == messages.StreamTypeMessageEnd && !closeSent {
				closeSent = true
				if err := sendSessionClose(runCtx, loop); err != nil {
					return finish(false, err)
				}
			}
			if shouldStopSessionLoop(msg, opts, closeSent) && (!durationExpired || msg.Type == messages.StreamTypeSessionClose) {
				return finish(durationExpired, nil)
			}
		}
	}
}

func sessionDurationTimerReady(timerCh <-chan time.Time) bool {
	if timerCh == nil {
		return false
	}
	select {
	case <-timerCh:
		return true
	default:
		return false
	}
}

func drainDurationSessionLoopMessages(out io.Writer, loop *agentloop.AgentLoop, planned bool, terminalWritten *bool) error {
	for {
		msg, ok := loop.Deltas().Read()
		if !ok {
			return nil
		}
		if planned && msg.Type == messages.StreamTypeSessionClose {
			msg, *terminalWritten = maxDurationTerminalMessage(msg)
		}
		if err := writeSessionReplayMessage(out, msg); err != nil {
			return err
		}
	}
}

func drainDurationSessionLoopMessagesUntilQuiet(out io.Writer, loop *agentloop.AgentLoop, planned bool, terminalWritten *bool) error {
	timer := time.NewTimer(sessionReplayDoneDrainIdleDelay)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-loop.Deltas().Chan():
			if !ok {
				return nil
			}
			if planned && msg.Type == messages.StreamTypeSessionClose {
				msg, *terminalWritten = maxDurationTerminalMessage(msg)
			}
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return err
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(sessionReplayDoneDrainIdleDelay)
		case <-timer.C:
			return nil
		}
	}
}

func maxDurationTerminalMessage(msg messages.StreamMessage) (messages.StreamMessage, bool) {
	value, ok := msg.Value.(*messages.SessionCloseValue)
	if !ok {
		return msg, false
	}
	clone := *value
	clone.Reason = string(SessionMaxDurationReason)
	clone.Classification = string(SessionMaxDurationReason)
	clone.TerminalReason = SessionMaxDurationReason
	clone.TerminalProvenance = messages.TerminalProvenanceLoop
	clone.OutputState = messages.TerminalOutputNotApplicable
	msg.Value = &clone
	return msg, true
}

func writeMaxDurationTerminal(out io.Writer) error {
	return writeSessionReplayMessage(out, messages.StreamMessage{
		Type: messages.StreamTypeSessionClose,
		Value: messages.NewSessionCloseValueWithTerminal(
			"",
			string(SessionMaxDurationReason),
			string(SessionMaxDurationReason),
			SessionMaxDurationReason,
			messages.TerminalProvenanceLoop,
			messages.TerminalOutputNotApplicable,
		),
	})
}

func sessionDurationLifecycleError(runtimeErr, closeErr error) error {
	var lifecycleErrs []error
	if runtimeErr != nil {
		lifecycleErrs = append(lifecycleErrs, wrapSessionPhaseError("session runtime", runtimeErr))
	}
	if closeErr != nil {
		lifecycleErrs = append(lifecycleErrs, wrapSessionPhaseError("close session", closeErr))
	}
	return errors.Join(lifecycleErrs...)
}

var _ SessionDurationClock = realSessionDurationClock{}
var _ SessionDurationTimer = realSessionDurationTimer{}
