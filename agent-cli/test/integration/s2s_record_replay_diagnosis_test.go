package integration

import (
	"bufio"
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
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/gateway"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
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

// TestSessionCommand_ScriptedInputTranscriptionReachesEverySurface drives one
// production command through a deterministic WebSocket provider. The same run
// is the source of terminal output, normalized transcript artifacts, the
// completed conversation log, and raw wire capture.
func TestSessionCommand_ScriptedInputTranscriptionReachesEverySurface(t *testing.T) {
	fixture := newRecordReplayWebSocketFixture(true, 1)
	fixture.inputTranscriptDelta = "heard "
	fixture.inputTranscriptCompleted = "heard clearly"
	fixture.assistantTranscriptDelta = "answering "
	fixture.assistantTranscriptCompleted = "answering now"
	server := httptest.NewServer(http.HandlerFunc(fixture.handle))
	defer server.Close()

	audioPath := writeRecordReplayAudio(t, "transcribed-turn.wav", 1200)
	recordPath := filepath.Join(t.TempDir(), "transcribed.session.json")
	recordDir := filepath.Join(t.TempDir(), "transcribed-recording")
	configDir := t.TempDir()
	writeSessionToolConfig(t, configDir, false)
	baseURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/realtime"

	stdout, stderr, err := executeProductionSessionCommand(t, []string{
		"--config-dir", configDir,
		"session",
		"--record", recordPath,
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "synthetic-transcription-key",
		"--base-url", baseURL,
		"--audio-in-turn", audioPath,
	})
	if err != nil {
		t.Fatalf("scripted input transcription session: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "User: heard ") || !strings.Contains(stdout, "Assistant: answering ") {
		t.Fatalf("terminal output = %q, want distinct user and assistant transcript labels", stdout)
	}

	assertRecordedInputTranscripts(t, filepath.Join(recordDir, "client.transcript.jsonl"))
	assertRecordedInputTranscriptSessionLog(t, filepath.Join(recordDir, "session-log.jsonl"))
	assertRawInputTranscriptEvents(t, recordPath)
}

// TestSessionCommand_ReplayInputTranscriptionCapturePreservesEverySurface
// proves that a new-format capture keeps its recorded enabled handshake and
// replays the same user transcript semantics without consulting the current
// live default. Both the original scripted run and the replay are checked
// through the shipped command and finalized recording directory.
func TestSessionCommand_ReplayInputTranscriptionCapturePreservesEverySurface(t *testing.T) {
	fixture := newRecordReplayWebSocketFixture(true, 1)
	fixture.inputTranscriptDelta = "heard "
	fixture.inputTranscriptCompleted = "heard clearly"
	fixture.assistantTranscriptDelta = "answering "
	fixture.assistantTranscriptCompleted = "answering now"
	server := httptest.NewServer(http.HandlerFunc(fixture.handle))
	defer server.Close()

	audioPath := writeRecordReplayAudio(t, "replayed-transcribed-turn.wav", 1200)
	recordPath := filepath.Join(t.TempDir(), "replayed-transcribed.session.json")
	recordDir := filepath.Join(t.TempDir(), "replayed-transcribed-recording")
	configDir := t.TempDir()
	writeSessionToolConfig(t, configDir, false)
	baseURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/realtime"

	stdout, stderr, err := executeProductionSessionCommand(t, []string{
		"--config-dir", configDir,
		"session",
		"--record", recordPath,
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "synthetic-replay-transcription-key",
		"--base-url", baseURL,
		"--audio-in-turn", audioPath,
	})
	if err != nil {
		t.Fatalf("record scripted input transcription session: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}

	capture, err := gwtesting.LoadSessionCapture(recordPath)
	if err != nil {
		t.Fatalf("load input transcription replay capture: %v", err)
	}
	assertEnabledInputTranscriptionHandshake(t, firstSessionUpdateRecord(t, capture))
	assertRecordedInputTranscripts(t, filepath.Join(recordDir, "client.transcript.jsonl"))
	assertRecordedInputTranscriptSessionLog(t, filepath.Join(recordDir, "session-log.jsonl"))
	if !strings.Contains(stdout, "User: heard ") || !strings.Contains(stdout, "Assistant: answering ") {
		t.Fatalf("record terminal output = %q, want distinct user and assistant transcript labels", stdout)
	}

	replayRecordDir := filepath.Join(t.TempDir(), "replayed-transcribed-recording")
	replayStdout, replayStderr, err := executeProductionSessionCommand(t, []string{
		"session",
		"--replay", recordPath,
		"--record-dir", replayRecordDir,
		"--audio-in-turn", audioPath,
	})
	if err != nil {
		t.Fatalf("replay input transcription capture: %v\nstdout=%s\nstderr=%s", err, replayStdout, replayStderr)
	}
	if !strings.Contains(replayStdout, "User: heard ") || !strings.Contains(replayStdout, "Assistant: answering ") {
		t.Fatalf("replay terminal output = %q, want distinct user and assistant transcript labels", replayStdout)
	}
	assertRecordedInputTranscripts(t, filepath.Join(replayRecordDir, "client.transcript.jsonl"))
	assertRecordedInputTranscriptSessionLog(t, filepath.Join(replayRecordDir, "session-log.jsonl"))
}

func assertEnabledInputTranscriptionHandshake(t *testing.T, record gwtesting.CapturedSessionEvent) {
	t.Helper()
	var envelope struct {
		Session struct {
			Audio struct {
				Input struct {
					Transcription json.RawMessage `json:"transcription"`
				} `json:"input"`
			} `json:"audio"`
			Legacy json.RawMessage `json:"input_audio_transcription"`
		} `json:"session"`
	}
	if err := json.Unmarshal(record.Payload, &envelope); err != nil {
		t.Fatalf("decode enabled input transcription handshake: %v", err)
	}
	if len(envelope.Session.Audio.Input.Transcription) == 0 || string(envelope.Session.Audio.Input.Transcription) == "null" {
		t.Fatalf("enabled input transcription handshake omitted GA audio.input.transcription: %s", record.Payload)
	}
	var transcription struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(envelope.Session.Audio.Input.Transcription, &transcription); err != nil {
		t.Fatalf("decode GA input transcription configuration: %v", err)
	}
	if transcription.Model != "gpt-live-transcribe" {
		t.Fatalf("input transcription model = %q, want gpt-live-transcribe", transcription.Model)
	}
	if len(envelope.Session.Legacy) != 0 && string(envelope.Session.Legacy) != "null" {
		t.Fatalf("enabled GA handshake also included legacy input_audio_transcription: %s", record.Payload)
	}
}

func assertRecordedInputTranscripts(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open client transcript: %v", err)
	}
	defer file.Close()

	var userDelta, userEnd, assistantDelta, assistantEnd int
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		record, decodeErr := transcript.Decode(scanner.Bytes())
		if decodeErr != nil {
			t.Fatalf("decode client transcript record: %v", decodeErr)
		}
		message, unmarshalErr := gwtesting.UnmarshalStreamMessage(record.Payload)
		if unmarshalErr != nil {
			t.Fatalf("decode client transcript payload: %v", unmarshalErr)
		}
		switch value := message.Value.(type) {
		case *messages.TranscriptDeltaValue:
			if message.Role != messages.RoleUser && message.Role != messages.RoleAssistant {
				t.Fatalf("transcript delta role = %q, want user or assistant", message.Role)
			}
			if message.Role == messages.RoleUser && value.Text == "heard " {
				userDelta++
			}
			if message.Role == messages.RoleAssistant && value.Text == "answering " {
				assistantDelta++
			}
		case *messages.TranscriptEndValue:
			if message.Role != messages.RoleUser && message.Role != messages.RoleAssistant {
				t.Fatalf("transcript end role = %q, want user or assistant", message.Role)
			}
			if message.Role == messages.RoleUser && value.FullText == "heard clearly" {
				userEnd++
			}
			if message.Role == messages.RoleAssistant && value.FullText == "answering now" {
				assistantEnd++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan client transcript: %v", err)
	}
	if userDelta != 1 || userEnd != 1 || assistantDelta != 1 || assistantEnd != 1 {
		t.Fatalf("client transcript user_delta=%d user_end=%d assistant_delta=%d assistant_end=%d; want one of each role-preserved record", userDelta, userEnd, assistantDelta, assistantEnd)
	}
}

func assertRecordedInputTranscriptSessionLog(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open session log: %v", err)
	}
	defer file.Close()

	type recordingEntry struct {
		TurnIndex int `json:"turn_index"`
		Input     struct {
			Text          string   `json:"text"`
			AudioBytes    uint64   `json:"audio_bytes"`
			Committed     bool     `json:"committed"`
			AudioSegments []string `json:"audio_segments"`
		} `json:"input"`
		Response struct {
			Text       string `json:"text"`
			Complete   bool   `json:"complete"`
			AudioBytes uint64 `json:"audio_bytes"`
		} `json:"response"`
	}
	var entries []recordingEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry recordingEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode session log entry: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan session log: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("session log entries = %d, want one: %#v", len(entries), entries)
	}
	entry := entries[0]
	if entry.TurnIndex != 1 || entry.Input.Text != "heard clearly" || entry.Input.AudioBytes == 0 || !entry.Input.Committed || len(entry.Input.AudioSegments) != 1 || entry.Response.Text != "answering now" || !entry.Response.Complete || entry.Response.AudioBytes == 0 {
		t.Fatalf("session log entry = %#v, want authoritative input transcript with retained audio/response fields", entry)
	}
}

func assertRawInputTranscriptEvents(t *testing.T, path string) {
	t.Helper()
	capture, err := gwtesting.LoadSessionCapture(path)
	if err != nil {
		t.Fatalf("load raw input transcription capture: %v", err)
	}
	var matched []gwtesting.CapturedSessionEvent
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionServerToClient && strings.HasPrefix(record.Type, "conversation.item.input_audio_transcription.") {
			matched = append(matched, record)
		}
	}
	if len(matched) != 2 || matched[0].Type != "conversation.item.input_audio_transcription.delta" || matched[1].Type != "conversation.item.input_audio_transcription.completed" {
		t.Fatalf("raw input transcription events = %#v, want ordered delta and completed events", matched)
	}
	for _, record := range matched {
		if record.PayloadType != gwtesting.SessionPayloadTypeWebSocketMessage {
			t.Fatalf("raw input transcription payload type = %q, want websocket_message", record.PayloadType)
		}
	}
	var delta, completed struct {
		Type       string `json:"type"`
		ItemID     string `json:"item_id"`
		Delta      string `json:"delta"`
		Transcript string `json:"transcript"`
	}
	if err := json.Unmarshal(matched[0].Payload, &delta); err != nil {
		t.Fatalf("decode raw input transcription delta: %v", err)
	}
	if err := json.Unmarshal(matched[1].Payload, &completed); err != nil {
		t.Fatalf("decode raw input transcription completed: %v", err)
	}
	if delta.Type != matched[0].Type || delta.ItemID != "item_record_replay_1" || delta.Delta != "heard " || completed.Type != matched[1].Type || completed.ItemID != delta.ItemID || completed.Transcript != "heard clearly" {
		t.Fatalf("raw input transcription payloads = delta:%#v completed:%#v", delta, completed)
	}
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
	upgrader                     websocket.Upgrader
	audio                        bool
	expectedResponses            int
	inputTranscriptDelta         string
	inputTranscriptCompleted     string
	assistantTranscriptDelta     string
	assistantTranscriptCompleted string
	mu                           sync.Mutex
	responseCount                int
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
		if f.inputTranscriptDelta != "" || f.inputTranscriptCompleted != "" {
			itemID := fmt.Sprintf("item_record_replay_%d", responseNumber)
			if f.inputTranscriptDelta != "" {
				if err := connection.WriteJSON(map[string]string{
					"type":    "conversation.item.input_audio_transcription.delta",
					"item_id": itemID,
					"delta":   f.inputTranscriptDelta,
				}); err != nil {
					return err
				}
			}
			if err := connection.WriteJSON(map[string]string{
				"type":       "conversation.item.input_audio_transcription.completed",
				"item_id":    itemID,
				"transcript": f.inputTranscriptCompleted,
			}); err != nil {
				return err
			}
		}
		if f.assistantTranscriptDelta != "" {
			if err := connection.WriteJSON(map[string]string{
				"type":  "response.output_audio_transcript.delta",
				"delta": f.assistantTranscriptDelta,
			}); err != nil {
				return err
			}
		}
		assistantTranscript := f.assistantTranscriptCompleted
		if assistantTranscript == "" {
			assistantTranscript = transcript
		}
		if err := connection.WriteJSON(map[string]string{
			"type":       "response.output_audio_transcript.done",
			"transcript": assistantTranscript,
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
