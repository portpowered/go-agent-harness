package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturns"
)

type testInferencer struct {
	session  messages.Session
	err      error
	connects atomic.Int32
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

func newRespondingService(session *testSession, sink sessionturns.TurnEventSink) (*Service, *testInferencer) {
	inferencer := &testInferencer{session: session}
	return New(Dependencies{SessionInferencer: inferencer, EventSink: sink}), inferencer
}

func TestServiceFiveTurnsReuseSessionAndCopySnapshots(t *testing.T) {
	provider := newTestSession()
	fact := "fact: the marigold key is hidden under stone seven"
	provider.sendFunc = func(message messages.StreamMessage) bool {
		if message.Type != messages.StreamTypeTextDelta && message.Type != messages.StreamTypeAudioDelta {
			return true
		}
		input := ""
		if value, ok := message.Value.(*messages.TextDeltaValue); ok {
			input = value.Content
		}
		if strings.HasPrefix(input, "fact:") {
			textResponse(provider, "stored")
		} else if strings.Contains(input, "recall") {
			textResponse(provider, "I remember "+fact)
		} else {
			textResponse(provider, "acknowledged")
		}
		return true
	}
	var eventsMu sync.Mutex
	var events []sessionturns.TurnEvent
	var service *Service
	var inferencer *testInferencer
	service, inferencer = newRespondingService(provider, func(event sessionturns.TurnEvent) {
		// Publication happens without the service state lock. Both snapshots are
		// safe to call from a callback.
		_, _ = service.ActiveTurn()
		_ = service.History()
		eventsMu.Lock()
		events = append(events, event)
		eventsMu.Unlock()
	})

	failure := errors.New("provider response failed")
	wantFailure := failure
	provider.sendFunc = func(message messages.StreamMessage) bool {
		if message.Type == messages.StreamTypeTextDelta || message.Type == messages.StreamTypeAudioDelta {
			if failure != nil {
				provider.push(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(failure)})
				failure = nil
				return true
			}
			input := ""
			if value, ok := message.Value.(*messages.TextDeltaValue); ok {
				input = value.Content
			}
			if strings.HasPrefix(input, "fact:") {
				textResponse(provider, "stored")
			} else if strings.Contains(input, "recall") {
				textResponse(provider, "I remember "+fact)
			} else {
				textResponse(provider, "acknowledged")
			}
		}
		return true
	}
	if _, err := service.RunTurn(context.Background(), sessionturns.NewTextTurnInput("failed"), sessionturns.TurnDirectionUser, 1, 2); !errors.Is(err, wantFailure) {
		t.Fatalf("failed turn error = %v, want provider cause", err)
	}

	audio := []byte{1, 2, 3}
	inputs := []sessionturns.TurnInput{
		sessionturns.NewTextTurnInput(fact),
		sessionturns.NewAudioTurnInput(audio, "audio/pcm"),
		sessionturns.NewAudioTurnInput([]byte{4, 5, 6}, "audio/pcm"),
		sessionturns.NewTextTurnInput("Please recall the fact."),
		sessionturns.NewTextTurnInput("End the scripted conversation."),
	}
	for i, input := range inputs {
		turn, err := service.RunTurn(context.Background(), input, sessionturns.TurnDirectionUser, uint64(2*i+3), uint64(2*i+4))
		if err != nil || turn.Index != uint64(i+1) {
			t.Fatalf("turn %d = %#v, err=%v", i+1, turn, err)
		}
	}
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
	defer eventsMu.Unlock()
	if len(events) != 11 {
		t.Fatalf("event count = %d, want 11 including the failed start", len(events))
	}
	if events[0].Type != sessionturns.TurnEventStart || events[0].Index != 1 {
		t.Fatalf("failed start event = %#v", events[0])
	}
	for i, event := range events[1:] {
		if event.Index != uint64(i/2+1) || event.Direction != sessionturns.TurnDirectionUser {
			t.Fatalf("event %d identity = %#v", i, event)
		}
		if i%2 == 0 && event.Type != sessionturns.TurnEventStart {
			t.Fatalf("event %d = %#v, want start", i, event)
		}
		if i%2 == 1 && event.Type != sessionturns.TurnEventEnd {
			t.Fatalf("event %d = %#v, want end", i, event)
		}
	}
	if err := service.Close(); err != nil || provider.closeCall.Load() != 1 {
		t.Fatalf("close = %v, calls=%d", err, provider.closeCall.Load())
	}
}

func TestServiceInvalidTransitionsPreserveState(t *testing.T) {
	service := New(Dependencies{})
	started, err := service.StartTurn(sessionturns.NewTextTurnInput("input"), sessionturns.TurnDirectionUser, 10)
	if err != nil || started.Index != 1 {
		t.Fatalf("StartTurn = %#v, %v", started, err)
	}
	for _, tc := range []struct {
		name string
		call func() error
		want error
	}{
		{"overlap", func() error {
			_, err := service.StartTurn(sessionturns.NewTextTurnInput("other"), sessionturns.TurnDirectionUser, 11)
			return err
		}, sessionturns.ErrTurnAlreadyActive},
		{"mismatch", func() error {
			_, err := service.EndTurn(2, sessionturns.TurnDirectionUser, messages.NewTextMessage(messages.RoleAssistant, "ok"), 11)
			return err
		}, sessionturns.ErrTurnMismatch},
		{"bad direction", func() error {
			_, err := service.EndTurn(1, sessionturns.TurnDirectionAssistant, messages.NewTextMessage(messages.RoleAssistant, "ok"), 11)
			return err
		}, sessionturns.ErrTurnMismatch},
		{"bad tick", func() error {
			_, err := service.EndTurn(1, sessionturns.TurnDirectionUser, messages.NewTextMessage(messages.RoleAssistant, "ok"), 10)
			return err
		}, sessionturns.ErrInvalidTurnTick},
		{"empty response", func() error {
			_, err := service.EndTurn(1, sessionturns.TurnDirectionUser, messages.Message{}, 11)
			return err
		}, sessionturns.ErrEmptyTurn},
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
	if err := service.Close(); !errors.Is(err, sessionturns.ErrSessionEndedWithActiveTurn) {
		t.Fatalf("active Close error = %v", err)
	}
	if _, active := service.ActiveTurn(); !active {
		t.Fatal("active turn was discarded by Close")
	}
	if _, err := service.EndTurn(0, "", messages.NewTextMessage(messages.RoleAssistant, "ok"), 11); err != nil {
		t.Fatalf("valid EndTurn = %v", err)
	}
	if _, err := service.StartTurn(sessionturns.NewTextTurnInput("later"), sessionturns.TurnDirectionUser, 11); !errors.Is(err, sessionturns.ErrInvalidTurnTick) {
		t.Fatalf("non-increasing restart error = %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("close after active turn = %v", err)
	}
}

func TestServiceSerializesBlockedEventPublicationWithoutStateLock(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var events []sessionturns.TurnEvent
	var service *Service
	service = New(Dependencies{EventSink: func(event sessionturns.TurnEvent) {
		if event.Type == sessionturns.TurnEventStart {
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
		_, _ = service.StartTurn(sessionturns.NewTextTurnInput("input"), sessionturns.TurnDirectionUser, 1)
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
	if len(events) != 2 || events[0].Type != sessionturns.TurnEventStart || events[1].Type != sessionturns.TurnEventEnd {
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
	input := sessionturns.NewAudioTurnInput([]byte{0, 1, 2, 3, 4}, "audio/pcm")
	if _, err := service.RunTurn(context.Background(), input, sessionturns.TurnDirectionUser, 1, 2); err != nil {
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
	if _, err := commitService.RunTurn(context.Background(), sessionturns.NewAudioTurnInput([]byte{9, 8, 7}, "audio/pcm"), sessionturns.TurnDirectionUser, 1, 2); !errors.Is(err, sessionturns.ErrTurnInputCommitRejected) {
		t.Fatalf("commit rejection = %v", err)
	}
	if len(commitService.History()) != 0 {
		t.Fatal("rejected commit published history")
	}
}

func TestServiceResponseErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configure  func(*testSession)
		want       error
		wantString string
	}{
		{
			name: "nonterminal is skipped",
			configure: func(s *testSession) {
				s.sendFunc = func(message messages.StreamMessage) bool {
					if message.Type == messages.StreamTypeTextDelta {
						s.push(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewNonTerminalErrorValue("not active yet", "informational")})
						textResponse(s, "after diagnostic")
					}
					return true
				}
			},
		},
		{
			name: "typed terminal",
			configure: func(s *testSession) {
				cause := errors.New("typed terminal")
				s.sendFunc = func(message messages.StreamMessage) bool {
					if message.Type == messages.StreamTypeTextDelta {
						s.push(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValueWithError(cause)})
					}
					return true
				}
				// The assertion is made by the dedicated case below.
			},
			wantString: "typed terminal",
		},
		{
			name: "message terminal",
			configure: func(s *testSession) {
				s.sendFunc = func(message messages.StreamMessage) bool {
					if message.Type == messages.StreamTypeTextDelta {
						s.push(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue("provider message")})
					}
					return true
				}
			},
			wantString: "provider message",
		},
		{
			name: "blank terminal",
			configure: func(s *testSession) {
				s.sendFunc = func(message messages.StreamMessage) bool {
					if message.Type == messages.StreamTypeTextDelta {
						s.push(messages.StreamMessage{Type: messages.StreamTypeError, Value: messages.NewErrorValue(" ")})
					}
					return true
				}
			},
			want: sessionturns.ErrSessionResponse,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newTestSession()
			tc.configure(provider)
			service, _ := newRespondingService(provider, nil)
			_, err := service.RunTurn(context.Background(), sessionturns.NewTextTurnInput("input"), sessionturns.TurnDirectionUser, 1, 2)
			if tc.name == "nonterminal is skipped" {
				if err != nil || len(service.History()) != 1 {
					t.Fatalf("nonterminal response = %v, history=%d", err, len(service.History()))
				}
				return
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.wantString != "" && !strings.Contains(err.Error(), tc.wantString) {
				t.Fatalf("error = %v, want message %q", err, tc.wantString)
			}
		})
	}

	provider := newTestSession()
	service, _ := newRespondingService(provider, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := service.RunTurn(ctx, sessionturns.NewTextTurnInput("wait"), sessionturns.TurnDirectionUser, 1, 2); !errors.Is(err, context.DeadlineExceeded) {
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
	if _, err := service.StartTurn(sessionturns.NewTextTurnInput("closed"), sessionturns.TurnDirectionUser, 1); !errors.Is(err, sessionturns.ErrSessionClosed) {
		t.Fatalf("closed StartTurn = %v", err)
	}

	missing := New(Dependencies{})
	if err := missing.Close(); err != nil {
		t.Fatalf("close without provider = %v", err)
	}
	missingInferencer := New(Dependencies{})
	if _, err := missingInferencer.RunTurn(context.Background(), sessionturns.NewTextTurnInput("input"), sessionturns.TurnDirectionUser, 1, 2); !errors.Is(err, sessionturns.ErrMissingTurnInferencer) {
		t.Fatalf("missing inferencer = %v", err)
	}
}

func TestServiceDeepCopiesResponseBytes(t *testing.T) {
	service := New(Dependencies{})
	if _, err := service.StartTurn(sessionturns.NewTextTurnInput("input"), sessionturns.TurnDirectionUser, 1); err != nil {
		t.Fatal(err)
	}
	responseBytes := []byte{1, 2, 3}
	response := messages.Message{Role: messages.RoleAssistant, ContentParts: []messages.ContentPart{messages.AudioPart{Bytes: responseBytes, MediaType: "audio/pcm"}}}
	turn, err := service.EndTurn(0, "", response, 2)
	if err != nil {
		t.Fatal(err)
	}
	responseBytes[0] = 99
	returnedAudio := turn.Response.ContentParts[0].(messages.AudioPart)
	if returnedAudio.Bytes[0] != 1 {
		t.Fatal("completed turn retained caller response bytes")
	}
	returnedAudio.Bytes[1] = 77
	storedAudio := service.History()[0].Response.ContentParts[0].(messages.AudioPart)
	if storedAudio.Bytes[1] != 2 {
		t.Fatal("history response snapshot aliases returned turn")
	}
}
