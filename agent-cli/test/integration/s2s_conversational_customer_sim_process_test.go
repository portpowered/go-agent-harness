package integration

import (
	"bytes"
	"context"
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

// TestShippedSessionProcessDuplexConversation drives the built agent binary
// through the public session command. The local websocket server is a small
// Realtime-shaped provider: it keeps response one open, waits for the shipped
// client to cancel it when correction audio arrives, then completes two more
// responses. The runner's output gates make the ordering observable without
// buffering the conversation or restarting the child.
func TestShippedSessionProcessDuplexConversation(t *testing.T) {
	fixture := newCustomerSimulationFixture()
	defer fixture.Close()
	recordDir := filepath.Join(t.TempDir(), "record")

	result, err := probe.RunDuplexSession(context.Background(), probe.DuplexSessionConfig{
		BinaryPath:       buildAgentBinary(t),
		RecordDir:        recordDir,
		WorkingDirectory: filepath.Join(t.TempDir(), "work"),
		ConfigDir:        filepath.Join(t.TempDir(), "config"),
		Provider:         "openai",
		Model:            "gpt-realtime",
		BaseURL:          fixture.WebSocketURL(),
		APIKey:           "hermetic-key",
		MaxDuration:      8 * time.Second,
		FrameDuration:    5 * time.Millisecond,
		Segments: []probe.DuplexAudioSegment{
			{ID: "first-speech", PCM16: customerSimulationFrame(1)},
			{ID: "first-silence", SilenceFor: 5 * time.Millisecond, WaitForOutputBytes: 4},
			{ID: "correction-speech", PCM16: customerSimulationFrame(2), WaitForOutputBytes: 4},
			{ID: "second-silence", SilenceFor: 5 * time.Millisecond, WaitForOutputBytes: 8},
			{ID: "final-speech", PCM16: customerSimulationFrame(3), WaitForOutputBytes: 8},
		},
	})
	if err != nil {
		t.Fatalf("shipped session duplex run: %v\nresult=%+v\nstdout=%x\nstderr=%s", err, result, result.Stdout, result.Stderr)
	}

	observation := fixture.Snapshot()
	if observation.protocolError != "" {
		t.Fatalf("fake provider protocol error: %s", observation.protocolError)
	}
	if observation.connectionCount != 1 {
		t.Fatalf("provider connections = %d, want one process-owned session", observation.connectionCount)
	}
	if observation.sessionUpdates != 1 {
		t.Fatalf("session.update count = %d, want one handshake", observation.sessionUpdates)
	}
	if len(observation.appends) != 5 {
		t.Fatalf("provider audio appends = %d, want five frames across three speech/silence segments", len(observation.appends))
	}
	if observation.silentAppends != 2 || observation.committedTurns != 2 {
		t.Fatalf("provider VAD-shaped boundaries = silent appends %d, committed turns %d; want two of each", observation.silentAppends, observation.committedTurns)
	}
	if observation.nonSilentAppends != 3 || !observation.finalAppendAfterFirst {
		t.Fatalf("provider speech progression = non-silent appends %d, final-after-first=%t; want three later frames on one connection", observation.nonSilentAppends, observation.finalAppendAfterFirst)
	}
	if observation.cancelCount != 1 {
		t.Fatalf("provider response.cancel count = %d, want one interruption", observation.cancelCount)
	}
	if !observation.firstOutputAt.Before(observation.correctionAt) || !observation.cancelAt.Before(observation.correctionAt) {
		t.Fatalf("barge-in ordering first_output=%s cancel=%s correction=%s", observation.firstOutputAt, observation.cancelAt, observation.correctionAt)
	}
	if got, want := strings.Join(observation.responseTerminalStatuses, ","), "cancelled,completed,completed"; got != want {
		t.Fatalf("provider response terminal statuses = %q, want %q", got, want)
	}

	if result.ExitCode != 0 || !result.ChildWaited || !result.InputFinished || !result.InputClosed || !result.StdoutClosed || !result.StderrClosed {
		t.Fatalf("process lifecycle result = %+v, want a completed, fully reaped child", result)
	}
	if len(result.Input) != 5 || len(result.Output) == 0 || len(result.Stdout) < 12 {
		t.Fatalf("stream evidence input=%d output_reads=%d stdout_bytes=%d, want five input frames and three audio responses", len(result.Input), len(result.Output), len(result.Stdout))
	}
	// Stdout is the PCM transport, so even a setup diagnostic corrupts audio
	// and can prematurely release the customer's output-gated interruption.
	wantAudio := []byte{1, 0x10, 0x20, 0x30, 2, 0x10, 0x20, 0x30, 3, 0x10, 0x20, 0x30}
	if !bytes.Equal(result.Stdout, wantAudio) {
		t.Fatalf("captured stdout = %x, want only ordered PCM %x", result.Stdout, wantAudio)
	}
	if strings.Contains(result.Command, "hermetic-key") || strings.Contains(strings.Join(result.SanitizedArgs, "\x00"), "hermetic-key") {
		t.Fatalf("API key leaked into process evidence: command=%q args=%q", result.Command, result.SanitizedArgs)
	}
	for _, forbidden := range []string{"--audio-in-turn", "--api-key"} {
		if containsIntegrationString(result.SanitizedArgs, forbidden) {
			t.Fatalf("runner unexpectedly used forbidden boundary/credential argument %q: %v", forbidden, result.SanitizedArgs)
		}
	}
	for _, required := range []string{"--audio-in", "--audio-out", "--record-dir", "--provider", "--model", "--max-duration"} {
		if !containsIntegrationString(result.SanitizedArgs, required) {
			t.Fatalf("sanitized process args = %v, missing required %q", result.SanitizedArgs, required)
		}
	}

	entries, err := os.ReadDir(recordDir)
	if err != nil {
		t.Fatalf("read shipped session record directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("shipped session record directory is empty")
	}
}

type customerSimulationFixture struct {
	server   *httptest.Server
	upgrader websocket.Upgrader

	mu                       sync.Mutex
	connectionCount          int
	sessionUpdates           int
	appends                  []customerSimulationAppend
	silentAppends            int
	nonSilentAppends         int
	committedTurns           int
	cancelCount              int
	firstOutputAt            time.Time
	cancelAt                 time.Time
	correctionAt             time.Time
	finalAppendAfterFirst    bool
	responseTerminalStatuses []string
	protocolError            string
}

type customerSimulationAppend struct {
	at     time.Time
	silent bool
}

type customerSimulationSnapshot struct {
	connectionCount          int
	sessionUpdates           int
	appends                  []customerSimulationAppend
	silentAppends            int
	nonSilentAppends         int
	committedTurns           int
	cancelCount              int
	firstOutputAt            time.Time
	cancelAt                 time.Time
	correctionAt             time.Time
	finalAppendAfterFirst    bool
	responseTerminalStatuses []string
	protocolError            string
}

func newCustomerSimulationFixture() *customerSimulationFixture {
	fixture := &customerSimulationFixture{
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	return fixture
}

func (f *customerSimulationFixture) WebSocketURL() string {
	return strings.Replace(f.server.URL, "http://", "ws://", 1)
}

func (f *customerSimulationFixture) Close() {
	if f.server != nil {
		f.server.Close()
	}
}

func (f *customerSimulationFixture) Snapshot() customerSimulationSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return customerSimulationSnapshot{
		connectionCount:          f.connectionCount,
		sessionUpdates:           f.sessionUpdates,
		appends:                  append([]customerSimulationAppend(nil), f.appends...),
		silentAppends:            f.silentAppends,
		nonSilentAppends:         f.nonSilentAppends,
		committedTurns:           f.committedTurns,
		cancelCount:              f.cancelCount,
		firstOutputAt:            f.firstOutputAt,
		cancelAt:                 f.cancelAt,
		correctionAt:             f.correctionAt,
		finalAppendAfterFirst:    f.finalAppendAfterFirst,
		responseTerminalStatuses: append([]string(nil), f.responseTerminalStatuses...),
		protocolError:            f.protocolError,
	}
}

func (f *customerSimulationFixture) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer hermetic-key" {
		f.failProtocol("authorization header did not arrive through the child environment")
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

	activeResponse := ""
	cancelPending := false
	responseNumber := 0
	for {
		_, payload, readErr := connection.ReadMessage()
		if readErr != nil {
			return
		}
		var event struct {
			Type  string `json:"type"`
			Audio string `json:"audio"`
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
			if err := f.send(connection, map[string]any{
				"type":    "session.created",
				"session": map[string]string{"id": "customer-simulation", "model": "gpt-realtime"},
			}); err != nil {
				return
			}
			if err := f.send(connection, map[string]any{
				"type":    "session.updated",
				"session": map[string]string{"id": "customer-simulation"},
			}); err != nil {
				return
			}
		case "input_audio_buffer.append":
			audio, decodeErr := base64.StdEncoding.DecodeString(event.Audio)
			if decodeErr != nil {
				f.failProtocol("decode input audio: " + decodeErr.Error())
				return
			}
			silent := customerSimulationSilent(audio)
			now := time.Now()
			f.mu.Lock()
			f.appends = append(f.appends, customerSimulationAppend{at: now, silent: silent})
			if silent {
				f.silentAppends++
				f.committedTurns++
			} else {
				f.nonSilentAppends++
				if f.nonSilentAppends == 2 {
					f.correctionAt = now
				}
				if f.nonSilentAppends >= 3 && len(f.appends) > 1 {
					f.finalAppendAfterFirst = f.appends[len(f.appends)-2].at.Before(now)
				}
			}
			f.mu.Unlock()
			if silent {
				if err := f.send(connection, map[string]string{"type": "input_audio_buffer.speech_stopped"}); err != nil {
					return
				}
				if err := f.send(connection, map[string]string{"type": "input_audio_buffer.committed"}); err != nil {
					return
				}
				continue
			}

			if cancelPending {
				if activeResponse == "" {
					f.failProtocol("response.cancel arrived without an active response")
					return
				}
				if err := f.send(connection, map[string]any{
					"type":     "response.output_audio.done",
					"response": map[string]string{"id": activeResponse},
				}); err != nil {
					return
				}
				if err := f.send(connection, map[string]any{
					"type":     "response.done",
					"response": map[string]string{"id": activeResponse, "status": "cancelled"},
				}); err != nil {
					return
				}
				f.recordResponseTerminal("cancelled")
				activeResponse = ""
				cancelPending = false
			}

			responseNumber++
			activeResponse = fmt.Sprintf("response-%d", responseNumber)
			complete := responseNumber > 1
			if err := f.sendResponse(connection, activeResponse, responseNumber, complete); err != nil {
				return
			}
			if complete {
				activeResponse = ""
				if responseNumber == 3 {
					time.Sleep(20 * time.Millisecond)
					_ = f.send(connection, map[string]string{"type": "session.closed", "reason": "customer_simulation_complete"})
				}
			}
		case "response.cancel":
			f.mu.Lock()
			f.cancelCount++
			f.cancelAt = time.Now()
			f.mu.Unlock()
			cancelPending = true
		case "input_audio_buffer.commit", "response.create":
			// The fixture models server-VAD-shaped committed turns from the
			// open stdin stream. The product's EOF commit is still accepted
			// after the final response has been emitted.
		default:
			// session.created and provider metadata are server-to-client only;
			// unknown client events are harmless for this focused fixture.
		}
	}
}

func (f *customerSimulationFixture) send(connection *websocket.Conn, event any) error {
	return connection.WriteJSON(event)
}

func (f *customerSimulationFixture) sendResponse(connection *websocket.Conn, responseID string, responseNumber int, complete bool) error {
	f.mu.Lock()
	if responseNumber == 1 {
		f.firstOutputAt = time.Now()
	}
	f.mu.Unlock()
	if err := f.send(connection, map[string]any{"type": "input_audio_buffer.speech_started"}); err != nil {
		return err
	}
	audio := []byte{byte(responseNumber), 0x10, 0x20, 0x30}
	if err := f.send(connection, map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": responseID},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":   "response.output_audio.delta",
		"delta":  base64.StdEncoding.EncodeToString(audio),
		"format": "pcm16",
	}); err != nil {
		return err
	}
	if !complete {
		return nil
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.output_audio.done",
		"response": map[string]string{"id": responseID},
	}); err != nil {
		return err
	}
	if err := f.send(connection, map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": responseID, "status": "completed"},
	}); err != nil {
		return err
	}
	f.recordResponseTerminal("completed")
	return nil
}

func (f *customerSimulationFixture) recordResponseTerminal(status string) {
	f.mu.Lock()
	f.responseTerminalStatuses = append(f.responseTerminalStatuses, status)
	f.mu.Unlock()
}

func (f *customerSimulationFixture) failProtocol(message string) {
	f.mu.Lock()
	if f.protocolError == "" {
		f.protocolError = message
	}
	f.mu.Unlock()
}

func customerSimulationFrame(seed byte) []byte {
	frame := make([]byte, probe.DefaultDuplexFrameSamples*2)
	for index := range frame {
		frame[index] = seed
	}
	return frame
}

func customerSimulationSilent(audio []byte) bool {
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

func containsIntegrationString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
