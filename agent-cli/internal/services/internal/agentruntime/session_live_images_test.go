package agentruntime_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	agentruntime "github.com/portpowered/go-agent-harness/agent-cli/internal/services/internal/agentruntime"
	sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/transport/cli"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	audioiowire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/audioio/wire"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/sessionturn"
	sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"
)

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
	err := agentruntime.RunSessionWithImages(context.Background(), io.Discard, agentruntime.SessionImageRunOptions{
		SessionRunOptions: agentruntime.SessionRunOptions{AudioService: audioiowire.NewService(), ModelCatalog: testModelCatalog(),
			Provider: "openai", Model: "gpt-realtime",
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
	assertSessionImagePart(t, requireImagePart(t, got.ContentParts[1]), mustReadSessionImage(t, png), "image/png")
	assertSessionImagePart(t, requireImagePart(t, got.ContentParts[2]), mustReadSessionImage(t, jpeg), "image/jpeg")
}

func TestRunSessionWithImages_ValidatesBeforeConnect(t *testing.T) {
	inf := &countingSessionImageInferencer{}
	missing := filepath.Join(t.TempDir(), "does-not-exist.png")
	err := agentruntime.RunSessionWithImages(context.Background(), io.Discard, agentruntime.SessionImageRunOptions{
		SessionRunOptions: agentruntime.SessionRunOptions{AudioService: audioiowire.NewService(), ModelCatalog: testModelCatalog(),
			RecordPath:        filepath.Join(t.TempDir(), "capture.json"),
			Provider:          "openai",
			Model:             "gpt-realtime",
			APIKey:            "sk-test-key",
			ConfigDir:         t.TempDir(),
			SessionInferencer: inf,
		},
		ImagePaths: []string{missing},
	})
	if err == nil || !errors.Is(err, sessionturn.ErrImageMissingFile) {
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
	err := agentruntime.RunSessionWithImages(context.Background(), io.Discard, agentruntime.SessionImageRunOptions{
		SessionRunOptions: agentruntime.SessionRunOptions{AudioService: audioiowire.NewService(), ModelCatalog: testModelCatalog(),
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
	var capabilityErr *sessionturn.ImageCapabilityError
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
			session, inf := runImageSessionCommand(t, filepath.Join(dir, tc.name), tc.images)
			if inf.connects != 1 {
				t.Fatalf("ConnectSession calls = %d, want one", inf.connects)
			}
			if len(tc.images) == 0 {
				if len(session.messages) != 0 || len(session.events) == 0 {
					t.Fatalf("text-only command messages/events = %d/%d, want none/some", len(session.messages), len(session.events))
				}
				return
			}
			assertImageCommandMessage(t, session, tc.images, tc.mimes)
		})
	}
}

// runImageSessionCommand executes the session command once against an
// injected provider that answers the first user turn.
func runImageSessionCommand(t *testing.T, dir string, images []string) (*recordingSessionImageSession, *countingSessionImageInferencer) {
	t.Helper()
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
	globalFlags.ConfigDirPath = filepath.Join(dir, "config")
	if err := os.MkdirAll(globalFlags.ConfigDirPath, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	command := cli.NewSessionCommand(flags.NewAskFlags(), globalFlags, newInjectedSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: inf}), nil).Generate()
	args := []string{"--record", filepath.Join(dir, "capture.json"), "--provider", "openai", "--model", "gpt-realtime", "--api-key", "sk-test-key"}
	for _, image := range images {
		args = append(args, "--image", image)
	}
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs(append(args, "describe these"))
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute session command: %v", err)
	}
	return session, inf
}

func assertImageCommandMessage(t *testing.T, session *recordingSessionImageSession, images, mimes []string) {
	t.Helper()
	if len(session.messages) != 1 {
		t.Fatalf("provider messages = %d, want one for %d image flags", len(session.messages), len(images))
	}
	got := session.messages[0]
	if got.TextContent() != "describe these" || len(got.ContentParts) != len(images)+1 {
		t.Fatalf("provider message = %#v, want text plus ordered image parts", got)
	}
	for i, wantMIME := range mimes {
		part, ok := got.ContentParts[i+1].(messages.ImagePart)
		if !ok {
			t.Fatalf("content part %d = %T, want messages.ImagePart", i+1, got.ContentParts[i+1])
		}
		assertSessionImagePart(t, part, mustReadSessionImage(t, images[i]), wantMIME)
	}
}

func TestSessionCommand_ImagePreservesDurationAndAudioFlags(t *testing.T) {
	dir := t.TempDir()
	imagePath := copySessionImageFixture(t, dir, "fixture.png")
	cases := []struct {
		name          string
		flags         []string
		wantArtifacts bool
	}{
		{name: "duration", flags: []string{"--max-duration", "1s"}, wantArtifacts: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capturePath := filepath.Join(dir, tc.name, "capture.json")
			session := newRecordingSessionImageSession()
			session.onMessage = func(ctx context.Context) {
				session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageStart, Value: messages.NewMessageStartValue()})
				session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeAudioDelta, Value: messages.NewAudioDeltaValue([]byte{1, 0})})
				session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeMessageEnd, Value: messages.NewMessageEndValue(messages.TokenUsage{})})
				session.recv.Write(ctx, messages.StreamMessage{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("image-flags", "done")})
			}
			inf := &countingSessionImageInferencer{session: session}
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = filepath.Join(dir, tc.name, "config")
			command := cli.NewSessionCommand(flags.NewAskFlags(), globalFlags, newInjectedSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}, SessionInferencer: inf}), nil).Generate()
			args := []string{
				"--record", capturePath,
				"--provider", "openai",
				"--model", "gpt-realtime",
				"--api-key", "sk-test-key",
				"--image", imagePath,
			}
			args = append(args, tc.flags...)
			args = append(args, "describe this")
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetArgs(args)
			if err := command.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("execute session command: %v", err)
			}
			if len(session.messages) != 1 || session.messages[0].TextContent() != "describe this" {
				t.Fatalf("provider messages = %#v, want one image turn with the positional prompt", session.messages)
			}
			if tc.wantArtifacts {
				for _, path := range []string{
					filepath.Join(dir, tc.name, "capture.wav"),
					filepath.Join(dir, tc.name, "capture.jsonl"),
				} {
					if _, err := os.Stat(path); err != nil {
						t.Fatalf("duration artifact %q: %v", path, err)
					}
				}
			}
		})
	}
}

func copySessionImageFixture(t *testing.T, dir, name string) string {
	t.Helper()
	data := mustReadSessionImage(t, filepath.Join("..", "..", "..", "..", "testdata", "images", name))
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
	return s.sendMessage(ctx, message)
}

func (s *recordingSessionImageSession) SendMessageWithoutResponse(ctx context.Context, message messages.Message) bool {
	return s.sendMessage(ctx, message)
}

func (s *recordingSessionImageSession) sendMessage(ctx context.Context, message messages.Message) bool {
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

// requireImagePart asserts one content part is an image.
func requireImagePart(t *testing.T, part messages.ContentPart) messages.ImagePart {
	t.Helper()
	image, ok := part.(messages.ImagePart)
	if !ok {
		t.Fatalf("content part = %T, want messages.ImagePart", part)
	}
	return image
}
