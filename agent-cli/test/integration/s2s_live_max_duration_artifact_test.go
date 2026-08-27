package integration

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

func TestSessionCommand_MaxDurationKeepsRawCaptureAndSidecarHonest(t *testing.T) {
	fixture := newMaxDurationWebSocketFixture()
	server := httptest.NewServer(http.HandlerFunc(fixture.handle))
	defer server.Close()

	recordPath := filepath.Join(t.TempDir(), "cutoff.session.json")
	agentCLI, err := wire.InitializeMockAgentCLI(
		&mockToolExecutor{},
		&mockInferencerError{err: os.ErrNotExist},
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{
		"session",
		"--record", recordPath,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--base-url", strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/realtime",
		"--max-duration", "100ms",
		"respond while streaming",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- rootCmd.ExecuteContext(ctx) }()
	select {
	case <-fixture.partialSent:
	case <-time.After(3 * time.Second):
		t.Fatal("hermetic provider did not send its mid-response output")
	}
	if err := <-errCh; err != nil {
		t.Fatalf("max-duration CLI returned an error: %v; stdout=%q stderr=%q", err, testWriter.StdoutString(), testWriter.StderrString())
	}

	stdout := testWriter.StdoutString()
	if !strings.Contains(stdout, "partial response") || !strings.Contains(stdout, "terminal_reason=max_duration") {
		t.Fatalf("CLI output did not report the bounded partial response: %q", stdout)
	}

	capture, err := gwtesting.LoadSessionCapture(recordPath)
	if err != nil {
		t.Fatalf("load raw provider capture: %v", err)
	}
	if !captureHasWireRecord(capture, gwtesting.DirectionServerToClient, "response.output_text.delta") {
		t.Fatalf("raw capture omitted the observed response delta: %+v", capture.Records)
	}
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient && (record.Type == "response.done" || record.Type == "session.closed") {
			t.Fatalf("raw capture fabricated provider terminal %q: %+v", record.Type, record)
		}
	}

	sidecarPath := strings.TrimSuffix(recordPath, filepath.Ext(recordPath)) + ".jsonl"
	terminal := readSessionDurationSidecarTerminal(t, sidecarPath)
	if terminal.count != 1 {
		t.Fatalf("duration sidecar terminal count = %d, want exactly one", terminal.count)
	}
	for field, want := range map[string]string{
		"reason":              "max_duration",
		"classification":      "max_duration",
		"terminal_reason":     "max_duration",
		"terminal_provenance": "loop",
		"output_state":        "partial",
	} {
		if got := terminal.fields[field]; got != want {
			t.Fatalf("duration sidecar %s = %q, want %q", field, got, want)
		}
	}
}

func TestSessionCommand_PromptOnlyRecordDirFinalizesCompleteBundle(t *testing.T) {
	const responseText = "prompt-only response"
	sessionInferencer := &promptOnlyRecordingSessionInferencer{
		events: []messages.StreamMessage{
			{Type: messages.StreamTypeMessageStart, Role: messages.RoleAssistant, Value: messages.NewMessageStartValue()},
			{Type: messages.StreamTypeTextStart, Role: messages.RoleAssistant, Value: messages.NewTextStartValue()},
			{Type: messages.StreamTypeTextDelta, Role: messages.RoleAssistant, Value: messages.NewTextDeltaValue(responseText)},
			{Type: messages.StreamTypeTextEnd, Role: messages.RoleAssistant, Value: messages.NewTextEndValue()},
			{Type: messages.StreamTypeAudioDelta, Role: messages.RoleAssistant, Value: messages.NewAudioDeltaValue([]byte{0x10, 0x00})},
			{Type: messages.StreamTypeMessageEnd, Role: messages.RoleAssistant, Value: messages.NewMessageEndValue(messages.TokenUsage{})},
			{Type: messages.StreamTypeSessionClose, Value: messages.NewSessionCloseValue("prompt-only-session", "complete")},
		},
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: os.ErrNotExist},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	recordDir := filepath.Join(t.TempDir(), "prompt-only-recording")
	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetArgs([]string{
		"session",
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--prompt", "prompt-only request",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("prompt-only record-dir CLI returned an error: %v; stdout=%q stderr=%q", err, testWriter.StdoutString(), testWriter.StderrString())
	}
	if !strings.Contains(testWriter.StdoutString(), responseText) {
		t.Fatalf("CLI output = %q, want %q", testWriter.StdoutString(), responseText)
	}

	wantFiles := []string{
		"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "manifest.json", "audio/out-000.pcm",
	}
	for _, relative := range wantFiles {
		data, err := os.ReadFile(filepath.Join(recordDir, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read prompt-only artifact %q: %v", relative, err)
		}
		if len(data) == 0 {
			t.Fatalf("prompt-only artifact %q is empty", relative)
		}
	}
	if _, err := os.Stat(filepath.Join(recordDir, "audio", "in-000.pcm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prompt-only input audio stat error = %v, want absent", err)
	}

	var manifest transcript.RecordingManifest
	manifestBytes, err := os.ReadFile(filepath.Join(recordDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read prompt-only manifest: %v", err)
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode prompt-only manifest: %v", err)
	}
	wantArtifacts := []string{
		"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "audio/out-000.pcm",
	}
	if len(manifest.Artifacts) != len(wantArtifacts) {
		t.Fatalf("prompt-only manifest artifact count = %d, want %d", len(manifest.Artifacts), len(wantArtifacts))
	}
	wantArtifactSet := make(map[string]struct{}, len(wantArtifacts))
	for _, path := range wantArtifacts {
		wantArtifactSet[path] = struct{}{}
	}
	seenArtifacts := make(map[string]struct{}, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if _, duplicate := seenArtifacts[artifact.Path]; duplicate {
			t.Fatalf("prompt-only manifest repeats artifact %q", artifact.Path)
		}
		seenArtifacts[artifact.Path] = struct{}{}
		if _, expected := wantArtifactSet[artifact.Path]; !expected {
			t.Fatalf("prompt-only manifest contains unexpected artifact %q", artifact.Path)
		}
		if strings.HasPrefix(artifact.Path, "audio/in-") {
			t.Fatalf("prompt-only manifest contains fabricated input audio %q", artifact.Path)
		}
		data, err := os.ReadFile(filepath.Join(recordDir, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read prompt-only manifest artifact %q: %v", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			t.Fatalf("prompt-only manifest hash for %q = %s, want %s", artifact.Path, artifact.SHA256, got)
		}
	}
	for path := range wantArtifactSet {
		if _, found := seenArtifacts[path]; !found {
			t.Fatalf("prompt-only manifest omits artifact %q", path)
		}
	}

	logBytes, err := os.ReadFile(filepath.Join(recordDir, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("read prompt-only session log: %v", err)
	}
	if !strings.Contains(string(logBytes), responseText) || !strings.Contains(string(logBytes), "prompt-only request") {
		t.Fatalf("prompt-only session log = %q, want input and response text", logBytes)
	}
}

type promptOnlyRecordingSessionInferencer struct {
	events []messages.StreamMessage
}

func (i *promptOnlyRecordingSessionInferencer) ConnectSession(ctx context.Context) (messages.Session, error) {
	session := &promptOnlyRecordingSession{
		events:  append([]messages.StreamMessage(nil), i.events...),
		receive: messages.NewTypedBuffer[messages.StreamMessage](32),
		done:    make(chan struct{}),
	}
	if !session.receive.Write(ctx, messages.StreamMessage{
		Type:  messages.StreamTypeSessionOpen,
		Value: messages.NewSessionOpenValue("prompt-only-session", "openai"),
	}) {
		return nil, ctx.Err()
	}
	for _, event := range session.events {
		if !session.receive.Write(ctx, event) {
			return nil, ctx.Err()
		}
	}
	return session, nil
}

type promptOnlyRecordingSession struct {
	events  []messages.StreamMessage
	receive *messages.TypedBuffer[messages.StreamMessage]
	done    chan struct{}
	once    sync.Once
}

func (s *promptOnlyRecordingSession) Send(ctx context.Context, msg messages.StreamMessage) bool {
	return true
}

func (s *promptOnlyRecordingSession) Receive() *messages.TypedBuffer[messages.StreamMessage] {
	return s.receive
}

func (s *promptOnlyRecordingSession) Done() <-chan struct{} { return s.done }

func (s *promptOnlyRecordingSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

type maxDurationWebSocketFixture struct {
	upgrader    websocket.Upgrader
	partialSent chan struct{}
	partialOnce sync.Once
}

func newMaxDurationWebSocketFixture() *maxDurationWebSocketFixture {
	return &maxDurationWebSocketFixture{
		upgrader:    websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		partialSent: make(chan struct{}),
	}
}

func (f *maxDurationWebSocketFixture) handle(writer http.ResponseWriter, request *http.Request) {
	connection, err := f.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	defer connection.Close()

	sessionCreated := false
	responseStarted := false
	for {
		_, payload, err := connection.ReadMessage()
		if err != nil {
			return
		}
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return
		}
		switch event.Type {
		case "session.update":
			if sessionCreated {
				continue
			}
			if err := connection.WriteJSON(map[string]any{
				"type":    "session.created",
				"session": map[string]string{"id": "sess_max_duration", "model": "gpt-realtime"},
			}); err != nil {
				return
			}
			sessionCreated = true
		case "response.create":
			if responseStarted {
				continue
			}
			responseStarted = true
			if err := connection.WriteJSON(map[string]string{"type": "response.created"}); err != nil {
				return
			}
			if err := connection.WriteJSON(map[string]string{
				"type":  "response.output_text.delta",
				"delta": "partial response",
			}); err != nil {
				return
			}
			f.partialOnce.Do(func() { close(f.partialSent) })
		}
	}
}

func captureHasWireRecord(capture gwtesting.SessionCapture, direction gwtesting.SessionEventDirection, typeName string) bool {
	for _, record := range capture.Records {
		if record.Direction == direction && record.Type == typeName {
			return true
		}
	}
	return false
}

type durationSidecarTerminal struct {
	count  int
	fields map[string]string
}

func readSessionDurationSidecarTerminal(t *testing.T, path string) durationSidecarTerminal {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open duration sidecar %q: %v", path, err)
	}
	defer file.Close()

	terminal := durationSidecarTerminal{fields: make(map[string]string)}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		record, err := transcript.Decode(scanner.Bytes())
		if err != nil {
			t.Fatalf("decode duration sidecar record: %v", err)
		}
		var event struct {
			Type  string         `json:"type"`
			Value map[string]any `json:"value"`
		}
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			t.Fatalf("decode duration sidecar event: %v", err)
		}
		if event.Type != "SESSION.CLOSE" && event.Type != "session.close" {
			continue
		}
		terminal.count++
		for key, value := range event.Value {
			if text, ok := value.(string); ok {
				terminal.fields[key] = text
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan duration sidecar %q: %v", path, err)
	}
	return terminal
}
