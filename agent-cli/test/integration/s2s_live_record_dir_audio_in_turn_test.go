package integration

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/transport"
)

// cliLiveRecordDirServer is a provider-shaped transport used at the shipped
// Cobra command boundary. It emits session.created once, answers each
// response.create with one terminal response, and intentionally never emits
// session.closed: the live scheduled-input lifecycle must close locally after
// the final response.done.
type cliLiveRecordDirServer struct {
	mu            sync.Mutex
	timeline      []string
	outbound      []cliLiveOutbound
	responses     chan int
	events        chan []byte
	closed        chan struct{}
	closeOnce     sync.Once
	dialOnce      sync.Once
	dialCount     int
	nextTurn      int
	providerError bool
	readErr       error
}

type cliLiveOutbound struct {
	typeName string
	audio    []byte
}

func newCLILiveRecordDirServer(providerError bool) *cliLiveRecordDirServer {
	return &cliLiveRecordDirServer{
		responses:     make(chan int, 8),
		events:        make(chan []byte, 64),
		closed:        make(chan struct{}),
		providerError: providerError,
	}
}

func newCLILiveRecordDirReadErrorServer(readErr error) *cliLiveRecordDirServer {
	server := newCLILiveRecordDirServer(false)
	server.readErr = readErr
	return server
}

func (s *cliLiveRecordDirServer) Dial(_ string, _ map[string]string) (transport.Conn, error) {
	s.mu.Lock()
	s.dialCount++
	s.mu.Unlock()
	s.dialOnce.Do(func() { go s.serve() })
	return &cliLiveRecordDirConn{server: s}, nil
}

func (s *cliLiveRecordDirServer) serve() {
	s.sendEvent(`{"type":"session.created","session":{"id":"sess_cli_live","model":"gpt-realtime"}}`)
	if s.providerError {
		s.sendEvent(`{"type":"error","error":{"type":"authentication_error","code":"invalid_api_key","message":"invalid API key"}}`)
		return
	}
	s.sendEvent(`{"type":"session.updated","session":{"id":"sess_cli_live","model":"gpt-realtime"}}`)

	for {
		select {
		case turn := <-s.responses:
			responseID := "resp_" + strconv.Itoa(turn)
			transcriptText := "response turn " + strconv.Itoa(turn)
			audio := base64.StdEncoding.EncodeToString([]byte{byte(turn), 0, byte(turn + 10), 0})
			s.sendEvent(`{"type":"response.created","response":{"id":"` + responseID + `"}}`)
			s.sendEvent(`{"type":"response.output_audio_transcript.done","transcript":"` + transcriptText + `"}`)
			s.sendEvent(`{"type":"response.output_audio.delta","delta":"` + audio + `","format":"pcm16"}`)
			s.sendEvent(`{"type":"response.output_audio.done"}`)
			s.sendEvent(`{"type":"response.done","response":{"id":"` + responseID + `","status":"completed"}}`)
		case <-s.closed:
			return
		}
	}
}

func (s *cliLiveRecordDirServer) sendEvent(payload string) {
	select {
	case s.events <- []byte(payload):
	case <-s.closed:
	}
}

func (s *cliLiveRecordDirServer) recordTimeline(event string) {
	s.mu.Lock()
	s.timeline = append(s.timeline, event)
	s.mu.Unlock()
}

func (s *cliLiveRecordDirServer) snapshots() ([]string, []cliLiveOutbound, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	timeline := append([]string(nil), s.timeline...)
	outbound := make([]cliLiveOutbound, len(s.outbound))
	for index, event := range s.outbound {
		outbound[index] = cliLiveOutbound{typeName: event.typeName, audio: append([]byte(nil), event.audio...)}
	}
	return timeline, outbound, s.dialCount
}

type cliLiveRecordDirConn struct {
	server *cliLiveRecordDirServer
}

func (c *cliLiveRecordDirConn) ReadMessage() (int, []byte, error) {
	if c.server.readErr != nil {
		return 0, nil, c.server.readErr
	}
	select {
	case payload := <-c.server.events:
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(payload, &envelope); err == nil {
			c.server.recordTimeline("in:" + envelope.Type)
		}
		return 1, payload, nil
	case <-c.server.closed:
		return 0, nil, errors.New("hermetic live connection closed")
	}
}

func (c *cliLiveRecordDirConn) WriteMessage(_ int, payload []byte) error {
	var envelope struct {
		Type  string `json:"type"`
		Audio string `json:"audio"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}

	var audio []byte
	if envelope.Audio != "" {
		var err error
		audio, err = base64.StdEncoding.DecodeString(envelope.Audio)
		if err != nil {
			return err
		}
	}

	c.server.mu.Lock()
	c.server.timeline = append(c.server.timeline, "out:"+envelope.Type)
	c.server.outbound = append(c.server.outbound, cliLiveOutbound{typeName: envelope.Type, audio: audio})
	if envelope.Type == "response.create" {
		c.server.nextTurn++
		turn := c.server.nextTurn
		c.server.mu.Unlock()
		select {
		case c.server.responses <- turn:
		case <-c.server.closed:
		}
		return nil
	}
	c.server.mu.Unlock()
	return nil
}

func (c *cliLiveRecordDirConn) Close() error {
	c.server.closeOnce.Do(func() { close(c.server.closed) })
	return nil
}

type cliLiveRecordingEntry struct {
	TurnIndex int `json:"turn_index"`
	Input     struct {
		AudioBytes    uint64   `json:"audio_bytes"`
		Committed     bool     `json:"committed"`
		AudioSegments []string `json:"audio_segments"`
	} `json:"input"`
	Response struct {
		Text          string   `json:"text"`
		Complete      bool     `json:"complete"`
		AudioBytes    uint64   `json:"audio_bytes"`
		AudioSegments []string `json:"audio_segments"`
	} `json:"response"`
}

func TestSessionCommand_LiveRecordDirAudioInTurnUsesLiveLifecycle(t *testing.T) {
	server := newCLILiveRecordDirServer(false)
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(server),
	)
	if err != nil {
		t.Fatalf("create hermetic OpenAI session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	recordDir := filepath.Join(t.TempDir(), "recording")
	firstAudio := locateCLIFixture(t, "multiturn_turn1.wav")
	secondAudio := locateCLIFixture(t, "multiturn_turn2.wav")
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--max-duration", "5s",
		"--audio-in-turn", firstAudio,
		"--audio-in-turn", secondAudio,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		timeline, outbound, _ := server.snapshots()
		t.Fatalf("execute live-shaped command: %v; timeline=%v outbound=%v", err, timeline, audioLengthsFromOutbound(outbound))
	}

	timeline, outbound, dialCount := server.snapshots()
	if dialCount != 1 {
		t.Fatalf("live provider dial count = %d, want 1; timeline=%v", dialCount, timeline)
	}
	if containsTimeline(timeline, "in:session.closed") {
		t.Fatal("hermetic provider unexpectedly supplied a captured session.closed event")
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 2 || countTimeline(timeline, "out:input_audio_buffer.commit") != 2 || countTimeline(timeline, "out:response.create") != 2 {
		t.Fatalf("live outbound lifecycle = %v, want two appends, commits, and response.create events", timeline)
	}
	firstAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 0)
	firstResponseDone := indexOfTimeline(timeline, "in:response.done", 0)
	secondAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 1)
	if firstAppend < 0 || firstResponseDone < 0 || secondAppend <= firstResponseDone {
		t.Fatalf("turn-zero or response-gated dispatch order is wrong: %v", timeline)
	}

	appendAudio := make([][]byte, 0, 2)
	for _, event := range outbound {
		if event.typeName == "input_audio_buffer.append" {
			appendAudio = append(appendAudio, event.audio)
		}
	}
	if len(appendAudio) != 2 || len(appendAudio[0]) == 0 || len(appendAudio[1]) == 0 {
		t.Fatalf("provider observed scheduled audio payloads = %d with lengths %v, want two non-empty payloads", len(appendAudio), audioLengths(appendAudio))
	}

	assertCLILiveRecordingBundle(t, recordDir, 2)
}

func TestSessionCommand_LiveRecordDirAudioInTurnProviderErrorWinsOverRecordingValidation(t *testing.T) {
	server := newCLILiveRecordDirServer(true)
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(server),
	)
	if err != nil {
		t.Fatalf("create hermetic OpenAI session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	recordDir := filepath.Join(t.TempDir(), "failed-recording")
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record-dir", recordDir,
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "test-key",
		"--system-prompt", "none",
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn1.wav"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = rootCmd.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("expected provider authentication error")
	}
	if !strings.Contains(err.Error(), "invalid API key") && !strings.Contains(err.Error(), "invalid_api_key") {
		timeline, outbound, _ := server.snapshots()
		t.Fatalf("provider authentication error was not preserved: %v; timeline=%v outbound=%v", err, timeline, audioLengthsFromOutbound(outbound))
	}
	if errors.Is(err, transcript.ErrInvalidRecording) || strings.Contains(err.Error(), "at least one segment is required") {
		t.Fatalf("recording validation masked provider authentication error: %v", err)
	}
}

func TestSessionCommand_LiveRecordDirAudioInTurnUnexpectedProviderCloseWinsOverIncompleteSchedule(t *testing.T) {
	server := newCLILiveRecordDirReadErrorServer(errors.New("websocket: close 1008 (policy violation): Incorrect API key provided: invalid-test-key"))
	sessionInferencer, err := services.NewOpenAIRealtimeSessionInferencerWithOptions(
		config.OpenAIConfig{APIKey: "test-key", Model: "gpt-realtime", BaseURL: "wss://hermetic.openai.test/v1/realtime"},
		oaiprovider.WithWebSocketDialer(server),
	)
	if err != nil {
		t.Fatalf("create hermetic OpenAI session inferencer: %v", err)
	}
	agentCLI, err := wire.InitializeMockAgentCLIWithSessionInferencer(
		&mockToolExecutor{},
		&mockInferencerError{err: errors.New("stateless inferencer should not be called")},
		sessionInferencer,
	)
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}

	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", t.TempDir(),
		"session",
		"--record-dir", filepath.Join(t.TempDir(), "failed-recording"),
		"--provider", "openai",
		"--model", "gpt-realtime",
		"--api-key", "invalid-test-key",
		"--system-prompt", "none",
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn1.wav"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = rootCmd.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("expected provider close error")
	}
	if !strings.Contains(err.Error(), "Incorrect API key") {
		t.Fatalf("unexpected provider close error: %v", err)
	}
	if strings.Contains(err.Error(), "scheduled audio session ended before all turns completed") || strings.Contains(err.Error(), "at least one segment is required") {
		t.Fatalf("provider close was masked by a secondary recording error: %v", err)
	}
}

func assertCLILiveRecordingBundle(t *testing.T, destination string, turns int) {
	t.Helper()
	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read finalized recording manifest: %v", err)
	}
	var manifest struct {
		Artifacts []struct {
			Path string `json:"path"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode recording manifest: %v", err)
	}

	inputArtifacts, outputArtifacts := 0, 0
	for _, artifact := range manifest.Artifacts {
		switch {
		case strings.HasPrefix(artifact.Path, "audio/in-"):
			inputArtifacts++
		case strings.HasPrefix(artifact.Path, "audio/out-"):
			outputArtifacts++
		}
	}
	if inputArtifacts != turns || outputArtifacts != turns {
		t.Fatalf("manifest audio artifacts = input:%d output:%d, want %d each", inputArtifacts, outputArtifacts, turns)
	}
	for index := 0; index < turns; index++ {
		for _, prefix := range []string{"audio/in-", "audio/out-"} {
			path := filepath.Join(destination, prefix+threeDigits(index)+".pcm")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read finalized audio artifact %q: %v", path, err)
			}
			if len(data) == 0 {
				t.Fatalf("finalized audio artifact %q is empty", path)
			}
		}
	}

	logFile, err := os.Open(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("open finalized session log: %v", err)
	}
	defer logFile.Close()
	entries := make([]cliLiveRecordingEntry, 0, turns)
	scanner := bufio.NewScanner(logFile)
	for scanner.Scan() {
		var entry cliLiveRecordingEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode session log entry: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read finalized session log: %v", err)
	}
	if len(entries) != turns {
		t.Fatalf("session log entries = %d, want %d", len(entries), turns)
	}
	for index, entry := range entries {
		if entry.TurnIndex != index+1 || !entry.Input.Committed || entry.Input.AudioBytes == 0 || !entry.Response.Complete || entry.Response.AudioBytes == 0 {
			t.Fatalf("session log entry %d does not prove a committed input and completed audio response: %#v", index+1, entry)
		}
		wantText := "response turn " + strconv.Itoa(index+1)
		if entry.Response.Text != wantText {
			t.Fatalf("session log response %d text = %q, want %q", index+1, entry.Response.Text, wantText)
		}
	}
}

func threeDigits(index int) string {
	value := strconv.Itoa(index)
	return strings.Repeat("0", 3-len(value)) + value
}

func containsTimeline(timeline []string, want string) bool {
	return indexOfTimeline(timeline, want, 0) >= 0
}

func countTimeline(timeline []string, want string) int {
	count := 0
	for _, event := range timeline {
		if event == want {
			count++
		}
	}
	return count
}

func indexOfTimeline(timeline []string, want string, occurrence int) int {
	seen := 0
	for index, event := range timeline {
		if event != want {
			continue
		}
		if seen == occurrence {
			return index
		}
		seen++
	}
	return -1
}

func audioLengths(audio [][]byte) []int {
	lengths := make([]int, len(audio))
	for index, data := range audio {
		lengths[index] = len(data)
	}
	return lengths
}

func audioLengthsFromOutbound(outbound []cliLiveOutbound) []int {
	lengths := make([]int, 0, len(outbound))
	for _, event := range outbound {
		if event.typeName == "input_audio_buffer.append" {
			lengths = append(lengths, len(event.audio))
		}
	}
	return lengths
}
