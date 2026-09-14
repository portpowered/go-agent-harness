package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionupdate"
)

type fakeSession struct {
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}

	closeOnce sync.Once
	doneOnce  sync.Once
	closeErr  error

	mu         sync.Mutex
	sent       []messages.StreamMessage
	complete   []messages.Message
	deferred   []messages.Message
	closed     bool
	send       func(context.Context, messages.StreamMessage) messages.SessionSendOutcome
	response   messages.SessionSendOutcome
	terminal   error
	capability bool
}

func newFakeSession(capacity int) *fakeSession {
	return &fakeSession{
		receive:  messages.NewTypedBuffer[messages.StreamMessage](capacity),
		done:     make(chan struct{}),
		response: messages.SessionSendOutcome{Status: messages.SessionSendSucceeded},
	}
}

func (s *fakeSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return s.SendWithOutcome(ctx, msg).OK()
}

func (s *fakeSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	if s.send != nil {
		return s.send(ctx, msg)
	}
	return messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
}

func (s *fakeSession) RequestResponse(context.Context) messages.SessionSendOutcome {
	return s.response
}

func (s *fakeSession) SupportsResponseRequests() bool { return s.capability }

func (s *fakeSession) SendMessage(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	s.complete = append(s.complete, msg)
	s.mu.Unlock()
	return s.capability
}

func (s *fakeSession) SendMessageWithoutResponse(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	s.deferred = append(s.deferred, msg)
	s.mu.Unlock()
	return s.capability
}

func (s *fakeSession) SupportsCompleteMessages() bool { return s.capability }

func (s *fakeSession) SupportsCompleteMessagesWithoutResponse() bool { return s.capability }

func (s *fakeSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }

func (s *fakeSession) Done() <-chan struct{} { return s.done }

func (s *fakeSession) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.doneOnce.Do(func() { close(s.done) })
	})
	return s.closeErr
}

func (s *fakeSession) TerminalError() error { return s.terminal }

func (s *fakeSession) emit(msg messages.StreamMessage) {
	if !s.receive.Write(context.Background(), msg) {
		panic("fake session receive buffer unexpectedly full")
	}
}

func (s *fakeSession) finish() { s.doneOnce.Do(func() { close(s.done) }) }

func (s *fakeSession) snapshotSent() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

func (s *fakeSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

type fakeInferencer struct {
	session *fakeSession
}

func (i fakeInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return i.session, nil
}

func readMessage(t *testing.T, buffer *messages.TypedBuffer[messages.StreamMessage]) messages.StreamMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, err := buffer.ReadContext(ctx)
	if err != nil {
		t.Fatalf("read decorated session message: %v", err)
	}
	return msg
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session Done")
	}
}

func TestDecorateSessionSendsOneUpdateAndDrainsAfterDone(t *testing.T) {
	fake := newFakeSession(8)
	service := New()
	rawSchema := json.RawMessage(`{"type":"object"}`)
	tools := []messages.ToolDefinition{
		{
			Name: "zeta",
			Parameters: []messages.ToolParameter{
				{Name: "zeta-param"},
				{Name: "alpha-param"},
			},
			ParameterSchema: rawSchema,
		},
		{Name: "alpha"},
	}
	wrapped := service.DecorateSession(context.Background(), fake, sessionupdate.Config{
		Instructions:    "immutable instructions",
		ToolDefinitions: tools,
	})
	if wrapped.Receive().Cap() != 8 {
		t.Fatalf("decorated receive capacity = %d, want 8", wrapped.Receive().Cap())
	}

	tools[0].Parameters[0].Name = "mutated"
	rawSchema[0] = 'x'
	fake.emit(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("pre-open")})
	fake.emit(messages.StreamMessage{Type: messages.StreamTypeSessionOpen})
	fake.emit(messages.StreamMessage{Type: messages.StreamTypeSessionCreated})
	fake.emit(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("tail")})
	fake.finish()

	if got := readMessage(t, wrapped.Receive()); got.Type != messages.StreamTypeTextDelta {
		t.Fatalf("pre-open event = %s, want TEXT.DELTA", got.Type)
	}
	if got := readMessage(t, wrapped.Receive()); got.Type != messages.StreamTypeSessionOpen {
		t.Fatalf("first lifecycle event = %s, want SESSION.OPEN", got.Type)
	}
	if got := readMessage(t, wrapped.Receive()); got.Type != messages.StreamTypeSessionCreated {
		t.Fatalf("second lifecycle event = %s, want SESSION.CREATED", got.Type)
	}
	if got := readMessage(t, wrapped.Receive()); got.Type != messages.StreamTypeTextDelta {
		t.Fatalf("drained event = %s, want TEXT.DELTA", got.Type)
	}
	waitDone(t, wrapped.Done())

	sent := fake.snapshotSent()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeSessionUpdate {
		t.Fatalf("sent events = %#v, want exactly one SESSION.UPDATE", sent)
	}
	update, ok := sent[0].Value.(*messages.SessionUpdateValue)
	if !ok {
		t.Fatalf("update value type = %T, want *SessionUpdateValue", sent[0].Value)
	}
	if update.Instructions != "immutable instructions" {
		t.Fatalf("instructions = %q", update.Instructions)
	}
	if len(update.Tools) != 2 || update.Tools[0].Name != "alpha" || update.Tools[1].Name != "zeta" {
		t.Fatalf("canonical tool order = %#v", update.Tools)
	}
	if len(update.Tools[1].Parameters) != 2 || update.Tools[1].Parameters[0].Name != "alpha-param" || update.Tools[1].Parameters[1].Name != "zeta-param" {
		t.Fatalf("canonical parameter order = %#v", update.Tools[1].Parameters)
	}
	if string(update.Tools[1].ParameterSchema) != `{"type":"object"}` {
		t.Fatalf("parameter schema was not cloned: %s", update.Tools[1].ParameterSchema)
	}
}

func TestDrainAfterDoneForwardsAlreadyBufferedDelta(t *testing.T) {
	fake := newFakeSession(1)
	wrapper := &decoratedSession{
		inner:   fake,
		config:  sessionupdate.Config{},
		receive: messages.NewTypedBuffer[messages.StreamMessage](1),
		ctx:     context.Background(),
		done:    make(chan struct{}),
	}
	final := messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("final delta")}
	fake.emit(final)
	fake.finish()

	wrapper.drainAfterDone(fake.receive)
	got, ok := wrapper.receive.Read()
	if !ok || got.Type != messages.StreamTypeTextDelta {
		t.Fatalf("drained message = %#v, ok=%v; want final TEXT.DELTA", got, ok)
	}
}

func TestDecorateSnapshotsInferencerConfiguration(t *testing.T) {
	fake := newFakeSession(4)
	toolDefinitions := []messages.ToolDefinition{{Name: "original"}}
	config := sessionupdate.Config{Instructions: "before", ToolDefinitions: toolDefinitions}
	decorated := New().Decorate(fakeInferencer{session: fake}, config)
	config.Instructions = "after"
	toolDefinitions[0].Name = "mutated"

	session, err := decorated.ConnectSession(context.Background())
	if err != nil {
		t.Fatalf("ConnectSession: %v", err)
	}
	fake.emit(messages.StreamMessage{Type: messages.StreamTypeSessionOpen})
	if got := readMessage(t, session.Receive()); got.Type != messages.StreamTypeSessionOpen {
		t.Fatalf("forwarded event = %s, want SESSION.OPEN", got.Type)
	}
	sent := fake.snapshotSent()
	if len(sent) != 1 {
		t.Fatalf("sent event count = %d, want 1", len(sent))
	}
	update, ok := sent[0].Value.(*messages.SessionUpdateValue)
	if !ok {
		t.Fatalf("update value type = %T, want *SessionUpdateValue", sent[0].Value)
	}
	if update.Instructions != "before" || update.Tools[0].Name != "original" {
		t.Fatalf("inferencer did not snapshot config: %#v", update)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("session Close: %v", err)
	}
}

func TestDecoratedSessionForwardsOptionalCapabilitiesAndParentCancellation(t *testing.T) {
	fake := newFakeSession(4)
	fake.capability = true
	fake.terminal = errors.New("provider terminal")
	parent, cancel := context.WithCancel(context.Background())
	wrapped := New().DecorateSession(parent, fake, sessionupdate.Config{})

	if !wrapped.SupportsResponseRequests() || wrapped.RequestResponse(context.Background()).Status != messages.SessionSendSucceeded {
		t.Fatal("response capability was not forwarded")
	}
	if outcome := wrapped.SendWithOutcome(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta}); !outcome.OK() {
		t.Fatalf("send outcome was not forwarded: %#v", outcome)
	}
	if !wrapped.SupportsCompleteMessages() || !wrapped.SupportsCompleteMessagesWithoutResponse() {
		t.Fatal("complete-message capabilities were not forwarded")
	}
	if !wrapped.SendMessage(context.Background(), messages.Message{}) || !wrapped.SendMessageWithoutResponse(context.Background(), messages.Message{}) {
		t.Fatal("complete-message senders were not forwarded")
	}
	if !errors.Is(wrapped.TerminalError(), fake.terminal) {
		t.Fatal("terminal error was not forwarded")
	}

	cancel()
	waitDone(t, wrapped.Done())
	waitDone(t, fake.Done())
	if !fake.isClosed() {
		t.Fatal("parent cancellation did not close the inner session")
	}
}

func TestConfigurationFailurePublishesTypedTerminalErrorAndStopsForwarding(t *testing.T) {
	fake := newFakeSession(4)
	sendErr := errors.New("update rejected")
	closeErr := errors.New("close failed")
	fake.closeErr = closeErr
	fake.send = func(context.Context, messages.StreamMessage) messages.SessionSendOutcome {
		return messages.SessionSendOutcome{Status: messages.SessionSendBufferFull, Err: sendErr}
	}
	wrapped := New().DecorateSession(context.Background(), fake, sessionupdate.Config{Instructions: "must apply"})
	fake.emit(messages.StreamMessage{Type: messages.StreamTypeSessionOpen})

	failure := readMessage(t, wrapped.Receive())
	if failure.Type != messages.StreamTypeError {
		t.Fatalf("failure event type = %s, want ERROR", failure.Type)
	}
	errorValue, ok := failure.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("failure value type = %T, want *ErrorValue", failure.Value)
	}
	var typed *sessionupdate.UpdateSendError
	if !errors.As(errorValue.Err, &typed) {
		t.Fatalf("failure error %v does not preserve UpdateSendError", errorValue.Err)
	}
	if typed.Status != messages.SessionSendBufferFull || !errors.Is(errorValue.Err, sendErr) || !errors.Is(errorValue.Err, closeErr) {
		t.Fatalf("typed failure = %#v, err = %v", typed, errorValue.Err)
	}
	if errorValue.Message == "" || !contains(errorValue.Message, "send session instructions") {
		t.Fatalf("failure message = %q", errorValue.Message)
	}
	waitDone(t, wrapped.Done())
	if !fake.isClosed() {
		t.Fatal("configuration failure did not close the inner session")
	}

	fake.emit(messages.StreamMessage{Type: messages.StreamTypeSessionCreated})
	if got := wrapped.Receive().Len(); got != 0 {
		t.Fatalf("post-failure receive length = %d, want no false lifecycle event", got)
	}
	if err := wrapped.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close error = %v, want close failure", err)
	}
}

func contains(value, substring string) bool {
	for len(substring) <= len(value) {
		if value[:len(substring)] == substring {
			return true
		}
		value = value[1:]
	}
	return substring == ""
}
