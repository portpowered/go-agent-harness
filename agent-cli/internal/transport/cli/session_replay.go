package cli

import (
	"fmt"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/services/replay"
	"github.com/spf13/cobra"
)

// SessionReplayCommand presents the offline bundle replay service.
type SessionReplayCommand struct{ service replay.Service }

func NewSessionReplayCommand(service replay.Service) *SessionReplayCommand {
	return &SessionReplayCommand{service: service}
}

func (c *SessionReplayCommand) Generate() *cobra.Command {
	return &cobra.Command{
		SilenceUsage: true,
		Use:          "replay <bundle-directory>",
		Short:        "Replay recorded provider and tool events through the agent loop offline",
		Long:         "Replay a --record-dir bundle through the agent loop using recorded provider and tool events. No credentials, live tools, or devices are used. Recorded render audio remains evidence of the original run; this command does not reproduce physical device behavior.",
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c == nil || c.service == nil {
				return fmt.Errorf("session replay service is required")
			}
			result, err := c.service.Run(cmd.Context(), cmd.OutOrStdout(), replay.Request{BundlePath: args[0]})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "Replay verified: %d wire events, %d tool calls. Recorded render audio: %t; render tap unavailable: %t.\n", result.WireEvents, result.ToolCalls, result.Scope.RecordedRender, result.Scope.RenderTapUnavailable)
			return err
		},
	}
}
