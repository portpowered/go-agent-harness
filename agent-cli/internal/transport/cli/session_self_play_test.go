package cli

import (
	"bytes"
	"context"
	"io"
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

	var got serviceSelfPlay.RunOptions
	subject.SetRunner(func(_ context.Context, _ io.Writer, opts serviceSelfPlay.RunOptions) error {
		got = opts
		return nil
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
	if got.ConfigDir != globalFlags.ConfigDir() {
		t.Fatalf("config dir = %q, want %q", got.ConfigDir, globalFlags.ConfigDir())
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
