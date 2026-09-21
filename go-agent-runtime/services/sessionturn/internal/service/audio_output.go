package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	audioOutputDrainWallSafety = 250 * time.Millisecond
	audioOutputDrainQuiet      = 25 * time.Millisecond
)

type audioOutputRuntime struct {
	inner    messages.SessionInferencer
	observer sessionturn.AudioDeltaObserver

	mu        sync.Mutex
	lastErr   error
	connected *audioOutputSession
}

func newAudioOutputRuntime(inner messages.SessionInferencer, observer sessionturn.AudioDeltaObserver) *audioOutputRuntime {
	return &audioOutputRuntime{inner: inner, observer: observer}
}

var _ sessionturn.AudioOutputRuntime = (*audioOutputRuntime)(nil)

func (o *audioOutputRuntime) Inferencer() messages.SessionInferencer { return o }

func (o *audioOutputRuntime) ConnectSession(ctx context.Context) (messages.Session, error) {
	if o == nil || o.inner == nil {
		return nil, sessionturn.ErrMissingTurnInferencer
	}
	if ctx == nil {
		return nil, errors.New("session turn audio output context is required")
	}
	inner, err := o.inner.ConnectSession(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	wrapped := newAudioOutputSession(ctx, inner, o.observe, o.recordErr)
	o.mu.Lock()
	o.connected = wrapped
	o.mu.Unlock()
	return wrapped, nil
}

func (o *audioOutputRuntime) observe(ctx context.Context, content []byte, msg messages.StreamMessage) error {
	if o == nil || o.observer == nil {
		return nil
	}
	if err := o.observer(ctx, content, msg); err != nil {
		o.recordErr(err)
		return err
	}
	return nil
}

func (o *audioOutputRuntime) recordErr(err error) {
	if o == nil || err == nil {
		return
	}
	o.mu.Lock()
	o.lastErr = errors.Join(o.lastErr, err)
	o.mu.Unlock()
}

func (o *audioOutputRuntime) Wait() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	connected := o.connected
	o.mu.Unlock()
	if connected != nil {
		if err := connected.Close(); err != nil {
			o.mu.Lock()
			o.lastErr = errors.Join(o.lastErr, err)
			o.mu.Unlock()
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lastErr
}

type audioOutputSession struct {
	messages.Session
	observe        sessionturn.AudioDeltaObserver
	receive        *messages.TypedBuffer[messages.StreamMessage]
	done           chan struct{}
	drainStarted   chan struct{}
	closeRequested chan struct{}
	closeOnce      sync.Once
	innerCloseOnce sync.Once
	innerCloseDone chan struct{}
	closeErr       error
	recordErr      func(error)
}

func newAudioOutputSession(ctx context.Context, inner messages.Session, observe sessionturn.AudioDeltaObserver, recordErr func(error)) *audioOutputSession {
	s := &audioOutputSession{
		Session:        inner,
		observe:        observe,
		receive:        messages.NewTypedBuffer[messages.StreamMessage](sessionturn.ReceiveCapacity),
		done:           make(chan struct{}),
		drainStarted:   make(chan struct{}),
		closeRequested: make(chan struct{}),
		innerCloseDone: make(chan struct{}),
		recordErr:      recordErr,
	}
	go s.forward(ctx)
	return s
}

func (s *audioOutputSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.Session.Send(ctx, msg)
}

func (s *audioOutputSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.Session, msg)
}

func (s *audioOutputSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.Session)
}

func (s *audioOutputSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

func (s *audioOutputSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(sessionturn.CompleteMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *audioOutputSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(sessionturn.CompleteMessageWithoutResponseSender)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *audioOutputSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *audioOutputSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func (s *audioOutputSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *audioOutputSession) Done() <-chan struct{} { return s.done }

func (s *audioOutputSession) RTCMedia() (sharedaudio.MediaEndpoints, bool) {
	if owner, ok := s.Session.(sharedaudio.MediaSession); ok {
		return owner.RTCMedia(), true
	}
	if owner, ok := s.Session.(interface {
		RTCMedia() (sharedaudio.MediaEndpoints, bool)
	}); ok {
		return owner.RTCMedia()
	}
	return sharedaudio.MediaEndpoints{}, false
}

func (s *audioOutputSession) TerminalError() error { return terminalSessionError(s.Session) }

func (s *audioOutputSession) forward(ctx context.Context) {
	defer func() {
		s.closeInner()
		close(s.done)
	}()
	input := s.Session.Receive()
	for {
		if ctx.Err() != nil {
			s.drainAfterCancellation(ctx, input)
			return
		}
		if s.forwardNext(ctx, input) {
			continue
		}
		if ctx.Err() != nil {
			s.drainAfterCancellation(ctx, input)
		} else {
			select {
			case <-s.closeRequested:
				s.drainAfterCancellation(ctx, input)
			default:
			}
		}
		return
	}
}

func (s *audioOutputSession) forwardNext(ctx context.Context, input *messages.TypedBuffer[messages.StreamMessage]) bool {
	select {
	case msg := <-input.Chan():
		retainingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), audioOutputDrainWallSafety)
		defer cancel()
		return s.forwardMessage(retainingCtx, msg, ctx.Err() != nil)
	case <-s.Session.Done():
		if ctx.Err() == nil {
			s.drain(input, ctx, false)
		}
	case <-s.closeRequested:
	case <-ctx.Done():
	}
	return false
}

func (s *audioOutputSession) drain(input *messages.TypedBuffer[messages.StreamMessage], ctx context.Context, retaining bool) bool {
	for {
		msg, ok := input.Read()
		if !ok {
			return true
		}
		if !s.forwardMessage(ctx, msg, retaining) {
			return false
		}
	}
}

func (s *audioOutputSession) drainAfterCancellation(ctx context.Context, input *messages.TypedBuffer[messages.StreamMessage]) {
	close(s.drainStarted)
	retainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), audioOutputDrainWallSafety)
	defer cancel()
	terminal := time.NewTimer(audioOutputDrainWallSafety)
	defer terminal.Stop()
	for {
		select {
		case msg := <-input.Chan():
			if !s.forwardMessage(retainCtx, msg, true) {
				return
			}
		case <-s.Session.Done():
			s.closeInner()
			s.drainAfterInnerClose(input, retainCtx)
			return
		case <-terminal.C:
			s.closeInner()
			s.drainAfterInnerClose(input, retainCtx)
			return
		}
	}
}

func (s *audioOutputSession) drainAfterInnerClose(input *messages.TypedBuffer[messages.StreamMessage], ctx context.Context) {
	select {
	case <-s.innerCloseDone:
	case <-ctx.Done():
		return
	}
	for {
		select {
		case msg := <-input.Chan():
			if !s.forwardMessage(ctx, msg, true) {
				return
			}
		case <-time.After(audioOutputDrainQuiet):
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *audioOutputSession) closeInner() {
	s.innerCloseOnce.Do(func() { go s.finishInnerClose() })
}

func (s *audioOutputSession) finishInnerClose() {
	s.closeErr = s.Session.Close()
	close(s.innerCloseDone)
}

func (s *audioOutputSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeRequested)
		<-s.done
		<-s.innerCloseDone
	})
	return s.closeErr
}

func (s *audioOutputSession) forwardMessage(ctx context.Context, msg messages.StreamMessage, retaining bool) bool {
	if msg.Type == messages.StreamTypeAudioDelta && assistantAudioDelta(msg) {
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok {
			s.recordErr(fmt.Errorf("AUDIO.DELTA has unexpected value %T", msg.Value))
			s.closeInner()
			return false
		}
		if s.observe != nil {
			if err := s.observe(ctx, value.Content, msg); err != nil {
				s.closeInner()
				return false
			}
		}
	}
	for {
		outcome := s.receive.WriteContext(ctx, msg)
		if outcome.OK() {
			return true
		}
		if outcome.Err != nil {
			return false
		}
		if retaining {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-s.closeRequested:
			return false
		case <-time.After(time.Millisecond):
		}
	}
}

func assistantAudioDelta(msg messages.StreamMessage) bool {
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}

func completeMessageCapabilities(session messages.Session) (complete, withoutResponse bool) {
	if capabilities, ok := session.(sessionturn.CompleteMessageCapabilities); ok {
		return capabilities.SupportsCompleteMessages(), capabilities.SupportsCompleteMessagesWithoutResponse()
	}
	_, complete = session.(sessionturn.CompleteMessageSender)
	_, withoutResponse = session.(sessionturn.CompleteMessageWithoutResponseSender)
	return complete, withoutResponse
}

func terminalSessionError(session messages.Session) error {
	source, ok := session.(sessionturn.TerminalErrorSource)
	if !ok {
		return nil
	}
	return source.TerminalError()
}
