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

	// observer optionally records per-turn and terminal diagnostics from the
	// consumed delta stream; nil keeps runtime behavior unchanged.
	observer *sessionProgressObserver
}

func runAgentLoopSession(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) error {
	err := runAgentLoopSessionStream(ctx, out, sessionInferencer, opts)
	opts.observer.finish(err)
	return err
}

func runAgentLoopSessionStream(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) error {
	observedInferencer := newObservedSessionInferencer(sessionInferencer)
	loop, err := agentloop.New(
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(observedInferencer),
	)
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
		return joinSessionTerminationErrors(waitRun(), waitAudio())
	}
	stopAndDrain := func() error {
		stopErr := stop()
		if drainErr := drainSessionLoopMessages(out, loop, opts.observer); drainErr != nil {
			stopErr = errors.Join(stopErr, drainErr)
		}
		return stopErr
	}

	promptSent := false
	closeSent := false
	audioDone := opts.AudioIn == nil
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
			audioDone = true
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
			opts.observer.observe(msg)
			opts.observer.dispatchScheduledInputs(runCtx, loop)
			if err := writeSessionReplayMessage(out, msg); err != nil {
				return errors.Join(err, stop())
			}
			if msg.Type == messages.StreamTypeSessionOpen {
				if opts.Prompt != "" && !promptSent {
					promptSent = true
					userMsg := messages.NewTextMessage(messages.RoleUser, opts.Prompt)
					if err := loop.Send(runCtx, []messages.Message{userMsg}); err != nil {
						return errors.Join(fmt.Errorf("send session message: %w", err), stop())
					}
					opts.observer.noteUserTextInput(opts.Prompt)
				}
				if opts.CloseAfterOpen && opts.Prompt == "" && !closeSent {
					closeSent = true
					if err := sendSessionClose(runCtx, loop); err != nil {
						return errors.Join(err, stop())
					}
				}
				startAudio()
			}
			if opts.CloseAfterOpen && opts.Prompt != "" && msg.Type == messages.StreamTypeMessageEnd && !closeSent {
				closeSent = true
				if err := sendSessionClose(runCtx, loop); err != nil {
					return errors.Join(err, stop())
				}
			}
			if opts.AudioIn != nil {
				if shouldStopAudioInputSessionLoop(msg, opts, closeSent, audioDone) {
					return stopAndDrain()
				}
			} else if shouldStopSessionLoop(msg, opts, closeSent) {
				return stopAndDrain()
			}
		}
	}
}

type observedSessionInferencer struct {
	inner messages.SessionInferencer
	done  chan struct{}
	once  sync.Once

	mu         sync.Mutex
	connectErr error
}

var _ messages.SessionInferencer = (*observedSessionInferencer)(nil)

func newObservedSessionInferencer(inner messages.SessionInferencer) *observedSessionInferencer {
	return &observedSessionInferencer{
		inner: inner,
		done:  make(chan struct{}),
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
	go func() {
		select {
		case <-session.Done():
			i.closeDone()
		case <-ctx.Done():
		}
	}()
	return &observedSession{Session: session, closeDone: i.closeDone}, nil
}

// connectFailure returns the remembered connect error, if any.
func (i *observedSessionInferencer) connectFailure() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.connectErr
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
	once      sync.Once
}

var _ messages.Session = (*observedSession)(nil)

func (s *observedSession) Close() error {
	err := s.Session.Close()
	s.markDone()
	return err
}

func (s *observedSession) markDone() {
	s.once.Do(s.closeDone)
}
