package integration

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/portpowered/agent-cli/internal/wire"
)

const (
	adHocEnvPrompt     = "AD_HOC_ASK_PROMPT"
	adHocEnvRecordPath = "AD_HOC_RECORD_PATH"
	adHocEnvReplayPath = "AD_HOC_REPLAY_PATH"
)

// TestAdHocAsk runs the ask command with a prompt from AD_HOC_ASK_PROMPT for ad hoc testing.
// Usage:
//
//	# With real API (requires config):
//	AD_HOC_ASK_PROMPT="What is the weather?" go test -v -run TestAdHocAsk ./test/integration/...
//
//	# Record LLM traffic to file:
//	AD_HOC_ASK_PROMPT="Hello" AD_HOC_RECORD_PATH=./captures.json go test -v -run TestAdHocAsk ./test/integration/...
//
//	# Replay from file (no live API; requires a capture from --record first):
//	AD_HOC_ASK_PROMPT="Hello" AD_HOC_REPLAY_PATH=./captures.json go test -v -run TestAdHocAsk ./test/integration/...
func TestAdHocAsk(t *testing.T) {
	prompt := ""
	if prompt == "" {
		t.Skipf("skipping ad hoc ask test: set %s to run", adHocEnvPrompt)
	}
	// TODO:
	// RECORD and create a replay test for verifying image reads
	// RECORD and verify behavior with an anthropic read
	// RECORD and verify multi tool use batch
	// RECORD and verify multi tool use multi turn

	replayPath := "" //"./testdata/interest.json" //"./testdata/streaming_2_2.json" // os.Getenv(adHocEnvReplayPath)
	recordPath := "" // "./testdata/interest.json" // os.Getenv(adHocEnvRecordPath)
	// When using replay, set minimal config so validation passes (HTTP is intercepted).
	// Replay requires a capture file produced by --record (streaming format must match).
	if replayPath != "" {
		if os.Getenv("AGENT_MODEL__PROVIDER") == "" {
			if err := os.Setenv("AGENT_MODEL__PROVIDER", "openrouter"); err != nil {
				t.Fatalf("set provider env: %v", err)
			}
		}
		if os.Getenv("AGENT_MODEL__OPENROUTER__API_KEY") == "" {
			if err := os.Setenv("AGENT_MODEL__OPENROUTER__API_KEY", "replay-dummy"); err != nil {
				t.Fatalf("set replay api key env: %v", err)
			}
		}
	}

	//os.Setenv("AGENT_MODEL__OPENROUTER__API_KEY", "")
	agentCLI, err := wire.InitializeAgentCLI()
	if err != nil {
		t.Fatalf("failed to initialize agent CLI: %v", err)
	}

	testWriter := NewTestWriter()
	rootCmd := agentCLI.Generate()
	rootCmd.SetOut(testWriter.Stdout())
	rootCmd.SetErr(testWriter.Stderr())
	rootCmd.SetIn(strings.NewReader("")) // avoid blocking on real stdin when debugging
	args := []string{"ask"}
	if replayPath != "" {
		args = append(args, "--replay", replayPath)
	}
	if recordPath != "" {
		args = append(args, "--record", recordPath)
	}
	args = append(args, prompt)
	// args = append(args, "./dh1.jpg")
	args = append(args, "--stream", "-vv")
	args = append(args, "--log-to-stdout")
	args = append(args, "--output-reasoning-tokens")
	rootCmd.SetArgs(args)

	ctx := context.Background()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("ask failed: %v", err)
	}
	actual := strings.TrimSpace(testWriter.StdoutString())
	t.Logf("prompt: %s", prompt)
	t.Logf("output: %s", actual)
	if actual == "" {
		t.Error("expected non-empty output")
	}
	// t.Fatalf("hello")
}
