package participants

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

// recordingSession records everything sent to the provider and allows tests to
// feed inbound session events through its Receive buffer.
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

func TestModelRunner_SendLatestUserTextPicksNewestUserText(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleUser, "first"),
			messages.NewTextMessage(messages.RoleAssistant, "reply"),
			messages.NewTextMessage(messages.RoleUser, "second"),
		},
	})

	sent := session.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	if sent[0].Type != messages.StreamTypeTextDelta {
		t.Fatalf("sent type = %s, want %s", sent[0].Type, messages.StreamTypeTextDelta)
	}
	value, ok := sent[0].Value.(*messages.TextDeltaValue)
	if !ok {
		t.Fatalf("sent value = %T, want *messages.TextDeltaValue", sent[0].Value)
	}
	if value.Content != "second" {
		t.Fatalf("text = %q, want %q", value.Content, "second")
	}
}

func TestModelRunner_SendLatestUserTextSkipsEmptyAndNonUserMessages(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleAssistant, "only assistant"),
		},
	})
	if got := len(session.sentMessages()); got != 0 {
		t.Fatalf("assistant-only request sent %d messages, want 0", got)
	}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleUser, "real"),
			messages.NewTextMessage(messages.RoleUser, ""),
		},
	})
	// The newest user message has empty text, so the search stops before
	// reaching the older non-empty one: nothing is sent.
	sent := session.sentMessages()
	if len(sent) != 0 {
		t.Fatalf("empty-text user message must stop the search without sending; got %d sends", len(sent))
	}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleAssistant, "noise"),
			messages.NewTextMessage(messages.RoleUser, "real"),
		},
	})
	if sent = session.sentMessages(); len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1 for older user text after non-user skip", len(sent))
	}
}

func TestModelRunner_SendLatestSessionToolResultsUsesCompleteMessagePath(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()
	imageBytes := []byte{0x89, 'P', 'N', 'G'}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{
		Messages: []messages.Message{
			messages.NewTextMessage(messages.RoleUser, "inspect the image"),
			{
				Role:         messages.RoleAssistant,
				ToolCalls:    []messages.ToolCall{{ID: "call-read-image", Name: "read_image"}},
				ContentParts: []messages.ContentPart{messages.TextPart{Text: "Reading the image."}},
			},
			{
				Role:       messages.RoleTool,
				ToolCallID: "call-read-image",
				ContentParts: []messages.ContentPart{
					messages.TextPart{Text: "image attached"},
					messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"},
				},
			},
		},
	})

	if got := len(session.sentMessages()); got != 0 {
		t.Fatalf("tool-result request sent %d streaming messages, want complete-message path", got)
	}
	sent := session.completeMessages()
	if len(sent) != 1 {
		t.Fatalf("complete-message sends = %d, want 1", len(sent))
	}
	if sent[0].Role != messages.RoleTool || sent[0].ToolCallID != "call-read-image" {
		t.Fatalf("complete message identity = %#v, want tool call-read-image", sent[0])
	}
	if len(sent[0].ContentParts) != 2 {
		t.Fatalf("complete message parts = %d, want text and image", len(sent[0].ContentParts))
	}
	gotImage, ok := sent[0].ContentParts[1].(messages.ImagePart)
	if !ok || gotImage.MediaType != "image/png" || string(gotImage.Bytes) != string(imageBytes) {
		t.Fatalf("complete image part = %#v, want original PNG bytes", sent[0].ContentParts[1])
	}
}

func TestModelRunner_SendLatestSessionToolResultsPreservesBatchOrder(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()
	first := messages.Message{Role: messages.RoleTool, ToolCallID: "call-1", ContentParts: []messages.ContentPart{
		messages.TextPart{Text: "one"},
		messages.ImagePart{Bytes: []byte("first image"), MediaType: "image/png"},
	}}
	second := messages.Message{Role: messages.RoleTool, ToolCallID: "call-2", ContentParts: []messages.ContentPart{
		messages.TextPart{Text: "two"},
		messages.ImagePart{Bytes: []byte("second image"), MediaType: "image/png"},
	}}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{Messages: []messages.Message{first, second}})

	deferred := session.completeMessagesWithoutResponse()
	if len(deferred) != 1 || deferred[0].ToolCallID != "call-1" {
		t.Fatalf("deferred tool results = %#v, want call-1", deferred)
	}
	sent := session.completeMessages()
	if len(sent) != 1 || sent[0].ToolCallID != "call-2" {
		t.Fatalf("final tool results = %#v, want call-2", sent)
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
