package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
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
		{name: "browser-cdp-url alone is not session content", args: []string{"--browser-cdp-url", testCDPURL}, want: false},
		{name: "browser-tools alone is not session content", args: []string{"--browser-tools", "webmcp"}, want: false},
		{name: "browser-headless alone is not session content", args: []string{"--browser-headless"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := newTestSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), testSessionDeps{}).Generate()
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
	command := newTestSessionCommand(flags.NewAskFlags(), globalFlags, testSessionDeps{}).Generate()
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
	configYAML := "model:\n  provider: openai\n  openai:\n    model: gpt-realtime\n    api_key: test-key\n"
	if err := os.WriteFile(filepath.Join(globalFlags.ConfigDirPath, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write session config: %v", err)
	}
	command := newTestLiveSessionCommand(flags.NewAskFlags(), globalFlags, nil, nil).Generate()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	missingPath := t.TempDir() + "/does-not-exist.wav"
	command.SetArgs([]string{"--audio-in", missingPath, "--provider", config.ProviderOpenAI, "--model", "gpt-realtime", "--api-key", "test-key"})

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
		{name: "endpoint only", args: []string{"--browser-cdp-url", testCDPURL}},
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
			command := newTestSessionCommand(flags.NewAskFlags(), globalFlags, testSessionDeps{}).Generate()
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
	root := newTestRootCommandWithProbeFleetCommand(NewProbeFleetCommand(nil, nil, newReplayRuntimeServiceForTest()), inferencer)
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--config-dir", filepath.Join(artifactRoot, "config"),
		"session",
		"--prompt", "seed the session",
		"--provider", config.ProviderOpenAI,
		"--model", servicetest.DefaultOpenAIRealtimeModel,
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
	configDir := filepath.Join(artifactRoot, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	configYAML := "model:\n  provider: openai\n  openai:\n    model: " + servicetest.DefaultOpenAIRealtimeModel + "\n    api_key: test-key\n"
	if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
		t.Fatalf("write session config: %v", err)
	}
	recordPath := filepath.Join(artifactRoot, "audio-in-success.json")
	audioInPath := filepath.Join(artifactRoot, "in.pcm")
	if err := os.WriteFile(audioInPath, []byte{0x10, 0x27, 0xf0, 0xd8}, 0o600); err != nil {
		t.Fatalf("write audio-in fixture: %v", err)
	}
	inferencer := newCLIAudioInputSuccessInferencer(cliDurationCompleteEvents())
	globalFlags := flags.NewGlobalFlags()
	globalFlags.ConfigDirPath = configDir
	command := newTestLiveSessionCommand(flags.NewAskFlags(), globalFlags, inferencer, nil).Generate()
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{
		"--audio-in", audioInPath,
		"--provider", config.ProviderOpenAI,
		"--model", servicetest.DefaultOpenAIRealtimeModel,
		"--api-key", "test-key",
		"--record", recordPath,
		"--max-duration", "40ms",
	})

	if err := command.Execute(); err != nil {
		t.Fatalf("session --audio-in --record: %v (audio commit sends=%d)\nstdout=%q\nstderr=%q", err, inferencer.audioCommits.Load(), stdout.String(), stderr.String())
	}
}

// TestSessionImageRefusedWhenModelCannotAcceptImageInput pins the session
// --image admission contract: an image is refused before any provider session
// opens when the selected provider/model cannot accept image input, whether
// the provider lacks image input or configured model metadata omits it.
func TestSessionImageRefusedWhenModelCannotAcceptImageInput(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		modelsYML string
		want      string
	}{
		{
			name: "provider without image input",
			args: []string{"--provider", config.ProviderGrok, "--model", "grok-voice"},
			want: `model "grok-voice" does not support image input capability`,
		},
		{
			name:      "configured model without image modality",
			args:      []string{"--provider", config.ProviderOpenAI, "--model", "gpt-realtime-2.1"},
			modelsYML: "models:\n  - name: gpt-realtime-2.1\n    providers: [openai]\n    input_modalities: [text, audio]\n",
			want:      `model "gpt-realtime-2.1" does not support image input capability`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			configDir := filepath.Join(root, "config")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatalf("create config directory: %v", err)
			}
			if test.modelsYML != "" {
				if err := os.WriteFile(filepath.Join(configDir, config.ModelsFileName), []byte(test.modelsYML), 0o600); err != nil {
					t.Fatalf("write models.yaml: %v", err)
				}
			}
			imagePath := filepath.Join(root, "photo.png")
			writeSessionCapabilityPNG(t, imagePath)
			inferencer := &connectRecordingInferencer{}
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = configDir
			command := newTestLiveSessionCommand(flags.NewAskFlags(), globalFlags, inferencer, nil).Generate()
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			command.SetArgs(append(append([]string(nil), test.args...), "--api-key", "test-key", "--image", imagePath))

			err := command.ExecuteContext(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("session --image error = %v, want %q\nstdout=%q\nstderr=%q", err, test.want, stdout.String(), stderr.String())
			}
			if inferencer.opened.Load() {
				t.Fatal("provider session opened for a model without image input")
			}
		})
	}
}

func writeSessionCapabilityPNG(t *testing.T, path string) {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

// connectRecordingInferencer records whether a provider session was requested
// and refuses it, so a test can prove admission failed before connection.
type connectRecordingInferencer struct{ opened atomic.Bool }

func (i *connectRecordingInferencer) ConnectSession(context.Context) (messages.Session, error) {
	i.opened.Store(true)
	return nil, errors.New("provider session must not open")
}

// TestSessionRunFailureRedactsProviderCredentialFromStderr pins the CLI
// error-rendering contract: when a provider failure echoes the session's API
// key, whether it came from --api-key or the config file, the error cobra
// renders to stderr carries the redaction marker instead of the key.
func TestSessionRunFailureRedactsProviderCredentialFromStderr(t *testing.T) {
	for _, test := range []struct {
		name      string
		configKey string
		flagKey   string
		secret    string
	}{
		{name: "flag credential", configKey: "sk-config-unused-000000", flagKey: "sk-flag-secret-1234567890", secret: "sk-flag-secret-1234567890"},
		{name: "config credential", configKey: "sk-config-secret-0987654321", secret: "sk-config-secret-0987654321"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			configYAML := "model:\n  provider: openai\n  openai:\n    model: gpt-realtime\n    api_key: " + test.configKey + "\n"
			if err := os.WriteFile(filepath.Join(configDir, config.ConfigFileName), []byte(configYAML), 0o600); err != nil {
				t.Fatalf("write session config: %v", err)
			}
			globalFlags := flags.NewGlobalFlags()
			globalFlags.ConfigDirPath = configDir
			inferencer := credentialEchoInferencer{secret: test.secret}
			command := newTestLiveSessionCommand(flags.NewAskFlags(), globalFlags, inferencer, nil).Generate()
			var stdout, stderr bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			args := []string{"--prompt", "hello", "--provider", config.ProviderOpenAI, "--model", "gpt-realtime"}
			if test.flagKey != "" {
				args = append(args, "--api-key", test.flagKey)
			}
			command.SetArgs(args)

			err := command.ExecuteContext(context.Background())
			if err == nil {
				t.Fatal("session returned nil error, want provider failure")
			}
			for _, rendered := range []string{err.Error(), stderr.String(), stdout.String()} {
				if strings.Contains(rendered, test.secret) {
					t.Fatalf("rendered session failure leaks the credential: %q", rendered)
				}
			}
			if !strings.Contains(stderr.String(), "Incorrect API key provided: [REDACTED]") {
				t.Fatalf("stderr = %q, want the redacted provider failure", stderr.String())
			}
		})
	}
}

// credentialEchoInferencer fails provider connection the way a provider
// rejecting a key does: by echoing the key in its error.
type credentialEchoInferencer struct{ secret string }

func (i credentialEchoInferencer) ConnectSession(context.Context) (messages.Session, error) {
	return nil, fmt.Errorf("realtime connect: 401 Unauthorized: Incorrect API key provided: %s", i.secret)
}
