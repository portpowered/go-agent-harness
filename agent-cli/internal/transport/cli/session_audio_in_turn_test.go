package cli

import servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"

import sessionclock "github.com/portpowered/go-agent-harness/go-audio/pkg/clock"

import sessionservicewire "github.com/portpowered/go-agent-harness/agent-cli/internal/services/wire"

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
)

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
			command := NewSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), newTestSessionService(sessionservicewire.SessionDependencies{Clock: sessionclock.Real{}}), nil).Generate()
			command.SetArgs(testCase.args)

			err := command.ExecuteContext(context.Background())
			if err == nil {
				t.Fatal("barge-in option unexpectedly succeeded")
			}
			if !errors.Is(err, servicetest.ErrSessionAudioInTurnBargeRequiresSequence) {
				t.Fatalf("error = %v, want scheduled-turn cardinality error", err)
			}
			var cardinalityErr *servicetest.SessionAudioInTurnBargeError
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
