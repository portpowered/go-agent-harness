package sessionwrap

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

func Factory(factory session.LiveInferencerFactory, capacity int) session.LiveInferencerFactory {
	return func(ctx context.Context, request session.LiveRequest) (messages.SessionInferencer, error) {
		inner, err := factory(ctx, request)
		if err != nil || inner == nil {
			return inner, err
		}
		if request.Replay.Kind == session.LiveReplayKindTurn {
			inner = WrapTurnReplay(inner, request.OutputAudioSampleRate, request.OutputAudioContinuous)
		}
		return TerminalDrain(inner, request.OutputAudioContinuous, capacity), nil
	}
}

func TerminalDrain(inner messages.SessionInferencer, continuous bool, capacity int) messages.SessionInferencer {
	return terminalDrainInferencer{inner: inner, continuous: continuous, capacity: capacity}
}

func WrapSession(ctx context.Context, inner messages.Session, continuous bool, capacity int) messages.Session {
	if inner == nil {
		return nil
	}
	if provider, ok := inner.(sharedaudio.MediaSession); ok {
		CaptureMediaEndpoints(inner, provider, continuous)
	}
	source := inner.Receive()
	if source != nil && source.Cap() > 0 {
		capacity = source.Cap()
	}
	if capacity <= 0 {
		capacity = 128
	}
	drained := &terminalDrainSession{
		inner: inner, receive: messages.NewTypedBuffer[messages.StreamMessage](capacity),
		done: make(chan struct{}), stop: make(chan struct{}),
	}
	go drained.forward(context.WithoutCancel(ctx), source, inner.Done())
	return drained
}

type terminalDrainInferencer struct {
	inner      messages.SessionInferencer
	continuous bool
	capacity   int
}

func (i terminalDrainInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil || inner == nil {
		return inner, err
	}
	return WrapSession(ctx, inner, i.continuous, i.capacity), nil
}

func (i terminalDrainInferencer) FlushCapture() error {
	if flusher, ok := i.inner.(interface{ FlushCapture() error }); ok {
		return flusher.FlushCapture()
	}
	return nil
}

type terminalDrainSession struct {
	inner      messages.Session
	receive    *messages.TypedBuffer[messages.StreamMessage]
	done, stop chan struct{}
	close      sync.Once
	closeErr   error
}

func (s *terminalDrainSession) forward(ctx context.Context, source *messages.TypedBuffer[messages.StreamMessage], sourceDone <-chan struct{}) {
	defer close(s.done)
	if source == nil {
		return
	}
	for {
		select {
		case msg, ok := <-source.Chan():
			if !ok || !s.forwardMessage(ctx, msg) {
				return
			}
		case <-sourceDone:
			s.drain(ctx, source)
			return
		case <-s.stop:
			return
		}
	}
}

func (s *terminalDrainSession) drain(ctx context.Context, source *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := source.Read()
		if !ok || !s.forwardMessage(ctx, msg) {
			return
		}
	}
}

func (s *terminalDrainSession) forwardMessage(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionClose {
		msg.ResponseID = ""
	}
	return s.receive.WriteWaitContextOrDone(ctx, s.stop, msg).OK()
}

func (s *terminalDrainSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *terminalDrainSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if s == nil || s.inner == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendClosed}
	}
	if sender, ok := s.inner.(messages.SessionSendOutcomeSender); ok {
		return sender.SendWithOutcome(ctx, msg)
	}
	if s.inner.Send(ctx, msg) {
		return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	}
	if ctx != nil && ctx.Err() != nil {
		status := messages.SessionSendCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = messages.SessionSendTimedOut
		}
		return messages.SessionSendOutcome{Status: status, Err: ctx.Err()}
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
}

func (s *terminalDrainSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}
func (s *terminalDrainSession) Done() <-chan struct{} { return s.done }

func (s *terminalDrainSession) Close() error {
	if s == nil {
		return nil
	}
	s.close.Do(func() {
		close(s.stop)
		if s.inner != nil {
			s.closeErr = s.inner.Close()
		}
	})
	<-s.done
	return s.closeErr
}

func (s *terminalDrainSession) SupportsCompleteMessages() bool {
	capability, ok := s.inner.(interface{ SupportsCompleteMessages() bool })
	return ok && capability.SupportsCompleteMessages()
}

func (s *terminalDrainSession) SupportsCompleteMessagesWithoutResponse() bool {
	capability, ok := s.inner.(interface{ SupportsCompleteMessagesWithoutResponse() bool })
	return ok && capability.SupportsCompleteMessagesWithoutResponse()
}

func (s *terminalDrainSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessage(ctx, msg)
}

func (s *terminalDrainSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.inner.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *terminalDrainSession) RTCMedia() sharedaudio.MediaEndpoints {
	provider, ok := s.inner.(sharedaudio.MediaSession)
	if !ok {
		return sharedaudio.MediaEndpoints{}
	}
	return provider.RTCMedia()
}

func (s *terminalDrainSession) RTCMediaWithOptions(options sharedaudio.MediaSessionOptions) sharedaudio.MediaEndpoints {
	provider, ok := s.inner.(sharedaudio.ConfigurableMediaSession)
	if !ok {
		return s.RTCMedia()
	}
	return provider.RTCMediaWithOptions(options)
}

func (s *terminalDrainSession) InitialSessionConfigSent() bool {
	marker, ok := s.inner.(interface{ InitialSessionConfigSent() bool })
	return ok && marker.InitialSessionConfigSent()
}

func (s *terminalDrainSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if requester, ok := s.inner.(messages.SessionResponseRequester); ok {
		return requester.RequestResponse(ctx)
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
}

func (s *terminalDrainSession) SupportsResponseRequests() bool {
	if capability, ok := s.inner.(messages.SessionResponseCapability); ok {
		return capability.SupportsResponseRequests()
	}
	_, ok := s.inner.(messages.SessionResponseRequester)
	return ok
}

func (s *terminalDrainSession) FlushOutbound(ctx context.Context) error {
	if flusher, ok := s.inner.(messages.SessionOutboundFlusher); ok {
		return flusher.FlushOutbound(ctx)
	}
	return nil
}

func (s *terminalDrainSession) TerminalError() error {
	provider, ok := s.inner.(interface{ TerminalError() error })
	if !ok {
		return nil
	}
	return provider.TerminalError()
}
