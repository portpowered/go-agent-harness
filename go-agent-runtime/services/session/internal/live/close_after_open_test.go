package live

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

const (
	richCallID     = "call-rich-image"
	richToolName   = "read_image"
	richResultText = "image result"
)

// completeMessageSession is a provider session that accepts image-bearing
// tool results through the complete-message path.
type completeMessageSession struct {
	*testSession
	accepted chan struct{}
	once     sync.Once
	mu       sync.Mutex
	complete []messages.Message
}

func (s *completeMessageSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	s.complete = append(s.complete, msg)
	s.mu.Unlock()
	s.once.Do(func() { close(s.accepted) })
	return true
}

func (*completeMessageSession) SupportsCompleteMessages() bool { return true }

func (*completeMessageSession) SupportsCompleteMessagesWithoutResponse() bool { return false }

func (s *completeMessageSession) completeMessages() []messages.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.Message(nil), s.complete...)
}

// richResultTool blocks, then returns text plus an image.
type richResultTool struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (tool *richResultTool) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	tool.once.Do(func() { close(tool.started) })
	select {
	case <-tool.release:
		return messages.ToolCallResponse{ToolCallID: call.ID, Name: call.Name, ContentParts: []messages.ContentPart{
			messages.TextPart{Text: richResultText},
			messages.ImagePart{Bytes: []byte("png-bytes"), MediaType: "image/png"},
		}}, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(ackSignalTimeout):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func assertStillOpen(t *testing.T, done <-chan error, label string) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("finite session finished %s: %v", label, err)
	case <-time.After(ackQuietWindow):
	}
}

// TestFinitePromptSessionClosesOnlyAfterAcceptedRichResultAndContinuation is
// the live-runtime close-after-open guarantee: a prompt-driven finite session
// whose provider response completes while a rich tool is still running must
// stay open until the correlated complete message is accepted by the provider
// and its grounded continuation terminates.
func TestFinitePromptSessionClosesOnlyAfterAcceptedRichResultAndContinuation(t *testing.T) {
	provider := &completeMessageSession{testSession: newTestSession(), accepted: make(chan struct{})}
	tool := &richResultTool{started: make(chan struct{}), release: make(chan struct{})}
	writeProvider(t, provider.testSession, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("provider-session", "audio_inference")})
	service := New(Dependencies{InferencerFactory: func(context.Context, session.LiveRequest) (messages.SessionInferencer, error) {
		return &testInferencer{session: provider}, nil
	}})
	handle, err := service.OpenLive(context.Background(), session.LiveRequest{
		SessionID: "close-after-open", OpeningPrompt: "inspect the screen", FinishAfterResponse: true,
		Capabilities: &session.LiveCapabilities{Executor: tool, Definitions: []messages.ToolDefinition{{Name: richToolName}}},
	})
	if err != nil {
		t.Fatalf("OpenLive: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	go func() {
		for range handle.Events() {
		}
	}()
	done := make(chan error, 1)
	go func() { done <- handle.Wait() }()

	waitForSentText(t, provider.testSession, "inspect the screen")
	writeProvider(t, provider.testSession, toolCallResponse(richCallID, richToolName)...)
	awaitSignal(t, tool.started, "rich tool to start")
	assertStillOpen(t, done, "while the rich tool was running")

	close(tool.release)
	awaitSignal(t, provider.accepted, "provider acceptance of the rich tool result")
	assertStillOpen(t, done, "after result acceptance but before its continuation")

	writeProvider(t, provider.testSession, assistantResponse("response-continuation", "", "final grounded continuation")...)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait = %v, want a clean close after the continuation", err)
		}
	case <-time.After(ackSignalTimeout):
		t.Fatal("finite session did not close after the grounded continuation")
	}
	complete := provider.completeMessages()
	if len(complete) != 1 || complete[0].Role != messages.RoleTool || complete[0].ToolCallID != richCallID || complete[0].TextContent() != richResultText {
		t.Fatalf("complete-message sends = %#v, want exactly one correlated rich result", complete)
	}
}
