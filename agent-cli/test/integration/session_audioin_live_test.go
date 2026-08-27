//go:build live

// Opt-in live proof that file-based --audio-in elicits a real spoken response
// from the OpenAI Realtime API. This test never runs in default CI or
// hermetic targets: it requires both the OPENAI_API_KEY environment variable
// and the AGENT_HARNESS_LIVE_AUDIOIN=1 opt-in flag. See
// agent-cli/docs/session-audio-in-live.md for operator instructions.
package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

const liveAudioInTimeout = 60 * time.Second

func TestLiveSessionAudioInElicitsSpokenResponse(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping the live OpenAI Realtime audio-in round trip")
	}
	if os.Getenv("AGENT_HARNESS_LIVE_AUDIOIN") != "1" {
		t.Skip("AGENT_HARNESS_LIVE_AUDIOIN!=1; this live test bills real API usage and must be opted into explicitly")
	}

	workDir := t.TempDir()
	outputPath := filepath.Join(workDir, "response.wav")
	capturePath := filepath.Join(workDir, "live-audioin.json")

	agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
	if err != nil {
		t.Fatalf("initialize CLI: %v", err)
	}
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.SetArgs([]string{
		"--config-dir", workDir,
		"session",
		"--provider", "openai",
		"--model", "gpt-realtime-2.1-mini",
		"--api-key", apiKey,
		"--audio-in", liveAudioInWAVPath(t),
		"--audio-out", outputPath,
		"--record", capturePath,
		"--max-duration", liveAudioInTimeout.String(),
	})

	runErr := rootCmd.ExecuteContext(context.Background())

	capture, loadErr := gwtesting.LoadSessionCapture(capturePath)
	if loadErr != nil {
		t.Fatalf("load live capture (run error: %v): %v", runErr, loadErr)
	}

	commitSent := false
	responseCreateSent := false
	transcriptDone := false
	audioBytes := 0
	for _, record := range capture.Records {
		if record.Direction == gwtesting.DirectionClientToServer {
			switch record.Type {
			case "input_audio_buffer.commit":
				commitSent = true
			case "response.create":
				responseCreateSent = true
			}
			continue
		}
		if record.Direction != gwtesting.DirectionServerToClient {
			continue
		}
		switch {
		case record.Type == "response.output_audio_transcript.done":
			transcriptDone = true
		case record.Type == "response.output_audio.delta" || record.Type == "response.audio.delta":
			var payload struct {
				Delta string `json:"delta"`
			}
			raw := record.Payload
			if len(raw) == 0 {
				raw = record.Data
			}
			if json.Unmarshal(raw, &payload) == nil && payload.Delta != "" {
				if decoded, decodeErr := base64.StdEncoding.DecodeString(payload.Delta); decodeErr == nil {
					audioBytes += len(decoded)
				}
			}
		}
	}
	if !commitSent || !responseCreateSent {
		t.Fatalf("live capture missing client end-of-turn signaling (commit=%v response.create=%v, run error: %v): %s", commitSent, responseCreateSent, runErr, describeCapture(&capture))
	}
	if !transcriptDone {
		t.Fatalf("live session never received response.output_audio_transcript.done within %s (run error: %v)", liveAudioInTimeout, describeCapture(&capture))
	}
	if audioBytes == 0 {
		t.Fatalf("live session produced zero output audio bytes despite transcript completion (run error: %v)", describeCapture(&capture))
	}
	if runErr != nil && !strings.Contains(runErr.Error(), "context deadline exceeded") {
		t.Fatalf("live session returned an unexpected error after a complete response: %v", runErr)
	}
	info, statErr := os.Stat(outputPath)
	if statErr != nil {
		t.Fatalf("stat recorded response audio: %v", statErr)
	}
	if info.Size() <= 44 {
		t.Fatalf("--audio-out recorded %d bytes; want non-empty response audio", info.Size())
	}
	t.Logf("live audio-in proof: transcript done, %d output audio bytes, %d recorded bytes", audioBytes, info.Size())
}

func TestLiveSessionRecordDirAudioInTurnFinalizesOrderedBundle(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping the live OpenAI Realtime record-dir audio-in-turn proof")
	}
	if os.Getenv("AGENT_HARNESS_LIVE_AUDIOIN") != "1" {
		t.Skip("AGENT_HARNESS_LIVE_AUDIOIN!=1; this live test bills real API usage and must be opted into explicitly")
	}

	for _, testCase := range []struct {
		name  string
		turns int
	}{
		{name: "single turn", turns: 1},
		{name: "two turns", turns: 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			workDir := t.TempDir()
			recordDir := filepath.Join(workDir, "recording")

			agentCLI, err := wire.InitializeMockAgentCLI(&mockToolExecutor{}, &mockInferencer{response: "unused"})
			if err != nil {
				t.Fatalf("initialize CLI: %v", err)
			}
			rootCmd := agentCLI.Generate()
			rootCmd.SetOut(io.Discard)
			rootCmd.SetErr(io.Discard)
			args := []string{
				"--config-dir", workDir,
				"session",
				"--provider", "openai",
				"--model", "gpt-realtime",
				"--api-key", apiKey,
				"--record-dir", recordDir,
				"--max-duration", liveAudioInTimeout.String(),
			}
			for index := 0; index < testCase.turns; index++ {
				args = append(args, "--audio-in-turn", liveAudioInWAVPath(t))
			}
			rootCmd.SetArgs(args)

			ctx, cancel := context.WithTimeout(context.Background(), liveAudioInTimeout+15*time.Second)
			defer cancel()
			if err := rootCmd.ExecuteContext(ctx); err != nil {
				t.Fatalf("live record-dir audio-in-turn command: %v", err)
			}
			assertLiveRecordDirBundle(t, recordDir, testCase.turns)
		})
	}
}

func assertLiveRecordDirBundle(t *testing.T, destination string, wantTurns int) {
	t.Helper()
	manifestBytes, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatalf("read live recording manifest: %v", err)
	}
	var manifest struct {
		Artifacts []struct {
			Path string `json:"path"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode live recording manifest: %v", err)
	}

	seen := make(map[string]bool, len(manifest.Artifacts))
	manifestInputCount := 0
	manifestOutputCount := 0
	for _, artifact := range manifest.Artifacts {
		seen[artifact.Path] = true
		switch {
		case strings.HasPrefix(artifact.Path, "audio/in-"):
			manifestInputCount++
		case strings.HasPrefix(artifact.Path, "audio/out-"):
			manifestOutputCount++
		}
	}
	if manifestInputCount != wantTurns || manifestOutputCount != wantTurns {
		t.Fatalf("live recording manifest audio artifacts = input:%d output:%d, want %d each", manifestInputCount, manifestOutputCount, wantTurns)
	}
	for _, path := range []string{"client.transcript.jsonl", "agent.transcript.jsonl", "session-log.jsonl", "manifest.json"} {
		if path != "manifest.json" && !seen[path] {
			t.Fatalf("live recording manifest does not list %q", path)
		}
		if info, err := os.Stat(filepath.Join(destination, path)); err != nil || info.Size() == 0 {
			t.Fatalf("live recording artifact %q is missing or empty: %v", path, err)
		}
	}

	inputCount := 0
	outputCount := 0
	for index := 0; index < wantTurns; index++ {
		for _, side := range []string{"in", "out"} {
			path := filepath.Join(destination, "audio", side+"-"+liveRecordingDigits(index)+".pcm")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat live %s audio segment %q: %v", side, path, err)
			}
			if info.Size() == 0 {
				t.Fatalf("live %s audio segment %q is empty", side, path)
			}
			if side == "in" {
				inputCount++
			} else {
				outputCount++
			}
		}
	}
	if inputCount != wantTurns || outputCount != wantTurns {
		t.Fatalf("live recording audio segments = input %d/output %d, want %d/%d", inputCount, outputCount, wantTurns, wantTurns)
	}

	logBytes, err := os.ReadFile(filepath.Join(destination, "session-log.jsonl"))
	if err != nil {
		t.Fatalf("read live session log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	if len(lines) != wantTurns {
		t.Fatalf("live session log entries = %d, want %d", len(lines), wantTurns)
	}
	for index, line := range lines {
		var entry struct {
			TurnIndex int `json:"turn_index"`
			Input     struct {
				AudioBytes uint64 `json:"audio_bytes"`
				Committed  bool   `json:"committed"`
			} `json:"input"`
			Response struct {
				AudioBytes uint64 `json:"audio_bytes"`
				Complete   bool   `json:"complete"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode live session log entry %d: %v", index+1, err)
		}
		if entry.TurnIndex != index+1 || !entry.Input.Committed || entry.Input.AudioBytes == 0 || !entry.Response.Complete || entry.Response.AudioBytes == 0 {
			t.Fatalf("live session log entry %d does not prove an ordered committed input and completed audio response: %#v", index+1, entry)
		}
	}
	t.Logf("live record-dir proof: %d ordered turn bundle(s) finalized with non-empty input/output audio", wantTurns)
}

func liveRecordingDigits(index int) string {
	value := strconv.Itoa(index)
	return strings.Repeat("0", 3-len(value)) + value
}

func liveAudioInWAVPath(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve WAV fixture path: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "internal", "services", "testdata", "session-audio-input", "utterance.wav")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("committed WAV fixture missing: %v", err)
	}
	return path
}

func describeCapture(capture *gwtesting.SessionCapture) string {
	types := make([]string, 0, len(capture.Records))
	for _, record := range capture.Records {
		types = append(types, record.Type)
	}
	return strings.Join(types, ",")
}
