package live

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session/internal/live/eventcodec"
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

func (i *liveInvocation) bindPlaybackController() {
	if i == nil || i.endpoints.Inbound == nil {
		return
	}
	var controller sharedaudio.PlaybackController
	if provider, ok := i.ports.Playback.(devices.PlaybackControllerProvider); ok {
		controller = provider.PlaybackController()
	}
	if controller == nil && i.options.Request.ReplayPlan != nil {
		controller = replayVirtualPlaybackController{}
	}
	if controlled, ok := i.endpoints.Inbound.(sharedaudio.PlaybackControlledInbound); ok && controller != nil {
		controlled.SetPlaybackController(controller)
	}
}

func (h *handle) observeTerminalValue(msg messages.StreamMessage) {
	value := eventcodec.TerminalValue(msg)
	if value == nil {
		return
	}
	h.mu.Lock()
	if msg.Type == messages.StreamTypeSessionClose {
		if !h.providerCloseObserved || h.terminalValue == nil {
			h.terminalValue = value
		}
		h.providerCloseObserved = true
		h.terminalOnce.Do(func() { close(h.terminalObserved) })
	} else if !h.providerCloseObserved {
		h.terminalValue = value
	}
	h.mu.Unlock()
}

const finiteTurnFrameBudget = 48_000

type sessionAudioInputSender interface {
	sendAudioInput(context.Context, []byte, messages.SessionAudioInputPolicy) error
}

type loopAudioOutbound struct {
	sender  sessionAudioInputSender
	onAdmit func(sharedaudio.PCMFrame)
}

func (i *liveInvocation) captureOutbound() sharedaudio.OutboundMedia {
	if i == nil {
		return nil
	}
	sender, ok := i.handle.(sessionAudioInputSender)
	if !ok || sender == nil {
		return i.endpoints.Outbound
	}
	return &loopAudioOutbound{sender: sender, onAdmit: i.captureAdmitted}
}

// sendAudioInput keeps local capture on the model runner's ordered ingress;
// peer media remains owned by the room graph.
func (h *handle) sendAudioInput(ctx context.Context, pcm []byte, policy messages.SessionAudioInputPolicy) error {
	if h == nil {
		return errors.New("live audio input handle is unavailable")
	}
	h.mu.Lock()
	loop := h.loop
	started, closed := h.started, h.closed
	providerClosed := h.providerCloseObserved
	h.mu.Unlock()
	if !started || loop == nil {
		return session.ErrLiveNotStarted
	}
	if closed {
		return session.ErrLiveClosed
	}
	if providerClosed {
		if err := h.scheduledAudioError(); err != nil {
			return err
		}
		return session.ErrLiveClosed
	}
	return loop.SendAudioInputWithPolicy(ctx, pcm, policy)
}

func (h *handle) recordCapturedAudio(frame sharedaudio.PCMFrame) {
	if h == nil {
		return
	}
	if h.runtimeTrace != nil {
		h.runtimeTrace.CapturedAudio(frame)
	}
	if observer := h.observationPort(); observer != nil {
		observer.QueueFrame(frame)
	}
}

func (o *loopAudioOutbound) WriteFrame(ctx context.Context, frame sharedaudio.PCMFrame) error {
	if o == nil || o.sender == nil {
		return errors.New("ordered audio input sender is unavailable")
	}
	if len(frame.Samples) == 0 {
		return sharedaudio.ErrSessionMediaEmptyFrame
	}
	if err := o.sender.sendAudioInput(ctx, codec.EncodePCM16(frame.Samples), messages.SessionAudioInputPolicyDefault); err != nil {
		return fmt.Errorf("admit ordered audio input: %w", err)
	}
	if o.onAdmit != nil {
		o.onAdmit(frame)
	}
	return nil
}

func (*loopAudioOutbound) Close() error { return nil }

func (i *liveInvocation) captureAdmitted(frame sharedaudio.PCMFrame) {
	if i == nil || i.handle == nil {
		return
	}
	if observer, ok := i.handle.(interface{ recordCapturedAudio(sharedaudio.PCMFrame) }); ok {
		observer.recordCapturedAudio(frame)
	}
}

func newFiniteTurnOutbound(target sharedaudio.OutboundMedia) (*sharedaudio.FrameAccumulator, error) {
	return sharedaudio.NewFrameAccumulator(target, finiteTurnFrameBudget)
}

func (i *liveInvocation) handleResponseIsActive() bool {
	if i == nil || i.handle == nil {
		return false
	}
	if snapshot, ok := i.handle.(interface{ responseIsActive() bool }); ok {
		return snapshot.responseIsActive()
	}
	return false
}

func openingMessageRequestsResponse(request session.LiveRequest) bool {
	if len(request.OpeningContentParts) > 0 {
		return request.OpeningMessageResponse != session.LiveOpeningMessageQueued
	}
	return request.OpeningPromptPresent || request.OpeningPrompt != ""
}

func (h *handle) finishMessageObservation(msg messages.StreamMessage) {
	if msg.Type == messages.StreamTypeSessionClose && !h.deferProviderClose() {
		h.stopGracefully()
	}
}

func finalizeRecorder(recorder session.LiveRecorder, ctx context.Context, runErr error) error {
	if recorder == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("live recorder finalization context is required")
	}
	return recorder.Finalize(context.WithoutCancel(ctx), runErr)
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
