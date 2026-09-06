package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
)

// TestSessionCLI_StartupAnnouncementRouting is a shipped-process regression
// for the binary PCM stdout boundary. The provider emits one tool call and a
// known PCM response; the fixture sends session.created only once so repeated
// session-update behavior remains outside this routing proof.
func TestSessionCLI_StartupAnnouncementRouting(t *testing.T) {
	const (
		toolPath    = "announcement-proof.txt"
		toolContent = "startup routing proof\n"
	)
	fixture := newStartupAnnouncementFixture(toolPath, toolContent)
	defer fixture.Close()

	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if err := os.MkdirAll(sandbox, 0o700); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	announcementWorkDir, err := filepath.EvalSymlinks(sandbox)
	if err != nil {
		t.Fatalf("resolve announcement workdir: %v", err)
	}
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}

	binaryPath := startupAnnouncementBinary(t)
	result, runErr := probe.RunDuplexSession(context.Background(), probe.DuplexSessionConfig{
		BinaryPath:       binaryPath,
		RecordDir:        filepath.Join(t.TempDir(), "record"),
		WorkingDirectory: sandbox,
		ConfigDir:        configDir,
		Provider:         "openai",
		Model:            "gpt-realtime",
		BaseURL:          fixture.WebSocketURL(),
		APIKey:           "hermetic-key",
		MaxDuration:      5 * time.Second,
		FrameDuration:    5 * time.Millisecond,
		AdditionalArgs:   []string{"--wait-for-close"},
		Segments:         []probe.DuplexAudioSegment{{ID: "startup-proof", PCM16: startupAnnouncementFrame()}},
	})

	t.Logf("startup routing evidence: binary_sha256=%s stdout_sha256=%x stdout_hex=%x stdout_bytes=%d stderr_sha256=%x stderr_bytes=%d stderr=%q command=%q", fileSHA256(t, binaryPath), sha256.Sum256(result.Stdout), result.Stdout, len(result.Stdout), sha256.Sum256(result.Stderr), len(result.Stderr), result.Stderr, result.Command)
	if runErr != nil {
		t.Fatalf("startup announcement routing run: %v\nresult=%+v\nstdout=%x\nstderr=%s", runErr, result, result.Stdout, result.Stderr)
	}
	if result.ExitCode != 0 || !result.ChildWaited || !result.InputFinished || !result.InputClosed || !result.StdoutClosed || !result.StderrClosed {
		t.Fatalf("process lifecycle result = %+v, want a clean shipped-process shutdown", result)
	}

	wantPCM := []byte{0x01, 0x23, 0x45, 0x67}
	if !bytes.Equal(result.Stdout, wantPCM) {
		t.Fatalf("stdout = %x, want byte-exact PCM %x; stderr=%q", result.Stdout, wantPCM, result.Stderr)
	}
	if strings.Contains(string(result.Stdout), "Filesystem scope:") || strings.Contains(string(result.Stdout), "Tools:") {
		t.Fatalf("startup announcement leaked back onto PCM stdout: %q", result.Stdout)
	}
	for _, announcement := range []string{
		"Filesystem scope: workdir=" + announcementWorkDir + "; additional_allowed_roots=none",
		"Filesystem tools are confined to the effective workdir and additional allowed roots; protected system and credential reads remain denied",
		"Tools: append_file, edit_file, exec, list_dir, read_file, read_image, write_file",
	} {
		if !strings.Contains(string(result.Stderr), announcement) {
			t.Fatalf("stderr = %q, missing established startup announcement %q", result.Stderr, announcement)
		}
	}

	observation := fixture.Snapshot()
	if observation.ProtocolError != "" {
		t.Fatalf("provider fixture protocol error: %s", observation.ProtocolError)
	}
	if observation.ConnectionCount != 1 || observation.SessionUpdates < 1 || observation.SessionUpdates > 2 || observation.ToolResults != 1 || observation.FinalResponses != 1 {
		t.Fatalf("provider effects = %+v, want one connection, bounded session setup, one tool result, and one final response", observation)
	}
	got, err := os.ReadFile(filepath.Join(sandbox, toolPath))
	if err != nil {
		t.Fatalf("read provider-requested file: %v", err)
	}
	if string(got) != toolContent {
		t.Fatalf("provider-requested file = %q, want %q", got, toolContent)
	}
}

func startupAnnouncementBinary(t *testing.T) string {
	t.Helper()
	if path := strings.TrimSpace(os.Getenv("C06_STARTUP_ANNOUNCEMENT_BINARY")); path != "" {
		return path
	}
	return buildAgentBinary(t)
}

func startupAnnouncementFrame() []byte {
	frame := make([]byte, probe.DefaultDuplexFrameSamples*2)
	for index := range frame {
		frame[index] = 1
	}
	return frame
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read binary %s: %v", path, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

type startupAnnouncementFixture struct {
	server      *httptest.Server
	upgrader    websocket.Upgrader
	toolPath    string
	toolContent string

	mu              sync.Mutex
	connectionCount int
	sessionUpdates  int
	toolResults     int
	finalResponses  int
	protocolError   string
}

type startupAnnouncementObservation struct {
	ConnectionCount int
	SessionUpdates  int
	ToolResults     int
	FinalResponses  int
	ProtocolError   string
}

func newStartupAnnouncementFixture(toolPath, toolContent string) *startupAnnouncementFixture {
	fixture := &startupAnnouncementFixture{
		upgrader:    websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		toolPath:    toolPath,
		toolContent: toolContent,
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	return fixture
}

func (f *startupAnnouncementFixture) WebSocketURL() string {
	return strings.Replace(f.server.URL, "http://", "ws://", 1)
}

func (f *startupAnnouncementFixture) Close() {
	if f.server != nil {
		f.server.Close()
	}
}

func (f *startupAnnouncementFixture) Snapshot() startupAnnouncementObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return startupAnnouncementObservation{
		ConnectionCount: f.connectionCount,
		SessionUpdates:  f.sessionUpdates,
		ToolResults:     f.toolResults,
		FinalResponses:  f.finalResponses,
		ProtocolError:   f.protocolError,
	}
}

func (f *startupAnnouncementFixture) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer hermetic-key" {
		f.failProtocol("authorization header did not arrive through the supported child environment")
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	connection, err := f.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		f.failProtocol("upgrade websocket: " + err.Error())
		return
	}
	defer connection.Close()
	f.mu.Lock()
	f.connectionCount++
	f.mu.Unlock()

	readySent := false
	toolCallSent := false
	finalSent := false
	for {
		_, payload, readErr := connection.ReadMessage()
		if readErr != nil {
			return
		}
		var event struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
			Item  struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			f.failProtocol("decode client event: " + err.Error())
			return
		}
		switch event.Type {
		case "session.update":
			f.mu.Lock()
			f.sessionUpdates++
			f.mu.Unlock()
			if readySent {
				// This fixture intentionally does not echo duplicate session.created
				// events; repeated-update behavior is not part of this slice.
				continue
			}
			readySent = true
			if err := f.send(connection, map[string]any{
				"type":    "session.created",
				"session": map[string]string{"id": "startup-routing", "model": "gpt-realtime"},
			}); err != nil {
				return
			}
			if err := f.send(connection, map[string]any{
				"type":    "session.updated",
				"session": map[string]string{"id": "startup-routing"},
			}); err != nil {
				return
			}
		case "input_audio_buffer.append":
			audio, decodeErr := base64.StdEncoding.DecodeString(event.Audio)
			if decodeErr != nil {
				f.failProtocol("decode input audio: " + decodeErr.Error())
				return
			}
			if toolCallSent || startupAnnouncementSilent(audio) {
				continue
			}
			toolCallSent = true
			if err := f.sendToolCall(connection); err != nil {
				return
			}
		case "conversation.item.create":
			if event.Item.Type != "function_call_output" {
				continue
			}
			if strings.TrimSpace(event.Item.Output) == "" {
				f.failProtocol("empty function call output")
				return
			}
			f.mu.Lock()
			f.toolResults++
			f.mu.Unlock()
			if finalSent {
				continue
			}
			finalSent = true
			if err := f.sendFinalResponse(connection); err != nil {
				return
			}
		}
	}
}

func (f *startupAnnouncementFixture) sendToolCall(connection *websocket.Conn) error {
	callID := "call-startup-routing"
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": "response-startup-tool"},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]string{
			"type": "function_call", "id": callID, "call_id": callID,
			"name": "write_file", "arguments": "",
		},
	}); err != nil {
		return err
	}
	arguments := fmt.Sprintf(`{"path":%q,"content":%q}`, f.toolPath, f.toolContent)
	if err := f.send(connection, map[string]any{
		"type": "response.function_call_arguments.done", "call_id": callID,
		"name": "write_file", "arguments": arguments,
	}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": "response-startup-tool", "status": "completed"},
	})
}

func (f *startupAnnouncementFixture) sendFinalResponse(connection *websocket.Conn) error {
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": "response-startup-final"},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":   "response.output_audio.delta",
		"delta":  base64.StdEncoding.EncodeToString([]byte{0x01, 0x23, 0x45, 0x67}),
		"format": "pcm16",
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.output_audio.done",
		"response": map[string]string{"id": "response-startup-final"},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": "response-startup-final", "status": "completed"},
	}); err != nil {
		return err
	}
	f.mu.Lock()
	f.finalResponses++
	f.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	return f.send(connection, map[string]string{"type": "session.closed", "reason": "startup_routing_complete"})
}

func (f *startupAnnouncementFixture) send(connection *websocket.Conn, event any) error {
	return connection.WriteJSON(event)
}

func (f *startupAnnouncementFixture) failProtocol(message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.protocolError == "" {
		f.protocolError = message
	}
}

func startupAnnouncementSilent(audio []byte) bool {
	if len(audio) == 0 {
		return true
	}
	for _, value := range audio {
		if value != 0 {
			return false
		}
	}
	return true
}
