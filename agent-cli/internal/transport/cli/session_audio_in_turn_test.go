package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"strings"
	"testing"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	servicetest "github.com/portpowered/go-agent-harness/agent-cli/internal/services/servicetest"
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
			command := newTestSessionCommand(flags.NewAskFlags(), flags.NewGlobalFlags(), testSessionDeps{}).Generate()
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

// A scripted browser announces Invoke entry before Invoke returns its ID. Releasing
// then can manufacture a terminal without broker provenance. This scenario tests
// interruption after admission, so wait for that actual protocol boundary.
func waitSessionAudioBrowserDispatch(ctx context.Context, events <-chan webmcp.BrokerEvent, tool string) error {
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("browser watch closed before dispatch of %s", tool)
			}
			if event.Type == webmcp.BrokerEventInvocationCreated && event.State == webmcp.InvocationDispatched && event.ToolName == tool {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("wait for browser dispatch of %s: %w", tool, ctx.Err())
		}
	}
}
