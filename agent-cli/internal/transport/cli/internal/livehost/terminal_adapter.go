package livehost

import (
	"context"
	"io"

	clioutput "github.com/portpowered/go-agent-harness/agent-cli/internal/output"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// renderTerminalEvent is a presentation-only bridge at the CLI boundary.
func renderTerminalEvent(ctx context.Context, out io.Writer, replay bool, event session.LiveEvent) error {
	return clioutput.NewLiveEventRenderer(replay).Render(ctx, out, event)
}
