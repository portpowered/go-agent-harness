package services_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	services "github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestPrepareSessionImageParts_ReturnsDistinctTypedErrors(t *testing.T) {
	dir := t.TempDir()
	valid := copySessionImageFixture(t, dir, "fixture.png")
	missing := filepath.Join(dir, "missing.png")
	unreadable := filepath.Join(dir, "unreadable.png")
	if err := os.Mkdir(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	unsupported := writeSessionImageFile(t, dir, "unsupported.gif", []byte("GIF89a"))
	disguised := writeSessionImageFile(t, dir, "disguised.png", []byte("plain text, not image bytes"))
	empty := writeSessionImageFile(t, dir, "empty.png", nil)

	metadata := services.SessionImageCapabilities{
		Model:                   "gpt-realtime",
		SupportsImageInput:      true,
		SupportedInputMIMETypes: []string{"image/png", "image/jpeg"},
	}
	cases := []struct {
		name string
		path string
		want error
		as   func(error) bool
	}{
		{
			name: "missing",
			path: missing,
			want: services.ErrSessionImageMissingFile,
			as: func(err error) bool {
				var typed *services.SessionImageMissingFileError
				return errors.As(err, &typed) && typed.Path == missing
			},
		},
		{
			name: "unreadable",
			path: unreadable,
			want: services.ErrSessionImageUnreadableFile,
			as: func(err error) bool {
				var typed *services.SessionImageUnreadableFileError
				return errors.As(err, &typed) && typed.Path == unreadable
			},
		},
		{
			name: "unsupported MIME",
			path: unsupported,
			want: services.ErrSessionImageUnsupportedMIME,
			as: func(err error) bool {
				var typed *services.SessionImageUnsupportedMIMEError
				return errors.As(err, &typed) && typed.Path == unsupported && typed.DetectedMIME == "image/gif"
			},
		},
		{
			name: "disguised content",
			path: disguised,
			want: services.ErrSessionImageInvalidContent,
			as: func(err error) bool {
				var typed *services.SessionImageInvalidContentError
				return errors.As(err, &typed) && typed.Path == disguised && typed.DetectedMIME == "image/png"
			},
		},
		{
			name: "empty",
			path: empty,
			want: services.ErrSessionImageEmptyFile,
			as: func(err error) bool {
				var typed *services.SessionImageEmptyFileError
				return errors.As(err, &typed) && typed.Path == empty
			},
		},
		{
			name: "model capability",
			path: valid,
			want: services.ErrSessionImageCapability,
			as: func(err error) bool {
				var typed *services.SessionImageCapabilityError
				return errors.As(err, &typed) && typed.Model == "text-only-model" && typed.Capability == "image input"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caseMetadata := metadata
			if tc.name == "model capability" {
				caseMetadata.Model = "text-only-model"
				caseMetadata.SupportsImageInput = false
			}
			_, err := services.PrepareSessionImageParts([]string{tc.path}, caseMetadata)
			if err == nil || !errors.Is(err, tc.want) || !tc.as(err) {
				t.Fatalf("error = %v, want typed %v", err, tc.want)
			}
		})
	}
}

func TestSendSessionImageTurn_UsesOneOrderedMessageAfterEarlierTurn(t *testing.T) {
	dir := t.TempDir()
	png := copySessionImageFixture(t, dir, "fixture.png")
	jpeg := copySessionImageFixture(t, dir, "fixture.jpeg")
	parts, err := services.PrepareSessionImageParts([]string{png, jpeg}, services.SessionImageCapabilities{
		Model:              "gpt-realtime",
		SupportsImageInput: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := newRecordingSessionImageSession()
	if !session.SendMessage(context.Background(), messages.NewTextMessage(messages.RoleUser, "earlier text turn")) {
		t.Fatal("earlier turn was not accepted")
	}
	if err := services.SendSessionImageTurn(context.Background(), session, "describe these", parts); err != nil {
		t.Fatalf("SendSessionImageTurn: %v", err)
	}
	if len(session.messages) != 2 {
		t.Fatalf("provider observed %d messages, want earlier turn plus image turn", len(session.messages))
	}
	got := session.messages[1]
	if got.Role != messages.RoleUser || got.TextContent() != "describe these" || len(got.ContentParts) != 3 {
		t.Fatalf("image turn = %#v, want text plus two image parts", got)
	}
	assertSessionImagePart(t, got.ContentParts[1].(messages.ImagePart), mustReadSessionImage(t, png), "image/png")
	assertSessionImagePart(t, got.ContentParts[2].(messages.ImagePart), mustReadSessionImage(t, jpeg), "image/jpeg")
}

func TestSendSessionImageTurn_ImageOnlyMessageHasNoPlaceholderText(t *testing.T) {
	dir := t.TempDir()
	png := copySessionImageFixture(t, dir, "fixture.png")
	parts, err := services.PrepareSessionImageParts([]string{png}, services.SessionImageCapabilities{
		Model:              "gpt-realtime",
		SupportsImageInput: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := newRecordingSessionImageSession()
	if err := services.SendSessionImageTurn(context.Background(), session, "", parts); err != nil {
		t.Fatalf("SendSessionImageTurn: %v", err)
	}
	if len(session.messages) != 1 || session.messages[0].TextContent() != "" || len(session.messages[0].ContentParts) != 1 {
		t.Fatalf("provider message = %#v, want image-only message without placeholder text", session.messages)
	}
}

func TestRunSessionWithImages_ProviderObservesOrderedFixtures(t *testing.T) {
	dir := t.TempDir()
	png := copySessionImageFixture(t, dir, "fixture.png")
	jpeg := copySessionImageFixture(t, dir, "fixture.jpeg")
	session := newRecordingSessionImageSession()
	session.onMessage = func(ctx context.Context) {
		session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()})
		session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
		session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("image-test", "done")})
	}
	inf := &countingSessionImageInferencer{session: session}
	err := services.RunSessionWithImages(context.Background(), io.Discard, services.SessionImageRunOptions{
		SessionRunOptions: services.SessionRunOptions{
			RecordPath: filepath.Join(dir, "capture.json"), Provider: "openai", Model: "gpt-realtime",
			APIKey: "sk-test-key", ConfigDir: filepath.Join(dir, "config"), Prompt: "describe these", SessionInferencer: inf,
		},
		ImagePaths: []string{png, jpeg},
	})
	if err != nil {
		t.Fatalf("RunSessionWithImages: %v", err)
	}
	if inf.connects != 1 || len(session.messages) != 1 {
		t.Fatalf("connects/messages = %d/%d, want 1/1", inf.connects, len(session.messages))
	}
	got := session.messages[0]
	if got.TextContent() != "describe these" || len(got.ContentParts) != 3 {
		t.Fatalf("provider message = %#v, want text plus two images", got)
	}
	assertSessionImagePart(t, got.ContentParts[1].(messages.ImagePart), mustReadSessionImage(t, png), "image/png")
	assertSessionImagePart(t, got.ContentParts[2].(messages.ImagePart), mustReadSessionImage(t, jpeg), "image/jpeg")
}

func TestRunSessionWithImages_ValidatesBeforeConnect(t *testing.T) {
	inf := &countingSessionImageInferencer{}
	missing := filepath.Join(t.TempDir(), "does-not-exist.png")
	err := services.RunSessionWithImages(context.Background(), io.Discard, services.SessionImageRunOptions{
		SessionRunOptions: services.SessionRunOptions{
			RecordPath:        filepath.Join(t.TempDir(), "capture.json"),
			Provider:          "openai",
			Model:             "gpt-realtime",
			APIKey:            "sk-test-key",
			ConfigDir:         t.TempDir(),
			SessionInferencer: inf,
		},
		ImagePaths: []string{missing},
	})
	if err == nil || !errors.Is(err, services.ErrSessionImageMissingFile) {
		t.Fatalf("error = %v, want missing image error", err)
	}
	if inf.connects != 0 {
		t.Fatalf("ConnectSession calls = %d, want zero before image validation", inf.connects)
	}
}

func TestRunSessionWithImages_RejectsConfiguredNonImageModelBeforeConnect(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "models.yaml"), []byte(`
models:
  - name: gpt-realtime
    providers: [openai]
    input_modalities: [text]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	imagePath := copySessionImageFixture(t, dir, "fixture.png")
	inf := &countingSessionImageInferencer{}

	err := services.RunSessionWithImages(context.Background(), io.Discard, services.SessionImageRunOptions{
		SessionRunOptions: services.SessionRunOptions{
			RecordPath:        filepath.Join(dir, "capture.json"),
			Provider:          "openai",
			Model:             "gpt-realtime",
			ModelProvided:     true,
			APIKey:            "sk-test-key",
			ConfigDir:         configDir,
			SessionInferencer: inf,
		},
		ImagePaths: []string{imagePath},
	})
	if err == nil {
		t.Fatal("expected configured non-image model rejection")
	}
	var capabilityErr *services.SessionImageCapabilityError
	if !errors.As(err, &capabilityErr) {
		t.Fatalf("error = %v, want SessionImageCapabilityError", err)
	}
	if capabilityErr.Model != "gpt-realtime" || capabilityErr.Capability != "image input" {
		t.Fatalf("capability error = %+v, want model and image input capability", capabilityErr)
	}
	if inf.connects != 0 {
		t.Fatalf("ConnectSession calls = %d, want zero before capability rejection", inf.connects)
	}
}

func TestSessionCommand_ImageFlagCardinalityAndOrder(t *testing.T) {
	dir := t.TempDir()
	png := copySessionImageFixture(t, dir, "fixture.png")
	jpeg := copySessionImageFixture(t, dir, "fixture.jpeg")
	cases := []struct {
		name   string
		images []string
		mimes  []string
	}{
		{name: "none"},
		{name: "one", images: []string{png}, mimes: []string{"image/png"}},
		{name: "repeated", images: []string{png, jpeg}, mimes: []string{"image/png", "image/jpeg"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := newRecordingSessionImageSession()
			var responseOnce sync.Once
			respond := func(ctx context.Context) {
				responseOnce.Do(func() {
					session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()})
					session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
					session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("command-image-test", "done")})
				})
			}
			session.onMessage = respond
			session.onEvent = func(ctx context.Context, event messages.StreamMessage) {
				if event.Type == messages.StreamTypeTextDelta {
					respond(ctx)
				}
			}
			inf := &countingSessionImageInferencer{session: session}
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = filepath.Join(dir, tc.name, "config")
			command := cli.NewSessionCommand(flags.NewAskFlags(), globalFlags, inf).Generate()
			args := []string{
				"--record", filepath.Join(dir, tc.name, "capture.json"),
				"--provider", "openai",
				"--model", "gpt-realtime",
				"--api-key", "sk-test-key",
			}
			for _, image := range tc.images {
				args = append(args, "--image", image)
			}
			args = append(args, "describe these")
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetArgs(args)
			if err := command.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("execute session command: %v", err)
			}
			if inf.connects != 1 {
				t.Fatalf("ConnectSession calls = %d, want one", inf.connects)
			}
			wantMessages := 0
			if len(tc.images) > 0 {
				wantMessages = 1
			}
			if len(session.messages) != wantMessages {
				t.Fatalf("provider messages = %d, want %d for %d image flags", len(session.messages), wantMessages, len(tc.images))
			}
			if len(tc.images) == 0 {
				if len(session.events) == 0 {
					t.Fatal("text-only command produced no provider events")
				}
				return
			}
			got := session.messages[0]
			if got.TextContent() != "describe these" || len(got.ContentParts) != len(tc.images)+1 {
				t.Fatalf("provider message = %#v, want text plus ordered image parts", got)
			}
			for i, wantMIME := range tc.mimes {
				part, ok := got.ContentParts[i+1].(messages.ImagePart)
				if !ok {
					t.Fatalf("content part %d = %T, want messages.ImagePart", i+1, got.ContentParts[i+1])
				}
				assertSessionImagePart(t, part, mustReadSessionImage(t, tc.images[i]), wantMIME)
			}
		})
	}
}

func copySessionImageFixture(t *testing.T, dir, name string) string {
	t.Helper()
	data := mustReadSessionImage(t, filepath.Join("..", "..", "testdata", "images", name))
	return writeSessionImageFile(t, dir, name, data)
}

func writeSessionImageFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustReadSessionImage(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read image fixture %q: %v", path, err)
	}
	return data
}

func assertSessionImagePart(t *testing.T, got messages.ImagePart, want []byte, mediaType string) {
	t.Helper()
	if got.MediaType != mediaType {
		t.Fatalf("media type = %q, want %q", got.MediaType, mediaType)
	}
	if len(got.Bytes) != len(want) {
		t.Fatalf("image length = %d, want %d", len(got.Bytes), len(want))
	}
	for i := range want {
		if got.Bytes[i] != want[i] {
			t.Fatalf("image byte %d = %d, want %d", i, got.Bytes[i], want[i])
		}
	}
}

type recordingSessionImageSession struct {
	mu        sync.Mutex
	messages  []messages.Message
	events    []messages.StreamMessage
	recv      *messages.TypedBuffer[messages.StreamMessage]
	done      chan struct{}
	once      sync.Once
	onMessage func(context.Context)
	onEvent   func(context.Context, messages.StreamMessage)
}

func newRecordingSessionImageSession() *recordingSessionImageSession {
	return &recordingSessionImageSession{
		recv: messages.NewTypedBuffer[messages.StreamMessage](8),
		done: make(chan struct{}),
	}
}

func (s *recordingSessionImageSession) SendMessage(ctx context.Context, message messages.Message) bool {
	s.mu.Lock()
	s.messages = append(s.messages, message)
	onMessage := s.onMessage
	s.mu.Unlock()
	if onMessage != nil {
		onMessage(ctx)
	}
	return true
}

func (s *recordingSessionImageSession) Send(ctx context.Context, event messages.StreamMessage) bool {
	s.mu.Lock()
	s.events = append(s.events, event)
	onEvent := s.onEvent
	s.mu.Unlock()
	if onEvent != nil {
		onEvent(ctx, event)
	}
	return true
}

func (s *recordingSessionImageSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.recv
}
func (s *recordingSessionImageSession) Done() <-chan struct{} { return s.done }
func (s *recordingSessionImageSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

type countingSessionImageInferencer struct {
	connects int
	session  *recordingSessionImageSession
}

func (i *countingSessionImageInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	i.connects++
	if i.session == nil {
		i.session = newRecordingSessionImageSession()
	}
	i.session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionOpen, Value: messages.NewSessionOpenValue("image-test", "gpt-realtime")})
	return i.session, nil
}
