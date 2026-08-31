package services

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// sessionAudioNormalizerInferencer inserts the customer-facing assistant
// audio boundary before any session decorators that observe or store provider
// output. One instance is created for one session run, so its error and
// response state cannot cross a concurrent session.
type sessionAudioNormalizerInferencer struct {
	inner  messages.SessionInferencer
	record func(error)

	mu        sync.Mutex
	lastErr   error
	connected *sessionAudioNormalizerSession
}

func newSessionAudioNormalizerInferencer(inner messages.SessionInferencer, record func(error)) *sessionAudioNormalizerInferencer {
	return &sessionAudioNormalizerInferencer{inner: inner, record: record}
}

func (i *sessionAudioNormalizerInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if i == nil || i.inner == nil {
		return nil, errors.New("session audio normalizer has no inner inferencer")
	}
	session, err := i.inner.ConnectSession(ctx)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("session audio normalizer received a nil session")
	}
	wrapped := newSessionAudioNormalizerSession(ctx, session, i.recordErr)
	i.mu.Lock()
	i.connected = wrapped
	i.mu.Unlock()
	return wrapped, nil
}

func (i *sessionAudioNormalizerInferencer) wait() {
	if i == nil {
		return
	}
	i.mu.Lock()
	connected := i.connected
	i.mu.Unlock()
	if connected != nil {
		<-connected.done
	}
}

func (i *sessionAudioNormalizerInferencer) recordErr(err error) {
	if i == nil || err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	i.mu.Lock()
	if i.lastErr != nil {
		i.mu.Unlock()
		return
	}
	i.lastErr = err
	record := i.record
	i.mu.Unlock()
	if record != nil {
		record(err)
	}
}

func (i *sessionAudioNormalizerInferencer) err() error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.lastErr
}

type sessionAudioNormalizerSession struct {
	messages.Session
	ctx    context.Context
	record func(error)

	normalizer *audio.PCM16Normalizer
	receive    *messages.TypedBuffer[messages.StreamMessage]
	done       chan struct{}

	once      sync.Once
	closeOnce sync.Once
	closeErr  error
	errorOnce sync.Once

	stateMu       sync.Mutex
	segmentActive bool
	audioAccepted bool
	audioEnvelope messages.StreamMessage
}

func newSessionAudioNormalizerSession(ctx context.Context, inner messages.Session, record func(error)) *sessionAudioNormalizerSession {
	return newSessionAudioNormalizerSessionWithConfig(ctx, inner, record, audio.DefaultPCM16NormalizerConfig)
}

func newSessionAudioNormalizerSessionWithConfig(ctx context.Context, inner messages.Session, record func(error), config audio.PCM16NormalizerConfig) *sessionAudioNormalizerSession {
	normalizer, err := audio.NewPCM16NormalizerWithConfig(config)
	if err != nil {
		// The production profile is package-owned and validated. Tests may use
		// this constructor to make a deterministic rate explicit; a bad profile
		// is still a construction failure rather than a partially live stream.
		panic(err)
	}
	capacity := inner.Receive().Cap()
	if capacity < 1024 {
		capacity = 1024
	}
	session := &sessionAudioNormalizerSession{
		Session:       inner,
		ctx:           ctx,
		record:        record,
		normalizer:    normalizer,
		receive:       messages.NewTypedBuffer[messages.StreamMessage](capacity),
		done:          make(chan struct{}),
		audioAccepted: true,
	}
	go session.forward()
	return session
}

func (s *sessionAudioNormalizerSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *sessionAudioNormalizerSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	s.prepareOutbound(msg.Type)
	return messages.SendSessionWithOutcome(ctx, s.Session, msg)
}

func (s *sessionAudioNormalizerSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	s.prepareOutbound(messages.StreamTypeResponseCreate)
	return messages.RequestSessionResponse(ctx, s.Session)
}

func (s *sessionAudioNormalizerSession) SupportsResponseRequests() bool {
	return messages.SupportsSessionResponseRequests(s.Session)
}

func (s *sessionAudioNormalizerSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSender)
	return ok && sender.SendMessage(ctx, msg)
}

func (s *sessionAudioNormalizerSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	sender, ok := s.Session.(SessionImageMessageSenderWithoutResponse)
	return ok && sender.SendMessageWithoutResponse(ctx, msg)
}

func (s *sessionAudioNormalizerSession) SupportsCompleteMessages() bool {
	complete, _ := completeMessageCapabilities(s.Session)
	return complete
}

func (s *sessionAudioNormalizerSession) SupportsCompleteMessagesWithoutResponse() bool {
	_, withoutResponse := completeMessageCapabilities(s.Session)
	return withoutResponse
}

func (s *sessionAudioNormalizerSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *sessionAudioNormalizerSession) Done() <-chan struct{} {
	return s.done
}

func (s *sessionAudioNormalizerSession) rtcMedia() (RTCMediaEndpoints, bool) {
	return rtcMediaFromSession(s.Session)
}

func (s *sessionAudioNormalizerSession) TerminalError() error {
	return terminalSessionError(s.Session)
}

func (s *sessionAudioNormalizerSession) Close() error {
	if s == nil {
		return nil
	}
	// AgentLoop may close the decorated session as part of cancellation before
	// the forwarding goroutine gets a chance to observe the canceled context.
	// Flush the bounded tail first so accepted provider audio is not truncated;
	// the subsequent reset still makes this response terminal and independent.
	s.closeUnderlying()
	return s.closeErr
}

func (s *sessionAudioNormalizerSession) closeUnderlying() {
	s.closeOnce.Do(func() {
		s.flushTailForClose()
		s.resetDiscarded()
		s.closeErr = s.Session.Close()
	})
}

func (s *sessionAudioNormalizerSession) flushTailForClose() {
	tail, err := s.finishSegment(messages.StreamMessage{})
	if err != nil {
		if s.record != nil {
			s.record(err)
		}
		return
	}
	for _, msg := range tail {
		if !s.emit(msg) {
			return
		}
	}
}

func (s *sessionAudioNormalizerSession) forward() {
	defer s.once.Do(func() { close(s.done) })
	input := s.Session.Receive()
	for {
		select {
		case msg := <-input.Chan():
			if !s.forwardMessage(msg) {
				return
			}
		case <-s.Session.Done():
			if !s.drain(input) {
				return
			}
			if !s.forwardTail(messages.StreamMessage{}) {
				return
			}
			return
		case <-s.ctx.Done():
			// Preserve samples already accepted from the provider, including a
			// bounded tail, before resetting the response-local state. The
			// cancellation still stops future provider output; it does not
			// silently truncate audio that was already admitted to the sink.
			_ = s.forwardTail(messages.StreamMessage{})
			s.resetDiscarded()
			s.closeUnderlying()
			return
		}
	}
}

func (s *sessionAudioNormalizerSession) drain(input *messages.TypedBuffer[messages.StreamMessage]) bool {
	for {
		msg, ok := input.Read()
		if !ok {
			return true
		}
		if !s.forwardMessage(msg) {
			return false
		}
	}
}

func (s *sessionAudioNormalizerSession) forwardMessage(msg messages.StreamMessage) bool {
	messagesToForward, err := s.normalizeMessage(msg)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.reportError(err)
		} else {
			s.resetDiscarded()
			s.closeUnderlying()
		}
		return false
	}
	for _, output := range messagesToForward {
		if !s.emit(output) {
			return false
		}
	}
	return true
}

func (s *sessionAudioNormalizerSession) normalizeMessage(msg messages.StreamMessage) ([]messages.StreamMessage, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.normalizeMessageLocked(msg)
}

func (s *sessionAudioNormalizerSession) normalizeMessageLocked(msg messages.StreamMessage) ([]messages.StreamMessage, error) {
	assistant := assistantAudioDelta(msg)
	switch msg.Type {
	case messages.StreamTypeSessionOpen:
		// A SESSION.OPEN starts a new provider connection. Any prior state is
		// stale and must not be reused by the new response stream.
		s.resetDiscardedLocked()
		s.audioAccepted = true
		return []messages.StreamMessage{msg}, nil
	case messages.StreamTypeMessageStart:
		// A response-created/message-start boundary reopens audio after an
		// outbound RESPONSE.CANCEL. This also keeps a late provider delta from
		// being reattributed to the replacement response.
		if assistant {
			s.audioAccepted = true
		}
		return []messages.StreamMessage{msg}, nil
	case messages.StreamTypeAudioStart:
		if !assistant {
			return []messages.StreamMessage{msg}, nil
		}
		if !s.audioAccepted {
			return nil, nil
		}
		prefix, err := s.finishSegmentLocked(msg)
		if err != nil {
			return nil, err
		}
		if err := s.normalizer.Reset(); err != nil {
			return nil, err
		}
		s.segmentActive = true
		return append(prefix, msg), nil
	case messages.StreamTypeAudioDelta:
		if !assistant {
			return []messages.StreamMessage{msg}, nil
		}
		if !s.audioAccepted {
			return nil, nil
		}
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || value == nil {
			return nil, fmt.Errorf("AUDIO.DELTA has unexpected value %T", msg.Value)
		}
		if !s.segmentActive {
			if err := s.normalizer.Reset(); err != nil {
				return nil, err
			}
			s.segmentActive = true
		}
		normalized, err := s.normalizer.ProcessPCM16(s.ctx, value.Content)
		if err != nil {
			return nil, fmt.Errorf("normalize assistant AUDIO.DELTA: %w", err)
		}
		s.audioEnvelope = msg
		msg.Value = messages.NewAudioDeltaValueWithMediaType(normalized, value.MediaType)
		return []messages.StreamMessage{msg}, nil
	case messages.StreamTypeAudioEnd, messages.StreamTypeMessageEnd, messages.StreamTypeSessionClose:
		if !assistant && msg.Type == messages.StreamTypeAudioEnd {
			return []messages.StreamMessage{msg}, nil
		}
		if !s.audioAccepted {
			s.resetDiscardedLocked()
			return []messages.StreamMessage{msg}, nil
		}
		prefix, err := s.finishSegmentLocked(msg)
		if err != nil {
			return nil, err
		}
		return append(prefix, msg), nil
	case messages.StreamTypeError:
		// A provider error terminates the current response. Do not flush a
		// partial tail after an error; cancellation/reset owns that state.
		s.resetDiscardedLocked()
		return []messages.StreamMessage{msg}, nil
	default:
		return []messages.StreamMessage{msg}, nil
	}
}

func (s *sessionAudioNormalizerSession) finishSegment(boundary messages.StreamMessage) ([]messages.StreamMessage, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.finishSegmentLocked(boundary)
}

func (s *sessionAudioNormalizerSession) finishSegmentLocked(boundary messages.StreamMessage) ([]messages.StreamMessage, error) {
	if !s.segmentActive {
		return nil, nil
	}
	tail, err := s.normalizer.FinishPCM16(context.Background())
	if err != nil {
		return nil, fmt.Errorf("finish assistant audio normalization: %w", err)
	}
	envelope := s.audioEnvelope
	if envelope.Type == "" {
		envelope = boundary
	}
	s.segmentActive = false
	s.audioEnvelope = messages.StreamMessage{}
	if resetErr := s.normalizer.Reset(); resetErr != nil {
		return nil, resetErr
	}
	if len(tail) == 0 {
		return nil, nil
	}
	mediaType := ""
	if value, ok := envelope.Value.(*messages.AudioDeltaValue); ok && value != nil {
		mediaType = value.MediaType
	}
	envelope.Type = messages.StreamTypeAudioDelta
	envelope.Value = messages.NewAudioDeltaValueWithMediaType(tail, mediaType)
	return []messages.StreamMessage{envelope}, nil
}

func (s *sessionAudioNormalizerSession) forwardTail(boundary messages.StreamMessage) bool {
	tail, err := s.finishSegment(boundary)
	if err != nil {
		s.reportError(err)
		return false
	}
	for _, msg := range tail {
		if !s.emit(msg) {
			return false
		}
	}
	return true
}

func (s *sessionAudioNormalizerSession) emit(msg messages.StreamMessage) bool {
	for {
		if s.receive.Write(context.Background(), msg) {
			return true
		}
		select {
		case <-s.ctx.Done():
			return false
		case <-time.After(time.Millisecond):
		}
	}
}

func (s *sessionAudioNormalizerSession) resetDiscarded() {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.resetDiscardedLocked()
}

func (s *sessionAudioNormalizerSession) resetDiscardedLocked() {
	_ = s.normalizer.Reset()
	s.segmentActive = false
	s.audioEnvelope = messages.StreamMessage{}
}

func (s *sessionAudioNormalizerSession) prepareOutbound(msgType messages.StreamMessageType) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	switch msgType {
	case messages.StreamTypeResponseCancel:
		// A response cancellation invalidates all queued audio for that
		// response. Late provider deltas remain suppressed until the next
		// response boundary, so they cannot be mixed into its replacement.
		s.resetDiscardedLocked()
		s.audioAccepted = false
	case messages.StreamTypeResponseCreate:
		s.resetDiscardedLocked()
		s.audioAccepted = true
	}
}

func (s *sessionAudioNormalizerSession) reportError(err error) {
	if s == nil || err == nil {
		return
	}
	s.errorOnce.Do(func() {
		s.resetDiscarded()
		if s.record != nil {
			s.record(err)
		}
		s.closeUnderlying()
	})
}

var _ messages.SessionInferencer = (*sessionAudioNormalizerInferencer)(nil)
var _ messages.Session = (*sessionAudioNormalizerSession)(nil)
var _ messages.SessionSendOutcomeSender = (*sessionAudioNormalizerSession)(nil)
