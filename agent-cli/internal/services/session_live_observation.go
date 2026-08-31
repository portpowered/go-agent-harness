// This file contains live-session inferencer and session observation adapters.
package services

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

type observedSessionInferencer struct {
	inner       messages.SessionInferencer
	done        chan struct{}
	once        sync.Once
	connectDone chan struct{}
	closeOnce   sync.Once
	closeErr    error
	runtime     *sessionRuntimeObservationRecorder
	progress    *sessionProgressObserver

	mu              sync.Mutex
	connectErr      error
	sessionErr      error
	session         messages.Session
	observed        *observedSession
	closeRequested  bool
	connectStarted  bool
	connectFinished bool
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
		inner:       inner,
		done:        make(chan struct{}),
		connectDone: make(chan struct{}),
		runtime:     observationRecorder,
	}
}

// ConnectSession wraps the inner connect and remembers a failed connect so
// the session runner can surface it: the engine runs model runners as
// background participants whose errors are not propagated to the hot loop.
func (i *observedSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.mu.Lock()
	i.connectStarted = true
	i.mu.Unlock()
	defer func() {
		i.mu.Lock()
		i.connectFinished = true
		i.mu.Unlock()
		close(i.connectDone)
	}()
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
	wrapped := &observedSession{Session: session, closeDone: i.closeDone, runtime: i.runtime, progress: i.progress}
	i.observed = wrapped
	closeRequested := i.closeRequested
	i.mu.Unlock()
	if closeRequested {
		// A cancellation can win while ConnectSession is still returning. Close
		// the late session before handing it to the model runner so the caller
		// never returns with a provider connection that escaped its owner.
		_ = wrapped.Close()
	}
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
	return wrapped, nil
}

// CloseSession closes the session established by this inferencer and waits
// until the connect attempt has completed. Bare live sessions use this seam
// because AgentLoop.Run may return immediately after cancellation while its
// model-runner goroutine is still unwinding its deferred session close.
//
// The method is intentionally idempotent. observedSession also guards its
// underlying provider close, so the model runner's deferred close and this
// owner-driven close share one provider lifecycle call.
func (i *observedSessionInferencer) CloseSession() error {
	if i == nil {
		return nil
	}
	i.closeOnce.Do(func() {
		i.mu.Lock()
		i.closeRequested = true
		observed := i.observed
		waitForConnect := i.connectStarted && !i.connectFinished
		i.mu.Unlock()
		if observed != nil {
			i.closeErr = observed.Close()
		}
		if waitForConnect {
			<-i.connectDone
		}
		if observed == nil {
			i.mu.Lock()
			observed = i.observed
			i.mu.Unlock()
			if observed != nil {
				i.closeErr = errors.Join(i.closeErr, observed.Close())
			}
		}
	})
	return i.closeErr
}

func closeBareSessionIfNeeded(bare bool, inferencer *observedSessionInferencer) error {
	if !bare || inferencer == nil {
		return nil
	}
	return inferencer.CloseSession()
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
	closeOnce sync.Once
	closeErr  error
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
		s.runtime.responseCreate(msg)
	}
	if msg.Type == messages.StreamTypeResponseCreate && s.runtime != nil {
		s.runtime.responseCreate(msg)
	}
	if msg.Type == messages.StreamTypeToolCallEnd && s.progress != nil {
		if value, ok := msg.Value.(*messages.ToolCallEndValue); ok && value != nil {
			s.progress.noteToolResultAccepted(value.ToolCallID)
		}
	}
	if msg.Type == messages.StreamTypeResponseCreate && s.progress != nil {
		s.progress.noteToolContinuationRequested()
	}
	if s.progress != nil {
		s.progress.observeProviderDispatch(msg)
	}
	return outcome
}

// RequestResponse forwards the optional explicit response request while
// preserving the capability boundary of replay and injected sessions.
func (s *observedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	outcome := messages.RequestSessionResponse(ctx, s.Session)
	if outcome.OK() && s.runtime != nil {
		s.runtime.responseCreate(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	}
	if outcome.OK() && s.progress != nil {
		s.progress.observeProviderDispatch(messages.StreamMessage{Type: messages.StreamTypeResponseCreate})
	}
	return outcome
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
	if s == nil || s.progress == nil {
		return
	}
	if outcome.OK() {
		if msg.ToolCallID != "" {
			s.progress.noteToolResultAccepted(msg.ToolCallID)
		}
		if requestsContinuation {
			if msg.ToolCallID != "" {
				s.progress.noteToolContinuationRequestedFor(msg.ToolCallID)
			}
			s.progress.armProviderProgress()
		}
		return
	}
	if msg.ToolCallID != "" {
		s.progress.noteToolResultRejected(msg.ToolCallID, outcome)
	}
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
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closeErr = s.Session.Close()
		s.markDone()
	})
	return s.closeErr
}

func (s *observedSession) markDone() {
	s.once.Do(s.closeDone)
}
