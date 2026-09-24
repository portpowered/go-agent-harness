package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	runtimeSelfPlay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
)

func TestSessionSelfPlayCommandParsesBoundedRunOptions(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	var got runtimeSelfPlay.Request
	runner := selfPlayRuntimeFunc(func(_ context.Context, request runtimeSelfPlay.Request) (runtimeSelfPlay.Result, error) {
		got = request
		return runtimeSelfPlay.Result{}, nil
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
		t.Fatalf("parsed self-play options = %#v", got)
	}
	if got.MaxDuration != 17*time.Second || got.MaxTurns != 4 {
		t.Fatalf("parsed bounds = (%s, %d), want (17s, 4)", got.MaxDuration, got.MaxTurns)
	}
}

func TestSessionSelfPlayCommandLeavesOmittedDefaultsForService(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	var got runtimeSelfPlay.Request
	runner := selfPlayRuntimeFunc(func(_ context.Context, request runtimeSelfPlay.Request) (runtimeSelfPlay.Result, error) {
		got = request
		return runtimeSelfPlay.Result{}, nil
	})
	subject := NewSessionSelfPlayCommand(globalFlags, runner)

	cmd := subject.Generate()
	cmd.SetArgs([]string{"--output-dir", filepath.Join(t.TempDir(), "self-play")})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute self-play command: %v", err)
	}
	if got.Provider != "" || got.Model != "" || got.MaxDuration != 0 || got.MaxTurns != 0 {
		t.Fatalf("omitted self-play values = provider %q, model %q, duration %s, turns %d; want zero values for service defaults", got.Provider, got.Model, got.MaxDuration, got.MaxTurns)
	}
}

func TestSessionSelfPlayCommandHelpDocumentsTransportContract(t *testing.T) {
	cmd := NewSessionSelfPlayCommand(flags.NewGlobalFlags(), nil).Generate()
	var helpOutput bytes.Buffer
	cmd.SetOut(&helpOutput)
	if err := cmd.Help(); err != nil {
		t.Fatalf("render self-play help: %v", err)
	}
	help := helpOutput.String()
	for _, want := range []string{
		"bounded self-play harness",
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

func TestSessionSelfPlayCommandTranslatesConfiguredRequestAndPresentsResult(t *testing.T) {
	t.Setenv("AGENT_MODEL__OPENAI__API_KEY", "environment-key")
	for _, test := range []struct {
		name       string
		apiKey     string
		wantAPIKey string
	}{
		{name: "flag credential overrides config", apiKey: "flag-key", wantAPIKey: "flag-key"},
		{name: "configured credential fallback", wantAPIKey: "environment-key"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got runtimeSelfPlay.Request
			runner := selfPlayRuntimeFunc(func(_ context.Context, request runtimeSelfPlay.Request) (runtimeSelfPlay.Result, error) {
				got = request
				return runtimeSelfPlay.Result{
					StopReason: runtimeSelfPlay.StopTurnTarget,
					Customer:   runtimeSelfPlay.SideResult{CompletedTurns: 2},
					Assistant:  runtimeSelfPlay.SideResult{CompletedTurns: 2},
				}, nil
			})
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = t.TempDir()
			command := NewSessionSelfPlayCommand(globalFlags, runner).Generate()
			var output bytes.Buffer
			command.SetOut(&output)
			outputDir := filepath.Join(t.TempDir(), "self-play")
			args := []string{"--output-dir", outputDir, "--base-url", "wss://example.test/realtime"}
			if test.apiKey != "" {
				args = append(args, "--api-key", test.apiKey)
			}
			command.SetArgs(args)
			if err := command.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("execute self-play command: %v", err)
			}
			if got.APIKey != test.wantAPIKey || got.Provider != "" || got.Model != "" || got.OutputDir != outputDir || got.BaseURL != "wss://example.test/realtime" || got.MaxDuration != 0 || got.MaxTurns != 0 {
				t.Fatalf("runtime request = %#v", got)
			}
			if output.String() != "self-play stopped: reason=turn_target customer_turns=2 assistant_turns=2\n" {
				t.Fatalf("presented result = %q", output.String())
			}
		})
	}
}

type selfPlayRuntimeFunc func(context.Context, runtimeSelfPlay.Request) (runtimeSelfPlay.Result, error)

func (f selfPlayRuntimeFunc) Run(ctx context.Context, request runtimeSelfPlay.Request) (runtimeSelfPlay.Result, error) {
	return f(ctx, request)
}
