package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
)

func TestSessionSelfPlayCommandParsesBoundedRunOptions(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	var got selfplay.Request
	runner := selfplay.RunFunc(func(_ context.Context, request selfplay.Request) (selfplay.Result, error) {
		got = request
		return selfplay.Result{StopReason: selfplay.StopTurnTarget, Customer: selfplay.SideResult{CompletedTurns: request.MaxTurns}, Assistant: selfplay.SideResult{CompletedTurns: request.MaxTurns}}, nil
	})
	subject := NewSessionSelfPlayCommand(globalFlags, runner)

	outputDir := filepath.Join(t.TempDir(), "self-play")
	cmd := subject.Generate()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{
		"--api-key", "sk-test",
		"--output-dir", outputDir,
		"--provider", "openai",
		"--model", "gpt-realtime-2.1-mini",
		"--base-url", "wss://example.test/realtime",
		"--max-duration", "17s",
		"--max-turns", "4",
	})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute self-play command: %v", err)
	}
	if got.APIKey != "sk-test" || got.OutputDir != outputDir || got.Provider != "openai" || got.Model != "gpt-realtime-2.1-mini" || got.BaseURL != "wss://example.test/realtime" {
		t.Fatalf("parsed self-play request = %#v", got)
	}
	if got.MaxDuration != 17*time.Second || got.MaxTurns != 4 {
		t.Fatalf("parsed bounds = (%s, %d), want (17s, 4)", got.MaxDuration, got.MaxTurns)
	}
	if !strings.Contains(output.String(), "reason=turn_target customer_turns=4 assistant_turns=4") {
		t.Fatalf("result presentation = %q", output.String())
	}
}

func TestSessionSelfPlayCommandHelpDocumentsFixedPhaseOneContract(t *testing.T) {
	cmd := NewSessionSelfPlayCommand(flags.NewGlobalFlags(), nil).Generate()
	var helpOutput bytes.Buffer
	cmd.SetOut(&helpOutput)
	if err := cmd.Help(); err != nil {
		t.Fatalf("render self-play help: %v", err)
	}
	help := helpOutput.String()
	for _, want := range []string{
		"Customer persona:",
		"Assistant persona:",
		"Opening seed (sent once as customer text):",
		"raw PCM16 audio",
		"tools and transcript/text bridging are disabled",
		"--api-key",
		"--output-dir",
		"--max-duration",
		"--max-turns",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("self-play help does not contain %q:\n%s", want, help)
		}
	}
}
