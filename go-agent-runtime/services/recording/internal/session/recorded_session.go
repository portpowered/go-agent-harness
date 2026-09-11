package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/recording"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

type inferencer struct {
	inner     messages.SessionInferencer
	recording *recorder
}

func (i *inferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.inner == nil {
		return nil, errors.New("recording inferencer is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("recording inferencer context is nil")
	}
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	if inner == nil {
		return nil, errors.New("recording inferencer returned nil session")
	}
	return newRecordedSession(ctx, inner, i.recording), nil
}

type recordedSession struct {
	inner     messages.Session
	recording *recorder
	ctx       context.Context
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func newRecordedSession(ctx context.Context, inner messages.Session, recording *recorder) *recordedSession {
	capacity := 1024
	if source := inner.Receive(); source != nil && source.Cap() > capacity {
		capacity = source.Cap()
	}
	s := &recordedSession{
		inner: inner, recording: recording, ctx: ctx,
		receive: messages.NewTypedBuffer[messages.StreamMessage](capacity), done: make(chan struct{}),
	}
	go s.relay(ctx)
	return s
}

func (s *recordedSession) Send(ctx context.Context, message messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, message).OK()
}

func (s *recordedSession) SendWithOutcome(ctx context.Context, message messages.StreamMessage) messages.SessionSendOutcome {
	outcome := messages.SendSessionWithOutcome(ctx, s.inner, message)
	if outcome.OK() {
		s.observeClient(message)
	}
	return outcome
}

func (s *recordedSession) observeClient(message messages.StreamMessage) {
	if err := s.recording.ObserveMessage(s.ctx, message, recording.SessionMessageFromClient); err != nil {
		s.recording.recordError(err)
	}
}

func (s *recordedSession) SendMessage(ctx context.Context, message messages.Message) bool {
	sender, ok := s.inner.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessage(ctx, message)
}

func (s *recordedSession) SendMessageWithoutResponse(ctx context.Context, message messages.Message) bool {
	sender, ok := s.inner.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessageWithoutResponse(ctx, message)
}

func (s *recordedSession) SupportsCompleteMessages() bool {
	capability, ok := s.inner.(interface{ SupportsCompleteMessages() bool })
	return ok && capability.SupportsCompleteMessages()
}

func (s *recordedSession) SupportsCompleteMessagesWithoutResponse() bool {
	capability, ok := s.inner.(interface{ SupportsCompleteMessagesWithoutResponse() bool })
	return ok && capability.SupportsCompleteMessagesWithoutResponse()
}

func (s *recordedSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if !messages.SupportsSessionResponseRequests(s.inner) {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return s.SendWithOutcome(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	})
}

func (s *recordedSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.inner)
}

func (s *recordedSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }

func (s *recordedSession) Done() <-chan struct{} { return s.inner.Done() }

func (s *recordedSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.inner.Close()
		select {
		case <-s.done:
			s.drain(s.ctx, s.inner.Receive())
		case <-time.After(time.Second):
			s.closeErr = errors.Join(s.closeErr, errors.New("recording session relay did not stop"))
		}
	})
	return s.closeErr
}

func (s *recordedSession) RTCMedia() sharedaudio.MediaEndpoints {
	if media, ok := s.inner.(sharedaudio.MediaSession); ok {
		return media.RTCMedia()
	}
	return sharedaudio.MediaEndpoints{}
}

func (s *recordedSession) TerminalError() error {
	if terminal, ok := s.inner.(interface{ TerminalError() error }); ok {
		return terminal.TerminalError()
	}
	return nil
}

func (s *recordedSession) relay(ctx context.Context) {
	defer close(s.done)
	source := s.inner.Receive()
	if source == nil {
		return
	}
	for {
		select {
		case message, ok := <-source.Chan():
			if !ok {
				return
			}
			s.observeAgent(message)
			if !s.forward(ctx, message) {
				return
			}
		case <-s.inner.Done():
			s.drain(ctx, source)
			return
		case <-ctx.Done():
			s.drain(ctx, source)
			return
		}
	}
}

func (s *recordedSession) observeAgent(message messages.StreamMessage) {
	if err := s.recording.ObserveMessage(s.ctx, message, recording.SessionMessageFromAgent); err != nil {
		s.recording.recordError(err)
	}
}

func (s *recordedSession) drain(ctx context.Context, source *messages.TypedBuffer[messages.StreamMessage]) {
	if source == nil {
		return
	}
	for {
		select {
		case message, ok := <-source.Chan():
			if !ok {
				return
			}
			s.observeAgent(message)
			if !s.forward(ctx, message) {
				return
			}
		default:
			return
		}
	}
}

func (s *recordedSession) forward(ctx context.Context, message messages.StreamMessage) bool {
	for {
		if s.receive.Write(ctx, message) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-s.inner.Done():
			return false
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func cloneStreamMessage(message messages.StreamMessage) messages.StreamMessage {
	clone := message
	switch value := message.Value.(type) {
	case *messages.AudioDeltaValue:
		if value != nil {
			copy := *value
			copy.Content = append([]byte(nil), value.Content...)
			clone.Value = &copy
		}
	case *messages.TextDeltaValue:
		if value != nil {
			copy := *value
			clone.Value = &copy
		}
	}
	return clone
}

func wrapRecordingError(kind error, operation, path string, cause error, secrets []string) error {
	_ = secrets
	return &transcript.RecordingError{Kind: kind, Operation: operation, Path: path, Cause: cause}
}

var _ messages.Session = (*recordedSession)(nil)
var _ messages.SessionSendOutcomeSender = (*recordedSession)(nil)
var _ messages.SessionResponseRequester = (*recordedSession)(nil)
var _ messages.SessionResponseCapability = (*recordedSession)(nil)
var _ sharedaudio.MediaSession = (*recordedSession)(nil)
