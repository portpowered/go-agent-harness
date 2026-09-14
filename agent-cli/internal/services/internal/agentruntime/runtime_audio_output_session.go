package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const runtimeAudioOutputBufferSize = 256

type runtimeAudioOutputInferencer struct {
	inner      messages.SessionInferencer
	output     *runtimeAudioOutput
	wirePrompt string
	seedValue  string
	mu         sync.Mutex
	lastErr    error
	connected  *runtimeAudioOutputSession
}

func newRuntimeAudioOutputInferencer(inner messages.SessionInferencer, output *runtimeAudioOutput, wirePrompt string, seedValue string) *runtimeAudioOutputInferencer {
	return &runtimeAudioOutputInferencer{
		inner:      inner,
		output:     output,
		wirePrompt: wirePrompt,
		seedValue:  seedValue,
	}
}

func (i *runtimeAudioOutputInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session, err := i.inner.ConnectSession(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	wrapped := newRuntimeAudioOutputSession(ctx, session, i.output, i.recordErr, i.wirePrompt, i.seedValue)
	i.mu.Lock()
	i.connected = wrapped
	i.mu.Unlock()
	return wrapped, nil
}

func (i *runtimeAudioOutputInferencer) wait() {
	i.mu.Lock()
	connected := i.connected
	i.mu.Unlock()
	if connected != nil {
		// Close completes retained draining and captures provider shutdown errors.
		i.recordErr(connected.Close())
	}
}

// recordErr joins stream and provider-close failures from the same session.
func (i *runtimeAudioOutputInferencer) recordErr(err error) {
	if err == nil {
		return
	}
	i.mu.Lock()
	i.lastErr = errors.Join(i.lastErr, err)
	i.mu.Unlock()
}

func (i *runtimeAudioOutputInferencer) err() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastErr
}

type runtimeAudioOutputSession struct {
	messages.Session
	ctx            context.Context
	output         *runtimeAudioOutput
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

func newRuntimeAudioOutputSession(ctx context.Context, inner messages.Session, output *runtimeAudioOutput, record func(error), wirePrompt string, seedValue string) *runtimeAudioOutputSession {
	s := &runtimeAudioOutputSession{
		Session:        inner,
		ctx:            ctx,
		output:         output,
		record:         record,
		wirePrompt:     wirePrompt,
		seedValue:      seedValue,
		receive:        messages.NewTypedBuffer[messages.StreamMessage](runtimeAudioOutputBufferSize),
		done:           make(chan struct{}),
		drainStarted:   make(chan struct{}),
		closeRequested: make(chan struct{}),
		innerCloseDone: make(chan struct{}),
	}
	go s.forward(ctx)
	return s
}
func (s *runtimeAudioOutputSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if s.replaceSeed(msg) {
		msg.Value = messages.NewTextDeltaValue(s.seedValue)
	}
	return s.Session.Send(ctx, msg)
}
func (s *runtimeAudioOutputSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.RequestSessionResponse(ctx, s.Session)
}
func (s *runtimeAudioOutputSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}
func (s *runtimeAudioOutputSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}
func (s *runtimeAudioOutputSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}
func (s *runtimeAudioOutputSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}
func (s *runtimeAudioOutputSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}
func (s *runtimeAudioOutputSession) replaceSeed(msg messages.StreamMessage) bool {
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
func (s *runtimeAudioOutputSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *runtimeAudioOutputSession) Done() <-chan struct{} { return s.done }

func (s *runtimeAudioOutputSession) rtcMedia() (audio.MediaEndpoints, bool) {
	return sessionMediaFromSession(s.Session)
}

func (s *runtimeAudioOutputSession) RTCMedia() audio.MediaEndpoints {
	media, _ := s.rtcMedia()
	return media
}

func (s *runtimeAudioOutputSession) TerminalError() error {
	return terminalSessionError(s.Session)
}

func (s *runtimeAudioOutputSession) forward(ctx context.Context) {
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
func (s *runtimeAudioOutputSession) forwardNext(ctx context.Context, input *messages.TypedBuffer[messages.StreamMessage]) bool {
	select {
	case msg := <-input.Chan():
		retainingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionStragglerDrainWallSafety)
		defer cancel()
		return s.forwardMessageWithContext(retainingCtx, msg, ctx.Err() != nil)
	case <-s.Session.Done():
		if ctx.Err() == nil {
			s.drain(input, ctx, false)
		}
	case <-s.closeRequested:
	case <-ctx.Done():
	}
	return false
}
func (s *runtimeAudioOutputSession) drain(input *messages.TypedBuffer[messages.StreamMessage], ctx context.Context, retaining bool) bool {
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
func (s *runtimeAudioOutputSession) drainAfterCancellation(ctx context.Context, input *messages.TypedBuffer[messages.StreamMessage]) {
	close(s.drainStarted)
	retainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionStragglerDrainWallSafety)
	defer cancel()
	terminal := time.NewTimer(sessionStragglerDrainWallSafety)
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

func (s *runtimeAudioOutputSession) drainAfterInnerClose(input *messages.TypedBuffer[messages.StreamMessage], ctx context.Context) {
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
		case <-time.After(sessionStragglerDrainQuietPeriod):
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *runtimeAudioOutputSession) closeInner() {
	s.innerCloseOnce.Do(func() { go s.finishInnerClose() })
}

func (s *runtimeAudioOutputSession) finishInnerClose() {
	s.closeErr = s.Session.Close()
	close(s.innerCloseDone)
}

func (s *runtimeAudioOutputSession) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeRequested)
		<-s.done
		<-s.innerCloseDone
	})
	return s.closeErr
}

func (s *runtimeAudioOutputSession) forwardMessageWithContext(ctx context.Context, msg messages.StreamMessage, retaining bool) bool {
	if !s.forwardAudioMessage(ctx, msg) || !s.forwardAudioEnd(ctx, msg) {
		return false
	}
	return s.retainForwardMessage(ctx, msg, retaining)
}

func (s *runtimeAudioOutputSession) forwardAudioMessage(ctx context.Context, msg messages.StreamMessage) bool {
	if msg.Type != messages.StreamTypeAudioDelta || !assistantAudioDelta(msg) {
		return true
	}
	value, ok := msg.Value.(*messages.AudioDeltaValue)
	if !ok {
		s.record(fmt.Errorf("AUDIO.DELTA has unexpected value %T", msg.Value))
		s.closeInner()
		return false
	}
	if err := s.output.writeDelta(ctx, value.Content, msg); err != nil {
		s.record(err)
		s.closeInner()
		return false
	}
	return true
}

func (s *runtimeAudioOutputSession) forwardAudioEnd(ctx context.Context, msg messages.StreamMessage) bool {
	if s.output.output == nil || msg.Type != messages.StreamTypeMessageEnd || !assistantAudioDelta(msg) {
		return true
	}
	if err := s.output.output.Write(ctx, audio.PCMFrame{EndOfResponse: true}); err != nil {
		s.record(err)
		s.closeInner()
		return false
	}
	return true
}

func (s *runtimeAudioOutputSession) retainForwardMessage(ctx context.Context, msg messages.StreamMessage, retaining bool) bool {
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
