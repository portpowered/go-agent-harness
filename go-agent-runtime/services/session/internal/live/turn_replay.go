package live

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/agentloop"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

type turnReplayMediaInferencer struct {
	inner      messages.SessionInferencer
	sampleRate int
	continuous bool
}

func (i turnReplayMediaInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	return newTurnReplayMediaSession(inner, i.sampleRate, i.continuous), nil
}

type turnReplayMediaSession struct {
	inner       messages.Session
	media       *sharedaudio.SessionMedia
	received    *messages.TypedBuffer[messages.StreamMessage]
	done        chan struct{}
	stop        chan struct{}
	forwarded   chan struct{}
	closeOnce   sync.Once
	closeErr    error
	errMu       sync.Mutex
	terminalErr error
}

func newTurnReplayMediaSession(inner messages.Session, sampleRate int, continuous bool) *turnReplayMediaSession {
	if sampleRate <= 0 {
		sampleRate = sharedaudio.DefaultSessionMediaSampleRate
	}
	media := sharedaudio.NewSessionMediaAtRateWithOptions(nil, sampleRate, sharedaudio.MediaSessionOptions{
		InboundContinuous: continuous,
	})
	s := &turnReplayMediaSession{
		inner:     inner,
		media:     media,
		received:  messages.NewTypedBuffer[messages.StreamMessage](128),
		done:      make(chan struct{}),
		stop:      make(chan struct{}),
		forwarded: make(chan struct{}),
	}
	go s.forward(context.Background())
	return s
}

func (s *turnReplayMediaSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg).OK()
}

func (s *turnReplayMediaSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
}

func (s *turnReplayMediaSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.received
}

func (s *turnReplayMediaSession) Done() <-chan struct{} { return s.done }

func (s *turnReplayMediaSession) RTCMedia() sharedaudio.MediaEndpoints {
	if s == nil || s.media == nil {
		return sharedaudio.MediaEndpoints{}
	}
	return sharedaudio.MediaEndpoints{Inbound: s.media.Endpoints().Inbound}
}

func (s *turnReplayMediaSession) TerminalError() error {
	if s == nil {
		return nil
	}
	s.errMu.Lock()
	err := s.terminalErr
	s.errMu.Unlock()
	if err != nil {
		return err
	}
	if terminal, ok := s.inner.(interface{ TerminalError() error }); ok {
		return terminal.TerminalError()
	}
	if terminal, ok := s.inner.(interface{ Err() error }); ok {
		return terminal.Err()
	}
	return nil
}

func (s *turnReplayMediaSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		close(s.stop)
		if s.media != nil {
			_ = s.media.Close()
		}
		if s.inner != nil {
			s.closeErr = s.inner.Close()
		}
		<-s.forwarded
	})
	return s.closeErr
}

func (s *turnReplayMediaSession) forward(ctx context.Context) {
	defer close(s.forwarded)
	defer close(s.done)
	defer func() {
		if err := s.media.FlushInbound(); err != nil {
			s.fail(err)
		}
		if err := s.TerminalError(); err != nil {
			s.media.FailInbound(err)
		}
		_ = s.media.Close()
	}()

	source := s.inner.Receive()
	if source == nil {
		s.fail(errors.New("turn replay session has no message stream"))
		return
	}
	for {
		select {
		case msg := <-source.Chan():
			if !s.forwardMessage(ctx, msg) {
				return
			}
		case <-s.inner.Done():
			s.drain(ctx, source)
			return
		case <-s.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *turnReplayMediaSession) drain(ctx context.Context, source *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := source.Read()
		if !ok || !s.forwardMessage(ctx, msg) {
			return
		}
	}
}

func (s *turnReplayMediaSession) forwardMessage(ctx context.Context, msg messages.StreamMessage) bool {
	switch msg.Type {
	case messages.StreamTypeAudioDelta:
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || value == nil {
			s.fail(errors.New("turn replay audio delta has no PCM payload"))
			break
		}
		samples, err := codec.DecodePCM16(value.Content)
		if err != nil {
			s.fail(fmt.Errorf("decode turn replay PCM16: %w", err))
			break
		}
		if err := s.media.PushInbound(samples); err != nil {
			s.fail(fmt.Errorf("queue turn replay PCM16: %w", err))
		}
	case messages.StreamTypeAudioEnd:
		if err := s.media.FlushInbound(); err != nil {
			s.fail(fmt.Errorf("flush turn replay PCM16: %w", err))
		}
	}

	outcome := s.received.WriteWaitContextOrDone(ctx, s.stop, msg)
	if !outcome.OK() {
		if outcome.Err != nil {
			s.fail(outcome.Err)
		}
		return false
	}
	if msg.Type == messages.StreamTypeSessionClose {
		_ = s.media.Close()
	}
	return true
}

func (s *turnReplayMediaSession) fail(err error) {
	if s == nil || err == nil {
		return
	}
	s.errMu.Lock()
	if s.terminalErr == nil {
		s.terminalErr = err
	}
	s.errMu.Unlock()
	s.media.FailInbound(err)
}

func (h *handle) liveControlLoop() (*agentloop.AgentLoop, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.started || h.loop == nil {
		return nil, session.ErrLiveNotStarted
	}
	if h.closed {
		return nil, session.ErrLiveClosed
	}
	return h.loop, nil
}

func (h *handle) sendLiveControl(ctx context.Context, loop *agentloop.AgentLoop, control session.LiveControl) error {
	ackID, ack, err := h.media.RegisterAck()
	if err != nil {
		return err
	}
	event, err := liveControlEvent(control)
	if err != nil {
		h.media.AbortAck(ackID)
		return err
	}
	if control.Kind == session.LiveControlAudioCommit && h.request.Replay.Kind == session.LiveReplayKindTurn {
		// Session-message captures preserve the historical type-only end
		// marker; realtime provider controls carry their negotiated value.
		event.Value = nil
	}
	event.ActorProvidedID = ackID
	if err := loop.SendSessionEvent(ctx, event); err != nil {
		h.media.AbortAck(ackID)
		return err
	}
	select {
	case accepted := <-ack:
		if !accepted {
			return fmt.Errorf("live provider rejected control %q", control.Kind)
		}
		return nil
	case <-ctx.Done():
		h.media.CancelAck(ackID)
		return ctx.Err()
	}
}

func liveControlEvent(control session.LiveControl) (messages.StreamMessage, error) {
	switch control.Kind {
	case session.LiveControlText:
		return messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(control.Text)}, nil
	case session.LiveControlAudioCommit:
		return messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})}, nil
	case session.LiveControlResponseCancel:
		return messages.StreamMessage{Type: messages.StreamTypeResponseCancel, Value: messages.NewResponseCancelValue()}, nil
	case session.LiveControlResponseCreate:
		return messages.StreamMessage{Type: messages.StreamTypeResponseCreate, Value: messages.NewResponseCreateValue()}, nil
	case session.LiveControlClose:
		return messages.StreamMessage{}, errors.New("close control is handled by the live lifecycle")
	default:
		return messages.StreamMessage{}, fmt.Errorf("unsupported live control %q", control.Kind)
	}
}
func (i *liveInvocation) wait() error {
	waitResult := make(chan error, 1)
	go func() { waitResult <- i.handle.Wait() }()
	events := i.handle.Events()
	var sinkErr error
	for {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if i.options.Events != nil && sinkErr == nil {
				if err := i.options.Events.Publish(i.ctx, event); err != nil {
					sinkErr = fmt.Errorf("publish live event: %w", err)
					i.handle.Cancel(sinkErr)
				}
			}
		case waitErr := <-waitResult:
			drainLiveEvents(events, i.options.Events, i.ctx, &sinkErr, i.handle)
			return i.finish(waitErr, sinkErr)
		}
	}
}

func (i *liveInvocation) finish(waitErr, sinkErr error) error {
	if i == nil {
		return errors.New("live invocation is unavailable")
	}
	var playbackErr error
	if shouldDrainPlayback(i.ctx, waitErr) {
		playbackErr = drainPlayback(i.ctx, i.ports.Playback, i.options.PlaybackDrainTimeout)
	}
	if i.stopPumps != nil {
		i.stopPumps()
	}
	var deviceErr error
	if i.device != nil {
		deviceErr = i.device.Close()
	}
	var pumpErr error
	for count := 0; count < i.count; count++ {
		candidate := <-i.pumps
		if !isExpectedMediaPumpError(candidate) {
			pumpErr = errors.Join(pumpErr, candidate)
		}
	}
	handleErr := i.handle.Close()
	result := errors.Join(waitErr, sinkErr, pumpErr, playbackErr, deviceErr, handleErr)
	return errors.Join(result, finalizeRecorder(i.options.Recorder, i.ctx, result))
}

func requestedTerminalError(s finishState) error {
	err := s.requestedErr
	if s.toolResultErr != nil && contextOnlyOrNil(s.requestedErr) {
		err = errors.Join(err, s.toolResultErr)
	}
	if s.providerErr != nil && !isContextTermination(s.providerErr) && !errors.Is(err, s.providerErr) {
		err = errors.Join(err, fmt.Errorf("session error: %w", s.providerErr))
	}
	return err
}

func (i *liveInvocation) closeAfterStartError(startErr error) error {
	if i == nil {
		return startErr
	}
	var deviceErr error
	if i.device != nil {
		deviceErr = i.device.Close()
	}
	handleErr := i.handle.Close()
	result := errors.Join(startErr, deviceErr, handleErr)
	return errors.Join(result, finalizeRecorder(i.options.Recorder, i.ctx, result))
}

// waitForResponseBoundary includes partial assistant terminals produced by
// barge-in cancellation.
func (h *handle) waitForResponseBoundary(ctx context.Context, target int) error {
	if h == nil {
		return context.Canceled
	}
	if ctx == nil {
		return errors.New("response boundary context is required")
	}
	for {
		h.mu.Lock()
		ready := h.observedResponseTerminals >= target && !h.responseActive && !h.responsePending
		terminalWake, responseWake := h.responseTerminalWake, h.replayResponseWake
		h.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-terminalWake:
		case <-responseWake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func normalizeAudioTurnAdmission(value session.AudioTurnAdmission) (session.AudioTurnAdmission, error) {
	if value == "" {
		return session.AudioTurnAdmissionCompletionGated, nil
	}
	switch value {
	case session.AudioTurnAdmissionCompletionGated, session.AudioTurnAdmissionBarge:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported audio turn admission %q", value)
	}
}

func openingMessageRequestsResponse(request session.LiveRequest) bool {
	if len(request.OpeningContentParts) > 0 {
		return request.OpeningMessageResponse != session.LiveOpeningMessageQueued
	}
	return request.OpeningPromptPresent || request.OpeningPrompt != ""
}
