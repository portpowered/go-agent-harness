package cli

import (
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const chatInteractiveTerminalMessage = "agent chat requires an interactive terminal; use agent ask for scripted or piped input."

// chatInputIsInteractive is a seam for composed command tests. Production
// uses detectInteractiveTerminal, which only admits an input that exposes a
// terminal file descriptor.
var chatInputIsInteractive = detectInteractiveTerminal

// detectInteractiveTerminal reports whether a command's input is attached to
// a terminal. Readers injected by Cobra tests, pipes, redirected files, and
// closed descriptors are intentionally not treated as interactive input.
func detectInteractiveTerminal(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}

	fdReader, ok := cmd.InOrStdin().(interface{ Fd() uintptr })
	if !ok {
		return false
	}

	fd := fdReader.Fd()
	if fd == ^uintptr(0) {
		return false
	}
	return term.IsTerminal(int(fd))
}
