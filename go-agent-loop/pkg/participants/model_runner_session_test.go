package participants

import (
	"context"
	"errors"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"sync"
	"testing"
	"time"
)

type recordingSession struct {
	mu                      sync.Mutex
	sent                    []messages.StreamMessage
	complete                []messages.Message
	completeWithoutResponse []messages.Message
	sendCh                  chan messages.StreamMessage
	recv                    *messages.TypedBuffer[messages.StreamMessage]
	done                    chan struct{}
	once                    sync.Once
}

func newRecordingSession() *recordingSession {
	return &recordingSession{
		sendCh: make(chan messages.StreamMessage, 64),
		recv:   messages.NewTypedBuffer[messages.StreamMessage](16),
		done:   make(chan struct{}),
	}
}

func (s *recordingSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	select {
	case s.sendCh <- msg:
	default:
	}
	return true
}

func (s *recordingSession) sentMessages() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]messages.StreamMessage, len(s.sent))
	copy(out, s.sent)
	return out
}

func (s *recordingSession) SendMessage(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	s.complete = append(s.complete, msg)
	s.mu.Unlock()
	return true
}

func (s *recordingSession) SendMessageWithoutResponse(_ context.Context, msg messages.Message) bool {
	s.mu.Lock()
	s.completeWithoutResponse = append(s.completeWithoutResponse, msg)
	s.mu.Unlock()
	return true
}

func (s *recordingSession) RequestResponse(ctx context.Context) messages.SessionSendOutcome {
	return messages.SendSessionWithOutcome(ctx, s, messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	})
}

func (s *recordingSession) completeMessages() []messages.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]messages.Message, len(s.complete))
	copy(out, s.complete)
	return out
}

func (s *recordingSession) completeMessagesWithoutResponse() []messages.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]messages.Message, len(s.completeWithoutResponse))
	copy(out, s.completeWithoutResponse)
	return out
}

func (s *recordingSession) Receive() *messages.TypedBuffer[messages.StreamMessage] { return s.recv }

func (s *recordingSession) Done() <-chan struct{} { return s.done }

func (s *recordingSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

// streamOnlyRecordingSession intentionally exposes only the stream Session
// contract. It verifies the model runner's compatibility fallback without
// accidentally advertising complete-message support through a method set.
type streamOnlyRecordingSession struct {
	mu   sync.Mutex
	sent []messages.StreamMessage
	recv *messages.TypedBuffer[messages.StreamMessage]
	done chan struct{}
	once sync.Once
}

func newStreamOnlyRecordingSession() *streamOnlyRecordingSession {
	return &streamOnlyRecordingSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](8),
		done: make(chan struct{}),
	}
}

func (s *streamOnlyRecordingSession) Send(_ context.Context, msg messages.StreamMessage) bool {
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	s.mu.Unlock()
	return true
}

func (s *streamOnlyRecordingSession) sentMessages() []messages.StreamMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.StreamMessage(nil), s.sent...)
}

func (s *streamOnlyRecordingSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}

func (s *streamOnlyRecordingSession) Done() <-chan struct{} { return s.done }

func (s *streamOnlyRecordingSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

// rejectingStreamSession keeps the legacy bool-only Session contract while
// making provider-boundary rejection observable to the model runner tests.
type rejectingStreamSession struct {
	*recordingSession
}

func newRejectingStreamSession() *rejectingStreamSession {
	return &rejectingStreamSession{recordingSession: newRecordingSession()}
}

func (*rejectingStreamSession) Send(context.Context, messages.StreamMessage) bool {
	return false
}

// outcomeRecordingSession exercises the typed provider-boundary result used by
// live sessions. Rejected messages are intentionally not recorded as sent.
type outcomeRecordingSession struct {
	*recordingSession
	outcomes map[messages.StreamMessageType]messages.SessionSendOutcome
}

func (s *outcomeRecordingSession) SendWithOutcome(ctx context.Context, msg messages.StreamMessage) messages.SessionSendOutcome {
	outcome := messages.SessionSendOutcome{Status: messages.SessionSendSucceeded}
	if configured, ok := s.outcomes[msg.Type]; ok {
		outcome = configured
	}
	if outcome.OK() {
		s.recordingSession.Send(ctx, msg)
	}
	return outcome
}

type failingConnectInferencer struct{ err error }

func (f *failingConnectInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, f.err
}

func TestSessionModelRunner_ConnectErrorWrapsFailure(t *testing.T) {
	si := &failingConnectInferencer{err: errors.New("handshake refused")}
	runner := NewSessionModelRunner(si, 8, nil)

	err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("expected connect error, got nil")
	}
	if !errors.Is(err, si.err) {
		t.Fatalf("error should wrap underlying failure, got %v", err)
	}
	if got := err.Error(); !contains(got, "session connect") {
		t.Fatalf("error = %q, want prefix context %q", got, "session connect")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestSessionModelRunner_SendsSessionUpdateOnSessionCreated(t *testing.T) {
	session := newRecordingSession()
	config := &messages.SessionUpdateConfig{
		Instructions: "be brief",
		Model:        "grok-3",
		Modalities:   []string{"audio", "text"},
	}
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, config)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()

	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("sess-1", "grok-3"),
	})

	select {
	case sent := <-session.sendCh:
		if sent.Type != messages.StreamTypeSessionUpdate {
			t.Fatalf("sent type = %s, want %s", sent.Type, messages.StreamTypeSessionUpdate)
		}
		value, ok := sent.Value.(*messages.SessionUpdateValue)
		if !ok {
			t.Fatalf("sent value = %T, want *messages.SessionUpdateValue", sent.Value)
		}
		if value.Instructions != config.Instructions || value.Model != config.Model {
			t.Fatalf("update value = %+v, want config %+v", value, config)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for SESSION.UPDATE")
	}

	forwarded, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
	if !ok {
		t.Fatal("context cancelled waiting for forwarded SESSION.CREATED")
	}
	if forwarded.Type != messages.StreamTypeSessionCreated {
		t.Fatalf("forwarded type = %s, want %s", forwarded.Type, messages.StreamTypeSessionCreated)
	}
}

type providerConfiguredRecordingSession struct {
	*recordingSession
}

func (*providerConfiguredRecordingSession) InitialSessionConfigSent() bool { return true }

func TestSessionModelRunner_DoesNotEchoProviderOwnedInitialConfig(t *testing.T) {
	session := &providerConfiguredRecordingSession{recordingSession: newRecordingSession()}
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, &messages.SessionUpdateConfig{
		Instructions: "be brief",
		Model:        "gpt-realtime",
		Tools:        []messages.ToolDefinition{{Name: "lookup_weather"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()

	if !session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("provider-owned", "gpt-realtime"),
	}) {
		t.Fatal("failed to enqueue SESSION.CREATED")
	}

	forwarded, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
	if !ok {
		t.Fatal("context cancelled waiting for forwarded SESSION.CREATED")
	}
	if forwarded.Type != messages.StreamTypeSessionCreated {
		t.Fatalf("forwarded type = %s, want %s", forwarded.Type, messages.StreamTypeSessionCreated)
	}
	if sent := session.sentMessages(); len(sent) != 0 {
		t.Fatalf("provider messages = %#v, want no echoed initial SESSION.UPDATE", sent)
	}
}

func TestSessionModelRunner_PreservesLaterSessionUpdateAndAcknowledgement(t *testing.T) {
	session := &providerConfiguredRecordingSession{recordingSession: newRecordingSession()}
	runner := NewSessionModelRunner(nil, 8, &messages.SessionUpdateConfig{
		Tools: []messages.ToolDefinition{{Name: "lookup_weather"}},
	})
	ctx := context.Background()

	failure, deferred, accepted := runner.forwardSessionEvent(ctx, session, messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdate,
		Value: messages.NewSessionUpdateValue(runner.sessionConfig),
	})
	if failure.Type != "" || deferred || accepted {
		t.Fatalf("later SESSION.UPDATE outcome = (%#v, %t, %t), want accepted ordinary update", failure, deferred, accepted)
	}
	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeSessionUpdate {
		t.Fatalf("later provider messages = %#v, want one SESSION.UPDATE", sent)
	}

	state := newSessionResponseState()
	runner.forwardSessionMessageState(ctx, session, state, messages.StreamMessage{
		Type:  messages.StreamTypeSessionUpdated,
		Value: messages.NewSessionUpdatedValue("provider-owned"),
	})
	acknowledgement, ok := runner.DeltaOutbox.Read()
	if !ok || acknowledgement.Type != messages.StreamTypeSessionUpdated {
		t.Fatalf("forwarded acknowledgement = %#v, ok=%t; want SESSION.UPDATED", acknowledgement, ok)
	}
}

func TestSessionModelRunner_SessionCreatedUpdateFailureIsObservable(t *testing.T) {
	session := &outcomeRecordingSession{
		recordingSession: newRecordingSession(),
		outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
			messages.StreamTypeSessionUpdate: {Status: messages.SessionSendTerminalFailure},
		},
	}
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, &messages.SessionUpdateConfig{
		Tools: []messages.ToolDefinition{{Name: "current_page_tool"}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- runner.Run(ctx) }()

	if !session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionCreated,
		Value: messages.NewSessionCreatedValue("failed-config-session", "test"),
	}) {
		t.Fatal("failed to enqueue SESSION.CREATED")
	}
	failure := waitForDelta(t, ctx, runner, messages.StreamTypeError)
	value, ok := failure.Value.(*messages.ErrorValue)
	if !ok || value.Classification != "unresolved_session_update" || !contains(value.Message, "tool definition update") {
		t.Fatalf("session-created update failure = %#v, want unresolved session update", failure.Value)
	}
	if sent := session.sentMessages(); len(sent) != 0 {
		t.Fatalf("rejected initial update sent %d provider messages, want zero", len(sent))
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not return after session close")
	}
}

func TestSessionModelRunnerQueuesSessionEventAfterPendingAudio(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()

	if err := runner.EnqueueSessionAudioInput(ctx, []byte{1, 2, 3}); err != nil {
		t.Fatalf("EnqueueSessionAudioInput: %v", err)
	}
	eventErrCh := make(chan error, 1)
	go func() {
		eventErrCh <- runner.EnqueueSessionEvent(ctx, messages.StreamMessage{
			Type:  messages.StreamTypeMessageEnd,
			Value: messages.NewMessageEndValue(messages.TokenUsage{}),
		})
	}()

	first := waitForSentMessage(t, ctx, session)
	second := waitForSentMessage(t, ctx, session)
	if first.Type != messages.StreamTypeAudioDelta {
		t.Fatalf("first outbound type = %s, want %s", first.Type, messages.StreamTypeAudioDelta)
	}
	if second.Type != messages.StreamTypeMessageEnd {
		t.Fatalf("second outbound type = %s, want %s", second.Type, messages.StreamTypeMessageEnd)
	}
	if err := <-eventErrCh; err != nil {
		t.Fatalf("EnqueueSessionEvent: %v", err)
	}
}

func TestSessionModelRunnerOrderedIngressReportsFullWithoutBlocking(t *testing.T) {
	runner := NewSessionModelRunner(nil, 8, nil)
	ctx := context.Background()
	for i := 0; i < cap(runner.sessionInputInbox); i++ {
		if err := runner.EnqueueSessionAudioInput(ctx, []byte{byte(i)}); err != nil {
			t.Fatalf("fill ordered session ingress at %d: %v", i, err)
		}
	}

	err := runner.EnqueueSessionEvent(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-full", "tool", "result"),
	})
	if !errors.Is(err, ErrSessionInputQueueFull) {
		t.Fatalf("full ordered session ingress error = %v, want ErrSessionInputQueueFull", err)
	}
	if runner.hasPendingSessionToolEvents() {
		t.Fatal("rejected full-queue tool event remained marked pending")
	}
}

func TestSessionModelRunner_BargeInSendsResponseCancelBeforeAudio(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()

	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioStart,
		Value: messages.NewAudioStartValue(),
	})
	waitForDelta(t, ctx, runner, messages.StreamTypeAudioStart)

	runner.UserAudioInbox <- []byte{1, 2, 3}

	var sawCancel bool
	for i := 0; i < 2; i++ {
		select {
		case sent := <-session.sendCh:
			switch sent.Type {
			case messages.StreamTypeResponseCancel:
				if sawCancel {
					t.Fatal("duplicate RESPONSE.CANCEL")
				}
				sawCancel = true
			case messages.StreamTypeAudioDelta:
				value, ok := sent.Value.(*messages.AudioDeltaValue)
				if !ok {
					t.Fatalf("audio delta value = %T", sent.Value)
				}
				if string(value.Content) != "\x01\x02\x03" {
					t.Fatalf("forwarded audio = %v", value.Content)
				}
				if !sawCancel {
					t.Fatal("RESPONSE.CANCEL must precede forwarded audio on barge-in")
				}
				// Done.
				session.Close()
				return
			default:
				t.Fatalf("unexpected outbound message %s", sent.Type)
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for barge-in messages")
		}
	}
}

func TestSessionModelRunner_BargeInAfterMessageStartSendsResponseCancelBeforeFirstAudio(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()

	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	waitForDelta(t, ctx, runner, messages.StreamTypeMessageStart)

	runner.UserAudioInbox <- []byte{4, 5, 6}
	first := waitForSentMessage(t, ctx, session)
	second := waitForSentMessage(t, ctx, session)
	if first.Type != messages.StreamTypeResponseCancel {
		t.Fatalf("first outbound type = %s, want %s", first.Type, messages.StreamTypeResponseCancel)
	}
	if second.Type != messages.StreamTypeAudioDelta {
		t.Fatalf("second outbound type = %s, want %s", second.Type, messages.StreamTypeAudioDelta)
	}

	// More speech in the same response overlap is still forwarded, but must not
	// dispatch another cancellation for the response already cancelled above.
	runner.UserAudioInbox <- []byte{7, 8, 9}
	third := waitForSentMessage(t, ctx, session)
	if third.Type != messages.StreamTypeAudioDelta {
		t.Fatalf("third outbound type = %s, want %s without a duplicate cancel", third.Type, messages.StreamTypeAudioDelta)
	}
	if got := len(session.sentMessages()); got != 3 {
		t.Fatalf("active overlap sent %d messages, want one cancel and two audio appends", got)
	}
}

func TestSessionModelRunner_SilenceFrameDoesNotCancelOpeningResponse(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()

	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	waitForDelta(t, ctx, runner, messages.StreamTypeMessageStart)

	// Room mixers emit zero-filled frames on every cadence before a peer has
	// spoken. They must reach the provider, but cannot be treated as barge-in.
	silence := make([]byte, 4)
	runner.UserAudioInbox <- silence
	sent := waitForSentMessage(t, ctx, session)
	if sent.Type != messages.StreamTypeAudioDelta {
		t.Fatalf("silence outbound type = %s, want %s", sent.Type, messages.StreamTypeAudioDelta)
	}
	if got := sent.Value.(*messages.AudioDeltaValue).Content; string(got) != string(silence) {
		t.Fatalf("forwarded silence = %v, want %v", got, silence)
	}
	if got := len(session.sentMessages()); got != 1 {
		t.Fatalf("silence sent %d messages, want 1 (no RESPONSE.CANCEL)", got)
	}

	// A later contentful frame still cancels the same in-flight response and
	// remains ordered ahead of that frame.
	runner.UserAudioInbox <- []byte{4, 5, 6, 7}
	first := waitForSentMessage(t, ctx, session)
	second := waitForSentMessage(t, ctx, session)
	if first.Type != messages.StreamTypeResponseCancel {
		t.Fatalf("contentful first outbound type = %s, want %s", first.Type, messages.StreamTypeResponseCancel)
	}
	if second.Type != messages.StreamTypeAudioDelta {
		t.Fatalf("contentful second outbound type = %s, want %s", second.Type, messages.StreamTypeAudioDelta)
	}
}

func TestSessionModelRunner_ResponseIdentityRejectsLateTerminalAndOutput(t *testing.T) {
	runner := NewSessionModelRunner(nil, 16, nil)
	session := newRecordingSession()
	state := newSessionResponseState()
	ctx := context.Background()

	runner.forwardSessionMessageWithState(ctx, session, messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "resp-old",
		Value:      messages.NewMessageStartValue(),
	}, state)
	runner.forwardSessionMessageWithState(ctx, session, messages.StreamMessage{
		Type:       messages.StreamTypeMessageStart,
		ResponseID: "resp-current",
		Value:      messages.NewMessageStartValue(),
	}, state)

	if !state.responseInFlight || state.currentResponseID != "resp-current" {
		t.Fatalf("replacement response state = in_flight:%t id:%q, want current response active", state.responseInFlight, state.currentResponseID)
	}
	if ended := runner.forwardSessionMessageWithState(ctx, session, messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-old",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	}, state); ended {
		t.Fatal("late old response terminal was accepted as the current response")
	}
	if !state.responseInFlight || state.currentResponseID != "resp-current" {
		t.Fatalf("late terminal changed response state = in_flight:%t id:%q", state.responseInFlight, state.currentResponseID)
	}

	runner.forwardSessionMessageWithState(ctx, session, messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		ResponseID: "resp-old",
		Value:      messages.NewTextDeltaValue("stale"),
	}, state)
	runner.forwardSessionMessageWithState(ctx, session, messages.StreamMessage{
		Type:       messages.StreamTypeTextDelta,
		ResponseID: "resp-current",
		Value:      messages.NewTextDeltaValue("current"),
	}, state)

	var textValues []string
	for {
		msg, ok := runner.DeltaOutbox.Read()
		if !ok {
			break
		}
		if value, ok := msg.Value.(*messages.TextDeltaValue); ok {
			textValues = append(textValues, value.Content)
		}
	}
	if len(textValues) != 1 || textValues[0] != "current" {
		t.Fatalf("forwarded text after late old response events = %#v, want [current]", textValues)
	}
	if ended := runner.forwardSessionMessageWithState(ctx, session, messages.StreamMessage{
		Type:       messages.StreamTypeMessageEnd,
		ResponseID: "resp-current",
		Value:      messages.NewMessageEndValue(messages.TokenUsage{}),
	}, state); !ended {
		t.Fatal("current response terminal was not accepted")
	}
}

func TestSessionModelRunner_BargeInChecksCancelAndAudioSendOutcomes(t *testing.T) {
	tests := []struct {
		name             string
		responseInFlight bool
		outcomes         map[messages.StreamMessageType]messages.SessionSendOutcome
		wantSent         int
		wantCancelSent   bool
		wantError        string
	}{
		{
			name:             "cancel buffer full",
			responseInFlight: true,
			outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
				messages.StreamTypeResponseCancel: {Status: messages.SessionSendBufferFull},
			},
			wantError: "response cancel",
		},
		{
			name:             "cancel closed",
			responseInFlight: true,
			outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
				messages.StreamTypeResponseCancel: {Status: messages.SessionSendClosed},
			},
			wantError: "response cancel",
		},
		{
			name: "audio buffer full",
			outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
				messages.StreamTypeAudioDelta: {Status: messages.SessionSendBufferFull},
			},
			wantError: "audio",
		},
		{
			name: "audio closed",
			outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
				messages.StreamTypeAudioDelta: {Status: messages.SessionSendClosed},
			},
			wantError: "audio",
		},
		{
			name:             "audio rejected after accepted cancel",
			responseInFlight: true,
			outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
				messages.StreamTypeResponseCancel: {Status: messages.SessionSendSucceeded},
				messages.StreamTypeAudioDelta:     {Status: messages.SessionSendClosed},
			},
			wantSent:       1,
			wantCancelSent: true,
			wantError:      "audio",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &outcomeRecordingSession{
				recordingSession: newRecordingSession(),
				outcomes:         test.outcomes,
			}
			runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
			responseInFlight := test.responseInFlight
			responseCancelSent := false

			err := runner.forwardSessionAudio(context.Background(), session, []byte{1, 2, 3}, &responseInFlight, &responseCancelSent)
			if err == nil || !contains(err.Error(), test.wantError) {
				t.Fatalf("forwardSessionAudio error = %v, want %q failure", err, test.wantError)
			}
			if !contains(err.Error(), "buffer_full") && !contains(err.Error(), "closed") {
				t.Fatalf("forwardSessionAudio error = %v, want typed send status", err)
			}
			if responseCancelSent != test.wantCancelSent {
				t.Fatalf("responseCancelSent = %t, want %t", responseCancelSent, test.wantCancelSent)
			}
			if sent := session.sentMessages(); len(sent) != test.wantSent {
				t.Fatalf("sent messages = %d, want %d (%#v)", len(sent), test.wantSent, sent)
			}
			if test.wantSent == 0 && responseCancelSent {
				t.Fatal("rejected audio path claimed a cancellation without an accepted audio frame")
			}
		})
	}
}

func TestSessionModelRunner_BargeInSendFailurePropagatesFromRun(t *testing.T) {
	tests := []struct {
		name     string
		outcomes map[messages.StreamMessageType]messages.SessionSendOutcome
		want     []string
	}{
		{
			name: "cancel buffer full",
			outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
				messages.StreamTypeResponseCancel: {Status: messages.SessionSendBufferFull},
			},
			want: []string{"response cancel", "buffer_full"},
		},
		{
			name: "audio closed after accepted cancel",
			outcomes: map[messages.StreamMessageType]messages.SessionSendOutcome{
				messages.StreamTypeResponseCancel: {Status: messages.SessionSendSucceeded},
				messages.StreamTypeAudioDelta:     {Status: messages.SessionSendClosed},
			},
			want: []string{"audio", "closed"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &outcomeRecordingSession{
				recordingSession: newRecordingSession(),
				outcomes:         test.outcomes,
			}
			runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			errCh := make(chan error, 1)
			go func() { errCh <- runner.Run(ctx) }()
			session.recv.Write(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeMessageStart,
				Value: messages.NewMessageStartValue(),
			})
			waitForDelta(t, ctx, runner, messages.StreamTypeMessageStart)
			runner.UserAudioInbox <- []byte{7, 8, 9}

			select {
			case err := <-errCh:
				if err == nil || !contains(err.Error(), "session") || !contains(err.Error(), "send failed") {
					t.Fatalf("Run error = %v, want propagated session send failure", err)
				}
				for _, fragment := range test.want {
					if !contains(err.Error(), fragment) {
						t.Fatalf("Run error = %v, want detail %q", err, fragment)
					}
				}
			case <-ctx.Done():
				t.Fatal("Run did not return after barge-in send failure")
			}
		})
	}
}

func TestSessionModelRunner_DropsProviderOutputAfterBargeInCancel(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()

	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	waitForDelta(t, ctx, runner, messages.StreamTypeMessageStart)
	runner.UserAudioInbox <- []byte{1, 2, 3}
	if sent := waitForSentMessage(t, ctx, session); sent.Type != messages.StreamTypeResponseCancel {
		t.Fatalf("first outbound type = %s, want %s", sent.Type, messages.StreamTypeResponseCancel)
	}
	if sent := waitForSentMessage(t, ctx, session); sent.Type != messages.StreamTypeAudioDelta {
		t.Fatalf("second outbound type = %s, want %s", sent.Type, messages.StreamTypeAudioDelta)
	}

	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewAudioDeltaValue([]byte{9, 8, 7}),
	})
	noDeltaCtx, noDeltaCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, ok := runner.DeltaOutbox.ReadBlocking(noDeltaCtx.Done())
	noDeltaCancel()
	if ok {
		t.Fatal("provider output after cancellation crossed the session boundary")
	}

	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	waitForDelta(t, ctx, runner, messages.StreamTypeMessageEnd)

	// The next response opens a fresh cancellation window and its output must
	// remain deliverable, proving that suppression does not poison continuation.
	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	waitForDelta(t, ctx, runner, messages.StreamTypeMessageStart)
	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewAudioDeltaValue([]byte{4, 5, 6}),
	})
	if got := waitForDelta(t, ctx, runner, messages.StreamTypeAudioDelta); len(got.Value.(*messages.AudioDeltaValue).Content) != 3 {
		t.Fatalf("continuation output was not delivered after cancellation window")
	}
}

func TestSessionModelRunner_CompletedResponseDoesNotCancelNextAudio(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()

	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	waitForDelta(t, ctx, runner, messages.StreamTypeMessageStart)
	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	waitForDelta(t, ctx, runner, messages.StreamTypeMessageEnd)

	runner.UserAudioInbox <- []byte{7, 8, 9}
	sent := waitForSentMessage(t, ctx, session)
	if sent.Type != messages.StreamTypeAudioDelta {
		t.Fatalf("outbound type = %s, want %s", sent.Type, messages.StreamTypeAudioDelta)
	}
	if got := len(session.sentMessages()); got != 1 {
		t.Fatalf("sent %d messages, want 1 (no RESPONSE.CANCEL after completion)", got)
	}
}

func TestSessionModelRunner_QueuedMessageEndWinsBeforePeerAudio(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 8, nil)
	state := sessionRunState{responseInFlight: true}

	// Both events are pending in the same logical transport turn. The provider
	// boundary is already observable, so it must update the runner before the
	// contentful peer frame is admitted.
	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	runner.UserAudioInbox <- []byte{1, 2, 3}

	handled, closed, err := runner.forwardPendingSessionInputs(ctx, session, &state)
	if err != nil {
		t.Fatalf("forwardPendingSessionInputs error = %v", err)
	}
	if !handled || closed {
		t.Fatalf("forwardPendingSessionInputs result = (handled=%t, closed=%t), want handled open", handled, closed)
	}
	if state.responseInFlight || state.responseCancelSent || !state.responseCompleted {
		t.Fatalf("response state after queued MESSAGE.END = %+v, want completed and uncancelled", state)
	}

	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("provider sends after queued MESSAGE.END = %#v, want one AUDIO.DELTA and no RESPONSE.CANCEL", sent)
	}
	if got := sent[0].Value.(*messages.AudioDeltaValue).Content; string(got) != string([]byte{1, 2, 3}) {
		t.Fatalf("peer audio = %v, want it forwarded unchanged", got)
	}
	if delta, ok := runner.DeltaOutbox.Read(); !ok || delta.Type != messages.StreamTypeMessageEnd {
		t.Fatalf("forwarded boundary = %#v, ok=%t; want MESSAGE.END", delta, ok)
	}

	// The next response starts a fresh cancellation window and reaches a normal
	// terminal boundary, proving the completed response's state did not leak.
	runner.forwardSessionMessageState(ctx, session, &state, messages.StreamMessage{
		Type:  messages.StreamTypeMessageStart,
		Value: messages.NewMessageStartValue(),
	})
	runner.forwardSessionMessageState(ctx, session, &state, messages.StreamMessage{
		Type:  messages.StreamTypeAudioDelta,
		Role:  messages.RoleAssistant,
		Value: messages.NewAudioDeltaValue([]byte{4, 5, 6}),
	})
	runner.forwardSessionMessageState(ctx, session, &state, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	if state.responseInFlight || state.responseCancelSent || !state.responseCompleted {
		t.Fatalf("response state after normal next turn = %+v, want completed and uncancelled", state)
	}

	nextEnd := readNextSessionMessageEnd(t, runner.DeltaOutbox)
	if nextEnd.TerminalReason == messages.TerminalReasonPartialOutput {
		t.Fatalf("next normal MESSAGE.END retained interrupted terminal reason: %+v", nextEnd)
	}
}

func TestSessionModelRunner_QueuedAudioAndMessageEndRemainFIFO(t *testing.T) {
	ctx := context.Background()
	session := newRecordingSession()
	runner := NewSessionModelRunner(nil, 8, nil)
	state := newSessionResponseState()

	// Forward the first frame through the normal session-input path before
	// queueing the next turn. This leaves MESSAGE.END and the next frame
	// pending together, which is the boundary where separate inbox selects can
	// reorder a commit ahead of (or behind) its following audio turn.
	if err := runner.EnqueueSessionAudioInput(ctx, []byte{1, 2, 3}); err != nil {
		t.Fatalf("enqueue first audio: %v", err)
	}
	if err := runner.drainSessionAudioWithState(ctx, session, state); err != nil {
		t.Fatalf("drain first audio: %v", err)
	}
	if err := runner.EnqueueSessionEvent(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd}); err != nil {
		t.Fatalf("enqueue message end: %v", err)
	}
	if err := runner.EnqueueSessionAudioInput(ctx, []byte{4, 5, 6}); err != nil {
		t.Fatalf("enqueue second audio: %v", err)
	}

	if handled, closed, err := runner.forwardPendingSessionInputs(ctx, session, state); err != nil {
		t.Fatalf("forward queued session inputs: %v", err)
	} else if !handled || closed {
		t.Fatalf("forward queued session inputs = handled:%t closed:%t, want handled open", handled, closed)
	}

	sent := session.sentMessages()
	if len(sent) != 3 {
		t.Fatalf("provider sent %d messages, want 3: %#v", len(sent), sent)
	}
	wantTypes := []messages.StreamMessageType{messages.StreamTypeAudioDelta, messages.StreamTypeMessageEnd, messages.StreamTypeAudioDelta}
	wantPayloads := [][]byte{{1, 2, 3}, nil, {4, 5, 6}}
	for index, msg := range sent {
		if msg.Type != wantTypes[index] {
			t.Fatalf("provider message %d type = %s, want %s; sent=%#v", index, msg.Type, wantTypes[index], sent)
		}
		if value, ok := msg.Value.(*messages.AudioDeltaValue); ok {
			if string(value.Content) != string(wantPayloads[index]) {
				t.Fatalf("provider message %d payload = %v, want %v", index, value.Content, wantPayloads[index])
			}
		}
	}
}

func TestSessionModelRunner_AudioWithoutActiveResponseForwardsDirectly(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ap := NewActiveParticipant(messages.Model, runner)
	ap.Start(ctx)
	defer ap.Stop()

	runner.UserAudioInbox <- []byte{9}

	select {
	case sent := <-session.sendCh:
		if sent.Type != messages.StreamTypeAudioDelta {
			t.Fatalf("sent type = %s, want %s", sent.Type, messages.StreamTypeAudioDelta)
		}
		if got := len(session.sentMessages()); got != 1 {
			t.Fatalf("sent %d messages, want 1 (no RESPONSE.CANCEL)", got)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for forwarded audio")
	}
}

func TestSessionModelRunner_AudioInboxCloseEndsRun(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runner.Run(ctx) }()

	close(runner.UserAudioInbox)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run after audio inbox close = %v, want nil", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not return after UserAudioInbox closed")
	}
}

func TestSessionModelRunner_ContinuesAfterAcceptedResponseRequest(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runner.Run(ctx) }()

	runner.UserEventInbox <- messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	}
	select {
	case sent := <-session.sendCh:
		if sent.Type != messages.StreamTypeResponseCreate {
			t.Fatalf("sent type = %s, want %s", sent.Type, messages.StreamTypeResponseCreate)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for RESPONSE.CREATE")
	}

	session.recv.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeMessageEnd,
		Value: messages.NewMessageEndValue(messages.TokenUsage{}),
	})
	waitForDelta(t, ctx, runner, messages.StreamTypeMessageEnd)

	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not return after accepted continuation completed")
	}
}

func TestSessionModelRunner_SuppressesContinuationAfterRejectedToolResult(t *testing.T) {
	session := newRejectingStreamSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- runner.Run(ctx) }()

	runner.UserEventInbox <- messages.StreamMessage{
		Type:  messages.StreamTypeToolCallEnd,
		Value: messages.NewToolCallEndValue("call-rejected", "date", "result"),
	}
	runner.UserEventInbox <- messages.StreamMessage{
		Type:  messages.StreamTypeResponseCreate,
		Value: messages.NewResponseCreateValue(),
	}

	failure := waitForDelta(t, ctx, runner, messages.StreamTypeError)
	value, ok := failure.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("failure value = %T, want *messages.ErrorValue", failure.Value)
	}
	if value.Classification != "unresolved_tool_result" {
		t.Fatalf("failure classification = %q, want unresolved_tool_result", value.Classification)
	}
	if !contains(value.Message, "call-rejected") {
		t.Fatalf("failure message = %q, want rejected call ID", value.Message)
	}
	if sent := session.sentMessages(); len(sent) != 0 {
		t.Fatalf("rejected lifecycle sent %d provider messages, want 0", len(sent))
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not return after rejected continuation was reported")
	}
}

func waitForDelta(t *testing.T, ctx context.Context, runner *ModelRunner, want messages.StreamMessageType) messages.StreamMessage {
	t.Helper()
	for {
		delta, ok := runner.DeltaOutbox.ReadBlocking(ctx.Done())
		if !ok {
			t.Fatal("context cancelled waiting for delta")
		}
		if delta.Type == want {
			return delta
		}
	}
}

func waitForSentMessage(t *testing.T, ctx context.Context, session *recordingSession) messages.StreamMessage {
	t.Helper()
	select {
	case sent := <-session.sendCh:
		return sent
	case <-ctx.Done():
		t.Fatal("timed out waiting for outbound session message")
		return messages.StreamMessage{}
	}
}
