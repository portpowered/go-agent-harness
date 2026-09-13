package service

import (
	"context"
	"errors"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/rtcsession"
	sharedaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/models"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport/rtc"
)

type inferencer struct {
	inner   messages.SessionInferencer
	runtime rtcsession.SessionRTCRuntime

	mu      sync.Mutex
	session *session
}

var _ rtcsession.Inferencer = (*inferencer)(nil)

func (i *inferencer) SetSessionAudioOutput(format models.AudioFormat, rate models.SampleRate) {
	if i == nil || i.inner == nil {
		return
	}
	if configurer, ok := i.inner.(interface {
		SetSessionAudioOutput(models.AudioFormat, models.SampleRate)
	}); ok {
		configurer.SetSessionAudioOutput(format, rate)
	}
}

func (i *inferencer) SetSessionAudioInput(format models.AudioFormat, rate models.SampleRate) {
	if i == nil || i.inner == nil {
		return
	}
	if configurer, ok := i.inner.(interface {
		SetSessionAudioInput(models.AudioFormat, models.SampleRate)
	}); ok {
		configurer.SetSessionAudioInput(format, rate)
	}
}

func (i *inferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.runtime == nil {
		return nil, wrapError("start", rtcsession.ErrSessionRTCRuntimeUnavailable)
	}
	if _, err := i.runtime.Start(ctx); err != nil {
		return nil, err
	}
	if i.inner == nil {
		closeErr := i.runtime.Close()
		return nil, errors.Join(wrapError("connect provider session", rtcsession.ErrSessionRTCRuntimeUnavailable), closeErr)
	}
	sessionValue, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, errors.Join(wrapError("connect provider session", err), i.runtime.Close())
	}
	if sessionValue == nil {
		closeErr := i.runtime.Close()
		return nil, errors.Join(wrapError("connect provider session", rtcsession.ErrSessionRTCRuntimeUnavailable), closeErr)
	}
	wrapped := &session{Session: sessionValue, runtime: i.runtime}
	i.mu.Lock()
	i.session = wrapped
	i.mu.Unlock()
	return wrapped, nil
}

func (i *inferencer) CloseSession() error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	sessionValue := i.session
	i.mu.Unlock()
	if sessionValue == nil {
		return nil
	}
	return sessionValue.Close()
}

type session struct {
	messages.Session
	runtime   rtcsession.SessionRTCRuntime
	closeOnce sync.Once
	closeErr  error
}

var _ messages.Session = (*session)(nil)
var _ messages.SessionSendOutcomeSender = (*session)(nil)
var _ messages.SessionResponseRequester = (*session)(nil)
var _ messages.SessionDropCounters = (*session)(nil)
var _ sharedaudio.MediaSession = (*session)(nil)

func (s *session) TerminalError() error {
	if s == nil || s.Session == nil {
		return nil
	}
	if source, ok := s.Session.(interface{ TerminalError() error }); ok {
		return source.TerminalError()
	}
	return nil
}

func (s *session) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	if s == nil || s.Session == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return messages.RequestSessionResponse(ctx, s.Session)
}

func (s *session) SupportsResponseRequests() bool {
	return s != nil && messages.SupportsSessionResponseRequests(s.Session)
}

func (s *session) SendMessage(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.Session == nil {
		return false
	}
	sender, ok := s.Session.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessage(ctx, msg)
}

func (s *session) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	if s == nil || s.Session == nil {
		return false
	}
	sender, ok := s.Session.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *session) SupportsCompleteMessages() bool {
	if s == nil {
		return false
	}
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *session) SupportsCompleteMessagesWithoutResponse() bool {
	if s == nil {
		return false
	}
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func completeMessageCapabilities(sessionValue messages.Session) (complete, withoutResponse bool) {
	if capabilities, ok := sessionValue.(interface {
		SupportsCompleteMessages() bool
		SupportsCompleteMessagesWithoutResponse() bool
	}); ok {
		return capabilities.SupportsCompleteMessages(), capabilities.SupportsCompleteMessagesWithoutResponse()
	}
	_, complete = sessionValue.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	_, withoutResponse = sessionValue.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return complete, withoutResponse
}

func (s *session) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	if s == nil || s.Session == nil {
		return messages.SessionSendOutcome{Status: messages.SessionSendTerminalFailure}
	}
	return messages.SendSessionWithOutcome(ctx, s.Session, msg)
}

func (s *session) InputDrops() int64 {
	if s == nil || s.Session == nil {
		return 0
	}
	counters, ok := s.Session.(messages.SessionDropCounters)
	if !ok {
		return 0
	}
	return counters.InputDrops()
}

func (s *session) OutputDrops() int64 {
	if s == nil || s.Session == nil {
		return 0
	}
	counters, ok := s.Session.(messages.SessionDropCounters)
	if !ok {
		return 0
	}
	return counters.OutputDrops()
}

func (s *session) RTCMedia() sharedaudio.MediaEndpoints {
	if s == nil || s.Session == nil {
		return sharedaudio.MediaEndpoints{}
	}
	media, ok := s.Session.(sharedaudio.MediaSession)
	if !ok {
		return sharedaudio.MediaEndpoints{}
	}
	return media.RTCMedia()
}

func (s *session) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		var sessionErr error
		if s.Session != nil {
			sessionErr = s.Session.Close()
		}
		s.closeErr = errors.Join(sessionErr, closeRuntime(s.runtime))
	})
	return s.closeErr
}

func closeRuntime(runtimeOwner rtcsession.SessionRTCRuntime) error {
	if runtimeOwner == nil {
		return nil
	}
	return runtimeOwner.Close()
}

type lazyDialer struct {
	runtime rtcsession.SessionRTCRuntime
}

var _ transport.Dialer = (*lazyDialer)(nil)

func (d *lazyDialer) Dial(endpoint string, headers map[string]string) (transport.Conn, error) {
	if d == nil || d.runtime == nil {
		return nil, wrapError("dial RTC data path", rtcsession.ErrSessionRTCRuntimeUnavailable)
	}
	dataPlane, err := d.runtime.Start(context.Background())
	if err != nil {
		return nil, wrapError("dial RTC data path", err)
	}
	if dataPlane == nil {
		return nil, wrapError("dial RTC data path", rtcsession.ErrSessionRTCDataPlaneUnavailable)
	}
	conn, err := dataPlane.Dial(endpoint, headers)
	if err != nil {
		return nil, wrapError("dial RTC data path", err)
	}
	if conn == nil {
		return nil, wrapError("dial RTC data path", rtc.ErrNilConnection)
	}
	return conn, nil
}
