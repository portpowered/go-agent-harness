//go:build live && darwin && cgo && !nomicrophone

package integration

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/wire"
	gwtesting "github.com/portpowered/go-agent-harness/go-llm-gateway/pkg/testing"
)

// TestLiveDarwinDeviceEACRoundTrip is the billed provider/device acceptance.
// File audio is intentionally forbidden here: both directions name the host
// defaults so AUVoiceIO owns sampling, callbacks, render reference, and EAC.
func TestLiveDarwinDeviceEACRoundTrip(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is not set; skipping billed device EAC acceptance")
	}
	if os.Getenv("AGENT_TEST_REAL_AUDIO") != "1" || os.Getenv("AGENT_HARNESS_LIVE_DEVICE_EAC") != "1" {
		t.Skip("AGENT_TEST_REAL_AUDIO=1 and AGENT_HARNESS_LIVE_DEVICE_EAC=1 are required")
	}

	workDir := t.TempDir()
	capturePath := filepath.Join(workDir, "darwin-device-eac.session.json")
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("initialize production CLI: %v", err)
	}
	stdout := &syncBuffer{}
	root := agentCLI.Generate()
	root.SetOut(stdout)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"--config-dir", workDir,
		"session",
		"--provider", "openai",
		"--model", "gpt-realtime-2.1-mini",
		"--api-key", apiKey,
		"--audio-in-device", "default",
		"--audio-out-device", "default",
		"--record", capturePath,
		"--max-duration", "20s",
		"Say exactly: native echo cancellation test passed. Then stop.",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("live device EAC command: %v\nstdout=%s", err, stdout.String())
	}
	capture, err := gwtesting.LoadSessionCapture(capturePath)
	if err != nil {
		t.Fatalf("load live device capture: %v", err)
	}
	counts := map[string]int{}
	for _, record := range capture.Records {
		counts[record.Type]++
		if record.Type == "error" {
			t.Fatalf("provider emitted error during device EAC round trip: %+v", record)
		}
	}
	if counts["input_audio_buffer.append"] == 0 {
		t.Fatal("physical microphone produced no provider-bound PCM")
	}
	if counts["response.output_audio.delta"] == 0 || counts["response.output_audio.done"] != 1 {
		t.Fatalf("physical replay evidence = deltas:%d done:%d, want audio and one completed replay", counts["response.output_audio.delta"], counts["response.output_audio.done"])
	}
	if counts["response.created"] != 1 {
		t.Fatalf("response.created count=%d, want exactly one; speaker echo may have retriggered the model", counts["response.created"])
	}
	normalizedTranscript := strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(stdout.String()), "assistant:", "")), " ")
	if !strings.Contains(normalizedTranscript, "native echo cancellation test passed") {
		t.Fatalf("assistant transcript missing expected phrase: %q", stdout.String())
	}
	t.Logf("live device EAC: model=gpt-realtime-2.1-mini input_appends=%d output_deltas=%d responses=1", counts["input_audio_buffer.append"], counts["response.output_audio.delta"])
}
