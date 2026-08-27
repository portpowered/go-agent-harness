package services_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/cli"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

func TestSessionCommandHelpExposesVoiceFlagAndSupportedValues(t *testing.T) {
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("session --help: %v", err)
	}
	help := out.String()
	if !strings.Contains(help, "--voice string") {
		t.Fatalf("session help does not expose --voice:\n%s", help)
	}
	for _, voice := range services.SupportedOpenAIRealtimeVoices() {
		if !strings.Contains(help, voice) {
			t.Fatalf("session help does not list supported voice %q:\n%s", voice, help)
		}
	}
}

func TestSessionCommandVoiceFlagParsesDocumentedValuesUnchanged(t *testing.T) {
	for _, voice := range services.SupportedOpenAIRealtimeVoices() {
		t.Run(voice, func(t *testing.T) {
			cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
			if err := cmd.ParseFlags([]string{"--voice", voice}); err != nil {
				t.Fatalf("ParseFlags: %v", err)
			}
			got, err := cmd.Flags().GetString("voice")
			if err != nil {
				t.Fatalf("GetString(voice): %v", err)
			}
			if got != voice {
				t.Fatalf("parsed voice = %q, want %q", got, voice)
			}
		})
	}
}

func TestSessionCommandInvalidVoiceFailsBeforeReplayConsumption(t *testing.T) {
	const rejected = "not-a-voice"
	cmd := cli.NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	cmd.SetArgs([]string{
		"--voice", rejected,
		"--replay", filepath.Join(t.TempDir(), "missing.session.json"),
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid voice error")
	}
	if !errors.Is(err, services.ErrInvalidOpenAIRealtimeVoice) {
		t.Fatalf("error = %v, want ErrInvalidOpenAIRealtimeVoice", err)
	}
	var typed *services.InvalidOpenAIRealtimeVoiceError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want InvalidOpenAIRealtimeVoiceError", err)
	}
	if typed.Voice != rejected {
		t.Fatalf("rejected voice = %q, want %q", typed.Voice, rejected)
	}
	if strings.Contains(err.Error(), "missing.session.json") {
		t.Fatalf("invalid voice validation consumed replay path: %v", err)
	}
}
