package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/config"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/flags"
	runtimeSelfPlay "github.com/portpowered/go-agent-harness/go-agent-runtime/services/selfplay"
	"github.com/spf13/cobra"
)

// SessionSelfPlayCommand exposes the bounded Phase 1 live two-agent audio
// conversation under `yui session self-play`.
type SessionSelfPlayCommand struct {
	globalFlags *flags.GlobalFlags
	service     runtimeSelfPlay.Service
}

// NewSessionSelfPlayCommand creates the transport adapter for the public
// runtime self-play contract.
func NewSessionSelfPlayCommand(globalFlags *flags.GlobalFlags, service runtimeSelfPlay.Service) *SessionSelfPlayCommand {
	return &SessionSelfPlayCommand{globalFlags: globalFlags, service: service}
}

func selfPlayRequest(request runtimeSelfPlay.Request, configDir string) (runtimeSelfPlay.Request, error) {
	provider := strings.ToLower(strings.TrimSpace(request.Provider))
	if provider != "" && provider != config.ProviderOpenAI {
		return request, nil
	}
	configProvider := provider
	if configProvider == "" {
		configProvider = config.ProviderOpenAI
	}
	storage, err := config.NewDefaultConfigStorage(configDir)
	if err != nil {
		return runtimeSelfPlay.Request{}, fmt.Errorf("self-play live session configuration: %w", err)
	}
	loaded, err := storage.Load()
	if err != nil {
		return runtimeSelfPlay.Request{}, fmt.Errorf("self-play live session configuration: %w", err)
	}
	effective := loaded.ApplyOverrides(request.APIKey, request.Model, configProvider, request.BaseURL)
	active, err := effective.ActiveOpenAIConfig()
	if err != nil {
		return runtimeSelfPlay.Request{}, fmt.Errorf("self-play live session configuration: %w", err)
	}
	request.APIKey = active.APIKey
	request.BaseURL = active.BaseURL
	return request, nil
}

// Generate returns the cobra command for the bounded self-play runner.
func (c *SessionSelfPlayCommand) Generate() *cobra.Command {
	var apiKey, provider, model, baseURL, outputDir string
	var maxDuration time.Duration
	var maxTurns int

	cmd := &cobra.Command{
		Use:   "self-play",
		Short: "Run a bounded two-agent live audio conversation",
		Long: "Run the bounded self-play harness with two continuously open realtime sessions.\n\n" +
			"Only emitted raw PCM16 audio crosses between agents; tools and transcript/text bridging are disabled.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if c == nil || c.service == nil {
				return errors.New("self-play service is required")
			}
			configDir := ""
			if c.globalFlags != nil {
				configDir = c.globalFlags.ConfigDir()
			}
			request, err := selfPlayRequest(runtimeSelfPlay.Request{
				APIKey:      apiKey,
				OutputDir:   outputDir,
				Provider:    provider,
				Model:       model,
				BaseURL:     baseURL,
				MaxDuration: maxDuration,
				MaxTurns:    maxTurns,
			}, configDir)
			if err != nil {
				return err
			}
			result, runErr := c.service.Run(cmd.Context(), request)
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "self-play stopped: reason=%s customer_turns=%d assistant_turns=%d\n", result.StopReason, result.Customer.CompletedTurns, result.Assistant.CompletedTurns); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("write self-play result: %w", err))
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "OpenAI API key; may also come from the configured AGENT_MODEL__OPENAI__API_KEY")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Empty directory for this self-play run (required)")
	cmd.Flags().StringVar(&provider, "provider", provider, "Realtime provider (openai only)")
	cmd.Flags().StringVar(&model, "model", model, "OpenAI Realtime model")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Optional OpenAI Realtime WebSocket endpoint override")
	cmd.Flags().DurationVar(&maxDuration, "max-duration", maxDuration, "Positive maximum run duration")
	cmd.Flags().IntVar(&maxTurns, "max-turns", maxTurns, "Positive completed-turn target per side")
	return cmd
}
