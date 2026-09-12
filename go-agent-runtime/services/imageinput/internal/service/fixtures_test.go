package service

import (
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"sync"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func testPNG(t testingT) []byte {
	t.Helper()
	var buffer bytesBuffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buffer.Bytes()
}

func testJPEG(t testingT) []byte {
	t.Helper()
	var buffer bytesBuffer
	if err := jpeg.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	return buffer.Bytes()
}

type testingT interface {
	Helper()
	Fatalf(string, ...any)
}

type bytesBuffer struct{ data []byte }

func (buffer *bytesBuffer) Write(data []byte) (int, error) {
	buffer.data = append(buffer.data, data...)
	return len(data), nil
}

func (buffer *bytesBuffer) Bytes() []byte { return append([]byte(nil), buffer.data...) }

var _ io.Writer = (*bytesBuffer)(nil)

type contentLoader struct {
	parts map[string]messages.ContentPart
	errs  map[string]error
	mu    sync.Mutex
	calls []string
}

func (loader *contentLoader) Load(_ context.Context, path string) (messages.ContentPart, error) {
	loader.mu.Lock()
	loader.calls = append(loader.calls, path)
	loader.mu.Unlock()
	if err := loader.errs[path]; err != nil {
		return nil, err
	}
	return loader.parts[path], nil
}

func (loader *contentLoader) callCount() int {
	loader.mu.Lock()
	defer loader.mu.Unlock()
	return len(loader.calls)
}

type fakeSession struct {
	mu            sync.Mutex
	messages      []messages.Message
	events        []messages.StreamMessage
	done          chan struct{}
	closeOnce     sync.Once
	complete      bool
	without       bool
	sendOK        bool
	withoutSendOK bool
	closeErr      error
}

func newFakeSession() *fakeSession {
	return &fakeSession{done: make(chan struct{}), sendOK: true, withoutSendOK: true}
}

func (session *fakeSession) Send(ctx context.Context, event messages.StreamMessage) bool {
	if err := contextError(ctx); err != nil {
		return false
	}
	session.mu.Lock()
	session.events = append(session.events, event)
	session.mu.Unlock()
	return true
}

func (session *fakeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return messages.NewTypedBuffer[messages.StreamMessage](4)
}

func (session *fakeSession) Done() <-chan struct{} { return session.done }

func (session *fakeSession) Close() error {
	session.closeOnce.Do(func() { close(session.done) })
	return session.closeErr
}

func (session *fakeSession) SendMessage(ctx context.Context, message messages.Message) bool {
	if contextError(ctx) != nil || !session.sendOK {
		return false
	}
	session.mu.Lock()
	session.messages = append(session.messages, cloneMessage(message))
	session.mu.Unlock()
	return true
}

func (session *fakeSession) SendMessageWithoutResponse(ctx context.Context, message messages.Message) bool {
	if contextError(ctx) != nil || !session.withoutSendOK {
		return false
	}
	session.mu.Lock()
	session.messages = append(session.messages, cloneMessage(message))
	session.mu.Unlock()
	return true
}

func (session *fakeSession) SupportsCompleteMessages() bool { return session.complete }

func (session *fakeSession) SupportsCompleteMessagesWithoutResponse() bool { return session.without }

func (session *fakeSession) messagesCopy() []messages.Message {
	session.mu.Lock()
	defer session.mu.Unlock()
	return append([]messages.Message(nil), session.messages...)
}

func (session *fakeSession) eventsCopy() []messages.StreamMessage {
	session.mu.Lock()
	defer session.mu.Unlock()
	return append([]messages.StreamMessage(nil), session.events...)
}

type fakeInferencer struct{ session *fakeSession }

func (inferencer *fakeInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return inferencer.session, nil
}

type errorSession struct {
	*fakeSession
	err error
}

func (session *errorSession) SendMessageWithError(context.Context, messages.Message) error {
	return session.err
}

func cloneMessage(message messages.Message) messages.Message {
	clone := message
	clone.ContentParts = make([]messages.ContentPart, len(message.ContentParts))
	for index, part := range message.ContentParts {
		switch value := part.(type) {
		case messages.TextPart:
			clone.ContentParts[index] = value
		case messages.ImagePart:
			value.Bytes = cloneBytes(value.Bytes)
			clone.ContentParts[index] = value
		default:
			clone.ContentParts[index] = part
		}
	}
	return clone
}
