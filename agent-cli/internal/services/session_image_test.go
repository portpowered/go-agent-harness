package services

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
)

func TestPrepareSessionImageParts_PreservesFixtureBytesAndFlagOrder(t *testing.T) {
	dir := t.TempDir()
	png := copySessionImageFixture(t, dir, "fixture.png")
	jpeg := copySessionImageFixture(t, dir, "fixture.jpeg")
	wantPNG := mustReadSessionImage(t, png)
	wantJPEG := mustReadSessionImage(t, jpeg)

	parts, err := PrepareSessionImageParts([]string{png, jpeg}, SessionImageCapabilities{
		Model:                   "gpt-realtime",
		SupportsImageInput:      true,
		SupportedInputMIMETypes: []string{"image/png", "image/jpeg"},
	})
	if err != nil {
		t.Fatalf("PrepareSessionImageParts: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	assertSessionImagePart(t, parts[0], wantPNG, "image/png")
	assertSessionImagePart(t, parts[1], wantJPEG, "image/jpeg")
}

func TestPrepareSessionImageParts_ReturnsDistinctTypedErrors(t *testing.T) {
	dir := t.TempDir()
	valid := copySessionImageFixture(t, dir, "fixture.png")
	missing := filepath.Join(dir, "missing.png")
	unreadable := filepath.Join(dir, "unreadable.png")
	if err := os.Mkdir(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	unsupported := filepath.Join(dir, "unsupported.gif")
	if err := os.WriteFile(unsupported, []byte("GIF89a"), 0o600); err != nil {
		t.Fatal(err)
	}
	disguised := filepath.Join(dir, "disguised.png")
	if err := os.WriteFile(disguised, []byte("plain text, not image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.png")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	metadata := SessionImageCapabilities{
		Model:                   "gpt-realtime",
		SupportsImageInput:      true,
		SupportedInputMIMETypes: []string{"image/png", "image/jpeg"},
	}
	cases := []struct {
		name     string
		path     string
		want     error
		as       func(error) bool
		wantText string
	}{
		{
			name: "missing",
			path: missing,
			want: ErrSessionImageMissingFile,
			as: func(err error) bool {
				var typed *SessionImageMissingFileError
				return errors.As(err, &typed) && typed.Path == missing
			},
		},
		{
			name: "unreadable",
			path: unreadable,
			want: ErrSessionImageUnreadableFile,
			as: func(err error) bool {
				var typed *SessionImageUnreadableFileError
				return errors.As(err, &typed) && typed.Path == unreadable
			},
		},
		{
			name: "unsupported MIME",
			path: unsupported,
			want: ErrSessionImageUnsupportedMIME,
			as: func(err error) bool {
				var typed *SessionImageUnsupportedMIMEError
				return errors.As(err, &typed) && typed.Path == unsupported && typed.DetectedMIME == "image/gif"
			},
		},
		{
			name: "disguised content",
			path: disguised,
			want: ErrSessionImageInvalidContent,
			as: func(err error) bool {
				var typed *SessionImageInvalidContentError
				return errors.As(err, &typed) && typed.Path == disguised && typed.DetectedMIME == "image/png"
			},
		},
		{
			name: "empty",
			path: empty,
			want: ErrSessionImageEmptyFile,
			as: func(err error) bool {
				var typed *SessionImageEmptyFileError
				return errors.As(err, &typed) && typed.Path == empty
			},
		},
		{
			name: "model capability",
			path: valid,
			want: ErrSessionImageCapability,
			as: func(err error) bool {
				var typed *SessionImageCapabilityError
				return errors.As(err, &typed) && typed.Model == "text-only-model" && typed.Capability == sessionImageCapability
			},
			wantText: "text-only-model",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caseMetadata := metadata
			if tc.name == "model capability" {
				caseMetadata.Model = "text-only-model"
				caseMetadata.SupportsImageInput = false
			}
			err := error(nil)
			_, err = PrepareSessionImageParts([]string{tc.path}, caseMetadata)
			if err == nil || !errors.Is(err, tc.want) || !tc.as(err) {
				t.Fatalf("error = %v, want typed %v for %s", err, tc.want, tc.wantText)
			}
		})
	}
}

func TestSendSessionImageTurn_UsesOneOrderedMessageAfterEarlierTurn(t *testing.T) {
	dir := t.TempDir()
	png := copySessionImageFixture(t, dir, "fixture.png")
	jpeg := copySessionImageFixture(t, dir, "fixture.jpeg")
	parts, err := PrepareSessionImageParts([]string{png, jpeg}, SessionImageCapabilities{
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
	if err := SendSessionImageTurn(context.Background(), session, "describe these", parts); err != nil {
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

func TestSessionImageSession_ImageOnlyMarkerDoesNotSendPlaceholderText(t *testing.T) {
	dir := t.TempDir()
	png := copySessionImageFixture(t, dir, "fixture.png")
	parts, err := PrepareSessionImageParts([]string{png}, SessionImageCapabilities{
		Model:              "gpt-realtime",
		SupportsImageInput: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	inner := newRecordingSessionImageSession()
	wrapped := &sessionImageSession{Session: inner, parts: parts}
	if !wrapped.Send(context.Background(), messages.StreamMessage{
		Type:  messages.StreamTypeTextDelta,
		Value: messages.NewTextDeltaValue(sessionImageOnlyPrompt),
	}) {
		t.Fatal("image-only turn was not accepted")
	}
	if len(inner.messages) != 1 || inner.messages[0].TextContent() != "" || len(inner.messages[0].ContentParts) != 1 {
		t.Fatalf("provider message = %#v, want image-only message without marker text", inner.messages)
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
	err := RunSessionWithImages(context.Background(), io.Discard, SessionImageRunOptions{
		SessionRunOptions: SessionRunOptions{
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

func TestSessionImageSession_NoImageControlForwardsTextOnly(t *testing.T) {
	inner := newRecordingSessionImageSession()
	wrapped := &sessionImageSession{Session: inner}
	if !wrapped.Send(context.Background(), messages.StreamMessage{Type: messages.StreamTypeTextDelta, Value: messages.NewTextDeltaValue("text only")}) {
		t.Fatal("text-only turn was not forwarded")
	}
	if len(inner.events) != 1 || inner.events[0].Type != messages.StreamTypeTextDelta || len(inner.messages) != 0 {
		t.Fatalf("text-only provider observations = %#v/%#v, want one text delta and no full image message", inner.events, inner.messages)
	}
}

func TestRunSessionWithImages_ValidatesBeforeConnect(t *testing.T) {
	inf := &countingSessionImageInferencer{}
	missing := filepath.Join(t.TempDir(), "does-not-exist.png")
	err := RunSessionWithImages(context.Background(), io.Discard, SessionImageRunOptions{
		SessionRunOptions: SessionRunOptions{
			RecordPath:        filepath.Join(t.TempDir(), "capture.json"),
			Provider:          "openai",
			Model:             "gpt-realtime",
			APIKey:            "sk-test-key",
			ConfigDir:         t.TempDir(),
			SessionInferencer: inf,
		},
		ImagePaths: []string{missing},
	})
	if err == nil || !errors.Is(err, ErrSessionImageMissingFile) {
		t.Fatalf("error = %v, want missing image error", err)
	}
	if inf.connects != 0 {
		t.Fatalf("ConnectSession calls = %d, want zero before image validation", inf.connects)
	}
}

func copySessionImageFixture(t *testing.T, dir, name string) string {
	t.Helper()
	data := mustReadSessionImage(t, filepath.Join("..", "..", "testdata", "images", name))
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
	mu       sync.Mutex
	messages []messages.Message
	events   []messages.StreamMessage
	recv     *messages.TypedBuffer[messages.StreamMessage]
	done     chan struct{}
	once     sync.Once
	onMessage func(context.Context)
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

func (s *recordingSessionImageSession) Send(_ context.Context, event messages.StreamMessage) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
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
