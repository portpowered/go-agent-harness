package live

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
	inner      messages.Session
	media      *sharedaudio.SessionMedia
	received   *messages.TypedBuffer[messages.StreamMessage]
	done       chan struct{}
	stop       chan struct{}
	forwarded  chan struct{}
	closeOnce  sync.Once
	closeErr   error
	errMu      sync.Mutex
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
	go s.forward(context.WithoutCancelContext(ctxForReplay(inner)))
	return s
}

func ctxForReplay(messages.Session) context.Context {
	return context.Background()
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
			for {
				msg, ok := source.Read()
				if !ok || !s.forwardMessage(ctx, msg) {
					return
				}
			}
		case <-s.stop:
			return
		case <-ctx.Done():
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
