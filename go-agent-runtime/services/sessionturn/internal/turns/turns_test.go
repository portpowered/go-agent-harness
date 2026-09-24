package turns

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
)

type testInferencer struct {
	session  messages.Session
	err      error
	connects atomic.Int32
}

type blockingInferencer struct {
	started chan struct{}
	release chan struct{}
	session messages.Session
}

func (i *blockingInferencer) ConnectSession(context.Context) (messages.Session, error) {
	close(i.started)
	<-i.release
	return i.session, nil
}

func (i *testInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.connects.Add(1)
	return i.session, i.err
}

type testSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	closeCall atomic.Int32

	mu       sync.Mutex
	sent     []messages.StreamMessage
	sendFunc func(messages.StreamMessage) bool
}

type heldCloseSession struct {
	*testSession
	started     chan struct{}
	release     chan struct{}
	once        sync.Once
	closeResult error
}

func (s *heldCloseSession) Close() error {
	s.once.Do(func() {
		close(s.started)
		<-s.release
		s.closeResult = s.testSession.Close()
	})
	return s.closeResult
}

func newTestSession() *testSession {
	return &testSession{receive: messages.NewTypedBuffer[messages.StreamMessage](32), done: make(chan struct{})}
}

func (s *testSession) Send(_ context.Context, message messages.StreamMessage) bool {
	s.mu.Lock()
	s.sent = append(s.sent, cloneStreamMessage(message))
	fn := s.sendFunc
	s.mu.Unlock()
	if fn != nil {
		return fn(message)
	}
	return true
}

func (s *testSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.receive }
func (s *testSession) Done() <-chan struct{}                                  { return s.done }
func (s *testSession) Close() error {
	s.closeOnce.Do(func() {
		s.closeCall.Add(1)
		close(s.done)
	})
	return s.closeErr
}

func (s *testSession) push(message messages.StreamMessage) {
	if !s.receive.Write(context.Background(), message) {
		panic("test session receive buffer is full")
	}
}

func (s *testSession) sentMessages() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

func cloneStreamMessage(message messages.StreamMessage) messages.StreamMessage {
	clone := message
	switch value := message.Value.(type) {
	case *messages.AudioDeltaValue:
		copyValue := *value
		copyValue.Content = append([]byte(nil), value.Content...)
		clone.Value = &copyValue
	}
	return clone
}

func textResponse(session *testSession, text string) {
	session.push(messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue(text)})
	session.push(messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
}

func newRespondingService(session *testSession, sink sessionturn.TurnEventSink) (*Service, *testInferencer) {
	inferencer := &testInferencer{session: session}
	return New(Dependencies{SessionInferencer: inferencer, EventSink: sink}), inferencer
}

func textInput(text string) sessionturn.TurnInput { return sessionturn.TurnInput{Text: text} }

func audioInput(audio []byte, mediaType string) sessionturn.TurnInput {
	return sessionturn.TurnInput{Audio: append([]byte(nil), audio...), MediaType: mediaType}
}

func TestServiceFiveTurnsReuseSessionAndCopySnapshots(t *testing.T) {
	provider := newTestSession()
	fact := "fact: the marigold key is hidden under stone seven"
	failure := errors.New("provider response failed")
	installFactResponder(provider, fact, &failure)
	var eventsMu sync.Mutex
	var events []sessionturn.TurnEvent
	var service *Service
	var inferencer *testInferencer
	service, inferencer = newRespondingService(provider, func(event sessionturn.TurnEvent) {
		_, _ = service.ActiveTurn()
		_ = service.History()
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	})
	wantFailure := failure
	if _, err := service.RunTurn(context.Background(), textInput("failed"), sessionturn.TurnDirectionUser, 1, 2); !errors.Is(err, wantFailure) {
		t.Fatalf("failed turn error = %v, want provider cause", err)
	}

	audio := []byte{1, 2, 3}
	inputs := []sessionturn.TurnInput{textInput(fact), audioInput(audio, "audio/pcm"), audioInput([]byte{4, 5, 6}, "audio/pcm"), textInput("Please recall the fact."), textInput("End the scripted conversation.")}
	runFiveTurns(t, service, inputs)
	audio[0] = 99
	history := service.History()
	if inferencer.connects.Load() != 1 || len(history) != 5 || service.NextTurnIndex() != 6 {
		t.Fatalf("connections/history/next = %d/%d/%d", inferencer.connects.Load(), len(history), service.NextTurnIndex())
	}
	if string(history[1].Input.Audio) != string([]byte{1, 2, 3}) || !strings.Contains(history[3].Response.TextContent(), "marigold key") {
		t.Fatalf("history lost copied input or response: %#v", history)
	}
	history[1].Input.Audio[0] = 88
	if service.History()[1].Input.Audio[0] != 1 {
		t.Fatal("history returned an aliased audio input")
	}
	eventsMu.Lock()
	observedEvents := append([]sessionturn.TurnEvent(nil), events...)
	eventsMu.Unlock()
	assertFiveTurnEvents(t, observedEvents)
	if err := service.Close(); err != nil || provider.closeCall.Load() != 1 {
		t.Fatalf("close = %v, calls=%d", err, provider.closeCall.Load())
	}
}

func installFactResponder(provider *testSession, fact string, failure *error) {
	provider.sendFunc = func(message messages.StreamMessage) bool {
		if message.Type != messages.StreamTypeTextDelta && message.Type != messages.StreamTypeAudioDelta {
			return true
		}
		if *failure != nil {
			provider.push(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(*failure)})
			*failure = nil
			return true
		}
		input := ""
		if value, ok := message.Value.(*messages.TextDeltaValue); ok {
			input = value.Content
		}
		switch {
		case strings.HasPrefix(input, "fact:"):
			textResponse(provider, "stored")
		case strings.Contains(input, "recall"):
			textResponse(provider, "I remember "+fact)
		default:
			textResponse(provider, "acknowledged")
		}
		return true
	}
}

func runFiveTurns(t *testing.T, service *Service, inputs []sessionturn.TurnInput) {
	t.Helper()
	for i, input := range inputs {
		turn, err := service.RunTurn(context.Background(), input, sessionturn.TurnDirectionUser, uint64(2*i+3), uint64(2*i+4))
		if err != nil || turn.Index != uint64(i+1) {
			t.Fatalf("turn %d = %#v, err=%v", i+1, turn, err)
		}
	}
}

func assertFiveTurnEvents(t *testing.T, events []sessionturn.TurnEvent) {
	t.Helper()
	if len(events) != 11 || events[0].Type != sessionturn.TurnEventStart || events[0].Index != 1 {
		t.Fatalf("events = %#v, want failed start plus five pairs", events)
	}
	for i, event := range events[1:] {
		if event.Index != uint64(i/2+1) || event.Direction != sessionturn.TurnDirectionUser {
			t.Fatalf("event %d identity = %#v", i, event)
		}
		if i%2 == 0 && event.Type != sessionturn.TurnEventStart {
			t.Fatalf("event %d = %#v, want start", i, event)
		}
		if i%2 == 1 && event.Type != sessionturn.TurnEventEnd {
			t.Fatalf("event %d = %#v, want end", i, event)
		}
	}
}

func TestServiceInvalidTransitionsPreserveState(t *testing.T) {
	service := New(Dependencies{})
	started, err := service.StartTurn(textInput("input"), sessionturn.TurnDirectionUser, 10)
	if err != nil || started.Index != 1 {
		t.Fatalf("StartTurn = %#v, %v", started, err)
	}
	for _, tc := range []struct {
		name string
		call func() error
		want error
	}{
		{"overlap", func() error {
			_, err := service.StartTurn(textInput("other"), sessionturn.TurnDirectionUser, 11)
			return err
		}, sessionturn.ErrTurnAlreadyActive},
		{"mismatch", func() error {
			_, err := service.EndTurn(2, sessionturn.TurnDirectionUser, messages.NewTextMessage(messages.RoleAssistant, "ok"), 11)
			return err
		}, sessionturn.ErrTurnMismatch},
		{"bad direction", func() error {
			_, err := service.EndTurn(1, sessionturn.TurnDirectionAssistant, messages.NewTextMessage(messages.RoleAssistant, "ok"), 11)
			return err
		}, sessionturn.ErrTurnMismatch},
		{"bad tick", func() error {
			_, err := service.EndTurn(1, sessionturn.TurnDirectionUser, messages.NewTextMessage(messages.RoleAssistant, "ok"), 10)
			return err
		}, sessionturn.ErrInvalidTurnTick},
		{"empty response", func() error {
			_, err := service.EndTurn(1, sessionturn.TurnDirectionUser, messages.Message{}, 11)
			return err
		}, sessionturn.ErrEmptyTurn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil || !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.want.Error()) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if _, active := service.ActiveTurn(); !active || len(service.History()) != 0 || service.NextTurnIndex() != 1 {
				t.Fatalf("invalid transition changed state: active=%v history=%d next=%d", active, len(service.History()), service.NextTurnIndex())
			}
		})
	}
	if err := service.Close(); err != nil {
		t.Fatalf("active Close error = %v", err)
	}
	if _, active := service.ActiveTurn(); active {
		t.Fatal("active turn was retained by Close")
	}
	if _, err := service.StartTurn(textInput("later"), sessionturn.TurnDirectionUser, 11); !errors.Is(err, sessionturn.ErrSessionClosed) {
		t.Fatalf("restart after close error = %v", err)
	}
	if _, err := service.EndTurn(1, sessionturn.TurnDirectionUser, messages.NewTextMessage(messages.RoleAssistant, "ok"), 11); !errors.Is(err, sessionturn.ErrSessionClosed) {
		t.Fatalf("end after close error = %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close after active turn = %v", err)
	}
}

func TestServiceSerializesBlockedEventPublicationWithoutStateLock(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var events []sessionturn.TurnEvent
	var service *Service
	service = New(Dependencies{EventSink: func(event sessionturn.TurnEvent) {
		if event.Type == sessionturn.TurnEventStart {
			close(started)
			<-release
		}
		// Reentrant read-only access must not deadlock on the state mutex.
		_ = service.History()
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	}})
	startDone := make(chan struct{})
	go func() {
		if _, err := service.StartTurn(textInput("input"), sessionturn.TurnDirectionUser, 1); err != nil {
			t.Errorf("StartTurn = %v", err)
		}
		close(startDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("start event did not enter sink")
	}
	endDone := make(chan error, 1)
	go func() {
		_, err := service.EndTurn(0, "", messages.NewTextMessage(messages.RoleAssistant, "response"), 2)
		endDone <- err
	}()
	select {
	case err := <-endDone:
		t.Fatalf("end completed while start sink was blocked: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-startDone:
	case <-time.After(time.Second):
		t.Fatal("start did not finish")
	}
	select {
	case err := <-endDone:
		if err != nil {
			t.Fatalf("end error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("end did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0].Type != sessionturn.TurnEventStart || events[1].Type != sessionturn.TurnEventEnd {
		t.Fatalf("events = %#v, want start then end", events)
	}
}

func TestServiceAudioProtocolAndRejectedCommit(t *testing.T) {
	provider := newTestSession()
	provider.sendFunc = func(message messages.StreamMessage) bool {
		if message.Type == messages.StreamTypeAudioDelta {
			textResponse(provider, "audio acknowledged")
		}
		return true
	}
	service, _ := newRespondingService(provider, nil)
	input := audioInput([]byte{0, 1, 2, 3, 4}, "audio/pcm")
	if _, err := service.RunTurn(context.Background(), input, sessionturn.TurnDirectionUser, 1, 2); err != nil {
		t.Fatalf("audio RunTurn = %v", err)
	}
	sent := provider.sentMessages()
	if len(sent) != 2 || sent[0].Type != messages.StreamTypeAudioDelta || sent[1].Type != messages.StreamTypeMessageEnd {
		t.Fatalf("audio messages = %#v, want delta then commit", sent)
	}
	audio, ok := sent[0].Value.(*messages.AudioDeltaValue)
	if !ok || string(audio.Content) != string([]byte{0, 1, 2, 3, 4}) || audio.MediaType != "audio/pcm" {
		t.Fatalf("audio payload = %#v", sent[0].Value)
	}

	commitProvider := newTestSession()
	commitProvider.sendFunc = func(message messages.StreamMessage) bool {
		if message.Type == messages.StreamTypeAudioDelta {
			textResponse(commitProvider, "response")
			return true
		}
		return false
	}
	commitService, _ := newRespondingService(commitProvider, nil)
	if _, err := commitService.RunTurn(context.Background(), audioInput([]byte{9, 8, 7}, "audio/pcm"), sessionturn.TurnDirectionUser, 1, 2); !errors.Is(err, sessionturn.ErrTurnInputCommitRejected) {
		t.Fatalf("commit rejection = %v", err)
	}
	if len(commitService.History()) != 0 {
		t.Fatal("rejected commit published history")
	}
}

func TestServiceNonTerminalResponseIsSkipped(t *testing.T) {
	provider := newTestSession()
	provider.sendFunc = func(message messages.StreamMessage) bool {
		if message.Type == messages.StreamTypeTextDelta {
			provider.push(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewNonTerminalErrorValue("not active yet", "informational")})
			textResponse(provider, "after diagnostic")
		}
		return true
	}
	service, _ := newRespondingService(provider, nil)
	_, err := service.RunTurn(context.Background(), textInput("input"), sessionturn.TurnDirectionUser, 1, 2)
	if err != nil || len(service.History()) != 1 || service.History()[0].Response.TextContent() != "after diagnostic" {
		t.Fatalf("nonterminal response = %v, history=%#v", err, service.History())
	}
}

func TestServiceTerminalResponsePreservesIdentityAndMessage(t *testing.T) {
	cause := errors.New("typed terminal")
	provider := newTestSession()
	provider.sendFunc = func(message messages.StreamMessage) bool {
		if message.Type == messages.StreamTypeTextDelta {
			provider.push(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(cause)})
		}
		return true
	}
	service, _ := newRespondingService(provider, nil)
	if _, err := service.RunTurn(context.Background(), textInput("typed"), sessionturn.TurnDirectionUser, 1, 2); !errors.Is(err, cause) {
		t.Fatalf("typed terminal = %v, want identity", err)
	}

	provider = newTestSession()
	provider.sendFunc = terminalMessageResponder(provider, "provider message")
	service, _ = newRespondingService(provider, nil)
	_, err := service.RunTurn(context.Background(), textInput("message"), sessionturn.TurnDirectionUser, 1, 2)
	if err == nil || !strings.Contains(err.Error(), "provider message") {
		t.Fatalf("message terminal = %v", err)
	}

	provider = newTestSession()
	provider.sendFunc = terminalMessageResponder(provider, " ")
	service, _ = newRespondingService(provider, nil)
	if _, err := service.RunTurn(context.Background(), textInput("blank"), sessionturn.TurnDirectionUser, 1, 2); !errors.Is(err, sessionturn.ErrSessionResponse) {
		t.Fatalf("blank terminal = %v", err)
	}
}

func terminalMessageResponder(provider *testSession, message string) func(messages.StreamMessage) bool {
	return func(input messages.StreamMessage) bool {
		if input.Type == messages.StreamTypeTextDelta {
			provider.push(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(message)})
		}
		return true
	}
}

func TestServiceResponseCancellationClearsActive(t *testing.T) {
	provider := newTestSession()
	service, _ := newRespondingService(provider, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := service.RunTurn(ctx, textInput("wait"), sessionturn.TurnDirectionUser, 1, 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
	if _, active := service.ActiveTurn(); active {
		t.Fatal("cancellation left an active turn")
	}
}

func TestServiceCloseIsSingleAndPreservesErrors(t *testing.T) {
	closeErr := errors.New("provider close failed")
	provider := newTestSession()
	provider.closeErr = closeErr
	service, _ := newRespondingService(provider, nil)
	if _, err := service.sessionFor(context.Background()); err != nil {
		t.Fatalf("connect before Close = %v", err)
	}
	if err := service.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close error = %v, want cause", err)
	}
	if err := service.Close(); !errors.Is(err, closeErr) || provider.closeCall.Load() != 1 {
		t.Fatalf("repeated Close = %v, calls=%d", err, provider.closeCall.Load())
	}
	if _, err := service.StartTurn(textInput("closed"), sessionturn.TurnDirectionUser, 1); !errors.Is(err, sessionturn.ErrSessionClosed) {
		t.Fatalf("closed StartTurn = %v", err)
	}

	missing := New(Dependencies{})
	if err := missing.Close(); err != nil {
		t.Fatalf("close without provider = %v", err)
	}
	missingInferencer := New(Dependencies{})
	if _, err := missingInferencer.RunTurn(context.Background(), textInput("input"), sessionturn.TurnDirectionUser, 1, 2); !errors.Is(err, sessionturn.ErrMissingTurnInferencer) {
		t.Fatalf("missing inferencer = %v", err)
	}
}

func TestServiceCloseDoesNotWaitForBlockedConnectionAdmission(t *testing.T) {
	provider := newTestSession()
	inferencer := &blockingInferencer{started: make(chan struct{}), release: make(chan struct{}), session: provider}
	service := New(Dependencies{SessionInferencer: inferencer})

	connected := make(chan struct{})
	go func() {
		if _, err := service.sessionFor(context.Background()); err != nil {
			close(connected)
			return
		}
		close(connected)
	}()
	select {
	case <-inferencer.started:
	case <-time.After(time.Second):
		t.Fatal("connection admission did not block")
	}

	closed := make(chan error, 1)
	go func() { closed <- service.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Close waited for blocked connection admission")
	}

	close(inferencer.release)
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("blocked connection admission did not settle after release")
	}
	if provider.closeCall.Load() != 1 {
		t.Fatalf("late connection close calls = %d, want 1", provider.closeCall.Load())
	}
}

func TestServiceCloseJoinsProviderCloseBeforeReturning(t *testing.T) {
	provider := &heldCloseSession{testSession: newTestSession(), started: make(chan struct{}), release: make(chan struct{})}
	textResponse(provider.testSession, "answer")
	inferencer := &testInferencer{session: provider}
	service := New(Dependencies{SessionInferencer: inferencer})
	if _, err := service.RunTurn(context.Background(), textInput("question"), sessionturn.TurnDirectionUser, 1, 2); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- service.Close() }()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider close did not start")
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before provider close joined: %v", err)
	default:
	}
	close(provider.release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not return after provider close finished")
	}
	if calls := provider.closeCall.Load(); calls != 1 {
		t.Fatalf("provider close calls = %d, want 1", calls)
	}
}

func TestServiceDeepCopiesResponseBytes(t *testing.T) {
	service := New(Dependencies{})
	if _, err := service.StartTurn(textInput("input"), sessionturn.TurnDirectionUser, 1); err != nil {
		t.Fatal(err)
	}
	responseBytes := []byte{1, 2, 3}
	response := messages.Message{Role: messages.RoleAssistant, ContentParts: []messages.ContentPart{messages.AudioPart{Bytes: responseBytes, MediaType: "audio/pcm"}}}
	turn, err := service.EndTurn(0, "", response, 2)
	if err != nil {
		t.Fatal(err)
	}
	responseBytes[0] = 99
	returnedAudio, ok := turn.Response.ContentParts[0].(messages.AudioPart)
	if !ok {
		t.Fatalf("returned response part = %#v", turn.Response.ContentParts[0])
	}
	if returnedAudio.Bytes[0] != 1 {
		t.Fatal("completed turn retained caller response bytes")
	}
	returnedAudio.Bytes[1] = 77
	storedAudio, ok := service.History()[0].Response.ContentParts[0].(messages.AudioPart)
	if !ok {
		t.Fatalf("stored response part = %#v", service.History()[0].Response.ContentParts[0])
	}
	if storedAudio.Bytes[1] != 2 {
		t.Fatal("history response snapshot aliases returned turn")
	}
}
