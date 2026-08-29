// This file contains live session-loop construction, operation, and lifecycle observation for the session command.
package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/engine"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/metrics"
)

const sessionReplayDoneDrainIdleDelay = 25 * time.Millisecond

// ErrSessionScheduledAudioIncomplete identifies a live scheduled-audio run
// that ended before every queued input received an assistant response.
var ErrSessionScheduledAudioIncomplete = errors.New("scheduled audio session ended before all turns completed")

// SessionScheduledAudioIncompleteError carries the deterministic schedule
// counts observed at a terminal boundary. It unwraps to
// ErrSessionScheduledAudioIncomplete so callers can use errors.Is while still
// retaining any provider, timeout, cancellation, or cleanup cause joined with
// it.
type SessionScheduledAudioIncompleteError struct {
	Completed  int
	Dispatched int
	Scheduled  int
}

func (e *SessionScheduledAudioIncompleteError) Error() string {
	if e == nil {
		return ErrSessionScheduledAudioIncomplete.Error()
	}
	return fmt.Sprintf("%s: completed=%d dispatched=%d scheduled=%d", ErrSessionScheduledAudioIncomplete, e.Completed, e.Dispatched, e.Scheduled)
}

func (e *SessionScheduledAudioIncompleteError) Unwrap() error {
	return ErrSessionScheduledAudioIncomplete
}

// ErrSessionAudioResponseIncomplete identifies an audio-input run that ended
// after a provider tool-call turn but before a final assistant response. A
// provider close or a duration cutoff is not a successful conversation when
// the tool round trip has no continuation.
var ErrSessionAudioResponseIncomplete = errors.New("audio session ended before the final assistant response")

// ErrSessionScheduledAudioConfigTimeout identifies a live scheduled-audio run
// whose current session never acknowledged its initial configuration.
var ErrSessionScheduledAudioConfigTimeout = errors.New("scheduled audio session timed out awaiting session.updated")

// sessionFirstTurnAckTimeout bounds how long the SESSION.OPEN handler waits
// for the first user turn acceptance before failing the run instead of
// streaming user audio over an unacknowledged turn.
const sessionFirstTurnAckTimeout = 30 * time.Second

// sessionScheduledAudioConfigTimeout bounds the wait after SESSION.OPEN for a
// scheduled live session's initial SESSION.UPDATED acknowledgement. The
// per-loop override exists only for deterministic service tests.
const sessionScheduledAudioConfigTimeout = 30 * time.Second

// awaitSessionFirstTurn blocks until the session's first user turn is
// accepted (nil), the run is cancelled, or the bounded wait expires.
func awaitSessionFirstTurn(ctx context.Context, ack <-chan error) error {
	timer := time.NewTimer(sessionFirstTurnAckTimeout)
	defer timer.Stop()
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("timed out awaiting session first user turn acceptance")
	}
}

type sessionLoopOptions struct {
	Prompt         string
	CloseAfterOpen bool
	WaitForClose   bool
	MaxDuration    time.Duration
	// RequireAssistantResponse is enabled for finite audio-input sessions.
	// A tool-call MESSAGE.END is an intermediate provider turn; the session
	// must observe a later non-tool assistant MESSAGE.END before clean success.
	RequireAssistantResponse bool
	Done                     <-chan struct{}
	DoneErr                  func() error
	// AudioIn optionally streams a bounded file or stdin audio source into
	// the loop after SESSION.OPEN. When nil, every session path behaves
	// exactly as it did before audio input existed.
	AudioIn *sessionAudioSource

	// awaitFirstTurn optionally blocks the SESSION.OPEN handler until the
	// session's first user turn (the realtime image turn) has been accepted
	// by the provider session's outbound queue. Without it, streamed user
	// audio can overtake the still-propagating prompt turn and reorder the
	// customer's question after their speech on the wire. Nil preserves
	// existing behavior for every non-image session path.
	awaitFirstTurn <-chan error

	// observer optionally records per-turn and terminal diagnostics from the
	// consumed delta stream; nil keeps runtime behavior unchanged.
	observer *sessionProgressObserver

	// ToolExecutor is the composed session tool executor. When non-nil it is
	// wrapped once by newSessionToolExecutor and handed to
	// agentloop.WithToolExecutor so provider-originated realtime tool calls
	// execute through the product executor instead of the loop default.
	// Nil keeps loop construction byte-for-byte identical to today.
	ToolExecutor messages.ToolExecutor

	// ToolDefinitions is the config-filtered tool surface advertised to the
	// session loop. It is paired with ToolExecutor by the runtime planner.
	ToolDefinitions []messages.ToolDefinition

	// AdvertiseToolDefinitions sends the definitions through the generic
	// SESSION.UPDATE seam used by injected sessions. Live provider-backed
	// sessions receive definitions in their initial provider-specific config;
	// strict websocket replays preserve their captured outbound sequence.
	AdvertiseToolDefinitions bool

	// ToolExecutionTimeout overrides the per-invocation adapter deadline in
	// tests. Zero selects defaultSessionToolExecutionTimeout; production plans
	// never set it.
	ToolExecutionTimeout time.Duration

	// runtime stamps audio input and lifecycle observations from inside the
	// session command. Nil keeps the existing runtime path unchanged.
	runtime *sessionRuntimeObservationRecorder

	// AudioOutputError lets the audio-output wrapper report a concrete artifact
	// failure before the incomplete-response guard classifies a tool round trip.
	// Without this seam, malformed output can stop the wrapper before the final
	// assistant boundary and be misreported as an unresolved tool result.
	AudioOutputError func() error

	// rtcDeviceBinding is opened by the enclosing runtime plan and is started
	// against the real session-owned media endpoints after ConnectSession.
	rtcDeviceBinding *RTCDeviceBinding

	// CloseAfterScheduledAudio requests a live scheduled-audio session close
	// only after every queued input has produced a terminal assistant turn.
	// Replay plans leave this false so capture-derived close behavior remains
	// authoritative.
	CloseAfterScheduledAudio bool

	// ScheduledAudioDispatch is the explicit policy selected for repeated
	// scheduled audio. Runtime planning always supplies a non-zero value;
	// direct loop callers treat the zero value as completion-gated.
	ScheduledAudioDispatch ScheduledAudioDispatchPolicy
	// AudioInterruptions is the run-scoped channel for event-driven customer
	// audio. Inputs are sent through AgentLoop.SendAudioInput and their optional
	// MESSAGE.END boundary is sent through AgentLoop.SendSessionEvent, preserving
	// the normal provider ordering and barge-in behavior.
	AudioInterruptions <-chan ScheduledAudioInput

	// RequireSessionUpdated makes scheduled audio wait for the current
	// connection's initial SESSION.UPDATED acknowledgement before dispatch.
	// It is enabled for live OpenAI scheduled sessions; replay paths and other
	// session modes retain their existing lifecycle unless they opt in.
	RequireSessionUpdated bool
	// SessionUpdatedTimeout overrides the bounded readiness wait in tests. Zero
	// selects sessionScheduledAudioConfigTimeout.
	SessionUpdatedTimeout time.Duration

	// loopReady receives the constructed loop before its hot loop starts. The
	// self-play coordinator uses this to bind an io.Pipe reader to the peer's
	// session audio inbox without exposing the loop through SessionRunOptions.
	loopReady chan<- *agentloop.AgentLoop
}

// duplexSessionLoopOptions is the single duplex loop construction seam. Both
// the plain and duration-bounded session runners build their loops here so an
// injected executor enables tool execution exactly once per session.
func duplexSessionLoopOptions(observedInferencer messages.SessionInferencer, opts sessionLoopOptions) []agentloop.Option {
	loopOpts := []agentloop.Option{
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(observedInferencer),
	}
	if opts.ToolExecutor != nil {
		if len(opts.ToolDefinitions) > 0 {
			loopOpts = append(loopOpts,
				agentloop.WithTools(opts.ToolDefinitions),
			)
			if opts.AdvertiseToolDefinitions {
				loopOpts = append(loopOpts, agentloop.WithSessionConfig(messages.SessionUpdateConfig{
					Tools: append([]messages.ToolDefinition(nil), opts.ToolDefinitions...),
				}))
			}
		}
		loopOpts = append(loopOpts, agentloop.WithToolExecutor(newSessionToolExecutorWithTimeout(opts.ToolExecutor, opts.ToolExecutionTimeout)))
	} else {
		loopOpts = append(loopOpts, agentloop.WithToolExecutionDisabled())
	}
	return loopOpts
}

func runAgentLoopSession(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) error {
	err := runAgentLoopSessionStream(ctx, out, sessionInferencer, opts)
	err = audioResponseCompletionError(err, opts)
	err = scheduledAudioCompletionError(err, opts)
	err = opts.observer.finish(err)
	return err
}

// audioResponseCompletionError prevents any session from reporting clean
// success when it observed a tool-call response but never observed the final
// assistant response that should follow accepted tool-result delivery. The
// guard is input-source agnostic; RequireAssistantResponse remains a stop-rule
// compatibility option, not the lifecycle contract.
func audioResponseCompletionError(err error, opts sessionLoopOptions) error {
	if opts.AudioOutputError != nil {
		if outputErr := opts.AudioOutputError(); outputErr != nil {
			return err
		}
	}
	if opts.observer == nil || !opts.observer.providerToolCallObserved() || opts.observer.assistantResponseCompleted() {
		return err
	}
	incomplete := ErrSessionAudioResponseIncomplete
	if err == nil {
		return incomplete
	}
	return errors.Join(err, incomplete)
}

// sessionRunTerminationError preserves a caller cancellation observed after
// the loop has already reported its terminal result. The session loop's
// select can receive both signals at once; cleanup intentionally filters the
// loop's expected context cancellation, but must not erase the caller's
// cancellation when the clean loop result wins that race.
func sessionRunTerminationError(ctx context.Context, err error) error {
	if ctx == nil {
		return err
	}
	return errors.Join(err, ctx.Err())
}

func scheduledAudioCompletionError(err error, opts sessionLoopOptions) error {
	err = withUnresolvedToolResults(err, opts.observer)
	if !opts.CloseAfterScheduledAudio || opts.observer == nil || !opts.observer.scheduledAudioIncomplete() {
		return err
	}
	if errors.Is(err, ErrSessionScheduledAudioIncomplete) {
		return err
	}
	completed, dispatched, scheduled := opts.observer.scheduledAudioCounts()
	incomplete := &SessionScheduledAudioIncompleteError{
		Completed:  completed,
		Dispatched: dispatched,
		Scheduled:  scheduled,
	}
	if err == nil {
		return incomplete
	}
	return errors.Join(err, incomplete)
}

func sessionScheduledAudioConfigTimeoutError(opts sessionLoopOptions) error {
	timeout := opts.SessionUpdatedTimeout
	if timeout <= 0 {
		timeout = sessionScheduledAudioConfigTimeout
	}
	return fmt.Errorf("%w after %s", ErrSessionScheduledAudioConfigTimeout, timeout)
}

type sessionLoopMessageState struct {
	promptSent            bool
	closeSent             bool
	closeAfterOpenPending bool
}

func handleSessionLoopMessage(ctx context.Context, out io.Writer, loop *agentloop.AgentLoop, opts sessionLoopOptions, msg messages.StreamMessage, state sessionLoopMessageState, awaitingResponse bool, startAudio func(), stopAndDrain func() error) (sessionLoopMessageState, bool, error) {
	opts.observer.observe(msg)
	if err := writeSessionReplayMessage(out, msg); err != nil {
		return state, false, errors.Join(err, stopAndDrain())
	}
	if msg.Type == messages.StreamTypeSessionOpen {
		if opts.Prompt != "" && !state.promptSent {
			state.promptSent = true
			userMsg := messages.NewTextMessage(messages.RoleUser, opts.Prompt)
			if err := loop.Send(ctx, []messages.Message{userMsg}); err != nil {
				return state, false, errors.Join(fmt.Errorf("send session message: %w", err), stopAndDrain())
			}
			opts.observer.noteUserTextInput(opts.Prompt)
			if opts.awaitFirstTurn != nil {
				if err := awaitSessionFirstTurn(ctx, opts.awaitFirstTurn); err != nil {
					return state, false, errors.Join(fmt.Errorf("send session first turn: %w", err), stopAndDrain())
				}
			}
		}
		if opts.CloseAfterOpen && opts.Prompt == "" && opts.AudioIn == nil && !state.closeSent {
			state.closeAfterOpenPending = true
			var closeErr error
			state, closeErr = closePendingSessionIfReady(ctx, loop, opts, state)
			if closeErr != nil {
				return state, false, errors.Join(closeErr, stopAndDrain())
			}
		}
		startAudio()
	}
	if shouldDispatchScheduledAudioForMessage(msg, opts.ScheduledAudioDispatch) {
		if err := opts.observer.dispatchScheduledInputs(ctx, loop); err != nil {
			return state, false, errors.Join(err, stopAndDrain())
		}
	}
	if opts.CloseAfterOpen && opts.Prompt != "" && msg.Type == messages.StreamTypeMessageEnd && !state.closeSent && (opts.observer == nil || opts.observer.lastMessageEndAdmitted()) {
		state.closeAfterOpenPending = true
		var closeErr error
		state, closeErr = closePendingSessionIfReady(ctx, loop, opts, state)
		if closeErr != nil {
			return state, false, errors.Join(closeErr, stopAndDrain())
		}
	}
	if opts.CloseAfterScheduledAudio && msg.Type == messages.StreamTypeMessageEnd && (opts.observer == nil || opts.observer.lastMessageEndAdmitted()) {
		var closeErr error
		state, closeErr = closePendingSessionIfReady(ctx, loop, opts, state)
		if closeErr != nil {
			return state, false, errors.Join(closeErr, stopAndDrain())
		}
	}
	if opts.AudioIn != nil {
		if shouldStopAudioInputSessionLoop(msg, opts, state.closeSent, awaitingResponse) {
			return state, true, stopAndDrain()
		}
	} else if shouldStopSessionLoop(msg, opts, state.closeSent) {
		return state, true, stopAndDrain()
	}
	return state, false, nil
}

// shouldDispatchScheduledAudioForMessage identifies stream boundaries that can
// make the next scheduled input eligible. Completion-gated scheduling keeps
// its existing session/open, configuration, terminal, and tool-lifecycle
// wakeups. Active-response scheduling additionally wakes at the first live
// response boundary so the model runner can own the normal barge-in path.
func shouldDispatchScheduledAudioForMessage(msg messages.StreamMessage, policy ScheduledAudioDispatchPolicy) bool {
	switch msg.Type {
	case messages.StreamTypeSessionOpen, messages.StreamTypeMessageEnd, messages.StreamTypeSessionUpdated:
		return true
	case messages.StreamTypeMessageStart, messages.StreamTypeAudioStart:
		return policy == ScheduledAudioDispatchActiveResponse
	default:
		return false
	}
}

func runAgentLoopSessionStream(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) error {
	var rtcPumpErrors <-chan error
	sessionInferencer, rtcPumpErrors = bindRTCDeviceSessionInferencer(sessionInferencer, opts.rtcDeviceBinding)
	observedInferencer := newObservedSessionInferencer(sessionInferencer, opts.runtime)
	observedInferencer.progress = opts.observer
	if opts.observer != nil {
		opts.observer.setToolResultsEnabled(opts.ToolExecutor != nil)
	}
	loop, err := agentloop.New(duplexSessionLoopOptions(observedInferencer, opts)...)
	if err != nil {
		return fmt.Errorf("create session agent loop: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if opts.loopReady != nil {
		select {
		case opts.loopReady <- loop:
		case <-runCtx.Done():
			return runCtx.Err()
		}
	}
	if opts.AudioIn != nil {
		opts.AudioIn.bindContext(runCtx)
	}

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- loop.Run(runCtx)
	}()
	timeout := make(<-chan time.Time)
	if opts.MaxDuration > 0 {
		timeout = time.After(opts.MaxDuration)
	}

	// The optional audio input producer starts only after SESSION.OPEN so
	// buffered frames cannot precede the provider handshake. Every terminal
	// path below awaits it before returning.
	var audioCh <-chan error
	startAudio := func() {
		if opts.AudioIn == nil || audioCh != nil {
			return
		}
		audioErrCh := make(chan error, 1)
		audioCh = audioErrCh
		go func() { audioErrCh <- streamSessionAudioInput(runCtx, loop, opts.AudioIn) }()
	}
	waitAudio := func() error {
		if audioCh == nil {
			return nil
		}
		audioErr := <-audioCh
		audioCh = nil
		return audioErr
	}

	var runErr error
	runDone := false
	waitRun := func() error {
		if !runDone {
			runErr = <-runErrCh
			runDone = true
		}
		return runErr
	}
	stop := func() error {
		cancel()
		var bindingErr error
		if opts.rtcDeviceBinding != nil {
			bindingErr = opts.rtcDeviceBinding.Close()
		}
		return errors.Join(joinSessionTerminationErrors(waitRun(), waitAudio()), bindingErr)
	}
	stopAndDrain := func() error {
		stopErr := stop()
		if drainErr := drainSessionLoopMessages(out, loop, opts.observer); drainErr != nil {
			stopErr = errors.Join(stopErr, drainErr)
		}
		if opts.observer != nil {
			// The engine may have committed a provider tool delta to conversation
			// history before cancellation prevented the consumer-facing outbox from
			// delivering it. Recover only provider tool lifecycle identity after the
			// hot loop is stopped, avoiding duplicate output accounting.
			opts.observer.observeBufferedProviderToolLifecycle(loop.GetConversationDeltas())
		}
		if sessionErr := observedInferencer.sessionFailure(); sessionErr != nil {
			stopErr = errors.Join(stopErr, fmt.Errorf("session transport: %w", sessionErr))
		}
		return stopErr
	}

	var sessionUpdatedTimer *time.Timer
	var sessionUpdatedTimeout <-chan time.Time
	startSessionUpdatedTimer := func() {
		if !opts.RequireSessionUpdated || opts.observer == nil || !opts.observer.scheduledAudioAwaitingConfiguration() || sessionUpdatedTimer != nil {
			return
		}
		timeout := opts.SessionUpdatedTimeout
		if timeout <= 0 {
			timeout = sessionScheduledAudioConfigTimeout
		}
		sessionUpdatedTimer = time.NewTimer(timeout)
		sessionUpdatedTimeout = sessionUpdatedTimer.C
	}
	stopSessionUpdatedTimer := func() {
		if sessionUpdatedTimer == nil {
			return
		}
		sessionUpdatedTimer.Stop()
		sessionUpdatedTimer = nil
		sessionUpdatedTimeout = nil
	}
	defer stopSessionUpdatedTimer()

	state := sessionLoopMessageState{}
	// awaitingResponse is the explicit end-of-turn state: it turns on only
	// after the finite audio source reached EOF AND its end-of-turn signal
	// (MESSAGE.END -> input_audio_buffer.commit + response.create) was
	// accepted by the loop. Local audio EOF alone never terminates the run;
	// while awaiting a response only a terminal response frame, an explicit
	// error, or max-duration expiry may end the session.
	awaitingResponse := opts.AudioIn == nil
	done := opts.Done
	audioInterruptions := opts.AudioInterruptions
	toolLifecycleEvents := opts.observer.toolLifecycleEvents()
	for {
		select {
		case input, ok := <-audioInterruptions:
			if !ok {
				audioInterruptions = nil
				continue
			}
			if len(input.PCM) == 0 {
				return errors.Join(errors.New("event-driven audio input is empty"), stopAndDrain())
			}
			if err := loop.SendAudioInput(runCtx, input.PCM); err != nil {
				return errors.Join(fmt.Errorf("send event-driven audio input: %w", err), stopAndDrain())
			}
			if opts.observer != nil {
				opts.observer.account(metrics.DirectionInput, metrics.ModalityAudio, len(input.PCM))
			}
			if input.EndOfTurn {
				if err := loop.SendSessionEvent(runCtx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); err != nil {
					return errors.Join(fmt.Errorf("send event-driven audio input end-of-turn: %w", err), stopAndDrain())
				}
			}
		case <-toolLifecycleEvents:
			// A tool lifecycle transition can make the next scheduled audio
			// input eligible without producing a provider delta. Re-run the
			// scheduler on the same serialized session-loop goroutine before
			// evaluating close, so result acceptance and continuation
			// completion cannot strand the next turn.
			if err := opts.observer.dispatchScheduledInputs(runCtx, loop); err != nil {
				return errors.Join(err, stopAndDrain())
			}
			var closeErr error
			state, closeErr = closePendingSessionIfReady(runCtx, loop, opts, state)
			if closeErr != nil {
				return errors.Join(closeErr, stopAndDrain())
			}
		case audioErr := <-audioCh:
			audioCh = nil
			if audioErr != nil && !isSessionCancellation(audioErr) {
				cancel()
				stopErr := errors.Join(audioErr, joinSessionTerminationErrors(waitRun(), nil))
				if drainErr := drainSessionLoopMessages(out, loop, opts.observer); drainErr != nil {
					stopErr = errors.Join(stopErr, drainErr)
				}
				return stopErr
			}
			awaitingResponse = audioErr == nil
		case pumpErr := <-rtcPumpErrors:
			cancel()
			stopErr := errors.Join(pumpErr, opts.rtcDeviceBinding.Close(), joinSessionTerminationErrors(waitRun(), waitAudio()))
			if drainErr := drainSessionLoopMessages(out, loop, opts.observer); drainErr != nil {
				stopErr = errors.Join(stopErr, drainErr)
			}
			return stopErr
		case <-done:
			doneErr := error(nil)
			if opts.DoneErr != nil {
				doneErr = opts.DoneErr()
			}
			var initialDrainErr error
			if doneErr == nil {
				initialDrainErr = drainSessionLoopMessagesUntilIdle(out, loop, sessionReplayDoneDrainIdleDelay, opts.observer)
			}
			stopErr := stop()
			if drainErr := drainSessionLoopMessages(out, loop, opts.observer); drainErr != nil {
				stopErr = errors.Join(stopErr, drainErr)
			}
			if initialDrainErr != nil {
				stopErr = errors.Join(stopErr, initialDrainErr)
			}
			if doneErr != nil {
				stopErr = errors.Join(stopErr, doneErr)
			}
			return stopErr
		case <-timeout:
			return stopAndDrain()
		case <-sessionUpdatedTimeout:
			stopSessionUpdatedTimer()
			return errors.Join(sessionScheduledAudioConfigTimeoutError(opts), stopAndDrain())
		case <-ctx.Done():
			// Cancellation can race the model runner's final provider deltas. Drain
			// briefly before stopping, then drain once more after the hot loop exits,
			// so a queued TOOLCALL.END or continuation boundary still contributes its
			// call ID to the terminal lifecycle error.
			preCancelDrainErr := drainSessionLoopMessagesUntilQuiet(out, loop, sessionReplayDoneDrainIdleDelay, opts.observer)
			stopErr := stopAndDrain()
			if preCancelDrainErr != nil {
				stopErr = errors.Join(stopErr, preCancelDrainErr)
			}
			if awaitingResponse {
				return errors.Join(stopErr, fmt.Errorf("session cancelled while awaiting model response after end-of-turn: %w", ctx.Err()))
			}
			return sessionRunTerminationError(ctx, stopErr)
		case <-observedInferencer.Done():
			drainErr := drainSessionLoopMessagesUntilQuiet(out, loop, 25*time.Millisecond, opts.observer)
			stopErr := stopAndDrain()
			if drainErr != nil {
				stopErr = errors.Join(stopErr, drainErr)
			}
			if connectErr := observedInferencer.connectFailure(); connectErr != nil {
				stopErr = errors.Join(stopErr, fmt.Errorf("session connect: %w", connectErr))
			}
			return stopErr
		case err := <-runErrCh:
			runErr = err
			runDone = true
			cancel()
			return sessionRunTerminationError(ctx, stopAndDrain())
		case msg := <-loop.Deltas().Chan():
			nextState, stopLoop, msgErr := handleSessionLoopMessage(runCtx, out, loop, opts, msg, state, awaitingResponse, startAudio, stopAndDrain)
			state = nextState
			if msgErr != nil {
				return msgErr
			}
			if msg.Type == messages.StreamTypeSessionOpen {
				startSessionUpdatedTimer()
			}
			if opts.observer != nil && opts.observer.scheduledAudioReady() {
				stopSessionUpdatedTimer()
			}
			if stopLoop {
				return nil
			}
		}
	}
}

// closePendingSessionIfReady is shared by response handling and the
// asynchronous tool-result acceptance wake-up. A final accepted result may
// arrive after the final response.done, so closure must be re-evaluated from
// both paths.
func closePendingSessionIfReady(ctx context.Context, loop *agentloop.AgentLoop, opts sessionLoopOptions, state sessionLoopMessageState) (sessionLoopMessageState, error) {
	if state.closeSent {
		return state, nil
	}
	if opts.observer != nil && opts.observer.hasToolLifecycleObligation() {
		return state, nil
	}
	closeAfterOpen := opts.CloseAfterOpen && state.closeAfterOpenPending
	closeAfterScheduled := opts.CloseAfterScheduledAudio && opts.observer != nil && opts.observer.scheduledAudioComplete()
	if !closeAfterOpen && !closeAfterScheduled {
		return state, nil
	}
	if err := sendSessionClose(ctx, loop); err != nil {
		return state, err
	}
	state.closeSent = true
	return state, nil
}
