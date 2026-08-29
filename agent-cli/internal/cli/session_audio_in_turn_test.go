package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
)

func TestSessionCommandAudioInTurnBargeHelpExplainsExplicitPolicy(t *testing.T) {
	command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--help"})

	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("session --help: %v", err)
	}
	help := out.String()
	for _, want := range []string{
		"--audio-in-turn",
		"--audio-in-turn-barge",
		"completion-gated by default",
		"active prior response",
		"non-terminal",
		"Ordinary scheduled turns do not interrupt responses",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("session help missing %q:\n%s", want, help)
		}
	}
}

func TestSessionCommandAudioInTurnBargeRequiresTwoTurnsBeforeSetup(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no scheduled turns", args: []string{"--audio-in-turn-barge"}, want: "got 0"},
		{name: "one scheduled turn", args: []string{"--audio-in-turn-barge", "--audio-in-turn", "turn-one.wav"}, want: "got 1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), nil, nil).Generate()
			command.SetArgs(testCase.args)

			err := command.ExecuteContext(context.Background())
			if err == nil {
				t.Fatal("barge-in option unexpectedly succeeded")
			}
			if !errors.Is(err, services.ErrSessionAudioInTurnBargeRequiresSequence) {
				t.Fatalf("error = %v, want scheduled-turn cardinality error", err)
			}
			var cardinalityErr *services.SessionAudioInTurnBargeError
			if !errors.As(err, &cardinalityErr) {
				t.Fatalf("error type = %T, want *SessionAudioInTurnBargeError", err)
			}
			if !strings.Contains(err.Error(), "--audio-in-turn-barge") ||
				!strings.Contains(err.Error(), "at least two --audio-in-turn values") ||
				!strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("focused cardinality error = %v", err)
			}
		})
	}
}
