package cli

import (
	"context"
	"io"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/services"
	"github.com/spf13/cobra"
)

// SessionSelfPlayCommand exposes the bounded Phase 1 live two-agent audio
// conversation under `yui session self-play`.
type SessionSelfPlayCommand struct {
	globalFlags *flags.GlobalFlags
	run         func(context.Context, io.Writer, services.SelfPlayRunOptions) error
}

// NewSessionSelfPlayCommand creates the production self-play command. The
// runner is kept as a small seam for CLI validation tests; normal callers use
// services.RunSelfPlay.
func NewSessionSelfPlayCommand(globalFlags *flags.GlobalFlags) *SessionSelfPlayCommand {
	return &SessionSelfPlayCommand{
		globalFlags: globalFlags,
		run:         services.RunSelfPlay,
	}
}

// SetRunner replaces the service runner used by this command. It is intended
// for hermetic command tests and does not change the production default.
func (c *SessionSelfPlayCommand) SetRunner(runner func(context.Context, io.Writer, services.SelfPlayRunOptions) error) {
	if c != nil && runner != nil {
		c.run = runner
	}
}

// Generate returns the cobra command for the bounded self-play runner.
func (c *SessionSelfPlayCommand) Generate() *cobra.Command {
	var apiKey string
	provider := services.SelfPlayDefaultProvider
	model := services.SelfPlayDefaultModel
	baseURL := ""
	outputDir := ""
	maxDuration := services.SelfPlayDefaultMaxDuration
	maxTurns := services.SelfPlayDefaultTurnTarget

	cmd := &cobra.Command{
		Use:   "self-play",
		Short: "Run two fixed-persona live agents through a PCM16 audio bridge",
		Long: "Run the Phase 1 live self-play harness with two continuously open OpenAI Realtime sessions.\n\n" +
			"Customer persona: " + services.SelfPlayCustomerPersona + "\n" +
			"Assistant persona: " + services.SelfPlayAssistantPersona + "\n" +
			"Opening seed (sent once as customer text): " + services.SelfPlayOpeningSeed + "\n\n" +
			"Only emitted raw PCM16 audio crosses between agents; tools and transcript/text bridging are disabled.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			configDir := ""
			if c.globalFlags != nil {
				configDir = c.globalFlags.ConfigDir()
			}
			return c.run(cmd.Context(), cmd.OutOrStdout(), services.SelfPlayRunOptions{
				APIKey:      apiKey,
				OutputDir:   outputDir,
				Provider:    provider,
				Model:       model,
				BaseURL:     baseURL,
				ConfigDir:   configDir,
				MaxDuration: maxDuration,
				MaxTurns:    maxTurns,
			})
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "OpenAI API key; may also come from the configured AGENT_MODEL__OPENAI__API_KEY")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Empty directory for this self-play run (required)")
	cmd.Flags().StringVar(&provider, "provider", provider, "Phase 1 realtime provider (openai only)")
	cmd.Flags().StringVar(&model, "model", model, "OpenAI Realtime model (default: gpt-realtime)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Optional OpenAI Realtime WebSocket endpoint override")
	cmd.Flags().DurationVar(&maxDuration, "max-duration", maxDuration, "Positive maximum run duration (default: 2m)")
	cmd.Flags().IntVar(&maxTurns, "max-turns", maxTurns, "Positive completed-turn target per side (default: 3)")
	return cmd
}
