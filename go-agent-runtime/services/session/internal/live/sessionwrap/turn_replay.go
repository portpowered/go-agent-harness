package sessionwrap

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/codec"
)

const turnReplayMessageCapacity = 128

type inferencerAdapter struct {
	inner      messages.SessionInferencer
	sampleRate int
	continuous bool
	mu         sync.Mutex
	connected  *mediaSession
}

func WrapTurnReplay(inner messages.SessionInferencer, sampleRate int, continuous bool) messages.SessionInferencer {
	return &inferencerAdapter{inner: inner, sampleRate: sampleRate, continuous: continuous}
}

func (i *inferencerAdapter) ConnectSession(ctx context.Context) (messages.Session, error) {
	if ctx == nil {
		return nil, errors.New("turn replay context is required")
	}
	inner, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	media := newMediaSession(ctx, inner, i.sampleRate, i.continuous)
	i.mu.Lock()
	i.connected = media
	i.mu.Unlock()
	return media, nil
}

func (i *inferencerAdapter) FlushCapture() error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	media := i.connected
	i.mu.Unlock()
	if media == nil {
		return nil
	}
	return media.FlushCapture()
}

type mediaSession struct {
	inner        messages.Session
	media        *sharedaudio.SessionMedia
	received     *messages.TypedBuffer[messages.StreamMessage]
	done         chan struct{}
	stop         chan struct{}
	forwarded    chan struct{}
	mediaFlushed bool
	closeOnce    sync.Once
	closeErr     error
	errMu        sync.Mutex
	terminalErr  error
}

func newMediaSession(ctx context.Context, inner messages.Session, sampleRate int, continuous bool) *mediaSession {
	if sampleRate <= 0 {
		sampleRate = sharedaudio.DefaultSessionMediaSampleRate
	}
	media := sharedaudio.NewSessionMediaAtRateWithOptions(nil, sampleRate, sharedaudio.MediaSessionOptions{
		InboundContinuous: continuous,
	})
	s := &mediaSession{
		inner:     inner,
		media:     media,
		received:  messages.NewTypedBuffer[messages.StreamMessage](turnReplayMessageCapacity),
		done:      make(chan struct{}),
		stop:      make(chan struct{}),
		forwarded: make(chan struct{}),
	}
	go s.forward(ctx)
	return s
}

func (s *mediaSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg).OK()
}

func (s *mediaSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s.inner, msg)
}

func (s *mediaSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.received
}

func (s *mediaSession) Done() <-chan struct{} { return s.done }

func (s *mediaSession) RTCMedia() sharedaudio.MediaEndpoints {
	if s == nil || s.media == nil {
		return sharedaudio.MediaEndpoints{}
	}
	return sharedaudio.MediaEndpoints{Inbound: s.media.Endpoints().Inbound}
}

func (s *mediaSession) TerminalError() error {
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
		if err := terminal.TerminalError(); err != nil {
			return fmt.Errorf("turn replay provider session: %w", err)
		}
	}
	if terminal, ok := s.inner.(interface{ Err() error }); ok {
		if err := terminal.Err(); err != nil {
			return fmt.Errorf("turn replay provider session: %w", err)
		}
	}
	return nil
}

func (s *mediaSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		close(s.stop)
		var innerErr error
		if s.inner != nil {
			innerErr = s.inner.Close()
		}
		<-s.forwarded
		var mediaErr error
		if s.media != nil {
			mediaErr = s.media.Close()
		}
		s.closeErr = errors.Join(mediaErr, innerErr)
	})
	return s.closeErr
}

func (s *mediaSession) FlushCapture() error {
	if s == nil || s.media == nil {
		return nil
	}
	if err := s.media.FlushInbound(); err != nil {
		if errors.Is(err, sharedaudio.ErrSessionMediaClosed) || errors.Is(err, sharedaudio.ErrClosed) {
			return nil
		}
		return err
	}
	s.mediaFlushed = true
	return nil
}

func (s *mediaSession) forward(ctx context.Context) {
	defer close(s.forwarded)
	defer close(s.done)
	// Keep media open until the live service drains frames behind the terminal message.
	defer func() {
		if !s.mediaFlushed {
			if err := s.media.FlushInbound(); err != nil {
				s.fail(fmt.Errorf("flush turn replay media after stream end: %w", err))
			}
		}
		if err := s.TerminalError(); err != nil {
			s.media.FailInbound(err)
		}
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

func (s *mediaSession) drain(ctx context.Context, source *messages.TypedBuffer[messages.StreamMessage]) {
	for {
		msg, ok := source.Read()
		if !ok || !s.forwardMessage(ctx, msg) {
			return
		}
	}
}

func (s *mediaSession) forwardMessage(ctx context.Context, msg messages.StreamMessage) bool {
	if err := s.processMediaMessage(msg); err != nil {
		s.fail(err)
	}
	outcome := s.received.WriteWaitContextOrDone(ctx, s.stop, msg)
	if !outcome.OK() {
		if outcome.Err != nil {
			s.fail(outcome.Err)
		}
		return false
	}
	return true
}

func (s *mediaSession) processMediaMessage(msg messages.StreamMessage) error {
	if msg.Type == messages.StreamTypeAudioDelta {
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || value == nil {
			return errors.New("turn replay audio delta has no PCM payload")
		}
		samples, err := codec.DecodePCM16(value.Content)
		if err != nil {
			return fmt.Errorf("decode turn replay PCM16: %w", err)
		}
		s.media.StartInboundResponse(sharedaudio.PlaybackResponse{ResponseID: msg.ResponseID})
		if err := s.media.PushInbound(samples); err != nil {
			return fmt.Errorf("queue turn replay PCM16: %w", err)
		}
		s.mediaFlushed = false
		return nil
	}
	if msg.Type == messages.StreamTypeAudioEnd || msg.Type == messages.StreamTypeSessionClose {
		if err := s.media.FlushInbound(); err != nil {
			return fmt.Errorf("flush turn replay PCM16: %w", err)
		}
		s.mediaFlushed = true
	}
	return nil
}

func (s *mediaSession) fail(err error) {
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

func CaptureMediaEndpoints(session messages.Session, providerMedia sharedaudio.MediaSession, continuous bool) sharedaudio.MediaEndpoints {
	if !continuous {
		return providerMedia.RTCMedia()
	}
	if configurable, ok := session.(sharedaudio.ConfigurableMediaSession); ok {
		return configurable.RTCMediaWithOptions(sharedaudio.MediaSessionOptions{InboundContinuous: true})
	}
	return providerMedia.RTCMedia()
}
