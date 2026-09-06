package integration

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-audio/pkg/wavio"
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
		AudioOffsetBytes uint64   `json:"audio_offset_bytes"`
		AudioBytes       uint64   `json:"audio_bytes"`
		Committed        bool     `json:"committed"`
		AudioSegments    []string `json:"audio_segments"`
	} `json:"input"`
	Response struct {
		Text             string   `json:"text"`
		Complete         bool     `json:"complete"`
		AudioOffsetBytes uint64   `json:"audio_offset_bytes"`
		AudioBytes       uint64   `json:"audio_bytes"`
		AudioSegments    []string `json:"audio_segments"`
	} `json:"response"`
}

func TestSessionCommand_LiveRecordDirAudioInTurnUsesLiveLifecycle(t *testing.T) {
	server := newCLILiveRecordDirServer(false)
	sessionInferencer, err := servicetest.NewOpenAIRealtimeSessionInferencerWithOptions(
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
	thirdAudio := locateCLIFixture(t, "multiturn_turn1.wav")
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
		"--audio-in-turn", thirdAudio,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err == nil {
		t.Fatal("record-dir run without provider capture should report incomplete evidence")
	} else if !strings.Contains(err.Error(), "finalize provider evidence") {
		t.Fatalf("record-dir run returned unrelated error for missing provider capture: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recordDir, "manifest.json")); err != nil {
		t.Fatalf("incomplete record-dir bundle was not published: %v", err)
	}
	timeline, outbound, dialCount := server.snapshots()
	if dialCount != 1 {
		t.Fatalf("live provider dial count = %d, want 1; timeline=%v", dialCount, timeline)
	}
	if containsTimeline(timeline, "in:session.closed") {
		t.Fatal("hermetic provider unexpectedly supplied a captured session.closed event")
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") != 3 || countTimeline(timeline, "out:input_audio_buffer.commit") != 3 || countTimeline(timeline, "out:response.create") != 3 {
		t.Fatalf("live outbound lifecycle = %v, want three appends, commits, and response.create events", timeline)
	}
	firstAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 0)
	firstResponseDone := indexOfTimeline(timeline, "in:response.done", 0)
	secondAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 1)
	secondResponseDone := indexOfTimeline(timeline, "in:response.done", 1)
	thirdAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 2)
	if firstAppend < 0 || firstResponseDone < 0 || secondAppend <= firstResponseDone || secondResponseDone < 0 || thirdAppend <= secondResponseDone {
		t.Fatalf("turn-zero or response-gated dispatch order is wrong: %v", timeline)
	}

	appendAudio := audioPayloadsFromOutbound(outbound)
	if len(appendAudio) != 3 || len(appendAudio[0]) == 0 || len(appendAudio[1]) == 0 || len(appendAudio[2]) == 0 {
		t.Fatalf("provider observed scheduled audio payloads = %d with lengths %v, want three non-empty payloads", len(appendAudio), audioLengths(appendAudio))
	}

	wantInput := bytes.Join(appendAudio, nil)
	// The provider fixture emits two PCM16 samples per response. Verify the
	// append-only stream itself, rather than only checking that each turn has
	// a non-empty file entry.
	wantOutput := []byte{1, 0, 11, 0, 2, 0, 12, 0, 3, 0, 13, 0}
	assertCLILiveRecordingBundle(t, recordDir, 3, wantInput, wantOutput)
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
		locateCLIFixture(t, "multiturn_turn1.wav"),
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
	if countTimeline(timeline, "out:input_audio_buffer.append") != 3 ||
		countTimeline(timeline, "out:input_audio_buffer.commit") != 3 ||
		countTimeline(timeline, "out:response.create") != 3 ||
		countTimeline(timeline, "out:response.cancel") != 1 ||
		countTimeline(timeline, "in:response.done") != 3 {
		t.Fatalf("active scheduled lifecycle = %v, want three inputs/responses and one cancellation", timeline)
	}
	firstResponse := indexOfTimeline(timeline, "in:response.created", 0)
	cancelIndex := indexOfTimeline(timeline, "out:response.cancel", 0)
	secondAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 1)
	secondResponseDone := indexOfTimeline(timeline, "in:response.done", 1)
	thirdAppend := indexOfTimeline(timeline, "out:input_audio_buffer.append", 2)
	if firstResponse < 0 || cancelIndex <= firstResponse || secondAppend <= cancelIndex || secondResponseDone < 0 || thirdAppend <= secondResponseDone {
		t.Fatalf("active scheduled ordering = %v, want response.created < response.cancel < second append < second response.done < third append", timeline)
	}
	if firstResponseDone := indexOfTimeline(timeline, "in:response.done", 0); firstResponseDone <= cancelIndex {
		t.Fatalf("cancel did not win before first response terminality: %v", timeline)
	}

	appendAudio := audioPayloadsFromOutbound(outbound)
	if len(appendAudio) != 3 || len(appendAudio[0]) == 0 || len(appendAudio[1]) == 0 || len(appendAudio[2]) == 0 {
		t.Fatalf("active scheduled input payloads = %v, want three non-empty append payloads", audioLengths(appendAudio))
	}
	observedMu.Lock()
	observedCopy := append([]messages.StreamMessage(nil), observed...)
	observedMu.Unlock()
	seenReplacement, seenThird, seenStale := false, false, false
	for _, msg := range observedCopy {
		value, ok := msg.Value.(*messages.AudioDeltaValue)
		if !ok || value == nil {
			continue
		}
		switch string(value.Content) {
		case string([]byte{2, 0, 22, 0}):
			seenReplacement = true
		case string([]byte{3, 0, 23, 0}):
			seenThird = true
		case "cancel-stale":
			seenStale = true
		}
	}
	if !seenReplacement {
		t.Fatalf("replacement response audio was not observed; stream=%#v", observedCopy)
	}
	if !seenThird {
		t.Fatalf("third scheduled response audio was not observed; stream=%#v", observedCopy)
	}
	if seenStale {
		t.Fatalf("stale post-cancel provider audio crossed the stream boundary; stream=%#v", observedCopy)
	}
}

func TestSessionCommand_LiveRecordDirAudioInTurnRejectsUndispatchedScheduledInput(t *testing.T) {
	server := newCLILiveRecordDirCloseAfterTurnServer(2)
	t.Cleanup(server.shutdown)
	sessionInferencer, err := servicetest.NewOpenAIRealtimeSessionInferencerWithOptions(
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
	if !errors.Is(err, servicetest.ErrSessionScheduledAudioIncomplete) {
		t.Fatalf("incomplete scheduled session error = %v, want ErrSessionScheduledAudioIncomplete", err)
	}
	var incomplete *servicetest.SessionScheduledAudioIncompleteError
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
	sessionInferencer, err := servicetest.NewOpenAIRealtimeSessionInferencerWithOptions(
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
}

func TestSessionCommand_LiveRecordDirAudioInTurnUnexpectedProviderCloseWinsOverIncompleteSchedule(t *testing.T) {
	server := newCLILiveRecordDirReadErrorServer(errors.New("websocket: close 1008 (policy violation): Incorrect API key provided: invalid-test-key"))
	sessionInferencer, err := servicetest.NewOpenAIRealtimeSessionInferencerWithOptions(
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
	if !errors.Is(err, servicetest.ErrSessionScheduledAudioIncomplete) {
		t.Fatalf("provider close did not retain the incomplete schedule signal: %v", err)
	}
	var incomplete *servicetest.SessionScheduledAudioIncompleteError
	if !errors.As(err, &incomplete) || incomplete.Completed != 0 || incomplete.Dispatched != 0 || incomplete.Scheduled != 1 {
		t.Fatalf("provider close incomplete schedule counts = %+v, want completed=0 dispatched=0 scheduled=1; error=%v", incomplete, err)
	}
}

// TestSessionCommand_LiveScheduledAudioSplitsLargeTurnAtProviderBudget keeps
// one finite turn larger than the provider media-port budget. The runtime must
// preserve every sample and the single commit/response boundary while the
// canonical audio accumulator emits bounded append frames.
func TestSessionCommand_LiveScheduledAudioSplitsLargeTurnAtProviderBudget(t *testing.T) {
	const sampleCount = 48_001
	samples := make([]int16, sampleCount)
	for index := range samples {
		samples[index] = int16((index % 401) - 200)
	}
	var encoded bytes.Buffer
	if err := wavio.Write(&encoded, wavio.Rate24kHz, samples); err != nil {
		t.Fatalf("encode large scheduled turn: %v", err)
	}
	inputPath := filepath.Join(t.TempDir(), "large-scheduled-turn.wav")
	if err := os.WriteFile(inputPath, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write large scheduled turn: %v", err)
	}

	server := newCLILiveScheduledBoundaryServer(false)
	t.Cleanup(server.shutdown)
	agentCLI := newCLIScheduledBoundaryAgent(t, server)
	recordDir := filepath.Join(t.TempDir(), "large-scheduled-recording")
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs(scheduledBoundaryArgs(t.TempDir(), recordDir, inputPath))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := rootCmd.ExecuteContext(ctx); err != nil && !strings.Contains(err.Error(), "finalize provider evidence") {
		timeline, outbound, providerErrors, _, _ := server.snapshots()
		t.Fatalf("large scheduled turn failed: %v; provider errors=%v; timeline=%v; audio lengths=%v", err, providerErrors, timeline, audioLengthsFromOutbound(outbound))
	}
	if _, err := os.Stat(filepath.Join(recordDir, "manifest.json")); err != nil {
		t.Fatalf("large scheduled record-dir manifest was not published: %v", err)
	}

	timeline, outbound, providerErrors, dialCount, serverVADEnabled := server.snapshots()
	if dialCount != 1 || serverVADEnabled || len(providerErrors) != 0 {
		t.Fatalf("large scheduled provider state = dials:%d server_vad:%t errors:%v timeline=%v", dialCount, serverVADEnabled, providerErrors, timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.append") < 2 {
		t.Fatalf("large scheduled turn used one unbounded append: %v", timeline)
	}
	if countTimeline(timeline, "out:input_audio_buffer.commit") != 1 || countTimeline(timeline, "out:response.create") != 1 || countTimeline(timeline, "in:response.done") != 1 {
		t.Fatalf("large scheduled turn boundaries = %v, want one commit/response", timeline)
	}
	appendAudio := audioPayloadsFromOutbound(outbound)
	totalBytes := 0
	for index, payload := range appendAudio {
		if len(payload) > 2*48_000 {
			t.Fatalf("large scheduled append %d has %d bytes, exceeds provider frame budget", index, len(payload))
		}
		totalBytes += len(payload)
	}
	if totalBytes != sampleCount*2 {
		t.Fatalf("large scheduled audio bytes = %d, want exact %d across %d appends", totalBytes, sampleCount*2, len(appendAudio))
	}
	assertScheduledBoundaryOrder(t, timeline, 1)
}

func assertCLILiveRecordingBundle(t *testing.T, destination string, turns int, expected ...[]byte) {
	t.Helper()
	manifest := readCLIRecordingManifest(t, destination)
	assertCLIRecordingArtifacts(t, manifest)
	inputData, outputData := readCLIRecordingAudio(t, destination, expected)
	entries := readCLIRecordingEntries(t, destination, turns)
	assertCLIRecordingEntries(t, entries, turns, inputData, outputData)
}

func readCLIRecordingAudio(t *testing.T, destination string, expected [][]byte) ([]byte, []byte) {
	t.Helper()
	paths := []string{filepath.Join(destination, "audio/in-000.pcm"), filepath.Join(destination, "audio/out-000.pcm")}
	data := make([][]byte, len(paths))
	for index, path := range paths {
		var err error
		data[index], err = os.ReadFile(path)
		if err != nil {
			t.Fatalf("read finalized audio artifact %q: %v", path, err)
		}
	}
	if len(data[0]) == 0 || len(data[1]) == 0 {
		t.Fatalf("finalized append-only audio streams are empty: input=%d output=%d", len(data[0]), len(data[1]))
	}
	if len(expected) >= 1 && !bytes.Equal(data[0], expected[0]) {
		t.Fatalf("concatenated input PCM differs from provider append stream: got %d bytes, want %d", len(data[0]), len(expected[0]))
	}
	if len(expected) >= 2 && !bytes.Equal(data[1], expected[1]) {
		t.Fatalf("concatenated output PCM differs from fixture: got %d bytes %v, want %d %v", len(data[1]), data[1], len(expected[1]), expected[1])
	}
	return data[0], data[1]
}

func readCLIRecordingEntries(t *testing.T, destination string, turns int) []cliLiveRecordingEntry {
	t.Helper()
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
	return entries
}

func assertCLIRecordingEntries(t *testing.T, entries []cliLiveRecordingEntry, turns int, inputData, outputData []byte) {
	t.Helper()
	if len(entries) != turns {
		t.Fatalf("session log entries = %d, want %d", len(entries), turns)
	}
	var inputOffset, outputOffset uint64
	for index, entry := range entries {
		if entry.TurnIndex != index+1 || !entry.Input.Committed || entry.Input.AudioBytes == 0 || !entry.Response.Complete || entry.Response.AudioBytes == 0 {
			t.Fatalf("session log entry %d does not prove a committed input and completed audio response: %#v", index+1, entry)
		}
		if len(entry.Input.AudioSegments) != 1 || entry.Input.AudioSegments[0] != "audio/in-000.pcm" || entry.Input.AudioOffsetBytes != inputOffset {
			t.Fatalf("session log input %d does not preserve append-only offset: %#v", index+1, entry.Input)
		}
		if len(entry.Response.AudioSegments) != 1 || entry.Response.AudioSegments[0] != "audio/out-000.pcm" || entry.Response.AudioOffsetBytes != outputOffset {
			t.Fatalf("session log response %d does not preserve append-only offset: %#v", index+1, entry.Response)
		}
		if entry.Input.AudioOffsetBytes+entry.Input.AudioBytes > uint64(len(inputData)) || entry.Response.AudioOffsetBytes+entry.Response.AudioBytes > uint64(len(outputData)) {
			t.Fatalf("session log entry %d points outside append-only streams: %#v", index+1, entry)
		}
		inputOffset += entry.Input.AudioBytes
		outputOffset += entry.Response.AudioBytes
		wantText := "response turn " + strconv.Itoa(index+1)
		if entry.Response.Text != wantText {
			t.Fatalf("session log response %d text = %q, want %q; entry=%#v", index+1, entry.Response.Text, wantText, entry)
		}
	}
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

func scheduledAppendRange(timeline []string, start, end int) (first, count int) {
	first = -1
	for index := start; index < end; index++ {
		if timeline[index] == "out:input_audio_buffer.append" {
			if first < 0 {
				first = index
			}
			count++
		}
	}
	return first, count
}
