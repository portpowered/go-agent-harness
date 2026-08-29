// This file contains live-session inferencer and session observation adapters.
package services

import (
	"context"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type observedSessionInferencer struct {
	inner    messages.SessionInferencer
	done     chan struct{}
	once     sync.Once
	runtime  *sessionRuntimeObservationRecorder
	progress *sessionProgressObserver

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
	return &observedSession{Session: session, closeDone: i.closeDone, runtime: i.runtime, progress: i.progress}, nil
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
	progress  *sessionProgressObserver
	once      sync.Once
}

var _ messages.Session = (*observedSession)(nil)

func (s *observedSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

// SendWithOutcome keeps the provider-facing acceptance result visible at the
// session lifecycle boundary. Tool calls are resolved only after this method
// reports success from the wrapped provider session.
func (s *observedSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	outcome := messages.SendSessionWithOutcome(ctx, s.Session, msg)
	if !outcome.OK() {
		if msg.Type == messages.StreamTypeToolCallEnd && s.progress != nil {
			if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
				s.progress.noteToolResultRejected(value.ToolCallID, outcome)
			}
		}
		return outcome
	}
	if msg.Type == messages.StreamTypeMessageEnd && s.runtime != nil {
		s.runtime.inputCommit()
	}
	if msg.Type == messages.StreamTypeToolCallEnd && s.progress != nil {
		if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
			s.progress.noteToolResultAccepted(value.ToolCallID)
		}
	}
	if msg.Type == messages.StreamTypeResponseCreate && s.progress != nil {
		s.progress.noteToolContinuationRequested()
	}
	return outcome
}

// RequestResponse forwards the optional explicit response request while
// preserving the capability boundary of replay and injected sessions.
func (s *observedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.Session)
}

func (s *observedSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

// SendMessage forwards the optional complete-message provider capability. The
// observation wrapper embeds the stream-only public Session interface, so it
// must preserve the rich tool-result path used by multimodal sessions.
func (s *observedSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSender)
	if !ok {
		return false
	}
	outcome := sessionCompleteMessageSendOutcome(ctx, sender.SendMessage(ctx, msg))
	s.observeCompleteMessageToolResult(msg, outcome, true)
	return outcome.OK()
}

// SendMessageWithoutResponse preserves deferred rich-message delivery for
// callers that batch tool results before requesting one provider response.
func (s *observedSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	if !ok {
		return false
	}
	outcome := sessionCompleteMessageSendOutcome(ctx, sender.SendMessageWithoutResponse(ctx, msg))
	s.observeCompleteMessageToolResult(msg, outcome, false)
	return outcome.OK()
}

func sessionCompleteMessageSendOutcome(ctx context.Context, sent bool) messages.SessionSendOutcome {
	if sent {
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if ctx != nil {
		switch ctx.Err() {
		case context.DeadlineExceeded:
			return messages.SessionSendOutcome{Status: messages.SessionSendTimedOut, Err: ctx.Err()}
		case context.Canceled:
			return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: ctx.Err()}
		}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
}

func (s *observedSession) observeCompleteMessageToolResult(msg messages.Message, outcome messages.SessionSendOutcome, requestsContinuation bool) {
	if s == nil || s.progress == nil || msg.ToolCallID == "" {
		return
	}
	if outcome.OK() {
		s.progress.noteToolResultAccepted(msg.ToolCallID)
		if requestsContinuation {
			s.progress.noteToolContinuationRequestedFor(msg.ToolCallID)
		}
		return
	}
	s.progress.noteToolResultRejected(msg.ToolCallID, outcome)
}

func (s *observedSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *observedSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func (s *observedSession) Close() error {
	err := s.Session.Close()
	s.markDone()
	return err
}

func (s *observedSession) markDone() {
	s.once.Do(s.closeDone)
}
