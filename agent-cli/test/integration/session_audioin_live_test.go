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

	transcriptDone := false
	audioBytes := 0
	for _, record := range capture.Records {
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
