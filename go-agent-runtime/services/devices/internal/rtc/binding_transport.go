package rtc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	devicert "github.com/portpowered/go-agent-harness/go-device-gateway/pkg/runtime"
)

const playbackDrainTimeout = 5 * time.Second

type boundSession struct {
	messages.Session
	binding                                  *binding
	lifecycleCtx                             context.Context
	receive                                  *messages.TypedBuffer[messages.StreamMessage]
	forwardStop, forwardDone                 chan struct{}
	forwardOnce                              sync.Once
	terminalObserved, gracefulCloseRequested atomic.Bool
}

func newBoundSession(session messages.Session, binding *binding, lifecycleCtx context.Context) *boundSession {
	bound := &boundSession{Session: session, binding: binding, lifecycleCtx: lifecycleCtx}
	bound.startReceiveForwarder(lifecycleCtx)
	return bound
}

func (s *boundSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *boundSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	if s == nil {
		return nil
	}
	if s.receive != nil {
		return s.receive
	}
	return s.Session.Receive()
}

func (s *boundSession) startReceiveForwarder(ctx context.Context) {
	if s == nil || s.Session == nil {
		return
	}
	source := s.Session.Receive()
	if source == nil {
		return
	}
	s.receive = messages.NewTypedBuffer[messages.StreamMessage](source.Cap())
	s.forwardStop = make(chan struct{})
	s.forwardDone = make(chan struct{})
	go s.forwardMessages(ctx, source)
}

func (s *boundSession) forwardMessages(ctx context.Context, source *messages.TypedBuffer[messages.StreamMessage]) {
	defer close(s.forwardDone)
	for {
		select {
		case msg, ok := <-source.Chan():
			if !ok || !s.forwardSessionMessage(ctx, msg) {
				return
			}
		case <-s.Session.Done():
			s.drainMessages(ctx, source)
			return
		case <-s.forwardStop:
			return
		}
	}
}

func (s *boundSession) drainMessages(ctx context.Context, source *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := source.Read()
		if !ok || !s.forwardSessionMessage(ctx, msg) {
			return
		}
	}
}

func (s *boundSession) forwardSessionMessage(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type == messages.StreamTypeSessionClose {
		s.terminalObserved.Store(true)
	}
	return s.receive != nil && s.receive.WriteWaitContextOrDone(ctx, s.forwardStop, msg).OK()
}

func (s *boundSession) stopReceiveForwarder() {
	if s == nil || s.forwardStop == nil {
		return
	}
	s.forwardOnce.Do(func() { close(s.forwardStop) })
	<-s.forwardDone
}

func (s *boundSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if outcome, blocked := s.admissionOutcome(msg); blocked {
		return outcome
	}
	outcome := messages.SendSessionWithOutcome(ctx, s.Session, msg)
	if !outcome.OK() {
		return outcome
	}
	if msg.Type == messages.StreamTypeSessionClose {
		s.gracefulCloseRequested.Store(true)
	}
	if err := s.applyPlaybackCommand(ctx, playbackCommand(msg.Type)); err != nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
	}
	return outcome
}

func (s *boundSession) admissionOutcome(msg messages.StreamMessage) (messages.SessionSendOutcome, bool) {
	if s == nil || s.Session == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}, true
	}
	closed, ok := s.Session.(interface{ SessionAdmissionClosed() bool })
	if !ok || !closed.SessionAdmissionClosed() {
		return messages.SessionSendOutcome{}, false
	}
	allowed, ok := s.Session.(interface {
		SessionAdmissionAllows(messages.StreamMessage) bool
	})
	if ok && !allowed.SessionAdmissionAllows(msg) {
		return messages.SessionSendOutcome{Status: messages.SessionSendCancelled, Err: context.Canceled}, true
	}
	return messages.SessionSendOutcome{}, false
}

func playbackCommand(kind messages.StreamMessageType) audio.PlaybackOperation {
	switch kind { //nolint:exhaustive // Only response control messages have playback side effects.
	case messages.StreamTypeResponseCancel:
		return audio.PlaybackDiscard
	case messages.StreamTypeResponseCreate:
		return audio.PlaybackResume
	default:
		return 0
	}
}

func (s *boundSession) applyPlaybackCommand(ctx context.Context, command audio.PlaybackOperation) error {
	if command == 0 || s.binding == nil || s.binding.sink == nil {
		return nil
	}
	return s.binding.sink.PlaybackCommand(ctx, command)
}

func (s *boundSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	create := messages.StreamMessage{Type: messages.StreamTypeResponseCreate}
	if outcome, blocked := s.admissionOutcome(create); blocked {
		return outcome
	}
	outcome := messages.RequestSessionResponse(ctx, s.Session)
	if !outcome.OK() {
		return outcome
	}
	if err := s.applyPlaybackCommand(ctx, audio.PlaybackResume); err != nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure, Err: err}
	}
	return outcome
}

func (s *boundSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

func (s *boundSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.Session == nil {
		return false
	}
	sender, ok := s.Session.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessage(ctx, msg)
}

func (s *boundSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.Session == nil {
		return false
	}
	sender, ok := s.Session.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *boundSession) SupportsCompleteMessages() bool {
	return s.supportsMessageCapability(true)
}

func (s *boundSession) SupportsCompleteMessagesWithoutResponse() bool {
	return s.supportsMessageCapability(false)
}

func (s *boundSession) supportsMessageCapability(withResponse bool) bool {
	if s == nil || s.Session == nil {
		return false
	}
	if capabilities, ok := s.Session.(interface {
		SupportsCompleteMessages() bool
		SupportsCompleteMessagesWithoutResponse() bool
	}); ok {
		if withResponse {
			return capabilities.SupportsCompleteMessages()
		}
		return capabilities.SupportsCompleteMessagesWithoutResponse()
	}
	if withResponse {
		_, ok := s.Session.(interface {
			SendMessage(context.Context, messages.Message) bool
		})
		return ok
	}
	_, ok := s.Session.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok
}

func (s *boundSession) FlushOutbound(ctx context.Context) error {
	if s == nil || s.Session == nil {
		return nil
	}
	flusher, ok := s.Session.(messages.SessionOutboundFlusher)
	if !ok {
		return nil
	}
	return flusher.FlushOutbound(ctx)
}

func (s *boundSession) InputDrops() int64 { return s.dropCount(true) }

func (s *boundSession) OutputDrops() int64 { return s.dropCount(false) }

func (s *boundSession) dropCount(input bool) int64 {
	if s == nil || s.Session == nil {
		return 0
	}
	counters, ok := s.Session.(messages.SessionDropCounters)
	if !ok {
		return 0
	}
	if input {
		return counters.InputDrops()
	}
	return counters.OutputDrops()
}

func (s *boundSession) Close() error {
	if s == nil {
		return nil
	}
	drainErr := s.drainIfTerminal()
	sessionErr := s.Session.Close()
	s.stopReceiveForwarder()
	return errors.Join(drainErr, sessionErr, s.binding.Close())
}

func (s *boundSession) drainIfTerminal() error {
	if !s.cleanTerminal() {
		return nil
	}
	drainParent := s.lifecycleCtx
	if drainParent == nil {
		drainParent = context.Background()
	}
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(drainParent), playbackDrainTimeout)
	defer cancel()
	return s.DrainPlayback(drainCtx)
}

func (s *boundSession) cleanTerminal() bool {
	return s.terminalObserved.Load() || s.gracefulCloseRequested.Load()
}

func (s *boundSession) DrainPlayback(ctx context.Context) error {
	if s == nil || s.binding == nil || s.binding.sink == nil {
		return nil
	}
	mediaOwner, ok := s.Session.(audio.MediaSession)
	if ok {
		media := mediaOwner.RTCMedia()
		if err := closeInboundMedia(media.Inbound); err != nil {
			return err
		}
	}
	return s.binding.sink.WaitForPump(ctx)
}

func closeInboundMedia(media audio.InboundMedia) error {
	if devicert.IsNilInboundMedia(media) {
		return nil
	}
	return media.Close()
}
