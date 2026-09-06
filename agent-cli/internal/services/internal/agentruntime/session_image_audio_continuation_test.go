package agentruntime

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audio "github.com/portpowered/go-agent-harness/go-audio/pkg/audio"
)

const imageAudioContinuationCallID = "image-audio-continuation-call"

// TestImageAudioSessionKeepsToolContinuationOpen covers both finite image
// entry points. The provider ends its first response with a tool call, while
// the executor is held until the test has observed that the session stayed
// open. The final assistant response can therefore only arrive when the
// image/audio loop ignores the intermediate tool-call MESSAGE.END.
func TestImageAudioSessionKeepsToolContinuationOpen(t *testing.T) {
	dir := t.TempDir()
	imagePath := writeImageAudioContinuationFixture(t, dir)

	cases := []struct {
		name string
		run  func(context.Context, io.Writer, SessionImageRunOptions, SessionAudioInput, string) error
	}{
		{
			name: "direct image audio",
			run: func(ctx context.Context, out io.Writer, opts SessionImageRunOptions, input SessionAudioInput, _ string) error {
				return RunSessionWithImagesAndAudioInput(ctx, out, opts, input)
			},
		},
		{
			name: "recorded image audio",
			run: func(ctx context.Context, out io.Writer, opts SessionImageRunOptions, input SessionAudioInput, destination string) error {
				return RunSessionWithImagesAndRecordingDirectoryAndAudioInput(ctx, out, opts, destination, input)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := newImageAudioContinuationSession()
			executor := newImageAudioContinuationExecutor()
			inferencer := &imageAudioContinuationInferencer{session: session}
			caseDir := filepath.Join(dir, tc.name)
			if err := os.MkdirAll(caseDir, 0o755); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			runErr := make(chan error, 1)
			go func() {
				runErr <- tc.run(ctx, io.Discard, SessionImageRunOptions{
					SessionRunOptions: SessionRunOptions{ModelCatalog: testModelCatalog(),
						RecordPath:        filepath.Join(caseDir, "capture.json"),
						Provider:          config.ProviderOpenAI,
						Model:             "gpt-realtime",
						APIKey:            "test-key",
						ConfigDir:         filepath.Join(caseDir, "config"),
						SessionInferencer: inferencer,
						ToolExecutor:      executor,
						ToolDefinitions: []messages.ToolDefinition{{
							Name:        "read_image",
							Description: "read an image",
						}},
					},
					ImagePaths:   []string{imagePath},
					SystemPrompt: "Use the image tool.",
				}, SessionAudioInput{
					Path:    "synthetic.pcm",
					Present: true,
					Source:  audio.NewSliceSource([]int16{1, 2, 3}),
				}, filepath.Join(caseDir, "recording"))
			}()

			select {
			case <-executor.started:
			case err := <-runErr:
				t.Fatalf("session finished before executing the provider tool call: %v", err)
			case <-ctx.Done():
				t.Fatalf("timed out waiting for provider tool call: %v", ctx.Err())
			}
			executor.releaseExecution()

			select {
			case err := <-runErr:
				if err != nil {
					t.Fatalf("image/audio session: %v", err)
				}
			case <-ctx.Done():
				t.Fatalf("timed out waiting for assistant continuation: %v", ctx.Err())
			}

			if got := len(session.toolResultsCopy()); got != 1 {
				t.Fatalf("provider tool-result messages = %d, want exactly one", got)
			}
			if !session.continuationWasSent() {
				t.Fatal("provider session did not emit the assistant continuation")
			}
			if got := len(session.imageMessagesCopy()); got != 1 {
				t.Fatalf("provider image messages = %d, want exactly one initial image turn", got)
			}
		})
	}
}

func writeImageAudioContinuationFixture(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "input.png")
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encode image fixture: %v", err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}
	return path
}

type imageAudioContinuationInferencer struct {
	session *imageAudioContinuationSession
}

func (i *imageAudioContinuationInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	if !i.session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("image-audio-continuation", "gpt-realtime"),
	}) {
		return nil, ctx.Err()
	}
	return i.session, nil
}

type imageAudioContinuationSession struct {
	receive   *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	closeOnce sync.Once

	toolCallOnce     sync.Once
	resultOnce       sync.Once
	continuationOnce sync.Once
	continuationSent chan struct{}

	mu            sync.Mutex
	imageMessages []messages.Message
	toolResults   []messages.Message
}

func newImageAudioContinuationSession() *imageAudioContinuationSession {
	return &imageAudioContinuationSession{
		receive:          messages.NewTypedBuffer[messages.StreamMessage](64),
		done:             make(chan struct{}),
		continuationSent: make(chan struct{}),
	}
}

func (s *imageAudioContinuationSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	if msg.Type == messages.StreamTypeMessageEnd {
		s.toolCallOnce.Do(func() {
			s.emitToolCall()
		})
	}
	return true
}

func (s *imageAudioContinuationSession) SendMessage(ctx context.Context, msg messages.Message) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	s.mu.Lock()
	s.toolResults = append(s.toolResults, msg)
	s.mu.Unlock()
	s.resultOnce.Do(func() {
		s.emitContinuation()
	})
	return true
}

func (s *imageAudioContinuationSession) SendMessageWithoutResponse(ctx context.Context, msg messages.Message) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	s.mu.Lock()
	s.imageMessages = append(s.imageMessages, msg)
	s.mu.Unlock()
	return true
}

func (s *imageAudioContinuationSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *imageAudioContinuationSession) Done() <-chan struct{} { return s.done }

func (s *imageAudioContinuationSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *imageAudioContinuationSession) emitToolCall() {
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeToolCallStart, Role: messages.RoleAssistant, Value: messages.NewToolCallStartValue(imageAudioContinuationCallID, "read_image")},
		{Type: messages.StreamTypeToolCallEnd, Role: messages.RoleAssistant, Value: messages.NewToolCallEndValue(imageAudioContinuationCallID, "read_image", `{"path":"input.png"}`)},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		if !s.receive.Write(context.Background(), msg) {
			return
		}
	}
}

func (s *imageAudioContinuationSession) emitContinuation() {
	for _, msg := range []messages.StreamMessage{
		{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
		{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue("final response after image tool result")},
		{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
	} {
		if !s.receive.Write(context.Background(), msg) {
			return
		}
	}
	s.continuationOnce.Do(func() { close(s.continuationSent) })
}

func (s *imageAudioContinuationSession) imageMessagesCopy() []messages.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.Message(nil), s.imageMessages...)
}

func (s *imageAudioContinuationSession) toolResultsCopy() []messages.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]messages.Message(nil), s.toolResults...)
}

func (s *imageAudioContinuationSession) continuationWasSent() bool {
	select {
	case <-s.continuationSent:
		return true
	default:
		return false
	}
}

type imageAudioContinuationExecutor struct {
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
}

func newImageAudioContinuationExecutor() *imageAudioContinuationExecutor {
	return &imageAudioContinuationExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (e *imageAudioContinuationExecutor) Execute(ctx context.Context, call messages.ToolCall) (messages.ToolCallResponse, error) {
	e.startOnce.Do(func() { close(e.started) })
	select {
	case <-e.release:
		return messages.ToolCallResponse{
			ToolCallID: call.ID,
			Name:       call.Name,
			ContentParts: []messages.ContentPart{
				messages.TextPart{Text: "image result"},
				messages.ImagePart{Bytes: []byte("image-bytes"), MediaType: "image/png"},
			},
		}, nil
	case <-ctx.Done():
		return messages.ToolCallResponse{}, ctx.Err()
	}
}

func (e *imageAudioContinuationExecutor) releaseExecution() {
	e.releaseOnce.Do(func() { close(e.release) })
}

var _ messages.SessionInferencer = (*imageAudioContinuationInferencer)(nil)
var _ messages.ToolExecutor = (*imageAudioContinuationExecutor)(nil)
var _ SessionImageMessageSender = (*imageAudioContinuationSession)(nil)
var _ SessionImageMessageSenderWithoutResponse = (*imageAudioContinuationSession)(nil)
