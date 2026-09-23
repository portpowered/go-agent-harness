package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	serviceSelfPlay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/selfplay"
)

func TestSessionSelfPlayCommandParsesBoundedRunOptions(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	subject := NewSessionSelfPlayCommand(globalFlags, nil)

	var got serviceSelfPlay.Request
	subject.SetRunner(func(_ context.Context, opts serviceSelfPlay.Request) (serviceSelfPlay.Result, error) {
		got = opts
		return serviceSelfPlay.Result{}, nil
	})

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
		t.Fatalf("parsed self-play options = %#v", got)
	}
	if got.MaxDuration != 17*time.Second || got.MaxTurns != 4 {
		t.Fatalf("parsed bounds = (%s, %d), want (17s, 4)", got.MaxDuration, got.MaxTurns)
	}
}

func TestSessionSelfPlayCommandResolvesProviderInputsFromConfig(t *testing.T) {
	t.Setenv("AGENT_MODEL__OPENAI__API_KEY", "sk-config")
	t.Setenv("AGENT_MODEL__OPENAI__BASE_URL", "wss://config.example.test/realtime")
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	subject := NewSessionSelfPlayCommand(globalFlags, nil)
	var got serviceSelfPlay.Request
	subject.SetRunner(func(_ context.Context, request serviceSelfPlay.Request) (serviceSelfPlay.Result, error) {
		got = request
		return serviceSelfPlay.Result{}, nil
	})
	cmd := subject.Generate()
	cmd.SetArgs([]string{"--output-dir", filepath.Join(t.TempDir(), "self-play")})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute self-play command: %v", err)
	}
	if got.APIKey != "sk-config" || got.BaseURL != "wss://config.example.test/realtime" {
		t.Fatalf("resolved self-play provider inputs = (key %q, base %q)", got.APIKey, got.BaseURL)
	}
	if got.Provider != "" || got.Model != "" || got.MaxDuration != 0 || got.MaxTurns != 0 {
		t.Fatalf("adapter supplied service-owned defaults: %#v", got)
	}
}

func TestSessionSelfPlayCommandHelpDocumentsServiceContract(t *testing.T) {
	cmd := NewSessionSelfPlayCommand(flags.NewGlobalFlags(), nil).Generate()
	var helpOutput bytes.Buffer
	cmd.SetOut(&helpOutput)
	if err := cmd.Help(); err != nil {
		t.Fatalf("render self-play help: %v", err)
	}
	help := helpOutput.String()
	for _, want := range []string{
		"Run a bounded live self-play conversation through the runtime service.",
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
