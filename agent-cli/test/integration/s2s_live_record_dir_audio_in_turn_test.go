package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/transcript"
	oaiprovider "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/providers/openai"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func (s *cliLiveRecordDirServer) shutdown() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() { close(s.closed) })
}
func newCLILiveBargeScheduledBoundaryServer() *cliLiveScheduledBoundaryServer {
	server := newCLILiveScheduledBoundaryServer(false)
	server.bargeIn = true
	server.firstResponseCancel = make(chan struct{})
	return server
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

func TestSessionCommand_LiveRecordDirAudioInTurnBargeInUsesActiveResponseBoundary(t *testing.T) {
	server := newCLILiveBargeScheduledBoundaryServer()
	t.Cleanup(server.shutdown)
	agentCLI := newCLIScheduledBoundaryAgent(t, server)

	var observed []messages.StreamMessage
	var observedMu sync.Mutex
	agentCLI.SetSessionStreamObserver(func(msg messages.StreamMessage) {
		observedMu.Lock()
		observed = append(observed, msg)
		observedMu.Unlock()
	})

	recordDir := filepath.Join(t.TempDir(), "barge-recording")
	args := scheduledBoundaryArgs(
		t.TempDir(),
		recordDir,
		locateCLIFixture(t, "multiturn_turn1.wav"),
		locateCLIFixture(t, "multiturn_turn2.wav"),
	)
	args = append(args, "--audio-in-turn-barge")
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs(args)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		timeline, outbound, _, _, _ := server.snapshots()
		t.Fatalf("execute active scheduled command: %v; timeline=%v outbound=%v", err, timeline, audioLengthsFromOutbound(outbound))
	}

	timeline, outbound, providerErrors, dialCount, serverVAD := server.snapshots()
	if dialCount != 1 || serverVAD || len(providerErrors) != 0 {
		t.Fatalf("active scheduled provider state = dials:%d server_vad:%t errors:%v timeline=%v; want one client-owned clean session", dialCount, serverVAD, providerErrors, timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 2 ||
		countTimeline(timeline, "out:input_audio_buffer.commit") != 2 ||
		countTimeline(timeline, "out:response.create") != 2 ||
		countTimeline(timeline, "out:response.cancel") != 1 ||
		countTimeline(timeline, "in:response.done") != 2 {
		t.Fatalf("active scheduled lifecycle = %v, want two inputs/responses and one cancellation", timeline)
	}
	firstResponse := indexOfTimeline(timeline, "in:response.created", 0)
	cancelIndex := indexOfTimeline(timeline, "out:response.cancel", 0)
	secondAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 1)
	if firstResponse < 0 || cancelIndex <= firstResponse || secondAppend <= cancelIndex {
		t.Fatalf("active scheduled ordering = %v, want response.created < response.cancel < second append", timeline)
	}
	if firstResponseDone := indexOfTimeline(timeline, "in:response.done", 0); firstResponseDone <= cancelIndex {
		t.Fatalf("cancel did not win before first response terminality: %v", timeline)
	}

	appendAudio := audioPayloadsFromOutbound(outbound)
	if len(appendAudio) != 2 || len(appendAudio[0]) == 0 || len(appendAudio[1]) == 0 {
		t.Fatalf("active scheduled input payloads = %v, want two non-empty append payloads", audioLengths(appendAudio))
	}
	observedMu.Lock()
	observedCopy := append([]messages.StreamMessage(nil), observed...)
	observedMu.Unlock()
	seenReplacement, seenStale := false, false
	for _, msg := range observedCopy {
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || value == nil {
			continue
		}
		switch string(value.Content) {
		case string([]byte{2, 0, 22, 0}):
			seenReplacement = true
		case "cancel-stale":
			seenStale = true
		}
	}
	if !seenReplacement {
		t.Fatalf("replacement response audio was not observed; stream=%#v", observedCopy)
	}
	if seenStale {
		t.Fatalf("stale post-cancel provider audio crossed the stream boundary; stream=%#v", observedCopy)
	}
}

func TestSessionCommand_LiveRecordDirAudioInTurnRejectsUndispatchedScheduledInput(t *testing.T) {
	server := newCLILiveRecordDirCloseAfterTurnServer(2)
	t.Cleanup(server.shutdown)
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

	recordDir := filepath.Join(t.TempDir(), "incomplete-scheduled-recording")
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
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn1.wav"),
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn2.wav"),
		"--audio-in-turn", locateCLIFixture(t, "multiturn_turn1.wav"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = rootCmd.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("clean provider close after turn 2 reported success with an undispatched third turn")
	}
	if !errors.Is(err, services.ErrSessionScheduledAudioIncomplete) {
		t.Fatalf("incomplete scheduled session error = %v, want ErrSessionScheduledAudioIncomplete", err)
	}
	var incomplete *services.SessionScheduledAudioIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("incomplete scheduled session error = %v, want typed counts", err)
	}
	if incomplete.Completed != 2 || incomplete.Dispatched != 2 || incomplete.Scheduled != 3 {
		t.Fatalf("incomplete scheduled counts = %+v, want completed=2 dispatched=2 scheduled=3", incomplete)
	}

	timeline, outbound, dialCount := server.snapshots()
	if dialCount != 1 {
		t.Fatalf("incomplete scheduled provider dial count = %d, want 1; timeline=%v", dialCount, timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 2 ||
		countTimeline(timeline, "out:input_audio_buffer.commit") != 2 ||
		countTimeline(timeline, "out:response.create") != 2 ||
		countTimeline(timeline, "in:response.done") != 2 ||
		countTimeline(timeline, "in:session.closed") != 1 {
		t.Fatalf("incomplete scheduled wire counts = %v; outbound=%v", timeline, audioLengthsFromOutbound(outbound))
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") >= 3 {
		t.Fatalf("undispatched third turn crossed the provider boundary: %v", timeline)
	}
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
	if !errors.Is(err, services.ErrSessionScheduledAudioIncomplete) {
		t.Fatalf("provider close did not retain the incomplete schedule signal: %v", err)
	}
	var incomplete *services.SessionScheduledAudioIncompleteError
	if !errors.As(err, &incomplete) || incomplete.Completed != 0 || incomplete.Dispatched != 0 || incomplete.Scheduled != 1 {
		t.Fatalf("provider close incomplete schedule counts = %+v, want completed=0 dispatched=0 scheduled=1; error=%v", incomplete, err)
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
