package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionlive"
	platformclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

type runState struct {
	ctx, runCtx, inputCtx  context.Context
	cancelRun, cancelInput context.CancelFunc
	opts                   sessionlive.RunOptions
	deadline               <-chan time.Time
	stopDeadline           func()
	runErrCh               chan error
	runErr                 error
	runDone                bool
	actions, toolActions   <-chan sessionlive.Action
	admissionClosed        <-chan struct{}
	providerErrors         <-chan error
	providerDone           <-chan struct{}
	inputErrCh             <-chan error
	inputErr               error
	inputStarted           bool
	awaitingResponse       bool
	firstTurnWaited        bool
	sessionUpdatedTimer    platformclock.Timer
	sessionUpdatedTimeout  <-chan time.Time
	deltaCh                <-chan sessionlive.StreamMessage
	terminateOnce          sync.Once
	terminalErr            error
}

type eventKind uint8

const (
	eventAdmission eventKind = iota
	eventBound
	eventAction
	eventToolAction
	eventProviderError
	eventInput
	eventDone
	eventDeadline
	eventSessionUpdatedTimeout
	eventContext
	eventProviderDone
	eventRunDone
	eventMessage
)

type runEvent struct {
	kind   eventKind
	action sessionlive.Action
	err    error
	value  sessionlive.StreamMessage
	open   bool
}

type loopResult struct {
	stop bool
	err  error
}

func continueLoop() loopResult { return loopResult{} }

func finishLoop(err error) loopResult { return loopResult{stop: true, err: err} }

func newRun(ctx context.Context, opts sessionlive.RunOptions) (*runState, error) {
	runCtx, cancelRun := context.WithCancel(ctx)
	inputCtx, cancelInput := context.WithCancel(runCtx)
	run := &runState{
		ctx:              ctx,
		runCtx:           runCtx,
		inputCtx:         inputCtx,
		cancelRun:        cancelRun,
		cancelInput:      cancelInput,
		opts:             opts,
		runErrCh:         make(chan error, 1),
		actions:          actionChannel(opts.Actions, runCtx),
		toolActions:      actionChannel(opts.ToolLifecycle, runCtx),
		admissionClosed:  opts.AdmissionClosed,
		providerErrors:   opts.Errors,
		providerDone:     opts.Lifecycle.Done,
		deltaCh:          opts.Loop.Deltas().Chan(),
		awaitingResponse: opts.InitiallyAwaitingResponse,
	}
	if opts.Bind != nil {
		if err := opts.Bind(runCtx, inputCtx, opts.Loop); err != nil {
			run.close()
			return nil, err
		}
	}
	deadline, stopDeadline, err := newDeadline(opts.Clock, opts.MaxDuration)
	if err != nil {
		run.close()
		return nil, err
	}
	run.deadline, run.stopDeadline = deadline, stopDeadline
	go func() { run.runErrCh <- opts.Loop.Run(runCtx) }()
	return run, nil
}

func (r *runState) close() {
	r.stopSessionUpdatedTimer()
	r.cancelInput()
	r.cancelRun()
	if r.stopDeadline != nil {
		r.stopDeadline()
	}
}

func (r *runState) loop() error {
	for {
		result := r.handle(r.nextEvent())
		if result.stop {
			return result.err
		}
	}
}

func (r *runState) nextEvent() runEvent {
	select {
	case <-r.admissionClosed:
		return runEvent{kind: eventAdmission}
	case <-r.opts.BoundCancellation:
		return runEvent{kind: eventBound}
	case action, open := <-r.actions:
		return runEvent{kind: eventAction, action: action, open: open}
	case action, open := <-r.toolActions:
		return runEvent{kind: eventToolAction, action: action, open: open}
	case err, open := <-r.providerErrors:
		return runEvent{kind: eventProviderError, err: err, open: open}
	case err, open := <-r.inputErrCh:
		return runEvent{kind: eventInput, err: err, open: open}
	case <-r.opts.Done:
		return runEvent{kind: eventDone}
	case <-r.deadline:
		return runEvent{kind: eventDeadline}
	case <-r.sessionUpdatedTimeout:
		return runEvent{kind: eventSessionUpdatedTimeout}
	case <-r.ctx.Done():
		return runEvent{kind: eventContext}
	case <-r.providerDone:
		return runEvent{kind: eventProviderDone}
	case err := <-r.runErrCh:
		return runEvent{kind: eventRunDone, err: err}
	case message, open := <-r.deltaCh:
		return runEvent{kind: eventMessage, value: message, open: open}
	}
}

func (r *runState) handle(event runEvent) loopResult {
	switch event.kind {
	case eventAdmission:
		r.admissionClosed = nil
		r.actions, r.toolActions = nil, nil
		if r.opts.OnAdmissionClosed != nil {
			r.opts.OnAdmissionClosed()
		}
		return continueLoop()
	case eventBound:
		return finishLoop(r.terminate(nil, false))
	case eventAction, eventToolAction:
		return r.handleAction(event.action, event.open, r.actionSource(event.kind))
	case eventProviderError:
		if !event.open {
			r.providerErrors = nil
			return continueLoop()
		}
		return finishLoop(r.terminate(event.err, false))
	case eventInput:
		return r.handleInput(event)
	case eventDone:
		return r.handleDone()
	case eventDeadline:
		return r.handleDeadline()
	case eventSessionUpdatedTimeout:
		return r.handleSessionUpdatedTimeout()
	case eventContext:
		return r.handleContext()
	case eventProviderDone:
		return r.handleProviderDone()
	case eventRunDone:
		return r.handleRunDone(event.err)
	case eventMessage:
		return r.handleMessageEvent(event)
	default:
		return finishLoop(errors.New("session live received an unknown run event"))
	}
}

func (r *runState) handleAction(action sessionlive.Action, open bool, source *<-chan sessionlive.Action) loopResult {
	if !open {
		*source = nil
		return continueLoop()
	}
	if action == nil {
		return continueLoop()
	}
	if err := action(r.runCtx, r.opts.Loop); err != nil {
		return finishLoop(r.terminate(err, false))
	}
	return continueLoop()
}

func (r *runState) handleInput(event runEvent) loopResult {
	if !event.open {
		r.inputErrCh = nil
		r.awaitingResponse = true
		if r.opts.InputDone != nil {
			r.opts.InputDone(nil)
		}
		return continueLoop()
	}
	r.inputErrCh, r.inputErr = nil, event.err
	if event.err != nil && !isCancellation(event.err) {
		return finishLoop(r.terminate(event.err, false))
	}
	r.awaitingResponse = event.err == nil
	if r.opts.InputDone != nil {
		r.opts.InputDone(event.err)
	}
	return continueLoop()
}

func (r *runState) handleDone() loopResult {
	var err error
	if r.opts.DoneErr != nil {
		err = r.opts.DoneErr()
	}
	return finishLoop(r.terminate(err, false))
}

func (r *runState) handleDeadline() loopResult {
	deadlineErr := error(sessionlive.ErrMaxDurationExpired)
	if r.opts.DeadlineError != nil {
		deadlineErr = r.opts.DeadlineError()
	}
	return finishLoop(r.terminate(deadlineErr, false))
}

func (r *runState) handleSessionUpdatedTimeout() loopResult {
	r.stopSessionUpdatedTimer()
	if r.opts.SessionUpdatedError != nil {
		return finishLoop(r.terminate(r.opts.SessionUpdatedError(effectiveTimeout(r.opts.SessionUpdatedTimeout)), false))
	}
	return finishLoop(r.terminate(errors.New("session live timed out awaiting session.updated"), false))
}

func (r *runState) handleContext() loopResult {
	if r.awaitingResponse {
		return finishLoop(r.terminate(fmt.Errorf("session cancelled while awaiting model response after end-of-turn: %w", r.ctx.Err()), false))
	}
	return finishLoop(terminateWithTerminationError(r.ctx, r.terminate(nil, false), r.opts.TerminationError))
}

func (r *runState) handleProviderDone() loopResult {
	if r.opts.Lifecycle.ConnectError != nil {
		if err := r.opts.Lifecycle.ConnectError(); err != nil {
			return finishLoop(r.terminate(fmt.Errorf("session connect: %w", err), false))
		}
	}
	if err := r.ctx.Err(); err != nil {
		if r.awaitingResponse {
			return finishLoop(r.terminate(fmt.Errorf("session cancelled while awaiting model response after end-of-turn: %w", err), false))
		}
		return finishLoop(terminateWithTerminationError(r.ctx, r.terminate(nil, false), r.opts.TerminationError))
	}
	r.providerDone = nil
	return continueLoop()
}

func (r *runState) handleRunDone(err error) loopResult {
	r.runErr, r.runDone, r.runErrCh = err, true, nil
	if err == nil {
		result, drainErr := r.drainPublished()
		if drainErr != nil {
			return finishLoop(r.terminate(drainErr, false))
		}
		if result.Stop {
			return finishLoop(r.terminate(nil, result.DrainPlayback))
		}
	}
	if ctxErr := r.ctx.Err(); ctxErr != nil && r.awaitingResponse {
		return finishLoop(r.terminate(fmt.Errorf("session cancelled while awaiting model response after end-of-turn: %w", ctxErr), false))
	}
	if err == nil && r.ctx.Err() == nil {
		return finishLoop(terminateWithTerminationError(r.ctx, r.terminate(nil, true), r.opts.TerminationError))
	}
	return finishLoop(terminateWithTerminationError(r.ctx, r.terminate(nil, false), r.opts.TerminationError))
}

func (r *runState) handleMessageEvent(event runEvent) loopResult {
	if !event.open {
		r.deltaCh = nil
		return continueLoop()
	}
	result, err := r.handleMessage(event.value)
	if err != nil {
		return finishLoop(r.terminate(err, false))
	}
	if result.Stop {
		return finishLoop(r.terminate(nil, result.DrainPlayback))
	}
	return continueLoop()
}

func (r *runState) handleMessage(message sessionlive.StreamMessage) (sessionlive.MessageResult, error) {
	result, err := r.opts.Handler(r.runCtx, r.opts.Loop, message, sessionlive.MessageContext{SessionDone: r.providerDone, Deadline: r.deadline, AwaitingResponse: r.awaitingResponse})
	if err != nil {
		return sessionlive.MessageResult{}, err
	}
	if message.Type == messages.StreamTypeSessionOpen {
		if r.opts.FirstTurnAck != nil && !r.firstTurnWaited {
			r.firstTurnWaited = true
			if err := waitForFirstTurn(r.ctx, r.opts.FirstTurnAck, r.opts.Clock, r.opts.FirstTurnTimeout); err != nil {
				return sessionlive.MessageResult{}, fmt.Errorf("send session first turn: %w", err)
			}
		}
		if err := r.startInput(); err != nil {
			return sessionlive.MessageResult{}, err
		}
		if err := r.startSessionUpdatedTimer(); err != nil {
			return sessionlive.MessageResult{}, err
		}
	}
	if r.opts.SessionUpdatedReady != nil && r.opts.SessionUpdatedReady() {
		r.stopSessionUpdatedTimer()
	}
	return result, nil
}

func (r *runState) startInput() error {
	if r.inputStarted || r.opts.StartInput == nil {
		return nil
	}
	r.inputStarted = true
	var err error
	r.inputErrCh, err = r.opts.StartInput(r.inputCtx, r.opts.Loop)
	return err
}

func (r *runState) startSessionUpdatedTimer() error {
	if !r.opts.RequireSessionUpdated || r.sessionUpdatedTimer != nil || (r.opts.SessionUpdatedReady != nil && r.opts.SessionUpdatedReady()) {
		return nil
	}
	timer, err := newTimer(r.opts.Clock, effectiveTimeout(r.opts.SessionUpdatedTimeout))
	if err != nil {
		return err
	}
	r.sessionUpdatedTimer, r.sessionUpdatedTimeout = timer, timer.C()
	return nil
}

func (r *runState) stopSessionUpdatedTimer() {
	if r.sessionUpdatedTimer != nil {
		r.sessionUpdatedTimer.Stop()
		r.sessionUpdatedTimer = nil
	}
	r.sessionUpdatedTimeout = nil
}

func (r *runState) drainPublished() (sessionlive.MessageResult, error) {
	var last sessionlive.MessageResult
	for {
		message, ok := r.opts.Loop.Deltas().Read()
		if !ok {
			return last, nil
		}
		result, err := r.handleMessage(message)
		if err != nil || result.Stop {
			return result, err
		}
		last = result
	}
}

func (r *runState) actionSource(kind eventKind) *<-chan sessionlive.Action {
	if kind == eventToolAction {
		return &r.toolActions
	}
	return &r.actions
}
