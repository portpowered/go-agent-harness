package output

import (
	"bytes"
	"errors"
	"github.com/portpowered/go-agent-harness/go-agent-loop/pkg/messages"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
	"strings"
	"testing"
)

func TestReplayRendererWaitsForJoinedTerminal(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "late failure"}[failed], func(t *testing.T) {
			assertReplayJoinedTerminal(t, failed)
		})
	}
}

func assertReplayJoinedTerminal(t *testing.T, failed bool) {
	t.Helper()
	renderer := NewLiveEventRenderer(true)
	var out bytes.Buffer
	provider := &messages.SessionCloseValue{TerminalReason: messages.TerminalReasonProviderAuthoredCompletion}
	if err := renderer.Render(t.Context(), &out, session.LiveEvent{Kind: string(messages.StreamTypeSessionClose), Terminal: provider}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("provider close prematurely rendered terminal: %q", out.String())
	}
	terminal := &messages.SessionCloseValue{TerminalReason: messages.TerminalReasonReplayComplete}
	var cause error
	if failed {
		terminal.TerminalReason = messages.TerminalReasonTerminalFailure
		cause = errors.New("late replay mismatch")
	}
	if err := renderer.Render(t.Context(), &out, session.LiveEvent{Kind: string(session.LiveEventTerminal), Terminal: terminal, Error: cause}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "[session terminal:") != 1 {
		t.Fatalf("terminal count: %q", out.String())
	}
	completed := strings.Contains(out.String(), "[session replay complete]")
	if completed == failed {
		t.Fatalf("completion=%t, failed=%t: %q", completed, failed, out.String())
	}
	if failed && !strings.Contains(out.String(), cause.Error()) {
		t.Fatalf("failure missing: %q", out.String())
	}
}
