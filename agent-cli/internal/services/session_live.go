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
	Prompt          string
	CloseAfterOpen  bool
	WaitForClose    bool
	MaxDuration     time.Duration
	Done            <-chan struct{}
	DoneErr         func() error
	ToolExecutor    messages.ToolExecutor
	ToolDefinitions []messages.ToolDefinition
}

func runAgentLoopSession(ctx context.Context, out io.Writer, sessionInferencer messages.SessionInferencer, opts sessionLoopOptions) error {
	observedInferencer := newObservedSessionInferencer(sessionInferencer)
	loopOpts := []agentloop.Option{
		agentloop.WithMode(engine.DuplexSession),
		agentloop.WithSessionInferencer(observedInferencer),
	}
	if opts.ToolExecutor != nil {
		loopOpts = append(loopOpts,
			agentloop.WithTools(opts.ToolDefinitions),
			agentloop.WithToolExecutor(opts.ToolExecutor),
		)
	} else {
		loopOpts = append(loopOpts, agentloop.WithToolExecutionDisabled())
	}
	loop, err := agentloop.New(loopOpts...)
	if err != nil {
		return fmt.Errorf("create session agent loop: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- loop.Run(runCtx)
	}()
	timeout := make(<-chan time.Time)
	if opts.MaxDuration > 0 {
		timeout = time.After(opts.MaxDuration)
	}

	promptSent := false
	closeSent := false
	done := opts.Done
	for {
		select {
		case <-done:
			doneErr := error(nil)
			if opts.DoneErr != nil {
				doneErr = opts.DoneErr()
			}
			if doneErr == nil {
				if drainErr := drainSessionLoopMessagesUntilIdle(out, loop, sessionReplayDoneDrainIdleDelay); drainErr != nil {
					return drainErr
				}
			}
			cancel()
			err := <-runErrCh
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				return drainErr
			}
			if doneErr != nil {
				return doneErr
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			return nil
		case <-timeout:
			cancel()
			err := <-runErrCh
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				return drainErr
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("session error: %w", err)
			}
			return nil
		case <-ctx.Done():
			cancel()
			err := <-runErrCh
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("session error: %w", err)
			}
			return ctx.Err()
		case <-observedInferencer.Done():
			if drainErr := drainSessionLoopMessagesUntilQuiet(out, loop, 25*time.Millisecond); drainErr != nil {
				cancel()
				<-runErrCh
				return drainErr
			}
			cancel()
			err := <-runErrCh
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				return drainErr
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("session error: %w", err)
			}
			return nil
		case err := <-runErrCh:
			if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
				return drainErr
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("session error: %w", err)
			}
			return nil
		case msg := <-loop.Deltas().Chan():
			if err := writeSessionReplayMessage(out, msg); err != nil {
				cancel()
				<-runErrCh
				return err
			}
			if msg.Type == messages.StreamTypeSessionOpen {
				if opts.Prompt != "" && !promptSent {
					promptSent = true
					userMsg := messages.NewTextMessage(messages.RoleUser, opts.Prompt)
					if err := loop.Send(runCtx, []messages.Message{userMsg}); err != nil {
						cancel()
						<-runErrCh
						return fmt.Errorf("send session message: %w", err)
					}
				}
				if opts.CloseAfterOpen && opts.Prompt == "" && !closeSent {
					closeSent = true
					if err := sendSessionClose(runCtx, loop); err != nil {
						cancel()
						<-runErrCh
						return err
					}
				}
			}
			if opts.CloseAfterOpen && opts.Prompt != "" && msg.Type == messages.StreamTypeMessageEnd && !closeSent {
				closeSent = true
				if err := sendSessionClose(runCtx, loop); err != nil {
					cancel()
					<-runErrCh
					return err
				}
			}
			if shouldStopSessionLoop(msg, opts, closeSent) {
				cancel()
				err := <-runErrCh
				if drainErr := drainSessionLoopMessages(out, loop); drainErr != nil {
					return drainErr
				}
				if err != nil && !errors.Is(err, context.Canceled) {
					return fmt.Errorf("session error: %w", err)
				}
				return nil
			}
		}
	}
}

type observedSessionInferencer struct {
	inner messages.SessionInferencer
	done  chan struct{}
	once  sync.Once
}

var _ messages.SessionInferencer = (*observedSessionInferencer)(nil)

func newObservedSessionInferencer(inner messages.SessionInferencer) *observedSessionInferencer {
	return &observedSessionInferencer{
		inner: inner,
		done:  make(chan struct{}),
	}
}

func (i *observedSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
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
