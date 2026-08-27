// This file contains live session-loop construction, operation, and lifecycle observation for the session command.
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

const sessionReplayDoneDrainIdleDelay = 25 * time.Millisecond

// ErrSessionScheduledAudioIncomplete identifies a live scheduled-audio run
// that ended before every queued input received an assistant response.
var ErrSessionScheduledAudioIncomplete = errors.New("scheduled audio session ended before all turns completed")

// sessionFirstTurnAckTimeout bounds how long the SESSION.OPEN handler waits
// for the first user turn acceptance before failing the run instead of
// streaming user audio over an unacknowledged turn.
const sessionFirstTurnAckTimeout = 30 * time.Second

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
	Done           <-chan struct{}
	DoneErr        func() error
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

	// rtcDeviceBinding is opened by the enclosing runtime plan and is started
	// against the real session-owned media endpoints after ConnectSession.
	rtcDeviceBinding *RTCDeviceBinding

	// CloseAfterScheduledAudio requests a live scheduled-audio session close
	// only after every queued input has produced a terminal assistant turn.
	// Replay plans leave this false so capture-derived close behavior remains
	// authoritative.
	CloseAfterScheduledAudio bool
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
	err = scheduledAudioCompletionError(err, opts)
	opts.observer.finish(err)
	return err
}

func scheduledAudioCompletionError(err error, opts sessionLoopOptions) error {
	if err != nil || !opts.CloseAfterScheduledAudio || opts.observer == nil || opts.observer.scheduledAudioComplete() {
		return err
	}
	return fmt.Errorf("%w: completed %d of %d", ErrSessionScheduledAudioIncomplete, opts.observer.turnsCompleted, opts.observer.scheduledInputs)
}

type sessionLoopMessageState struct {
	promptSent bool
	closeSent  bool
}

func handleSessionLoopMessage(ctx context.Context, out io.Writer, loop *agentloop.AgentLoop, opts sessionLoopOptions, msg messages.StreamMessage, state sessionLoopMessageState, awaitingResponse bool, startAudio func(), stop func() error, stopAndDrain func() error) (sessionLoopMessageState, bool, error) {
	opts.observer.observe(msg)
	if err := writeSessionReplayMessage(out, msg); err != nil {
		return state, false, errors.Join(err, stop())
	}
	if msg.Type == messages.StreamTypeSessionOpen {
		if opts.Prompt != "" && !state.promptSent {
			state.promptSent = true
			userMsg := messages.NewTextMessage(messages.RoleUser, opts.Prompt)
			if err := loop.Send(ctx, []messages.Message{userMsg}); err != nil {
				return state, false, errors.Join(fmt.Errorf("send session message: %w", err), stop())
			}
			opts.observer.noteUserTextInput(opts.Prompt)
			if opts.awaitFirstTurn != nil {
				if err := awaitSessionFirstTurn(ctx, opts.awaitFirstTurn); err != nil {
					return state, false, errors.Join(fmt.Errorf("send session first turn: %w", err), stop())
				}
			}
		}
		if opts.CloseAfterOpen && opts.Prompt == "" && opts.AudioIn == nil && !state.closeSent {
			state.closeSent = true
			if err := sendSessionClose(ctx, loop); err != nil {
				return state, false, errors.Join(err, stop())
			}
		}
		startAudio()
	}
	if msg.Type == messages.StreamTypeSessionOpen || msg.Type == messages.StreamTypeMessageEnd {
		if err := opts.observer.dispatchScheduledInputs(ctx, loop); err != nil {
			return state, false, errors.Join(err, stopAndDrain())
		}
	}
	if opts.CloseAfterOpen && opts.Prompt != "" && msg.Type == messages.StreamTypeMessageEnd && !state.closeSent {
		state.closeSent = true
		if err := sendSessionClose(ctx, loop); err != nil {
			return state, false, errors.Join(err, stop())
		}
	}
	if opts.CloseAfterScheduledAudio && msg.Type == messages.StreamTypeMessageEnd && opts.observer.scheduledAudioComplete() && !state.closeSent {
		state.closeSent = true
		if err := sendSessionClose(ctx, loop); err != nil {
			return state, false, errors.Join(err, stop())
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

func runAgentLoopSessionStream(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) error {
	var rtcPumpErrors <-chan error
	sessionInferencer, rtcPumpErrors = bindRTCDeviceSessionInferencer(sessionInferencer, opts.rtcDeviceBinding)
	observedInferencer := newObservedSessionInferencer(sessionInferencer, opts.runtime)
	loop, err := agentloop.New(duplexSessionLoopOptions(observedInferencer, opts)...)
	if err != nil {
		return fmt.Errorf("create session agent loop: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
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
		if sessionErr := observedInferencer.sessionFailure(); sessionErr != nil {
			stopErr = errors.Join(stopErr, fmt.Errorf("session transport: %w", sessionErr))
		}
		return stopErr
	}

	promptSent := false
	closeSent := false
	// awaitingResponse is the explicit end-of-turn state: it turns on only
	// after the finite audio source reached EOF AND its end-of-turn signal
	// (MESSAGE.END -> input_audio_buffer.commit + response.create) was
	// accepted by the loop. Local audio EOF alone never terminates the run;
	// while awaiting a response only a terminal response frame, an explicit
	// error, or max-duration expiry may end the session.
	awaitingResponse := opts.AudioIn == nil
	done := opts.Done
	for {
		select {
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
		case <-ctx.Done():
			stopErr := stop()
			if awaitingResponse {
				return errors.Join(stopErr, fmt.Errorf("session cancelled while awaiting model response after end-of-turn: %w", ctx.Err()))
			}
			if stopErr != nil {
				return stopErr
			}
			return ctx.Err()
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
			return stopAndDrain()
		case msg := <-loop.Deltas().Chan():
			state, stopLoop, msgErr := handleSessionLoopMessage(runCtx, out, loop, opts, msg, sessionLoopMessageState{promptSent: promptSent, closeSent: closeSent}, awaitingResponse, startAudio, stop, stopAndDrain)
			promptSent = state.promptSent
			closeSent = state.closeSent
			if msgErr != nil {
				return msgErr
			}
			if stopLoop {
				return nil
			}
		}
	}
}

type observedSessionInferencer struct {
	inner   messages.SessionInferencer
	done    chan struct{}
	once    sync.Once
	runtime *sessionRuntimeObservationRecorder

	mu         sync.Mutex
	connectErr error
	sessionErr error
	session    messages.Session
}

type sessionTerminalErrorSource interface {
	TerminalError() error
}

var _ messages.SessionInferencer = (*observedSessionInferencer)(nil)

func newObservedSessionInferencer(inner messages.SessionInferencer, runtime ...*sessionRuntimeObservationRecorder) *observedSessionInferencer {
	var observationRecorder *sessionRuntimeObservationRecorder
	if len(runtime) > 0 {
		observationRecorder = runtime[0]
	}
	return &observedSessionInferencer{
		inner:   inner,
		done:    make(chan struct{}),
		runtime: observationRecorder,
	}
}

// ConnectSession wraps the inner connect and remembers a failed connect so
// the session runner can surface it: the engine runs model runners as
// background participants whose errors are not propagated to the hot loop.
func (i *observedSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		i.mu.Lock()
		i.connectErr = err
		i.mu.Unlock()
		i.closeDone()
		return nil, err
	}
	i.mu.Lock()
	i.session = session
	i.mu.Unlock()
	go func() {
		select {
		case <-session.Done():
			if err := terminalSessionError(session); err != nil {
				i.mu.Lock()
				i.sessionErr = err
				i.mu.Unlock()
			}
			i.closeDone()
		case <-ctx.Done():
		}
	}()
	return &observedSession{Session: session, closeDone: i.closeDone, runtime: i.runtime}, nil
}

// connectFailure returns the remembered connect error, if any.
func (i *observedSessionInferencer) connectFailure() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.connectErr
}

// sessionFailure returns an unexpected terminal error reported by the
// provider session after a successful connection. The optional interface keeps
// the generic messages.Session contract unchanged for injected and replay
// sessions that do not expose transport details.
func (i *observedSessionInferencer) sessionFailure() error {
	i.mu.Lock()
	session, remembered := i.session, i.sessionErr
	i.mu.Unlock()
	if err := terminalSessionError(session); err != nil {
		return err
	}
	return remembered
}

func terminalSessionError(session messages.Session) error {
	source, ok := session.(sessionTerminalErrorSource)
	if !ok {
		return nil
	}
	return source.TerminalError()
}

func (i *observedSessionInferencer) Done() <-chan struct{} {
	return i.done
}

func (i *observedSessionInferencer) closeDone() {
	i.once.Do(func() {
		close(i.done)
	})
}

type observedSession struct {
	messages.Session
	closeDone func()
	runtime   *sessionRuntimeObservationRecorder
	once      sync.Once
}

var _ messages.Session = (*observedSession)(nil)

func (s *observedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	ok := s.Session.Send(ctx, msg)
	if ok && msg.Type == messages.StreamTypeMessageEnd && s.runtime != nil {
		s.runtime.inputCommit()
	}
	return ok
}

// SendMessage forwards the optional complete-message provider capability. The
// observation wrapper embeds the stream-only public Session interface, so it
// must preserve the rich tool-result path used by multimodal sessions.
func (s *observedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

// SendMessageWithoutResponse preserves deferred rich-message delivery for
// callers that batch tool results before requesting one provider response.
func (s *observedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *observedSession) Close() error {
	err := s.Session.Close()
	s.markDone()
	return err
}

func (s *observedSession) markDone() {
	s.once.Do(s.closeDone)
}
