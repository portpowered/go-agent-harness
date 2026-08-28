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

func TestModelRunner_ForwardSessionEventReportsProviderBoundaryOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("ordinary rejection remains best effort", func(t *testing.T) {
		runner := NewSessionModelRunner(nil, 8, nil)
		session := newRejectingStreamSession()

		failure, deferred, accepted := runner.forwardSessionEvent(ctx, session, messages.StreamMessage{
			Type: messages.StreamTypeTextDelta,
		})
		if failure.Type != "" || deferred || accepted {
			t.Fatalf("ordinary rejection outcome = (%#v, %v, %v), want zero/false/false", failure, deferred, accepted)
		}
		if _, ok := runner.DeltaOutbox.Read(); ok {
			t.Fatal("ordinary rejection emitted an error delta")
		}
	})

	t.Run("result rejection is deferred", func(t *testing.T) {
		runner := NewSessionModelRunner(nil, 8, nil)
		session := newRejectingStreamSession()

		failure, deferred, accepted := runner.forwardSessionEvent(ctx, session, messages.StreamMessage{
			Type:  messages.StreamTypeToolCallEnd,
			Value: messages.NewToolCallEndValue("call-result", "date", "today"),
		})
		if failure.Type != messages.StreamTypeError || !deferred || accepted {
			t.Fatalf("result rejection outcome = (%#v, %v, %v), want ERROR/true/false", failure, deferred, accepted)
		}
		value, ok := failure.Value.(*messages.ErrorValue)
		if !ok {
			t.Fatalf("failure value = %T, want *messages.ErrorValue", failure.Value)
		}
		if value.Classification != "unresolved_tool_result" || !contains(value.Message, "call-result") {
			t.Fatalf("failure = %+v, want unresolved call-result", value)
		}

		runner.flushPendingSessionSendErrors(ctx, []messages.StreamMessage{failure})
		forwarded, ok := runner.DeltaOutbox.Read()
		if !ok || forwarded.Type != messages.StreamTypeError {
			t.Fatalf("flushed failure = %#v, ok=%v; want ERROR", forwarded, ok)
		}
	})

	t.Run("continuation rejection is emitted immediately", func(t *testing.T) {
		runner := NewSessionModelRunner(nil, 8, nil)
		session := newRejectingStreamSession()

		failure, deferred, accepted := runner.forwardSessionEvent(ctx, session, messages.StreamMessage{
			Type: messages.StreamTypeResponseCreate,
		})
		if failure.Type != "" || deferred || accepted {
			t.Fatalf("continuation rejection return = (%#v, %v, %v), want zero/false/false", failure, deferred, accepted)
		}
		forwarded, ok := runner.DeltaOutbox.Read()
		if !ok || forwarded.Type != messages.StreamTypeError {
			t.Fatalf("continuation failure = %#v, ok=%v; want ERROR", forwarded, ok)
		}
		value, ok := forwarded.Value.(*messages.ErrorValue)
		if !ok {
			t.Fatalf("continuation failure value = %T, want *messages.ErrorValue", forwarded.Value)
		}
		if value.Classification != "unresolved_tool_continuation" || !contains(value.Message, "not requested") {
			t.Fatalf("continuation failure = %+v, want unresolved continuation", value)
		}
	})
}

func TestModelRunner_DrainSessionAudioForwardsQueuedFrames(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	runner.UserAudioInbox <- []byte{4, 5, 6}

	responseInFlight := false
	responseCancelSent := false
	runner.drainSessionAudio(context.Background(), session, &responseInFlight, &responseCancelSent)

	sent := session.sentMessages()
	if len(sent) != 1 || sent[0].Type != messages.StreamTypeAudioDelta {
		t.Fatalf("drained sends = %#v, want one AUDIO.DELTA", sent)
	}
	value, ok := sent[0].Value.(*messages.AudioDeltaValue)
	if !ok || string(value.Content) != string([]byte{4, 5, 6}) {
		t.Fatalf("drained audio = %#v, want original frame", sent[0].Value)
	}

	close(runner.UserAudioInbox)
	runner.drainSessionAudio(context.Background(), session, &responseInFlight, &responseCancelSent)
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

func TestModelRunner_SendLatestUserTextRequestsResponseForAudioOnlyToolResult(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)

	runner.sendLatestUserText(context.Background(), session, messages.InferenceRequest{
		Messages: []messages.Message{
			{Role: messages.RoleUser},
			{
				Role:      messages.RoleAssistant,
				ToolCalls: []messages.ToolCall{{ID: "call-read-file", Name: "read_file"}},
			},
			{
				Role:         messages.RoleTool,
				ToolCallID:   "call-read-file",
				ContentParts: []messages.ContentPart{messages.TextPart{Text: "file not found"}},
			},
		},
	})

	sent := session.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want one explicit response request", len(sent))
	}
	if sent[0].Type != messages.StreamTypeResponseCreate {
		t.Fatalf("sent type = %s, want %s", sent[0].Type, messages.StreamTypeResponseCreate)
	}
	if _, ok := sent[0].Value.(*messages.ResponseCreateValue); !ok {
		t.Fatalf("sent value = %T, want *messages.ResponseCreateValue", sent[0].Value)
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

func TestModelRunner_SendLatestSessionToolResultsMixedBatchUsesOneCompleteResponse(t *testing.T) {
	session := newRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()
	imageBytes := []byte("image bytes")
	history := []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "inspect both results"),
		{
			Role: messages.RoleAssistant,
			ToolCalls: []messages.ToolCall{
				{ID: "call-text", Name: "text_tool"},
				{ID: "call-image", Name: "read_image"},
			},
		},
		{
			Role:         messages.RoleTool,
			ToolCallID:   "call-text",
			ContentParts: []messages.ContentPart{messages.TextPart{Text: "text result"}},
		},
		{
			Role:       messages.RoleTool,
			ToolCallID: "call-image",
			ContentParts: []messages.ContentPart{
				messages.TextPart{Text: "image result"},
				messages.ImagePart{Bytes: imageBytes, MediaType: "image/png"},
			},
		},
	}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{Messages: history})

	if sent := session.sentMessages(); len(sent) != 0 {
		t.Fatalf("mixed rich batch sent %d flat messages, want complete-message path only", len(sent))
	}
	deferred := session.completeMessagesWithoutResponse()
	if len(deferred) != 1 || deferred[0].ToolCallID != "call-text" {
		t.Fatalf("deferred tool results = %#v, want one call-text result", deferred)
	}
	complete := session.completeMessages()
	if len(complete) != 1 || complete[0].ToolCallID != "call-image" {
		t.Fatalf("response-requesting tool results = %#v, want one call-image result", complete)
	}
	imageParts := 0
	for _, part := range complete[0].ContentParts {
		if image, ok := part.(messages.ImagePart); ok {
			imageParts++
			if image.MediaType != "image/png" || string(image.Bytes) != string(imageBytes) {
				t.Fatalf("image part = %#v, want original PNG content", image)
			}
		}
	}
	if imageParts != 1 {
		t.Fatalf("image parts in final result = %d, want 1", imageParts)
	}
}

func TestModelRunner_SendLatestSessionToolResultsFallsBackForStreamOnlySession(t *testing.T) {
	session := newStreamOnlyRecordingSession()
	runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
	ctx := context.Background()
	history := []messages.Message{
		messages.NewTextMessage(messages.RoleUser, "inspect both results"),
		{
			Role: messages.RoleAssistant,
			ToolCalls: []messages.ToolCall{
				{ID: "call-text", Name: "text_tool"},
				{ID: "call-image", Name: "read_image"},
			},
		},
		{
			Role:         messages.RoleTool,
			ToolCallID:   "call-text",
			ContentParts: []messages.ContentPart{messages.TextPart{Text: "text result"}},
		},
		{
			Role:       messages.RoleTool,
			ToolCallID: "call-image",
			ContentParts: []messages.ContentPart{
				messages.ImagePart{Bytes: []byte("image bytes"), MediaType: "image/png"},
			},
		},
	}

	runner.sendLatestUserText(ctx, session, messages.InferenceRequest{Messages: history})

	sent := session.sentMessages()
	if len(sent) != 3 {
		t.Fatalf("stream-only sends = %d, want two tool results and one response trigger", len(sent))
	}
	for index, wantID := range []string{"call-text", "call-image"} {
		if sent[index].Type != messages.StreamTypeToolCallEnd {
			t.Fatalf("sent[%d] type = %s, want TOOLCALL.END", index, sent[index].Type)
		}
		value, ok := sent[index].Value.(*messages.ToolCallEndValue)
		if !ok {
			t.Fatalf("sent[%d] value = %T, want *ToolCallEndValue", index, sent[index].Value)
		}
		if value.ToolCallID != wantID {
			t.Fatalf("sent[%d] call ID = %q, want %q", index, value.ToolCallID, wantID)
		}
	}
	first, _ := sent[0].Value.(*messages.ToolCallEndValue)
	if first.Arguments != "text result" || first.Name != "text_tool" {
		t.Fatalf("text fallback = %#v, want correlated text result", first)
	}
	second, _ := sent[1].Value.(*messages.ToolCallEndValue)
	if second.Arguments != "" || second.Name != "read_image" {
		t.Fatalf("image fallback = %#v, want correlated empty flat output", second)
	}
	if sent[2].Type != messages.StreamTypeResponseCreate {
		t.Fatalf("response trigger type = %s, want RESPONSE.CREATE", sent[2].Type)
	}
	if _, ok := sent[2].Value.(*messages.ResponseCreateValue); !ok {
		t.Fatalf("response trigger value = %T, want *ResponseCreateValue", sent[2].Value)
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
