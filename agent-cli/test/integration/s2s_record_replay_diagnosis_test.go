package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/wavio"
)

const (
	recordReplayPrompt       = "record-replay prompt PROBE_RECORD_REPLAY"
	recordReplayInstructions = "recorded instructions PROBE_INSTRUCTIONS"
	recordReplayCorruption   = "CORRUPTED_NESTED_PROMPT"
)

// TestSessionCommand_RecordThenReplayPromptUsesShippedCLI proves that a raw
// provider capture made through the normal command boundary can be replayed
// without credentials, while the captured tool-enabled handshake remains
// authoritative and later prompt traffic remains strict.
func TestSessionCommand_RecordThenReplayPromptUsesShippedCLI(t *testing.T) {
	recordPath := recordProductionPromptCapture(t)
	replayConfigDir := t.TempDir()
	writeSessionToolConfig(t, replayConfigDir, false)

	stdout, stderr, err := executeProductionSessionCommand(t, []string{
		"--config-dir", replayConfigDir,
		"session",
		"--replay", recordPath,
		"--system-prompt", "current instructions must not replace the capture",
		recordReplayPrompt,
	})
	if err != nil {
		t.Fatalf("replay recorded prompt through shipped CLI: %v\nstderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "prompt response") {
		t.Fatalf("replay output = %q, want prompt response", stdout)
	}

	capture, err := gwtesting.LoadSessionCapture(recordPath)
	if err != nil {
		t.Fatalf("load prompt capture: %v", err)
	}
	initial := firstSessionUpdateRecord(t, capture)
	assertSessionUpdateHasInstructionsAndExec(t, initial)
	assertOutboundWireTypes(t, capture, []string{
		"session.update",
		"conversation.item.create",
		"response.create",
	})
}

// TestSessionCommand_RecordThenReplayScheduledAudioUsesShippedCLI proves the
// repeatable --audio-in-turn path records both provider wire traffic and the
// finalized audio sidecar, then replays the exact scheduled sequence through
// the shipped CLI without a provider key.
func TestSessionCommand_RecordThenReplayScheduledAudioUsesShippedCLI(t *testing.T) {
	fixture := newRecordReplayWebSocketFixture(true, 2)
	server := httptest.NewServer(http.HandlerFunc(fixture.handle))
	defer server.Close()

	audioOne := writeRecordReplayAudio(t, "turn-one.wav", 900)
	audioTwo := writeRecordReplayAudio(t, "turn-two.wav", -900)
	recordPath := filepath.Join(t.TempDir(), "spoken.session.json")
	recordDir := filepath.Join(t.TempDir(), "spoken-recording")
	configDir := t.TempDir()
	writeSessionToolConfig(t, configDir, true)
	baseURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/realtime"

	stdout, stderr, err := executeProductionSessionCommand(t, []string{
		"--config-dir", configDir,
		"session",
		"--record", recordPath,
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "synthetic-recording-key",
		"--base-url", baseURL,
		"--system-prompt", recordReplayInstructions,
		"--audio-in-turn", audioOne,
		"--audio-in-turn", audioTwo,
	})
	if err != nil {
		t.Fatalf("record scheduled audio through shipped CLI: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	capture, err := gwtesting.LoadSessionCapture(recordPath)
	if err != nil {
		t.Fatalf("load spoken capture: %v", err)
	}
	initial := firstSessionUpdateRecord(t, capture)
	assertSessionUpdateHasInstructionsAndExec(t, initial)
	assertScheduledAudioOutboundWireTypes(t, capture)
	assertCLILiveRecordingBundle(t, recordDir, 2)

	replayConfigDir := t.TempDir()
	writeSessionToolConfig(t, replayConfigDir, false)
	replayRecordDir := filepath.Join(t.TempDir(), "spoken-replay-recording")
	_, replayStderr, err := executeProductionSessionCommand(t, []string{
		"--config-dir", replayConfigDir,
		"session",
		"--replay", recordPath,
		"--record-dir", replayRecordDir,
		"--audio-in-turn", audioOne,
		"--audio-in-turn", audioTwo,
	})
	if err != nil {
		t.Fatalf("replay scheduled audio through shipped CLI: %v\nstderr=%s", err, replayStderr)
	}
	assertCLILiveRecordingBundle(t, replayRecordDir, 2)
}

// TestSessionCommand_ReplayCorruptedProducedCaptureReportsNestedDivergence
// mutates one nested field in a capture produced by the prompt test path.
// The shipped replay command must fail quickly with its sequence, event type,
// JSON pointer, and bounded expected/actual values visible to the operator.
func TestSessionCommand_ReplayCorruptedProducedCaptureReportsNestedDivergence(t *testing.T) {
	recordPath := recordProductionPromptCapture(t)
	capture, err := gwtesting.LoadSessionCapture(recordPath)
	if err != nil {
		t.Fatalf("load prompt capture for corruption: %v", err)
	}
	mutatedSequence, mutatedType := corruptPromptPayload(t, recordPath, capture)

	replayConfigDir := t.TempDir()
	writeSessionToolConfig(t, replayConfigDir, false)
	stdout, stderr, err := executeProductionSessionCommand(t, []string{
		"--config-dir", replayConfigDir,
		"session",
		"--replay", recordPath,
		recordReplayPrompt,
	})
	if err == nil {
		t.Fatalf("corrupted replay unexpectedly succeeded; stdout=%q stderr=%q", stdout, stderr)
	}
	if !errors.Is(err, gateway.ErrReplayMismatch) {
		t.Fatalf("corrupted replay error = %v, want replay mismatch", err)
	}
	errText := err.Error()
	for _, want := range []string{
		fmt.Sprintf("event type %q at sequence %d", mutatedType, mutatedSequence),
		`JSON pointer /item/content/0/text`,
		`expected "` + recordReplayCorruption + `"`,
		`actual "` + recordReplayPrompt[:len("record-replay prompt PRO")],
		`...(truncated)`,
	} {
		if !strings.Contains(errText, want) {
			t.Fatalf("corrupted replay error missing %q: %v", want, err)
		}
	}
}

func assertSessionUpdateHasInstructionsAndExec(t *testing.T, record gwtesting.CapturedSessionEvent) {
	t.Helper()
	var payload struct {
		Session struct {
			Instructions string `json:"instructions"`
			Tools        []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"session"`
	}
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatalf("decode recorded session.update: %v", err)
	}
	if !strings.Contains(payload.Session.Instructions, recordReplayInstructions) {
		t.Fatalf("recorded session.update instructions = %q, want %q", payload.Session.Instructions, recordReplayInstructions)
	}
	for _, tool := range payload.Session.Tools {
		if tool.Name == "exec" {
			return
		}
	}
	t.Fatalf("recorded session.update tools omit exec: %s", record.Payload)
}

func recordProductionPromptCapture(t *testing.T) string {
	t.Helper()
	fixture := newRecordReplayWebSocketFixture(false, 1)
	server := httptest.NewServer(http.HandlerFunc(fixture.handle))
	defer server.Close()

	recordPath := filepath.Join(t.TempDir(), "prompt.session.json")
	configDir := t.TempDir()
	writeSessionToolConfig(t, configDir, true)
	baseURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/realtime"
	stdout, stderr, err := executeProductionSessionCommand(t, []string{
		"--config-dir", configDir,
		"session",
		"--record", recordPath,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "synthetic-recording-key",
		"--base-url", baseURL,
		"--system-prompt", recordReplayInstructions,
		recordReplayPrompt,
	})
	if err != nil {
		t.Fatalf("record prompt through shipped CLI: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "prompt response") {
		t.Fatalf("record output = %q, want prompt response", stdout)
	}
	return recordPath
}

func executeProductionSessionCommand(t *testing.T, args []string) (string, string, error) {
	t.Helper()
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production agent CLI: %v", err)
	}
	writer := NewTestWriter()
	root := agentCLI.Generate()
	root.SetOut(writer.Stdout())
	root.SetErr(writer.Stderr())
	root.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = root.ExecuteContext(ctx)
	return writer.StdoutString(), writer.StderrString(), err
}

func writeRecordReplayAudio(t *testing.T, name string, sample int16) string {
	t.Helper()
	samples := make([]int16, 160)
	for index := range samples {
		samples[index] = sample
	}
	var data bytes.Buffer
	if err := wavio.Write(&data, wavio.Rate16kHz, samples); err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func corruptPromptPayload(t *testing.T, path string, capture gwtesting.SessionCapture) (int, string) {
	t.Helper()
	for index, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer || record.Type != "conversation.item.create" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			t.Fatalf("decode prompt payload: %v", err)
		}
		item, ok := payload["item"].(map[string]any)
		if !ok {
			t.Fatalf("prompt payload item = %#v, want object", payload["item"])
		}
		content, ok := item["content"].([]any)
		if !ok || len(content) == 0 {
			t.Fatalf("prompt payload content = %#v, want non-empty array", item["content"])
		}
		firstContent, ok := content[0].(map[string]any)
		if !ok {
			t.Fatalf("prompt payload first content = %#v, want object", content[0])
		}
		firstContent["text"] = recordReplayCorruption
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode corrupted prompt payload: %v", err)
		}
		capture.Records[index].Payload = payloadBytes
		capture.Records[index].Data = nil
		captureBytes, err := json.MarshalIndent(capture, "", "  ")
		if err != nil {
			t.Fatalf("encode corrupted prompt capture: %v", err)
		}
		if err := os.WriteFile(path, captureBytes, 0o600); err != nil {
			t.Fatalf("write corrupted prompt capture: %v", err)
		}
		return record.Sequence, record.Type
	}
	t.Fatal("prompt capture has no conversation.item.create record")
	return 0, ""
}

func assertOutboundWireTypes(t *testing.T, capture gwtesting.SessionCapture, want []string) {
	t.Helper()
	got := make([]string, 0, len(want))
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionClientToServer {
			got = append(got, record.Type)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("outbound wire types = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("outbound wire types = %v, want %v", got, want)
		}
	}
}

func assertScheduledAudioOutboundWireTypes(t *testing.T, capture gwtesting.SessionCapture) {
	t.Helper()
	counts := make(map[string]int)
	ordered := make([]string, 0)
	for _, record := range capture.Records {
		if record.Direction != gwtesting.DirectionClientToServer {
			continue
		}
		ordered = append(ordered, record.Type)
		counts[record.Type]++
	}
	for _, eventType := range []string{"session.update", "input_audio_buffer.append", "input_audio_buffer.commit", "response.create"} {
		if counts[eventType] == 0 {
			t.Fatalf("scheduled audio capture omitted outbound %q: %v", eventType, ordered)
		}
	}
	if counts["input_audio_buffer.commit"] != 2 || counts["response.create"] != 2 {
		t.Fatalf("scheduled audio outbound counts = commits:%d responses:%d, want 2 each; ordered=%v", counts["input_audio_buffer.commit"], counts["response.create"], ordered)
	}
	if counts["input_audio_buffer.append"] != 2 {
		t.Fatalf("scheduled audio append count = %d, want one per audio-in-turn file; ordered=%v", counts["input_audio_buffer.append"], ordered)
	}
	want := []string{
		"session.update",
		"input_audio_buffer.append",
		"input_audio_buffer.commit",
		"response.create",
		"input_audio_buffer.append",
		"input_audio_buffer.commit",
		"response.create",
	}
	if len(ordered) != len(want) {
		t.Fatalf("scheduled audio outbound sequence = %v, want %v", ordered, want)
	}
	for index := range want {
		if ordered[index] != want[index] {
			t.Fatalf("scheduled audio outbound sequence = %v, want %v", ordered, want)
		}
	}
}

type recordReplayWebSocketFixture struct {
	upgrader          websocket.Upgrader
	audio             bool
	expectedResponses int
	mu                sync.Mutex
	responseCount     int
}

func newRecordReplayWebSocketFixture(audio bool, expectedResponses int) *recordReplayWebSocketFixture {
	return &recordReplayWebSocketFixture{
		upgrader:          websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		audio:             audio,
		expectedResponses: expectedResponses,
	}
}

func (f *recordReplayWebSocketFixture) handle(writer http.ResponseWriter, request *http.Request) {
	connection, err := f.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	defer connection.Close()

	sessionCreated := false
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
				"session": map[string]string{"id": "sess_record_replay", "model": "gpt-realtime"},
			}); err != nil {
				return
			}
			if err := connection.WriteJSON(map[string]any{
				"type":    "session.updated",
				"session": map[string]string{"id": "sess_record_replay", "model": "gpt-realtime"},
			}); err != nil {
				return
			}
			sessionCreated = true
		case "response.create":
			f.mu.Lock()
			f.responseCount++
			responseNumber := f.responseCount
			f.mu.Unlock()
			if err := f.writeResponse(connection, responseNumber, f.audio && responseNumber == f.expectedResponses); err != nil {
				return
			}
		}
	}
}

func (f *recordReplayWebSocketFixture) writeResponse(connection *websocket.Conn, responseNumber int, closeAfter bool) error {
	responseID := fmt.Sprintf("resp_record_replay_%d", responseNumber)
	if err := connection.WriteJSON(map[string]any{
		"type":     "response.created",
		"response": map[string]string{"id": responseID},
	}); err != nil {
		return err
	}
	if f.audio {
		transcript := fmt.Sprintf("response turn %d", responseNumber)
		if err := connection.WriteJSON(map[string]string{
			"type":       "response.output_audio_transcript.done",
			"transcript": transcript,
		}); err != nil {
			return err
		}
		audio := base64.StdEncoding.EncodeToString([]byte{byte(responseNumber), 0, byte(responseNumber + 10), 0})
		if err := connection.WriteJSON(map[string]string{
			"type":   "response.output_audio.delta",
			"delta":  audio,
			"format": "pcm16",
		}); err != nil {
			return err
		}
		if err := connection.WriteJSON(map[string]string{"type": "response.output_audio.done"}); err != nil {
			return err
		}
	} else {
		if err := connection.WriteJSON(map[string]string{
			"type":  "response.output_text.delta",
			"delta": "prompt response",
		}); err != nil {
			return err
		}
		if err := connection.WriteJSON(map[string]string{"type": "response.output_text.done"}); err != nil {
			return err
		}
	}
	if err := connection.WriteJSON(map[string]any{
		"type":     "response.done",
		"response": map[string]string{"id": responseID, "status": "completed"},
	}); err != nil {
		return err
	}
	if closeAfter {
		if err := connection.WriteJSON(map[string]string{
			"type":       "session.closed",
			"session_id": "sess_record_replay",
			"reason":     "fixture_complete",
		}); err != nil {
			return err
		}
	}
	return nil
}
