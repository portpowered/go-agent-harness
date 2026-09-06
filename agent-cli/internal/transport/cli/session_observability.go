package cli

import (
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	serviceSession "github.com/portpowered/go-agent-harness/agent-cli/internal/services/agentsession"
	serviceSelfPlay "github.com/portpowered/go-agent-harness/agent-cli/internal/services/selfplay"
)

// NewSessionCommand creates the session command with both public service
// contracts. Tests pass nil for the self-play service when they do not invoke
// that subcommand.
func NewSessionCommand(
	askFlags *flags.AskFlags,
	globalFlags *flags.GlobalFlags,
	sessionService serviceSession.Service,
	selfPlayService serviceSelfPlay.Service,
) *SessionCommand {
	return &SessionCommand{
		askFlags: askFlags, globalFlags: globalFlags, sessionService: sessionService,
		selfPlayService: selfPlayService,
	}
}
