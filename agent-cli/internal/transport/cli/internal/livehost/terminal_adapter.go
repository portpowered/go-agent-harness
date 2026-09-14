package livehost

import (
	"context"
	"io"

	clioutput "github.com/portpowered/go-agent-harness/agent-cli/internal/output"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/session"
)

// newTerminalEventRenderer creates the invocation-scoped presentation state.
func newTerminalEventRenderer(replay bool) *clioutput.LiveEventRenderer {
	return clioutput.NewLiveEventRenderer(replay)
}

// renderTerminalEventWithRenderer is a presentation-only bridge at the CLI boundary.
func renderTerminalEventWithRenderer(ctx context.Context, out io.Writer, renderer *clioutput.LiveEventRenderer, event session.LiveEvent) error {
	return renderer.Render(ctx, out, event)
}
