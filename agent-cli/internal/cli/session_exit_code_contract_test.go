package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

// TestSessionHasExplicitModeMatrix pins the fix for "session --prompt exits 0
// and prints a help dump instead of doing the work" (and its widened
// --audio-in shape): sessionHasExplicitMode must recognize every content flag
// that previously fell through the cracks, while continuing to treat a truly
// empty invocation and pure browser flags as having no explicit mode (their
// own dedicated non-admission contract is unaffected by this fix).
func TestSessionHasExplicitModeMatrix(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "bare invocation has no explicit mode", args: nil, want: false},
		{name: "prompt flag", args: []string{"--prompt", "hello"}, want: true},
		{name: "positional prompt words", args: []string{"do", "the", "thing"}, want: true},
		{name: "audio-in flag", args: []string{"--audio-in", "in.wav"}, want: true},
		{name: "system-prompt flag", args: []string{"--system-prompt", "be nice"}, want: true},
		{name: "audio-out flag", args: []string{"--audio-out", "out.wav"}, want: true},
		{name: "audio-in-turn-barge flag alone", args: []string{"--audio-in-turn-barge"}, want: true},
		{name: "audio-interrupt-on-tool flag alone", args: []string{"--audio-interrupt-on-tool", "tool"}, want: true},
		{name: "record flag", args: []string{"--record", "cap.json"}, want: true},
		{name: "replay flag", args: []string{"--replay", "cap.json"}, want: true},
		{name: "record-dir flag", args: []string{"--record-dir", "dir"}, want: true},
		{name: "audio-in-turn flag", args: []string{"--audio-in-turn", "turn.wav"}, want: true},
		{name: "audio-interrupt flag", args: []string{"--audio-interrupt", "in.wav"}, want: true},
		{name: "browser-cdp-url alone is not session content", args: []string{"--browser-cdp-url", "http://127.0.0.1:9222"}, want: false},
		{name: "browser-tools alone is not session content", args: []string{"--browser-tools", "webmcp"}, want: false},
		{name: "browser-headless alone is not session content", args: []string{"--browser-headless"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
			if err := command.ParseFlags(tt.args); err != nil {
				t.Fatalf("parse flags %v: %v", tt.args, err)
			}
			got := sessionHasExplicitMode(command, command.Flags().Args(), nil)
			if got != tt.want {
				t.Fatalf("sessionHasExplicitMode(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// TestSessionPromptOnlyExitsNonZeroAndNamesTheProblem pins Instance 1: the
// exact reproduced shape ("session --prompt" with nothing else) used to exit
// 0 and print a help dump. It must now exit non-zero and name the actual live
// provider configuration problem instead of silently printing help.
func TestSessionPromptOnlyExitsNonZeroAndNamesTheProblem(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := NewSessionCommand(flags.NewAskFlags(), globalFlags, nil, nil).Generate()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--prompt", "hello"})

	err := command.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("session --prompt alone returned a nil error; want a named failure instead of a silent help dump")
	}
	for _, want := range []string{"openai realtime api key is missing", "AGENT_MODEL__OPENAI__API_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to name %q as the live-session problem", err, want)
		}
	}
}

// TestSessionAudioInMissingFileExitsNonZeroAndSaysFileDoesNotExist pins the
// widened shape of Instance 1: "session --audio-in <missing file>" alone used
// to also exit 0 and print a help dump instead of surfacing the missing-file
// error. It must now exit non-zero and say the file does not exist.
func TestSessionAudioInMissingFileExitsNonZeroAndSaysFileDoesNotExist(t *testing.T) {
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = t.TempDir()
	command := NewSessionCommand(flags.NewAskFlags(), globalFlags, nil, nil).Generate()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	missingPath := t.TempDir() + "/does-not-exist.wav"
	command.SetArgs([]string{"--audio-in", missingPath})

	err := command.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("session --audio-in <missing file> returned a nil error; want a named failure instead of a silent help dump")
	}
	if !strings.Contains(err.Error(), missingPath) {
		t.Fatalf("error = %q, want it to name the missing path %q", err, missingPath)
	}
	if !strings.Contains(err.Error(), "no such file or directory") && !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error = %q, want it to say the file does not exist", err)
	}
}

// TestSessionBareAndBrowserNonAdmissionStillExitZero asserts no
// over-triggering from the sessionHasExplicitMode fix: an invocation that
// genuinely has nothing to do (bare, or a browser flag/config without
// explicit --browser-tools admission) must still print help and exit 0,
// exactly as before.
func TestSessionBareAndBrowserNonAdmissionStillExitZero(t *testing.T) {
	tests := []struct {
		name       string
		configYAML string
		args       []string
	}{
		{name: "endpoint only", args: []string{"--browser-cdp-url", "http://127.0.0.1:9222"}},
		{name: "managed control only", args: []string{"--browser-headless"}},
		{
			name: "config only",
			configYAML: `
browser:
  tools:
    enabled: true
    backend: webmcp
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configDir := t.TempDir()
			if tt.configYAML != "" {
				if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(tt.configYAML), 0600); err != nil {
					t.Fatalf("write config: %v", err)
				}
			}
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = configDir
			command := NewSessionCommand(flags.NewAskFlags(), globalFlags, nil, nil).Generate()
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetArgs(tt.args)

			if err := command.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("invocation with nothing to do returned an error: %v", err)
			}
			if !strings.Contains(out.String(), "Usage:") {
				t.Fatalf("invocation with nothing to do did not print help:\n%s", out.String())
			}
		})
	}
}

// TestSessionPromptWithRecordStillSucceeds asserts no over-triggering from
// the sessionHasExplicitMode fix: a fully specified, previously-successful
// invocation using --prompt (now newly routed past the admission gate)
// continues to run a real session end to end and exit 0.
func TestSessionPromptWithRecordStillSucceeds(t *testing.T) {
	artifactRoot := t.TempDir()
	recordPath := filepath.Join(artifactRoot, "prompt-success.json")
	inferencer := newCLIDurationInferencer(cliDurationPartialEvents())
	root := newTestRootCommandWithProbeFleetCommand(NewProbeFleetCommand(), inferencer)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--config-dir", filepath.Join(artifactRoot, "config"),
		"session",
		"--prompt", "seed the session",
		"--provider", config.ProviderOpenAI,
		"--model", services.DefaultOpenAIRealtimeModel,
		"--api-key", "test-key",
		"--record", recordPath,
		"--max-duration", "40ms",
	})

	if err := root.Execute(); err != nil {
		t.Fatalf("session --prompt --record: %v\nstdout=%q\nstderr=%q", err, stdout.String(), stderr.String())
	}
}

// TestSessionAudioInWithExistingFileStillSucceeds asserts no over-triggering
// from the --audio-in file-existence preflight: a real, existing --audio-in
// file must not be rejected as missing, and the fully specified invocation
// continues to run a real session end to end and exit 0.
func TestSessionAudioInWithExistingFileStillSucceeds(t *testing.T) {
	artifactRoot := t.TempDir()
	recordPath := filepath.Join(artifactRoot, "audio-in-success.json")
	audioInPath := filepath.Join(artifactRoot, "in.pcm")
	if err := os.WriteFile(audioInPath, []byte{0, 0, 1, 0, 2, 0, 3, 0}, 0o600); err != nil {
		t.Fatalf("write audio-in fixture: %v", err)
	}
	inferencer := newCLIDurationInferencer(cliDurationPartialEvents())
	root := newTestRootCommandWithProbeFleetCommand(NewProbeFleetCommand(), inferencer)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--config-dir", filepath.Join(artifactRoot, "config"),
		"session",
		"--audio-in", audioInPath,
		"--provider", config.ProviderOpenAI,
		"--model", services.DefaultOpenAIRealtimeModel,
		"--api-key", "test-key",
		"--record", recordPath,
		"--max-duration", "40ms",
	})

	if err := root.Execute(); err != nil {
		t.Fatalf("session --audio-in --record: %v\nstdout=%q\nstderr=%q", err, stdout.String(), stderr.String())
	}
}
