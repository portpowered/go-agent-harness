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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/probe"
)

func TestSessionCLI_DuplexPCMOverlap(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	aToB, bToA := v8LoudFrames(t, v8AudioFixturePath(t, "overlap_16k.wav"))
	run := runV8Duplex(t, aToB, bToA, false)
	if err := verifyV8Run(run, map[string][]byte{"A-to-B": aToB, "B-to-A": bToA}); err != nil {
		t.Fatalf("positive v8 duplex proof failed: %v", err)
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "positive duplex run")
	t.Logf("v8 positive evidence: shared clock base=%s tick_duration=%s overlap_tick=%d final_tick=%d crossings=%d", run.base.Format(time.RFC3339Nano), v8TickDuration, v8OverlapTick, run.finalTick, len(run.crossings))
}

func TestSessionCLI_DuplexPCMOverlapRejectsSilenceControl(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	aToB, bToA := v8LoudFrames(t, v8AudioFixturePath(t, "overlap_16k.wav"))
	run := runV8Duplex(t, aToB, bToA, true)
	err := verifyV8Run(run, map[string][]byte{"A-to-B": aToB, "B-to-A": bToA})
	if err == nil {
		t.Fatal("silence negative control passed the positive audio verification")
	}
	diagnostic := err.Error()
	if !strings.Contains(diagnostic, "A-to-B") || !strings.Contains(diagnostic, fmt.Sprintf("logical tick %d", v8OverlapTick)) || !strings.Contains(diagnostic, "RMS") || !strings.Contains(diagnostic, "hash=") {
		t.Fatalf("negative control diagnostic lacks direction/tick/hash/RMS details: %v", err)
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "silence negative control")
	t.Logf("v8 silence negative control rejected as expected: %v", err)
}

func TestSessionCLI_DuplexPCMMultiTurnSchedule(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
	run := runV8MultiTurnDuplex(t, frames, frames)
	if err := verifyV8MultiTurnRun(run, frames, frames); err != nil {
		t.Fatalf("positive v8 multi-turn duplex proof failed: %v", err)
	}
	t.Logf("v8 multi-turn evidence: final_tick=%d crossings=%d A_runtime=%d B_runtime=%d", run.finalTick, len(run.crossings), len(run.harnesses["A"].Runtime), len(run.harnesses["B"].Runtime))
	assertV8GoroutinesSettled(t, baselineGoroutines, "multi-turn duplex run")
}

func TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnAudioControl(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
	run := runV8MultiTurnDuplex(t, frames, frames)
	if err := mutateV8ViewPayload(&run, "B/agent", 2, frames[0]); err != nil {
		t.Fatalf("mutate later-turn PCM control: %v", err)
	}
	err := verifyV8MultiTurnRun(run, frames, frames)
	if err == nil {
		t.Fatal("later-turn PCM negative control passed the positive multi-turn verifier")
	}
	diagnostic := err.Error()
	for _, part := range []string{"B/agent", "turn 2", "PCM", "expected hash=", "observed hash="} {
		if !strings.Contains(diagnostic, part) {
			t.Fatalf("later-turn PCM diagnostic lacks %q: %v", part, err)
		}
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "later-turn PCM negative control")
	t.Logf("v8 later-turn PCM negative control rejected as expected: %v", err)
}

func TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnTranscriptControl(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
	run := runV8MultiTurnDuplex(t, frames, frames)
	if err := mutateV8TranscriptMarker(&run, "B", 2, "A transcript turn 2"); err != nil {
		t.Fatalf("mutate later-turn transcript control: %v", err)
	}
	err := verifyV8MultiTurnRun(run, frames, frames)
	if err == nil {
		t.Fatal("later-turn transcript negative control passed the positive multi-turn verifier")
	}
	diagnostic := err.Error()
	for _, part := range []string{"harness B", "turn 2", "transcript", "expected=", "observed="} {
		if !strings.Contains(diagnostic, part) {
			t.Fatalf("later-turn transcript diagnostic lacks %q: %v", part, err)
		}
	}
	assertV8GoroutinesSettled(t, baselineGoroutines, "later-turn transcript negative control")
	t.Logf("v8 later-turn transcript negative control rejected as expected: %v", err)
}

func TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls(t *testing.T) {
	t.Run("missing commit", func(t *testing.T) {
		baselineGoroutines := runtime.NumGoroutine()
		frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
		run := runV8MultiTurnDuplex(t, frames, frames)
		if err := dropV8InputCommit(&run, "A", 2); err != nil {
			t.Fatalf("drop later-turn input commit control: %v", err)
		}
		err := verifyV8MultiTurnRun(run, frames, frames)
		if err == nil {
			t.Fatal("missing later-turn input commit negative control passed the positive multi-turn verifier")
		}
		diagnostic := err.Error()
		for _, part := range []string{"harness A", "B-to-A", "B-turn-2", "input commit", "expected=2", "observed=3"} {
			if !strings.Contains(diagnostic, part) {
				t.Fatalf("missing input commit diagnostic lacks %q: %v", part, err)
			}
		}
		assertV8GoroutinesSettled(t, baselineGoroutines, "missing later-turn input commit negative control")
		t.Logf("v8 missing later-turn input commit negative control rejected as expected: %v", err)
	})

	t.Run("cross-attributed commit", func(t *testing.T) {
		baselineGoroutines := runtime.NumGoroutine()
		frames := v8LoudFrameSet(t, v8AudioFixturePath(t, "overlap_16k.wav"), v8MultiTurnCount)
		run := runV8MultiTurnDuplex(t, frames, frames)
		if err := mutateV8InputCommitPayload(&run, "A", 2, frames[0]); err != nil {
			t.Fatalf("mutate later-turn input commit control: %v", err)
		}
		err := verifyV8MultiTurnRun(run, frames, frames)
		if err == nil {
			t.Fatal("cross-attributed later-turn input commit negative control passed the positive multi-turn verifier")
		}
		diagnostic := err.Error()
		for _, part := range []string{"harness A", "B-to-A", "B-turn-2", "input commit", "expected hash=", "observed hash="} {
			if !strings.Contains(diagnostic, part) {
				t.Fatalf("cross-attributed input commit diagnostic lacks %q: %v", part, err)
			}
		}
		assertV8GoroutinesSettled(t, baselineGoroutines, "cross-attributed later-turn input commit negative control")
		t.Logf("v8 cross-attributed later-turn input commit negative control rejected as expected: %v", err)
	})
}

// TestSessionCLI_StartupAnnouncementRouting is a shipped-process regression
// for the binary PCM stdout boundary. The provider emits one tool call and a
// known PCM response; the fixture sends session.created only once so repeated
// session-update behavior remains outside this routing proof.
func TestSessionCLI_StartupAnnouncementRouting(t *testing.T) {
	proof := runStartupAnnouncementProof(t)
	assertStartupAnnouncementLifecycle(t, proof)
	assertStartupAnnouncementStreams(t, proof)
	assertStartupAnnouncementEffects(t, proof)
}

type startupAnnouncementProof struct {
	result              probe.DuplexRunResult
	runErr              error
	fixture             startupAnnouncementObservation
	binaryPath          string
	sandbox             string
	announcementWorkDir string
	toolPath            string
	toolContent         string
}

func runStartupAnnouncementProof(t *testing.T) startupAnnouncementProof {
	t.Helper()
	const (
		toolPath    = "announcement-proof.txt"
		toolContent = "startup routing proof\n"
	)
	fixture := newStartupAnnouncementFixture(toolPath, toolContent)
	defer fixture.Close()

	sandbox := startupAnnouncementDirectory(t, "sandbox")
	configDir := startupAnnouncementDirectory(t, "config")
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
	wantPCM := startupAnnouncementPCM()
	writeStartupAnnouncementCaptures(t, os.Getenv("C06_STARTUP_ANNOUNCEMENT_CAPTURE_DIR"), result.Stdout, result.Stderr, wantPCM)
	t.Logf("startup routing evidence: binary_sha256=%s stdout_sha256=%x stdout_hex=%x stdout_bytes=%d stderr_sha256=%x stderr_bytes=%d stderr=%q command=%q", fileSHA256(t, binaryPath), sha256.Sum256(result.Stdout), result.Stdout, len(result.Stdout), sha256.Sum256(result.Stderr), len(result.Stderr), result.Stderr, result.Command)
	return startupAnnouncementProof{
		result:              result,
		runErr:              runErr,
		fixture:             fixture.Snapshot(),
		binaryPath:          binaryPath,
		sandbox:             sandbox,
		announcementWorkDir: sandbox,
		toolPath:            toolPath,
		toolContent:         toolContent,
	}
}

func startupAnnouncementDirectory(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("create startup announcement directory: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve startup announcement directory: %v", err)
	}
	return resolved
}

func startupAnnouncementPCM() []byte {
	return []byte{0x01, 0x23, 0x45, 0x67}
}

func assertStartupAnnouncementLifecycle(t *testing.T, proof startupAnnouncementProof) {
	t.Helper()
	if proof.runErr != nil {
		t.Fatalf("startup announcement routing run: %v\nresult=%+v\nstdout=%x\nstderr=%s", proof.runErr, proof.result, proof.result.Stdout, proof.result.Stderr)
	}
	if proof.result.ExitCode != 0 || !proof.result.ChildWaited || !proof.result.InputFinished || !proof.result.InputClosed || !proof.result.StdoutClosed || !proof.result.StderrClosed {
		t.Fatalf("process lifecycle result = %+v, want a clean shipped-process shutdown", proof.result)
	}
}

func assertStartupAnnouncementStreams(t *testing.T, proof startupAnnouncementProof) {
	t.Helper()
	wantPCM := startupAnnouncementPCM()
	if !bytes.Equal(proof.result.Stdout, wantPCM) {
		t.Fatalf("stdout = %x, want byte-exact PCM %x; stderr=%q", proof.result.Stdout, wantPCM, proof.result.Stderr)
	}
	if strings.Contains(string(proof.result.Stdout), "Filesystem scope:") || strings.Contains(string(proof.result.Stdout), "Tools:") {
		t.Fatalf("startup announcement leaked back onto PCM stdout: %q", proof.result.Stdout)
	}
	for _, announcement := range []string{
		"Filesystem scope: workdir=" + proof.announcementWorkDir + "; additional_allowed_roots=none",
		"Filesystem tools are confined to the effective workdir and additional allowed roots; protected system and credential reads remain denied",
		"Tools: append_file, edit_file, exec, list_dir, read_file, read_image, write_file",
	} {
		if !strings.Contains(string(proof.result.Stderr), announcement) {
			t.Fatalf("stderr = %q, missing established startup announcement %q", proof.result.Stderr, announcement)
		}
	}
}

func assertStartupAnnouncementEffects(t *testing.T, proof startupAnnouncementProof) {
	t.Helper()
	if proof.fixture.ProtocolError != "" {
		t.Fatalf("provider fixture protocol error: %s", proof.fixture.ProtocolError)
	}
	if proof.fixture.ConnectionCount != 1 || proof.fixture.SessionUpdates < 1 || proof.fixture.SessionUpdates > 2 || proof.fixture.ToolResults != 1 || proof.fixture.FinalResponses != 1 {
		t.Fatalf("provider effects = %+v, want one connection, bounded session setup, one tool result, and one final response", proof.fixture)
	}
	got, err := os.ReadFile(filepath.Join(proof.sandbox, proof.toolPath))
	if err != nil {
		t.Fatalf("read provider-requested file: %v", err)
	}
	if string(got) != proof.toolContent {
		t.Fatalf("provider-requested file = %q, want %q", got, proof.toolContent)
	}
}

func writeStartupAnnouncementCaptures(t *testing.T, captureDir string, stdout, stderr, expectedPCM []byte) {
	t.Helper()
	captureDir = strings.TrimSpace(captureDir)
	if captureDir == "" {
		return
	}
	if err := os.MkdirAll(captureDir, 0o700); err != nil {
		t.Fatalf("create startup announcement capture directory: %v", err)
	}
	for name, data := range map[string][]byte{
		"expected-pcm.raw": expectedPCM,
		"stderr.raw":       stderr,
		"stdout.raw":       stdout,
	} {
		if err := os.WriteFile(filepath.Join(captureDir, name), data, 0o600); err != nil {
			t.Fatalf("write startup announcement capture %s: %v", name, err)
		}
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

type startupAnnouncementEvent struct {
	Type  string `json:"type"`
	Audio string `json:"audio"`
	Item  struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output string `json:"output"`
	} `json:"item"`
}

type startupAnnouncementConversation struct {
	readySent    bool
	toolCallSent bool
	finalSent    bool
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
	defer func() {
		if err := connection.Close(); err != nil {
			f.failProtocol("close websocket: " + err.Error())
		}
	}()
	f.recordConnection()
	conversation := startupAnnouncementConversation{}
	for {
		if err := f.handleMessage(connection, &conversation); err != nil {
			return
		}
	}
}

func (f *startupAnnouncementFixture) recordConnection() {
	f.mu.Lock()
	f.connectionCount++
	f.mu.Unlock()
}

func (f *startupAnnouncementFixture) handleMessage(connection *websocket.Conn, conversation *startupAnnouncementConversation) error {
	_, payload, err := connection.ReadMessage()
	if err != nil {
		return err
	}
	var event startupAnnouncementEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		f.failProtocol("decode client event: " + err.Error())
		return err
	}
	switch event.Type {
	case "session.update":
		return f.handleSessionUpdate(connection, conversation)
	case "input_audio_buffer.append":
		return f.handleAudioAppend(connection, conversation, event.Audio)
	case "conversation.item.create":
		return f.handleFunctionCallOutput(connection, conversation, event.Item)
	default:
		return nil
	}
}

func (f *startupAnnouncementFixture) handleSessionUpdate(connection *websocket.Conn, conversation *startupAnnouncementConversation) error {
	f.mu.Lock()
	f.sessionUpdates++
	f.mu.Unlock()
	if conversation.readySent {
		return nil
	}
	conversation.readySent = true
	return f.sendSessionReady(connection)
}

func (f *startupAnnouncementFixture) sendSessionReady(connection *websocket.Conn) error {
	if err := f.send(connection, map[string]any{
		"type":    "session.created",
		"session": map[string]string{"id": "startup-routing", "model": "gpt-realtime"},
	}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{
		"type":    "session.updated",
		"session": map[string]string{"id": "startup-routing"},
	})
}

func (f *startupAnnouncementFixture) handleAudioAppend(connection *websocket.Conn, conversation *startupAnnouncementConversation, encodedAudio string) error {
	audio, err := base64.StdEncoding.DecodeString(encodedAudio)
	if err != nil {
		f.failProtocol("decode input audio: " + err.Error())
		return err
	}
	if conversation.toolCallSent || startupAnnouncementSilent(audio) {
		return nil
	}
	conversation.toolCallSent = true
	return f.sendToolCall(connection)
}

func (f *startupAnnouncementFixture) handleFunctionCallOutput(connection *websocket.Conn, conversation *startupAnnouncementConversation, item struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}) error {
	if item.Type != "function_call_output" {
		return nil
	}
	if strings.TrimSpace(item.Output) == "" {
		f.failProtocol("empty function call output")
		return fmt.Errorf("empty function call output")
	}
	f.mu.Lock()
	f.toolResults++
	f.mu.Unlock()
	if conversation.finalSent {
		return nil
	}
	conversation.finalSent = true
	return f.sendFinalResponse(connection)
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
		"delta":  base64.StdEncoding.EncodeToString(startupAnnouncementPCM()),
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
