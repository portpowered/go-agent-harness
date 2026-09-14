package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/audiooutput"
	goaudio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	bufferCapacity      = 256
	stragglerQuiet      = 25 * time.Millisecond
	stragglerWallSafety = 250 * time.Millisecond
)

type inferencer struct {
	inner   messages.SessionInferencer
	output  audiooutput.Output
	options audiooutput.SessionOptions

	mu        sync.Mutex
	lastErr   error
	connected *outputSession
}

func newInferencer(inner messages.SessionInferencer, output audiooutput.Output, options audiooutput.SessionOptions) audiooutput.SessionInferencer {
	return &inferencer{inner: inner, output: output, options: options}
}

func (i *inferencer) ConnectSession(ctx context.Context) (messages.Session, error) { //nolint:contextcheck // provider connection must outlive caller cancellation until the decorated session closes.
	if err := validateSessionDependencies(i.inner, i.output); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := i.inner.ConnectSession(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("audio output session is nil")
	}
	if i.options.AdaptSession != nil {
		session = i.options.AdaptSession(session)
		if session == nil {
			return nil, errors.New("audio output session adapter returned nil")
		}
	}
	base := newBaseOutputSession(ctx, session, i.output, i.recordErr, i.options.WirePrompt, i.options.SeedValue)
	i.mu.Lock()
	i.connected = base
	i.mu.Unlock()
	return decorateOutputSession(base, session), nil
}

func (i *inferencer) Wait() {
	i.mu.Lock()
	connected := i.connected
	i.mu.Unlock()
	if connected != nil {
		i.recordErr(connected.Close())
	}
}

func (i *inferencer) recordErr(err error) {
	if err == nil {
		return
	}
	i.mu.Lock()
	i.lastErr = errors.Join(i.lastErr, err)
	i.mu.Unlock()
}

func (i *inferencer) Err() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastErr
}

type outputSession struct {
	messages.Session
	ctx            context.Context
	output         audiooutput.Output
	record         func(error)
	wirePrompt     string
	seedValue      string
	receive        *messages.TypedBuffer[messages.StreamMessage]
	done           chan struct{}
	drainStarted   chan struct{}
	closeRequested chan struct{}
	closeOnce      sync.Once
	innerCloseOnce sync.Once
	innerCloseDone chan struct{}
	closeErr       error
	seedMu         sync.Mutex
	seedSent       bool
}

func newBaseOutputSession(ctx context.Context, inner messages.Session, output audiooutput.Output, record func(error), wirePrompt, seedValue string) *outputSession {
	s := &outputSession{
		Session:        inner,
		ctx:            ctx,
		output:         output,
		record:         record,
		wirePrompt:     wirePrompt,
		seedValue:      seedValue,
		receive:        messages.NewTypedBuffer[messages.StreamMessage](bufferCapacity),
		done:           make(chan struct{}),
		drainStarted:   make(chan struct{}),
		closeRequested: make(chan struct{}),
		innerCloseDone: make(chan struct{}),
	}
	go s.forward() //nolint:contextcheck // forwarding retains the bounded provider tail after caller cancellation.
	return s
}

// mediaOutputSession is only returned when the input exposes media. Keeping
// the method on a separate type preserves optional RTC capability discovery.
type mediaOutputSession struct{ *outputSession }

func decorateOutputSession(base *outputSession, inner messages.Session) messages.Session {
	if _, ok := inner.(goaudio.MediaSession); ok {
		return &mediaOutputSession{outputSession: base}
	}
	return base
}

func (s *outputSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if s.replaceSeed(msg) {
		msg.Value = messages.NewTextDeltaValue(s.seedValue)
	}
	return s.Session.Send(ctx, msg)
}

func (s *outputSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.Session)
}

func (s *outputSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

func (s *outputSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessage(ctx, msg)
}

func (s *outputSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *outputSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *outputSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func (s *outputSession) TerminalError() error {
	if source, ok := s.Session.(interface{ TerminalError() error }); ok {
		return source.TerminalError()
	}
	return nil
}

func (s *mediaOutputSession) RTCMedia() goaudio.MediaEndpoints {
	if source, ok := s.Session.(goaudio.MediaSession); ok {
		return source.RTCMedia()
	}
	return goaudio.MediaEndpoints{}
}

func (s *outputSession) replaceSeed(msg messages.StreamMessage) bool {
	if s.wirePrompt == "" || msg.Type != messages.StreamTypeTextDelta {
		return false
	}
	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok || value.Content != s.wirePrompt {
		return false
	}
	s.seedMu.Lock()
	defer s.seedMu.Unlock()
	if s.seedSent {
		return false
	}
	s.seedSent = true
	return true
}

func (s *outputSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }

func (s *outputSession) Done() <-chan struct{} { return s.done }

func (s *outputSession) forward() {
	defer func() {
		s.closeInner()
		close(s.done)
	}()
	input := s.Session.Receive()
	for {
		if s.ctx.Err() != nil {
			s.drainAfterCancellation(input)
			return
		}
		if s.forwardNext(input) {
			continue
		}
		if s.ctx.Err() != nil {
			s.drainAfterCancellation(input)
		} else {
			select {
			case <-s.closeRequested:
				s.drainAfterCancellation(input)
			default:
			}
		}
		return
	}
}

func (s *outputSession) forwardNext(input *messages.TypedBuffer[messages.StreamMessage]) bool {
	select {
	case msg := <-input.Chan():
		retainingCtx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), stragglerWallSafety)
		defer cancel()
		return s.forwardMessageWithContext(retainingCtx, msg, s.ctx.Err() != nil)
	case <-s.Session.Done():
		if s.ctx.Err() == nil {
			s.drain(input, s.ctx, false)
		}
	case <-s.closeRequested:
	case <-s.ctx.Done():
	}
	return false
}

func (s *outputSession) drain(input *messages.TypedBuffer[messages.StreamMessage], ctx context.Context, retaining bool) bool {
	for {
		msg, ok := input.Read()
		if !ok {
			return true
		}
		if !s.forwardMessageWithContext(ctx, msg, retaining) {
			return false
		}
	}
}

func (s *outputSession) drainAfterCancellation(input *messages.TypedBuffer[messages.StreamMessage]) {
	close(s.drainStarted)
	retainCtx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), stragglerWallSafety)
	defer cancel()
	terminal := time.NewTimer(stragglerWallSafety)
	defer terminal.Stop()
	for {
		select {
		case msg := <-input.Chan():
			if !s.forwardMessageWithContext(retainCtx, msg, true) {
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

func (s *outputSession) drainAfterInnerClose(input *messages.TypedBuffer[messages.StreamMessage], ctx context.Context) {
	select {
	case <-s.innerCloseDone:
	case <-ctx.Done():
		return
	}
	for {
		select {
		case msg := <-input.Chan():
			if !s.forwardMessageWithContext(ctx, msg, true) {
				return
			}
		case <-time.After(stragglerQuiet):
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *outputSession) closeInner() {
	s.innerCloseOnce.Do(func() { go s.finishInnerClose() })
}

func (s *outputSession) finishInnerClose() {
	s.closeErr = s.Session.Close()
	close(s.innerCloseDone)
}

func (s *outputSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeRequested)
		<-s.done
		<-s.innerCloseDone
	})
	return s.closeErr
}

func (s *outputSession) forwardMessageWithContext(ctx context.Context, msg messages.StreamMessage, retaining bool) bool {
	if msg.Type == messages.StreamTypeAudioDelta && assistantAudioDelta(msg) {
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok {
			s.record(fmtUnexpectedAudioValue(msg.Value))
			s.closeInner()
			return false
		}
		if err := s.output.WriteDelta(ctx, value.Content, msg); err != nil {
			s.record(err)
			s.closeInner()
			return false
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

func completeMessageCapabilities(session messages.Session) (complete, withoutResponse bool) {
	if capabilities, ok := session.(interface {
		SupportsCompleteMessages() bool
		SupportsCompleteMessagesWithoutResponse() bool
	}); ok {
		return capabilities.SupportsCompleteMessages(), capabilities.SupportsCompleteMessagesWithoutResponse()
	}
	_, complete = session.(interface {
		SendMessage(context.Context, messages.Message) bool
	})
	_, withoutResponse = session.(interface {
		SendMessageWithoutResponse(context.Context, messages.Message) bool
	})
	return complete, withoutResponse
}

func assistantAudioDelta(msg messages.StreamMessage) bool {
	return msg.Role == "" || msg.Role == messages.RoleAssistant
}

func fmtUnexpectedAudioValue(value messages.StreamMessageValue) error {
	return errors.New("AUDIO.DELTA has unexpected value " + valueType(value))
}

func valueType(value any) string {
	if value == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", value)
}
