package participants

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestModelRunnerPublishSessionAudioFailurePreservesCauseAndMetadata(t *testing.T) {
	rootErr := errors.New("provider audio queue closed")
	runner := NewSessionModelRunner(nil, 1, nil)
	runner.currentPassID = 17

	runner.publishSessionAudioFailure(rootErr, false)

	delta, ok := runner.DeltaOutbox.Read()
	if !ok {
		t.Fatal("terminal audio failure was not published")
	}
	if delta.Type != messages.StreamTypeError {
		t.Fatalf("delta type = %q, want %q", delta.Type, messages.StreamTypeError)
	}
	if delta.ActorID != messages.Model {
		t.Fatalf("actor = %q, want %q", delta.ActorID, messages.Model)
	}
	if delta.LoopPassID != 17 {
		t.Fatalf("loop pass = %d, want 17", delta.LoopPassID)
	}
	value, ok := delta.Value.(*messages.ErrorValue)
	if !ok {
		t.Fatalf("delta value = %T, want *messages.ErrorValue", delta.Value)
	}
	if value.Message != rootErr.Error() {
		t.Fatalf("message = %q, want %q", value.Message, rootErr)
	}
	if !errors.Is(value.Err, rootErr) {
		t.Fatalf("error cause = %v, want %v", value.Err, rootErr)
	}
	if value.Classification != sessionAudioSendFailureClassification {
		t.Fatalf("classification = %q, want %q", value.Classification, sessionAudioSendFailureClassification)
	}
	if value.TerminalReason != messages.TerminalReasonTerminalFailure {
		t.Fatalf("terminal reason = %q, want %q", value.TerminalReason, messages.TerminalReasonTerminalFailure)
	}
	if value.TerminalProvenance != messages.TerminalProvenanceLoop {
		t.Fatalf("terminal provenance = %q, want %q", value.TerminalProvenance, messages.TerminalProvenanceLoop)
	}
	if value.OutputState != messages.TerminalOutputNone {
		t.Fatalf("output state = %q, want %q", value.OutputState, messages.TerminalOutputNone)
	}
}

func TestModelRunnerPublishSessionAudioFailureEvictsOrdinaryDelta(t *testing.T) {
	runner := NewSessionModelRunner(nil, 1, nil)
	if !runner.DeltaOutbox.Write(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue("stale"),
	}) {
		t.Fatal("ordinary delta was not queued")
	}

	runner.publishSessionAudioFailure(errors.New("audio write failed"), true)
	got, ok := runner.DeltaOutbox.Read()
	if !ok || got.Type != messages.StreamTypeError {
		t.Fatalf("queued delta = %#v, ok=%t, want terminal ERROR", got, ok)
	}
	value, ok := got.Value.(*messages.ErrorValue)
	if !ok || value.OutputState != messages.TerminalOutputPartial {
		t.Fatalf("terminal value = %#v, want partial output state", got.Value)
	}
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
	if !ok || value.Classification != unresolvedSessionUpdateClassification || !contains(value.Message, "tool definition update") {
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

// A provider SESSION.CLOSE ends the session. An inference request the loop
// schedules afterwards (such as the follow-up to a final tool result) must not
// reach the closed wire as a late RESPONSE.CREATE or user turn.
func TestSessionModelRunner_InferenceRequestAfterSessionCloseSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		history []messages.Message
	}{
		{
			name: "tool result continuation",
			history: []messages.Message{
				{Role: messages.RoleUser},
				{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{{ID: "call-1", Name: "read_file"}}},
				{Role: messages.RoleTool, ToolCallID: "call-1", ContentParts: []messages.ContentPart{messages.NewTextPart("done")}},
			},
		},
		{
			name:    "user text",
			history: []messages.Message{messages.NewTextMessage(messages.RoleUser, "hello")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newRecordingSession()
			runner := NewSessionModelRunner(&testSessionInferencer{session: session}, 8, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// The provider close is queued before the runner starts, so the
			// runner's provider preflight observes it before it reads the
			// inference request.
			session.recv.Write(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeSessionClose,
				Value: messages.NewSessionCloseValue("", "session_closed"),
			})
			runner.Inbox.Write(ctx, messages.InferenceRequest{Messages: tc.history})

			errCh := make(chan error, 1)
			go func() { errCh <- runner.Run(ctx) }()
			waitForDelta(t, ctx, runner, messages.StreamTypeSessionClose)

			// The runner handles one select branch at a time. Once the request
			// has left the inbox, a later provider message is forwarded only
			// after the request has been fully handled.
			for runner.Inbox.Len() != 0 {
				select {
				case <-ctx.Done():
					t.Fatal("runner did not read the inference request")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			session.recv.Write(ctx, messages.StreamMessage{
				Type:  messages.StreamTypeTextDelta,
				Value: messages.NewTextDeltaValue("barrier"),
			})
			waitForDelta(t, ctx, runner, messages.StreamTypeTextDelta)

			if sent := session.sentMessages(); len(sent) != 0 {
				t.Fatalf("sent %d messages after SESSION.CLOSE, want none: %#v", len(sent), sent)
			}
			closeRecordingSession(t, session)
			if err := <-errCh; err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
		})
	}
}

func audioDeltaContent(t *testing.T, value any) []byte {
	t.Helper()
	delta, ok := value.(*messages.AudioDeltaValue)
	if !ok {
		t.Fatalf("value = %T, want *messages.AudioDeltaValue", value)
	}
	return delta.Content
}

func toolCallEndValue(t *testing.T, value any) *messages.ToolCallEndValue {
	t.Helper()
	end, ok := value.(*messages.ToolCallEndValue)
	if !ok {
		t.Fatalf("value = %T, want *messages.ToolCallEndValue", value)
	}
	return end
}

func closeSessionForTest(t *testing.T, session interface{ Close() error }) {
	t.Helper()
	if err := session.Close(); err != nil {
		t.Fatalf("session Close() error = %v", err)
	}
}
