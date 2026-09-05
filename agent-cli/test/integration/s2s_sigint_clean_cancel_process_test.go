package integration

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
)

// These tests deliberately drive the built agent binary. The local server is
// a Realtime-shaped provider, so the signal boundary includes Cobra, the CLI
// signal watcher, the provider session, the tool runner, and the recording
// finalizer without credentials or a live provider.
func TestShippedSessionSIGINTAfterToolResultAcceptedFinalizesCleanly(t *testing.T) {
	fixture := newSIGINTRealtimeFixture(sigintToolContinuationFixture, "")
	defer fixture.Close()

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "sigint-fixture.txt"), []byte("fixture tool result\n"), 0o600); err != nil {
		t.Fatalf("write tool fixture: %v", err)
	}
	recordDir := filepath.Join(t.TempDir(), "record")
	result := runSIGINTAgent(t, fixture, workDir, recordDir, "read_file")

	if result.exitCode != 0 {
		t.Fatalf("SIGINT tool-continuation exit code = %d, want 0\nstdout=%q\nstderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	observation := fixture.Snapshot()
	if observation.protocolError != "" {
		t.Fatalf("tool-continuation fixture protocol error: %s", observation.protocolError)
	}
	if observation.functionCallOutputCount != 1 || !observation.continuationRequested {
		t.Fatalf("tool boundary = output_count:%d continuation_requested:%t, want accepted result and outstanding continuation", observation.functionCallOutputCount, observation.continuationRequested)
	}
	if observation.continuationResponseDone {
		t.Fatal("fixture observed a completed continuation after the SIGINT gate")
	}

	assertSIGINTProcessOutput(t, result, "partial")
	assertSIGINTRecordingBundle(t, recordDir, "partial", true, true)
}

func TestShippedSessionSIGINTDuringToolExecutionFinalizesCleanly(t *testing.T) {
	// The marker path must be set before the fixture's HTTP server starts
	// accepting connections: the handler goroutine reads inFlightMarker from
	// a request it can, in principle, start serving as soon as the server is
	// live, and a field write racing that read after construction is a real
	// data race (caught under -race), not just a theoretical one.
	inFlightMarker := filepath.Join(t.TempDir(), "sigint-tool-started")
	fixture := newSIGINTRealtimeFixture(sigintInFlightToolFixture, inFlightMarker)
	defer fixture.Close()

	workDir := t.TempDir()
	recordDir := filepath.Join(t.TempDir(), "record")
	result := runSIGINTAgent(t, fixture, workDir, recordDir, "exec")

	if result.exitCode != 0 {
		t.Fatalf("SIGINT in-flight-tool exit code = %d, want 0\nstdout=%q\nstderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	observation := fixture.Snapshot()
	if observation.protocolError != "" {
		t.Fatalf("in-flight-tool fixture protocol error: %s", observation.protocolError)
	}
	if !observation.toolCallObserved {
		t.Fatal("fixture did not observe the in-flight tool call gate")
	}
	if observation.functionCallOutputCount != 0 {
		t.Fatalf("in-flight tool produced %d provider result(s), want none after user cancellation", observation.functionCallOutputCount)
	}

	assertSIGINTProcessOutput(t, result, "none")
	assertSIGINTRecordingBundle(t, recordDir, "none", false, true)
}

func TestShippedSessionSIGINTWithoutToolFinalizesCleanly(t *testing.T) {
	fixture := newSIGINTRealtimeFixture(sigintNoToolFixture, "")
	defer fixture.Close()

	recordDir := filepath.Join(t.TempDir(), "record")
	result := runSIGINTAgent(t, fixture, t.TempDir(), recordDir, "text")

	if result.exitCode != 0 {
		t.Fatalf("SIGINT no-tool exit code = %d, want 0\nstdout=%q\nstderr=%q", result.exitCode, result.stdout, result.stderr)
	}
	observation := fixture.Snapshot()
	if observation.protocolError != "" {
		t.Fatalf("no-tool fixture protocol error: %s", observation.protocolError)
	}
	if observation.toolCallObserved || observation.functionCallOutputCount != 0 {
		t.Fatalf("no-tool fixture observed tool activity: %+v", observation)
	}

	assertSIGINTProcessOutput(t, result, "partial")
	assertSIGINTRecordingBundle(t, recordDir, "partial", false, false)
}

type sigintFixtureMode string

const (
	sigintToolContinuationFixture sigintFixtureMode = "tool-continuation"
	sigintInFlightToolFixture     sigintFixtureMode = "in-flight-tool"
	sigintNoToolFixture           sigintFixtureMode = "no-tool"
)

type sigintRealtimeFixture struct {
	server         *httptest.Server
	upgrader       websocket.Upgrader
	mode           sigintFixtureMode
	inFlightMarker string
	ready          chan struct{}
	readyOnce      sync.Once

	mu                       sync.Mutex
	protocolError            string
	connectionCount          int
	sessionUpdates           int
	responseCreates          int
	toolCallObserved         bool
	functionCallOutputCount  int
	continuationRequested    bool
	continuationResponseDone bool
}

type sigintRealtimeFixtureSnapshot struct {
	protocolError            string
	connectionCount          int
	sessionUpdates           int
	responseCreates          int
	toolCallObserved         bool
	functionCallOutputCount  int
	continuationRequested    bool
	continuationResponseDone bool
}

// newSIGINTRealtimeFixture starts the fixture's HTTP server only after every
// field its handler can read is already set. inFlightMarker in particular
// must be populated before the server accepts its first connection: setting
// it on the returned fixture afterward is a genuine data race between the
// caller's write and the handler goroutine's read, not just a theoretical one.
func newSIGINTRealtimeFixture(mode sigintFixtureMode, inFlightMarker string) *sigintRealtimeFixture {
	fixture := &sigintRealtimeFixture{
		mode:           mode,
		inFlightMarker: inFlightMarker,
		ready:          make(chan struct{}),
		upgrader:       websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	return fixture
}

func (f *sigintRealtimeFixture) WebSocketURL() string {
	return strings.Replace(f.server.URL, "http://", "ws://", 1)
}

func (f *sigintRealtimeFixture) Close() {
	if f.server != nil {
		f.server.Close()
	}
}

func (f *sigintRealtimeFixture) Snapshot() sigintRealtimeFixtureSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return sigintRealtimeFixtureSnapshot{
		protocolError:            f.protocolError,
		connectionCount:          f.connectionCount,
		sessionUpdates:           f.sessionUpdates,
		responseCreates:          f.responseCreates,
		toolCallObserved:         f.toolCallObserved,
		functionCallOutputCount:  f.functionCallOutputCount,
		continuationRequested:    f.continuationRequested,
		continuationResponseDone: f.continuationResponseDone,
	}
}

func (f *sigintRealtimeFixture) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer hermetic-key" {
		f.fail("authorization header did not arrive through the child process")
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	connection, err := f.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		f.fail("upgrade websocket: " + err.Error())
		return
	}
	defer connection.Close()

	f.mu.Lock()
	f.connectionCount++
	f.mu.Unlock()
	for {
		_, payload, readErr := connection.ReadMessage()
		if readErr != nil {
			return
		}
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
				Output string `json:"output"`
			} `json:"item"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			f.fail("decode client event: " + err.Error())
			return
		}
		switch event.Type {
		case "session.update":
			f.mu.Lock()
			f.sessionUpdates++
			f.mu.Unlock()
			if err := f.send(connection, map[string]any{
				"type":    "session.created",
				"session": map[string]string{"id": "sigint-session", "model": "gpt-realtime"},
			}); err != nil {
				return
			}
			if err := f.send(connection, map[string]any{
				"type":    "session.updated",
				"session": map[string]string{"id": "sigint-session"},
			}); err != nil {
				return
			}
		case "conversation.item.create":
			if event.Item.Type != "function_call_output" {
				continue
			}
			f.mu.Lock()
			f.functionCallOutputCount++
			f.mu.Unlock()
		case "response.create":
			f.mu.Lock()
			f.responseCreates++
			responseNumber := f.responseCreates
			mode := f.mode
			outputCount := f.functionCallOutputCount
			f.mu.Unlock()
			switch mode {
			case sigintToolContinuationFixture:
				if responseNumber == 1 {
					if err := f.sendToolCall(connection); err != nil {
						return
					}
					continue
				}
				if responseNumber != 2 || outputCount != 1 {
					f.fail(fmt.Sprintf("unexpected continuation boundary response=%d output_count=%d", responseNumber, outputCount))
					return
				}
				f.mu.Lock()
				f.continuationRequested = true
				f.mu.Unlock()
				f.readyOnce.Do(func() { close(f.ready) })
			case sigintInFlightToolFixture:
				if responseNumber != 1 {
					f.fail(fmt.Sprintf("unexpected in-flight response.create count %d", responseNumber))
					return
				}
				if err := f.sendInFlightToolCall(connection); err != nil {
					return
				}
			case sigintNoToolFixture:
				if responseNumber != 1 {
					f.fail(fmt.Sprintf("unexpected no-tool response.create count %d", responseNumber))
					return
				}
				if err := f.sendNoToolOutput(connection); err != nil {
					return
				}
			}
		case "response.cancel":
			// The session may close the provider directly when its context is
			// canceled. Both that close and an explicit response.cancel are valid
			// fixture observations; the terminal boundary is asserted from the
			// child output and recording manifest.
		default:
			// User prompt and session metadata are expected client traffic. The
			// focused fixture only gates on response boundaries above.
		}
	}
}

func (f *sigintRealtimeFixture) sendToolCall(connection *websocket.Conn) error {
	const responseID = "response-tool"
	const callID = "call-sigint-tool"
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": responseID},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]string{
			"id":        "item-sigint-tool",
			"type":      "function_call",
			"call_id":   callID,
			"name":      "read_file",
			"arguments": "",
		},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":      "response.function_call_arguments.done",
		"call_id":   callID,
		"name":      "read_file",
		"arguments": `{"path":"sigint-fixture.txt"}`,
	}); err != nil {
		return err
	}
	return f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": responseID, "status": "completed"},
	})
}

func (f *sigintRealtimeFixture) sendInFlightToolCall(connection *websocket.Conn) error {
	const responseID = "response-in-flight-tool"
	const callID = "call-sigint-exec"
	f.mu.Lock()
	f.toolCallObserved = true
	f.mu.Unlock()
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": responseID},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type": "response.output_item.added",
		"item": map[string]string{
			"id":        "item-sigint-exec",
			"type":      "function_call",
			"call_id":   callID,
			"name":      "exec",
			"arguments": "",
		},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":      "response.function_call_arguments.done",
		"call_id":   callID,
		"name":      "exec",
		"arguments": fmt.Sprintf(`{"command":"touch %s && sleep 30"}`, shellQuote(f.inFlightMarker)),
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": responseID, "status": "completed"},
	}); err != nil {
		return err
	}
	f.readyOnce.Do(func() { close(f.ready) })
	return nil
}

func (f *sigintRealtimeFixture) sendNoToolOutput(connection *websocket.Conn) error {
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": "response-no-tool"},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":  "response.output_text.delta",
		"delta": "partial no-tool output",
	}); err != nil {
		return err
	}
	f.readyOnce.Do(func() { close(f.ready) })
	return nil
}

func (f *sigintRealtimeFixture) send(connection *websocket.Conn, event any) error {
	return connection.WriteJSON(event)
}

func (f *sigintRealtimeFixture) fail(message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.protocolError == "" {
		f.protocolError = message
	}
}

type sigintProcessResult struct {
	exitCode int
	stdout   string
	stderr   string
}

type sigintOutputBuffer struct {
	mu     sync.Mutex
	data   bytes.Buffer
	notify chan struct{}
}

func newSIGINTOutputBuffer() *sigintOutputBuffer {
	return &sigintOutputBuffer{notify: make(chan struct{})}
}

func (b *sigintOutputBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.data.Write(data)
	close(b.notify)
	b.notify = make(chan struct{})
	return n, err
}

func (b *sigintOutputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func (b *sigintOutputBuffer) waitFor(fragment string, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		b.mu.Lock()
		if strings.Contains(b.data.String(), fragment) {
			b.mu.Unlock()
			return true
		}
		notify := b.notify
		b.mu.Unlock()
		select {
		case <-notify:
		case <-timer.C:
			return false
		}
	}
}

func runSIGINTAgent(t *testing.T, fixture *sigintRealtimeFixture, workDir, recordDir, toolName string) sigintProcessResult {
	t.Helper()
	configDir := filepath.Join(t.TempDir(), "config")
	args := []string{
		"--config-dir", configDir,
		"session",
		"--record-dir", recordDir,
		"--wait-for-close",
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "hermetic-key",
		"--base-url", fixture.WebSocketURL(),
		"--system-prompt", "confirm in five words or fewer",
		"--max-duration", "30s",
		"sigint " + toolName,
	}
	command := exec.Command(buildAgentBinary(t), args...)
	command.Dir = workDir
	command.Env = append(os.Environ(),
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"ALL_PROXY=http://127.0.0.1:1",
	)
	stdout := newSIGINTOutputBuffer()
	var stderr bytes.Buffer
	command.Stdout = stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start agent process: %v", err)
	}

	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	waitForSIGINTFixture(t, command, fixture.ready, wait, fixture.mode, fixture.inFlightMarker, stdout)

	select {
	case err := <-wait:
		return sigintProcessResult{exitCode: sigintExitCode(err), stdout: stdout.String(), stderr: stderr.String()}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-wait
		t.Fatalf("agent process did not exit within 10s after SIGINT\nstdout=%q\nstderr=%q", stdout.String(), stderr.String())
		return sigintProcessResult{}
	}
}

func waitForSIGINTFixture(t *testing.T, command *exec.Cmd, ready <-chan struct{}, wait <-chan error, mode sigintFixtureMode, inFlightMarker string, stdout *sigintOutputBuffer) {
	t.Helper()
	select {
	case <-ready:
	case err := <-wait:
		t.Fatalf("agent exited before the SIGINT gate: %v", err)
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-wait
		t.Fatal("agent did not reach the fixture SIGINT gate within 10s")
	}
	if mode == sigintInFlightToolFixture && !waitForSIGINTFile(inFlightMarker, 5*time.Second) {
		_ = command.Process.Kill()
		<-wait
		t.Fatalf("agent did not enter the in-flight tool before the SIGINT gate; stdout=%q", stdout.String())
	}
	if stdout != nil && mode == sigintNoToolFixture && !stdout.waitFor("partial no-tool output", 5*time.Second) {
		_ = command.Process.Kill()
		<-wait
		t.Fatalf("agent did not flush the no-tool output gate\nstdout=%q", stdout.String())
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		_ = command.Process.Kill()
		<-wait
		t.Fatalf("send SIGINT to agent: %v", err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func waitForSIGINTFile(path string, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return true
		} else if !os.IsNotExist(err) {
			return false
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			return false
		}
	}
}

func sigintExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}

func assertSIGINTProcessOutput(t *testing.T, result sigintProcessResult, outputState string) {
	t.Helper()
	combined := result.stdout + result.stderr
	wantTerminal := "[session terminal: classification=user_cancelled terminal_reason=cancellation terminal_provenance=cli output_state=" + outputState + "]"
	if strings.Count(combined, "[session terminal:") != 1 || !strings.Contains(combined, wantTerminal) {
		t.Fatalf("SIGINT output terminal = %q, want exactly %q", combined, wantTerminal)
	}
	if strings.Count(combined, "classification=user_cancelled") != 1 {
		t.Fatalf("SIGINT output user_cancelled count = %d, want one: %q", strings.Count(combined, "classification=user_cancelled"), combined)
	}
	for _, forbidden := range []string{
		"context canceled",
		"session error",
		"scheduled audio session ended",
		"scheduled input incomplete",
		"unresolved tool",
		"tool continuation was not completed",
		"image tool continuation was not completed",
	} {
		if strings.Contains(strings.ToLower(combined), forbidden) {
			t.Fatalf("SIGINT output contains failure-shaped text %q: %q", forbidden, combined)
		}
	}
}

func assertSIGINTRecordingBundle(t *testing.T, recordDir, outputState string, wantToolResult, wantToolCall bool) {
	t.Helper()
	entries, err := os.ReadDir(recordDir)
	if err != nil {
		t.Fatalf("read SIGINT recording directory: %v", err)
	}
	if len(entries) != 6 {
		t.Fatalf("SIGINT recording top-level entries = %d, want six final entries (including audio-trace): %v", len(entries), entries)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".staging-") {
			t.Fatalf("recording retained staging entry %q", entry.Name())
		}
	}

	manifestBytes, err := os.ReadFile(filepath.Join(recordDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read SIGINT manifest: %v", err)
	}
	var manifest transcript.RecordingManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode SIGINT manifest: %v", err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("validate SIGINT manifest: %v", err)
	}
	if manifest.Terminal == nil {
		t.Fatal("SIGINT manifest omitted terminal summary")
	}
	wantTerminal := map[string]string{
		"reason":              "user_cancelled",
		"classification":      "user_cancelled",
		"terminal_reason":     "cancellation",
		"terminal_provenance": "cli",
		"output_state":        outputState,
	}
	gotTerminal := map[string]string{
		"reason":              manifest.Terminal.Reason,
		"classification":      manifest.Terminal.Classification,
		"terminal_reason":     string(manifest.Terminal.TerminalReason),
		"terminal_provenance": string(manifest.Terminal.TerminalProvenance),
		"output_state":        string(manifest.Terminal.OutputState),
	}
	for field, want := range wantTerminal {
		if gotTerminal[field] != want {
			t.Fatalf("SIGINT manifest terminal %s = %q, want %q", field, gotTerminal[field], want)
		}
	}

	wantArtifacts := map[string]bool{
		"client.transcript.jsonl": false,
		"agent.transcript.jsonl":  false,
		"session-log.jsonl":       false,
	}
	for _, artifact := range manifest.Artifacts {
		if _, ok := wantArtifacts[artifact.Path]; !ok {
			t.Fatalf("SIGINT manifest contains unexpected artifact %q", artifact.Path)
		}
		data, err := os.ReadFile(filepath.Join(recordDir, filepath.FromSlash(artifact.Path)))
		if err != nil {
			t.Fatalf("read SIGINT artifact %q: %v", artifact.Path, err)
		}
		digest := sha256.Sum256(data)
		if got := hex.EncodeToString(digest[:]); got != artifact.SHA256 {
			t.Fatalf("SIGINT artifact hash %q = %s, want %s", artifact.Path, got, artifact.SHA256)
		}
		wantArtifacts[artifact.Path] = true
		if bytes.Contains(data, []byte("hermetic-key")) {
			t.Fatalf("SIGINT artifact %q leaked the fixture credential", artifact.Path)
		}
	}
	for path, found := range wantArtifacts {
		if !found {
			t.Fatalf("SIGINT manifest omitted artifact %q", path)
		}
	}

	assertSIGINTTranscriptJSONL(t, filepath.Join(recordDir, "client.transcript.jsonl"))
	assertSIGINTTranscriptJSONL(t, filepath.Join(recordDir, "agent.transcript.jsonl"))
	assertSIGINTSessionLog(t, filepath.Join(recordDir, "session-log.jsonl"), wantToolResult, wantToolCall)
	var anyJSON map[string]any
	if err := json.Unmarshal(manifestBytes, &anyJSON); err != nil {
		t.Fatalf("decode final SIGINT manifest JSON: %v", err)
	}

	partialFound := false
	if err := filepath.WalkDir(recordDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.Contains(path, ".staging-") {
			t.Fatalf("recording retained partial staging path %q", path)
		}
		if entry.Type().IsRegular() && strings.Contains(entry.Name(), ".tmp") {
			partialFound = true
		}
		return nil
	}); err != nil {
		t.Fatalf("walk SIGINT recording bundle: %v", err)
	}
	if partialFound {
		t.Fatal("SIGINT recording retained a temporary artifact")
	}
}

func assertSIGINTTranscriptJSONL(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open transcript artifact %q: %v", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		if _, err := transcript.Decode(scanner.Bytes()); err != nil {
			t.Fatalf("decode transcript artifact %q line %d: %v", path, count+1, err)
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan transcript artifact %q: %v", path, err)
	}
	if count == 0 {
		t.Fatalf("transcript artifact %q is empty", path)
	}
}

func assertSIGINTSessionLog(t *testing.T, path string, wantToolResult, wantToolCall bool) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open session log %q: %v", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	lineCount := 0
	toolCalls := 0
	toolResults := 0
	failedResults := 0
	for scanner.Scan() {
		var entry struct {
			ToolEvents []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Content string `json:"content"`
			} `json:"tool_events"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode session log line %d: %v", lineCount+1, err)
		}
		for _, event := range entry.ToolEvents {
			switch event.Type {
			case "tool_call":
				toolCalls++
			case "tool_result":
				toolResults++
				if event.Status == "failed" {
					failedResults++
				}
				if strings.Contains(strings.ToLower(event.Content), "canceled") {
					t.Fatalf("session log recorded a failure-shaped canceled tool result: %q", event.Content)
				}
			}
		}
		lineCount++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan session log: %v", err)
	}
	if lineCount == 0 {
		t.Fatal("session log is empty")
	}
	if wantToolResult && (toolCalls != 1 || toolResults != 1 || failedResults != 0) {
		t.Fatalf("completed tool session log calls=%d results=%d failed=%d, want one successful result", toolCalls, toolResults, failedResults)
	}
	if wantToolCall && !wantToolResult && (toolCalls != 1 || toolResults != 0 || failedResults != 0) {
		t.Fatalf("in-flight tool session log calls=%d results=%d failed=%d, want one unresolved user-canceled call", toolCalls, toolResults, failedResults)
	}
	if !wantToolCall && (toolCalls != 0 || toolResults != 0) {
		t.Fatalf("no-tool session log unexpectedly contains tool events: calls=%d results=%d", toolCalls, toolResults)
	}
}
