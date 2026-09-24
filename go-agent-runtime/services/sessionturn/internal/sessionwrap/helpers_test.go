package sessionwrap

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const (
	bufferSize = 16
	waitBound  = 5 * time.Second
	sessionID  = "wrap-session"
)

// plainSession is a stream-only provider session.
type plainSession struct {
	mu        sync.Mutex
	sent      []messages.StreamMessage
	reject    bool
	recv      *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
	closes    int
	closeErr  error
}

func newPlainSession() *plainSession {
	return &plainSession{recv: messages.NewTypedBuffer[messages.StreamMessage](bufferSize), done: make(chan struct{})}
}

func (s *plainSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reject {
		return false
	}
	s.sent = append(s.sent, msg)
	return true
}

func (s *plainSession) sentSnapshot() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

func (s *plainSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }
func (s *plainSession) Done() <-chan struct{}                                  { return s.done }

func (s *plainSession) Close() error {
	s.mu.Lock()
	s.closes++
	s.mu.Unlock()
	s.closeOnce.Do(func() { close(s.done) })
	return s.closeErr
}

func (s *plainSession) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

// richSession adds every optional provider capability.
type richSession struct {
	*plainSession
	messagesSent  []messages.Message
	deferred      []messages.Message
	responses     int
	terminal      error
	mediaRequests int
}

func newRichSession() *richSession { return &richSession{plainSession: newPlainSession()} }

func (s *richSession) SendMessage(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messagesSent = append(s.messagesSent, msg)
	return !s.reject
}

func (s *richSession) SendMessageWithoutResponse(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deferred = append(s.deferred, msg)
	return !s.reject
}

func (s *richSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	s.responses++
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *richSession) TerminalError() error { return s.terminal }

func (s *richSession) RTCMedia() audio.MediaEndpoints {
	s.mediaRequests++
	return audio.MediaEndpoints{}
}

type fixedInferencer struct {
	session messages.Session
	err     error
	calls   int
}

func (i *fixedInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.calls++
	return i.session, i.err
}

func textDelta(text string) messages.StreamMessage {
	return messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(text)}
}

func textOf(msg messages.StreamMessage) string {
	value, ok := msg.Value.(*messages.TextDeltaValue)
	if !ok {
		return ""
	}
	return value.Content
}

func readMessage(session messages.Session) (messages.StreamMessage, error) {
	select {
	case msg := <-session.Receive().Chan():
		return msg, nil
	case <-time.After(waitBound):
		return messages.StreamMessage{}, errors.New("timed out waiting for relayed message")
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) / 2, nil }
