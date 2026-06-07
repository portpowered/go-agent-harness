package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/portpowered/go-llm-gateway/pkg/gateway"
	"github.com/spf13/cobra"
)

// InteractionCommand is the interaction group (parent command); subcommands are wired in routes.go.
type InteractionCommand struct{}

// NewInteractionCommand returns the interaction group command constructor.
func NewInteractionCommand() *InteractionCommand {
	return &InteractionCommand{}
}

// Generate returns the cobra command for the interaction group.
func (c *InteractionCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "interaction",
		Short: "Inspect provider-neutral gateway interactions",
		Long: "Inspect provider-neutral gateway interactions.\n\n" +
			"Use the replay subcommand to stream normalized PNIG fixture events as one JSON object per line without provider credentials or live network calls.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
}

// InteractionReplayCommand replays a normalized interaction fixture to stdout.
type InteractionReplayCommand struct{}

// NewInteractionReplayCommand returns the interaction replay command constructor.
func NewInteractionReplayCommand() *InteractionReplayCommand {
	return &InteractionReplayCommand{}
}

// Generate returns the cobra command for interaction replay.
func (c *InteractionReplayCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "replay <fixture-path>",
		Short: "Replay a normalized interaction fixture as NDJSON",
		Long: "Load a normalized PNIG interaction fixture and print one JSON event per line to stdout.\n\n" +
			"This command is credential-free: it validates and replays the fixture locally without reading provider API keys or making live provider network calls.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInteractionReplay(cmd.Context(), cmd.OutOrStdout(), args[0])
		},
	}
}

func runInteractionReplay(ctx context.Context, out io.Writer, fixturePath string) error {
	replayer, err := gateway.NewInteractionFixtureReplayerFromFile(fixturePath)
	if err != nil {
		return fmt.Errorf("replay interaction fixture %q: %w", fixturePath, err)
	}

	encoder := json.NewEncoder(out)
	for event := range replayer.Replay(ctx) {
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("write interaction event: %w", err)
		}
	}
	return nil
}
