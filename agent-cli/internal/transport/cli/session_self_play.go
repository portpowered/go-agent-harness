package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/spf13/cobra"
)

// SessionSelfPlayCommand translates CLI values and presents the runtime result.
type SessionSelfPlayCommand struct {
	globalFlags *flags.GlobalFlags
	run         selfplay.Service
}

func NewSessionSelfPlayCommand(globalFlags *flags.GlobalFlags, service selfplay.Service) *SessionSelfPlayCommand {
	return &SessionSelfPlayCommand{globalFlags: globalFlags, run: service}
}

func (c *SessionSelfPlayCommand) Generate() *cobra.Command {
	var apiKey, provider, model, baseURL, outputDir string
	var maxDuration = selfplay.SelfPlayDefaultMaxDuration
	var maxTurns = selfplay.SelfPlayDefaultTurnTarget
	provider = selfplay.SelfPlayDefaultProvider
	model = selfplay.SelfPlayDefaultModel

	cmd := &cobra.Command{
		Use:   "self-play",
		Short: "Run two fixed-persona live agents through a PCM16 audio bridge",
		Long: "Run the Phase 1 live self-play harness with two continuously open OpenAI Realtime sessions.\n\n" +
			"Customer persona: " + selfplay.SelfPlayCustomerPersona + "\n" +
			"Assistant persona: " + selfplay.SelfPlayAssistantPersona + "\n" +
			"Opening seed (sent once as customer text): " + selfplay.SelfPlayOpeningSeed + "\n\n" +
			"Only emitted raw PCM16 audio crosses between agents; tools and transcript/text bridging are disabled.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if c == nil || c.run == nil {
				return errors.New("self-play service is required")
			}
			apiKey, baseURL, err := resolveSelfPlayHostConfig(c.globalFlags, apiKey, provider, model, baseURL)
			if err != nil {
				return err
			}
			result, runErr := c.run.Run(cmd.Context(), selfplay.Request{
				APIKey: apiKey, OutputDir: outputDir, Provider: provider, Model: model,
				BaseURL: baseURL, MaxDuration: maxDuration, MaxTurns: maxTurns,
			})
			if result.StopReason != "" {
				_, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "self-play stopped: reason=%s customer_turns=%d assistant_turns=%d\n", result.StopReason, result.Customer.CompletedTurns, result.Assistant.CompletedTurns)
				runErr = errors.Join(runErr, writeErr)
			}
			return runErr
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

func resolveSelfPlayHostConfig(globalFlags *flags.GlobalFlags, apiKey, provider, model, baseURL string) (string, string, error) {
	if globalFlags == nil || strings.ToLower(strings.TrimSpace(provider)) != selfplay.SelfPlayDefaultProvider {
		return apiKey, baseURL, nil
	}
	storage, err := config.NewDefaultConfigStorage(globalFlags.ConfigDir())
	if err != nil {
		return "", "", fmt.Errorf("load self-play host config: %w", err)
	}
	loaded, err := storage.Load()
	if err != nil {
		return "", "", fmt.Errorf("load self-play host config: %w", err)
	}
	effective := loaded.ApplyOverrides(apiKey, model, provider, baseURL)
	active, err := effective.ActiveOpenAIConfig()
	if err != nil {
		return "", "", fmt.Errorf("resolve self-play host config: %w", err)
	}
	return active.APIKey, active.BaseURL, nil
}
